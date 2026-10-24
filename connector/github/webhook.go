package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the GitHub connector's webhook binding (ADR 0029): the
// reachable-endpoint transport alongside the polling binding in inbound.go.
//
// ADR 0029's governing decisions, and where each is implemented:
//
//   - d1/d2  the endpoint is connector-owned; the navi owns the listener and a
//     dumb router. RegisterRoute registers here at Start and removes at Stop.
//   - d3     the router is the events-log choke point: every delivery is
//     logged here with its verification and outcome.
//   - d4     the signature is verified over the RAW body, BEFORE parsing.
//     HMAC-SHA256, constant time. This binding does not trust the relay.
//   - d5     the secret is a REFERENCE, an array so a rotation can overlap; an
//     unresolvable secret refuses every request rather than accepting
//     unverified input, and the navi still runs.
//   - d6     dedupe keys the SAME persisted seen-set on the SAME normalized
//     event identity the poll loop computes, so the two transports dedupe
//     against each other. The provider delivery id is logged, not the key.
//   - d7     the payload is untrusted and bounded; it becomes the thought's
//     stimulus, never the assembler's configuration.
//   - d8     the thought acts as the subscription's declared service principal.
//   - d9     the acknowledgement is explicit and provider-correct.
//   - d10    one connector, two transports: the same subscription, client,
//     credential, seen-set, and enqueue path.
//   - d12    the navi rate-limits per source and per route, and sheds (429).

// DefaultWebhookRoute is the path a GitHub webhook binding serves when the
// declaration names none.
const DefaultWebhookRoute = "/github"

// maxWebhookBody bounds a delivery before it is read whole (ADR 0029 decision
// 7). GitHub's own limit on a push payload is 25 MB, but a webhook this navi
// acts on is an issue or comment event, which is far smaller; a body over this
// bound is refused 413 rather than parsed.
const maxWebhookBody = 1 << 20

// DefaultWebhookRate is the per-source, per-route sustained delivery rate the
// navi enforces when the declaration names none (ADR 0029 decision 12). It is
// deliberately generous: a homelab navi leans on the relay for the fleet edge,
// and this bound exists so a navi exposed directly is not an open door.
const DefaultWebhookRate = 5.0 // deliveries per second

// DefaultWebhookBurst is how many deliveries one source may make at once
// before the rate applies.
const DefaultWebhookBurst = 20

// WebhookBinding is one build-time webhook declaration (ADR 0029 decisions 1-2,
// 5). It is declared where the connector is composed, so what a navi can be
// reached by is visible in main.go.
type WebhookBinding struct {
	// Name identifies the binding and scopes its rate limit. It is also the
	// subscription a delivery is matched against: a binding serves the
	// subscription of the same name, so one declaration governs both
	// transports (ADR 0029 decision 10).
	Name string
	// Route is the path the navi's webhook listener dispatches to this
	// binding (e.g. "/github"). Empty = DefaultWebhookRoute.
	Route string
	// SecretRefs name where the shared secret lives — environment variables or
	// file paths, never the value (ADR 0008, ADR 0029 decision 5). It is an
	// ARRAY so a rotation can overlap: a delivery is accepted if it matches ANY
	// configured secret, so rotating one is a config change rather than a
	// cutover. One reference is the common case.
	SecretRefs []string
	// Env resolves SecretRefs. Nil = os.Getenv.
	Env func(string) string
	// Rate is the sustained deliveries-per-second this binding accepts per
	// source. Zero = DefaultWebhookRate.
	Rate float64
	// Burst is how many deliveries one source may make at once. Zero =
	// DefaultWebhookBurst.
	Burst int
}

// webhookBinding is a validated declaration with its resolved runtime state.
type webhookBinding struct {
	decl  WebhookBinding
	sub   Subscription
	route string

	mu      sync.Mutex
	limiter *sourceLimiter
	refused bool // the secret could not be resolved: refuse, don't accept
	warned  bool
}

// validateWebhookBinding rejects a declaration the binding cannot serve, at
// composition time rather than by silently dropping deliveries at runtime
// (ADR 0029 decision 10, the validateSubscription pattern).
func validateWebhookBinding(b WebhookBinding) error {
	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("webhook binding requires a name")
	}
	route := b.Route
	if strings.TrimSpace(route) == "" {
		route = DefaultWebhookRoute
	}
	if err := connector.Route(route); err != nil {
		return fmt.Errorf("webhook binding %q: %w", b.Name, err)
	}
	if len(b.SecretRefs) == 0 {
		return fmt.Errorf("webhook binding %q: at least one SecretRefs entry is required (ADR 0029 decision 5)", b.Name)
	}
	for _, ref := range b.SecretRefs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("webhook binding %q: a SecretRefs entry is empty", b.Name)
		}
	}
	if b.Rate < 0 {
		return fmt.Errorf("webhook binding %q: Rate must not be negative", b.Name)
	}
	if b.Burst < 0 {
		return fmt.Errorf("webhook binding %q: Burst must not be negative", b.Name)
	}
	return nil
}

// startWebhooks validates the declared bindings, registers each route on the
// navi's shared router, and returns the stop function. A binding whose
// subscription is not declared is refused at composition: the delivery would
// have no declared intent or principal, which is exactly what the build-time
// declaration exists to prevent.
func (c *Connector) startWebhooks(ctx context.Context, deps connector.Deps) (func(), error) {
	if len(c.cfg.Webhooks) == 0 {
		return func() {}, nil
	}
	if deps.Webhooks == nil {
		// The navi composed no webhook listener. This is a composition error,
		// not a runtime degradation: the routes could never be reached (ADR
		// 0029 decision 2).
		return nil, fmt.Errorf("github: %w", connector.ErrNoWebhookRouter)
	}
	subs := map[string]Subscription{}
	for _, s := range c.cfg.Subscriptions {
		subs[s.Name] = s
	}
	var registered []string
	for _, decl := range c.cfg.Webhooks {
		if err := validateWebhookBinding(decl); err != nil {
			c.unregisterWebhooks(deps, registered)
			return nil, fmt.Errorf("github: %w", err)
		}
		sub, ok := subs[decl.Name]
		if !ok {
			c.unregisterWebhooks(deps, registered)
			return nil, fmt.Errorf("github: webhook binding %q has no subscription of that name; declare one so the delivery has a declared intent and principal (ADR 0029 decision 10)", decl.Name)
		}
		// The binding serves the subscription's declared event kinds, so an
		// intent or event kind the webhook transport cannot serve is refused
		// here rather than at the first delivery.
		if err := validateSubscription(sub); err != nil {
			c.unregisterWebhooks(deps, registered)
			return nil, fmt.Errorf("github: %w", err)
		}
		route := decl.Route
		if strings.TrimSpace(route) == "" {
			route = DefaultWebhookRoute
		}
		b := &webhookBinding{decl: decl, sub: sub, route: route, limiter: newSourceLimiter(rateOrDefault(decl.Rate), burstOrDefault(decl.Burst))}
		if err := deps.Webhooks.Handle(route, c.webhookHandler(deps, b)); err != nil {
			c.unregisterWebhooks(deps, registered)
			return nil, fmt.Errorf("github: %w", err)
		}
		registered = append(registered, route)
		c.bindings = append(c.bindings, b)
	}
	return func() { c.unregisterWebhooks(deps, registered) }, nil
}

// unregisterWebhooks removes the routes a partially failed Start registered, so
// a connector that failed to start leaves the router as it found it.
func (c *Connector) unregisterWebhooks(deps connector.Deps, routes []string) {
	if deps.Webhooks == nil {
		return
	}
	for _, r := range routes {
		deps.Webhooks.Remove(r)
	}
}

// webhookHandler builds the http.Handler for one binding. It is the whole of
// ADR 0029 decisions 4, 6, 7, and 9: bound the body, verify the signature over
// the raw bytes BEFORE parsing, dedupe on the normalized event identity, and
// answer with the provider-correct status.
func (c *Connector) webhookHandler(deps connector.Deps, b *webhookBinding) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := c.clock()
		// A non-POST is refused 405 before anything else (decision 9).
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			c.logDelivery(deps, b, r, "", "refused", "method_not_allowed", now)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The source key for the rate limit: the relay forwards the real client
		// in X-Forwarded-For, else the connection's peer. Per source AND per
		// route (decision 12) — the route is b.route, so one binding's burst
		// cannot starve another's.
		source := clientSource(r)
		if !b.limiter.allow(source, now) {
			c.logDelivery(deps, b, r, "", "refused", "rate_limited", now)
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		// The body is bounded BEFORE it is read whole (decision 7): a body over
		// the bound is refused 413 rather than buffered.
		body, err := readBounded(r.Body, maxWebhookBody)
		if err != nil {
			c.logDelivery(deps, b, r, "", "refused", "body_too_large", now)
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
		// The signature is verified over the RAW bytes BEFORE any parsing
		// (decision 4). Every configured secret is tried, so a rotation can
		// overlap (decision 5); an unresolvable set refuses rather than
		// accepting unverified input. The capability is inert, not fatal.
		secrets, ok := c.webhookSecrets(b, deps)
		if !ok {
			c.logDelivery(deps, b, r, delivery, "refused", "secret_unresolved", now)
			http.Error(w, "webhook secret unresolved", http.StatusUnauthorized)
			return
		}
		if !verifySignatureAny(secrets, body, r.Header.Get("X-Hub-Signature-256")) {
			c.logDelivery(deps, b, r, delivery, "refused", "bad_signature", now)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		// Only now is the payload trusted enough to parse.
		event, kind, declared, perr := parseWebhookEvent(b.sub, r.Header.Get("X-GitHub-Event"), body)
		if perr != nil {
			c.logDelivery(deps, b, r, delivery, "refused", "malformed_body", now)
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}
		if !declared {
			// An undeclared event kind is acknowledged and ignored, NOT
			// refused: GitHub sends only what the App subscribes to, and a
			// refusal would make it retry forever (decision 9).
			c.logDelivery(deps, b, r, delivery, "ignored", "undeclared_event", now)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// The dedupe key is the SAME normalized event identity the poll loop
		// computes, so the two transports dedupe against each other (decision
		// 6). A redelivery is acknowledged and spawns no thought. The store is
		// the connector's ONE seen-set, built at Start, so both transports share
		// the table rather than each opening their own.
		seen, err := c.seenStore()
		if err != nil {
			c.logDelivery(deps, b, r, delivery, "error", "seen_store", now)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if seen.has(b.sub.Name, event.ID) {
			c.logDelivery(deps, b, r, delivery, "duplicate", "seen", now)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// A webhook's author is the event's sender; the navi's own egress
		// cannot re-enter as an event, matching the poll loop's self-filter.
		// The login is resolved lazily from Config.SelfLogin or GET /user, the
		// same signal the poll loop uses, so both transports agree.
		if cl, err := c.live.Get(); err == nil {
			if self := c.selfLogin(r.Context(), cl, deps); self != "" && strings.EqualFold(self, event.Author) {
				c.logDelivery(deps, b, r, delivery, "ignored", "self_authored", now)
				w.WriteHeader(http.StatusAccepted)
				return
			}
		}
		if eerr := c.enqueueEvent(deps, b.sub, kind, event, now); eerr != nil {
			if errors.Is(eerr, thought.ErrDuplicateID) {
				// The deterministic thought id already exists: the seen-set was
				// lost but the queue survived, so the event has been handled.
				seen.mark(b.sub.Name, event.ID, now)
				c.logDelivery(deps, b, r, delivery, "duplicate", "queue_collision", now)
				w.WriteHeader(http.StatusAccepted)
				return
			}
			// The enqueue failed: NOT marked seen, and the provider is asked to
			// retry with a 5xx (decision 9). This is the one case where a retry
			// is the right response.
			c.logDelivery(deps, b, r, delivery, "error", "enqueue_failed", now)
			http.Error(w, "enqueue failed", http.StatusInternalServerError)
			return
		}
		// The seen-set is marked ONLY after a successful enqueue, so a failure
		// is retried by the provider rather than lost (decision 6).
		seen.mark(b.sub.Name, event.ID, now)
		c.logDelivery(deps, b, r, delivery, "accepted", event.ID, now)
		w.WriteHeader(http.StatusAccepted)
	})
}

// webhookSecrets resolves EVERY configured secret reference, so a delivery is
// accepted if it matches ANY of them and a rotation can overlap (ADR 0029
// decision 5). It fails closed: no reference resolving means no secrets, and
// the caller refuses. The references are warned about, never a value.
func (c *Connector) webhookSecrets(b *webhookBinding, deps connector.Deps) ([]string, bool) {
	env := b.decl.Env
	if env == nil {
		env = os.Getenv
	}
	var out []string
	for _, ref := range b.decl.SecretRefs {
		if v := strings.TrimSpace(env(ref)); v != "" {
			out = append(out, v)
		}
	}
	if len(out) > 0 {
		return out, true
	}
	b.mu.Lock()
	warn := !b.warned
	b.warned = true
	b.refused = true
	b.mu.Unlock()
	if warn && deps.Log != nil {
		deps.Log.Warn("github: webhook secret is unresolved; every delivery is refused until it resolves (fail closed, ADR 0029 decision 5)",
			"binding", b.decl.Name, "refs", strings.Join(b.decl.SecretRefs, ","))
	}
	return nil, false
}

// repoMatches reports whether a delivery is for the repository the
// subscription declares. GitHub repository names are case-insensitive, so the
// comparison is too — a mismatch here would silently drop legitimate deliveries
// rather than refuse them, which is the wrong direction to fail in. An empty
// declared repository matches anything (the subscription is not repo-scoped).
func repoMatches(sub Subscription, fullName string) bool {
	if strings.TrimSpace(sub.Repository) == "" {
		return true
	}
	return strings.EqualFold(sub.Repository, fullName)
}

// verifySignature checks GitHub's X-Hub-Signature-256 over the exact received
// bytes, in constant time (ADR 0029 decision 4). The scheme is HMAC-SHA256;
// there is no signed timestamp, so replay is bounded by the dedupe (decision 6)
// rather than by a clock. A malformed or absent header is a failure, never a
// pass.
func verifySignature(secret string, body []byte, header string) bool {
	return verifySignatureAny([]string{secret}, body, header)
}

// verifySignatureAny accepts the delivery when it matches ANY of the secrets,
// which is what lets a rotation overlap (ADR 0029 decision 5). Every candidate
// is compared in constant time, and the loop does not short-circuit on the
// header being malformed only after at least one attempt — a malformed header
// is a failure regardless.
func verifySignatureAny(secrets []string, body []byte, header string) bool {
	header = strings.TrimSpace(header)
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	if err != nil {
		return false
	}
	ok := false
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		// hmac.Equal is constant-time; `ok = ok || ...` avoids leaking which
		// secret matched through timing.
		ok = hmac.Equal(mac.Sum(nil), want) || ok
	}
	return ok
}

// readBounded reads a body up to limit bytes, reporting an error when the body
// exceeds it rather than silently truncating (ADR 0029 decision 7). One byte
// past the limit distinguishes "exactly at the bound" from "over it".
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("github: webhook body exceeds %d bytes", limit)
	}
	return body, nil
}

// parseWebhookEvent maps a verified delivery to the normalized event the
// enqueue path consumes. It returns declared=false for an event kind the
// subscription does not name — which is acknowledged and ignored, not refused
// (ADR 0029 decision 9) — and an error only for a body that does not parse.
//
// The normalized event ID is the SAME identity the poll loop computes
// (`comment-<id>`, `issue-<number>`), which is what makes the two transports
// dedupe against each other (decision 6).
func parseWebhookEvent(sub Subscription, eventHeader string, body []byte) (inboundEvent, EventKind, bool, error) {
	switch strings.TrimSpace(eventHeader) {
	case "issues":
		var p struct {
			Action string `json:"action"`
			Issue  struct {
				Number  int    `json:"number"`
				Title   string `json:"title"`
				Body    string `json:"body"`
				HTMLURL string `json:"html_url"`
				User    struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"issue"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return inboundEvent{}, "", false, err
		}
		// Only a newly opened issue is EventIssueOpened; an edit or a label
		// change is not the declared stimulus.
		if p.Action != "opened" || !sub.declares(EventIssueOpened) {
			return inboundEvent{}, EventIssueOpened, false, nil
		}
		if !repoMatches(sub, p.Repository.FullName) {
			return inboundEvent{}, EventIssueOpened, false, nil
		}
		return inboundEvent{
			ID:     fmt.Sprintf("issue-%d", p.Issue.Number),
			Repo:   p.Repository.FullName,
			Title:  p.Issue.Title,
			Body:   p.Issue.Body,
			URL:    p.Issue.HTMLURL,
			Author: p.Issue.User.Login,
		}, EventIssueOpened, true, nil
	case "issue_comment":
		var p struct {
			Action  string `json:"action"`
			Comment struct {
				ID      int64  `json:"id"`
				Body    string `json:"body"`
				HTMLURL string `json:"html_url"`
				User    struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"comment"`
			Issue struct {
				Number int `json:"number"`
			} `json:"issue"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return inboundEvent{}, "", false, err
		}
		if p.Action != "created" || !sub.declares(EventIssueMention) {
			return inboundEvent{}, EventIssueMention, false, nil
		}
		if !repoMatches(sub, p.Repository.FullName) {
			return inboundEvent{}, EventIssueMention, false, nil
		}
		// A comment is only the declared stimulus when it mentions the watched
		// login, exactly as the poll loop decides — so the two transports agree
		// on what an event IS, not only on its identity.
		if !mentions(p.Comment.Body, sub.Mention) {
			return inboundEvent{}, EventIssueMention, false, nil
		}
		return inboundEvent{
			ID:     fmt.Sprintf("comment-%d", p.Comment.ID),
			Repo:   p.Repository.FullName,
			Body:   p.Comment.Body,
			URL:    p.Comment.HTMLURL,
			Author: p.Comment.User.Login,
		}, EventIssueMention, true, nil
	default:
		return inboundEvent{}, "", false, nil
	}
}

// logDelivery writes the events-log line for one delivery (ADR 0029 decision
// 3): when, from where, which route (provider), the delivery id, and the
// outcome. It holds metadata and outcome — NEVER the secret, and not the raw
// payload body, which may carry private or untrusted content. It starts as
// structured log lines; the persisted, browsable form is NAVI-433.
func (c *Connector) logDelivery(deps connector.Deps, b *webhookBinding, r *http.Request, delivery, outcome, detail string, now time.Time) {
	if deps.Log == nil {
		return
	}
	deps.Log.Info("webhook delivery",
		"provider", "github",
		"route", b.route,
		"binding", b.decl.Name,
		"source", clientSource(r),
		"delivery_id", delivery,
		"event", r.Header.Get("X-GitHub-Event"),
		"outcome", outcome,
		"detail", detail,
		"at", now.UTC().Format(time.RFC3339Nano),
	)
}

// clientSource is the rate-limit key for a delivery: the forwarded client when
// the relay set one, else the connection's peer host.
func clientSource(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); strings.TrimSpace(fwd) != "" {
		// The leftmost entry is the original client; the relay appends.
		if i := strings.IndexByte(fwd, ','); i >= 0 {
			fwd = fwd[:i]
		}
		return strings.TrimSpace(fwd)
	}
	if host, _, err := splitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// splitHostPort is net.SplitHostPort without the import, so this file's
// dependencies stay obvious. It handles the "host:port" and "[v6]:port" shapes.
func splitHostPort(addr string) (string, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", errors.New("empty addr")
	}
	if strings.HasPrefix(addr, "[") {
		end := strings.LastIndexByte(addr, ']')
		if end < 0 {
			return "", "", errors.New("unterminated bracket")
		}
		host := addr[1:end]
		port := strings.TrimPrefix(addr[end+1:], ":")
		return host, port, nil
	}
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return addr, "", nil
	}
	return addr[:i], addr[i+1:], nil
}

// ── the per-source rate limiter (ADR 0029 decision 12) ──────────────────

// sourceLimiter is a per-source token bucket, stdlib-only (the connector
// contract is stdlib-only, so golang.org/x/time/rate is not available). It
// sheds load rather than queueing unbounded work: a source over its rate is
// refused 429 and the delivery is not processed late.
type sourceLimiter struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newSourceLimiter(rate float64, burst int) *sourceLimiter {
	return &sourceLimiter{rate: rate, burst: float64(burst), buckets: map[string]*bucket{}}
}

// allow reports whether a delivery from source may proceed at time now,
// refilling the bucket for the elapsed time first. An unknown source starts
// full, so a first delivery is never refused.
func (l *sourceLimiter) allow(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[source]
	if !ok {
		// A new source starts with a full bucket. The map is bounded in
		// practice by the number of distinct sources seen; a bound with
		// eviction is a follow-up if a deployment ever needs one.
		l.buckets[source] = &bucket{tokens: l.burst - 1, last: now}
		return l.burst >= 1
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func rateOrDefault(r float64) float64 {
	if r <= 0 {
		return DefaultWebhookRate
	}
	return r
}

func burstOrDefault(b int) int {
	if b <= 0 {
		return DefaultWebhookBurst
	}
	return b
}
