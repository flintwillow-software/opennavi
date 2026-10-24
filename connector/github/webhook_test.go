package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/thought"
)

// ── helpers ─────────────────────────────────────────────────────────────

// sign returns GitHub's X-Hub-Signature-256 header value for body under secret.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// webhookConnector builds a connector with one webhook binding over a
// subscription, and a router the test owns. It returns the connector, the
// router, and a start function that registers the routes.
func webhookConnector(t *testing.T, cfg Config, secrets map[string]string) (*Connector, *connector.Router, *fakeTS, func()) {
	t.Helper()
	ts := &fakeTS{}
	router := connector.NewRouter()
	c := New(cfg, WithStateStore(nil))
	deps := connector.Deps{
		ThoughtSystem: ts,
		Log:           discardLogger(),
		Webhooks:      router,
	}
	if secrets != nil {
		// The binding's Env resolves the declared references from the map.
		for i := range c.cfg.Webhooks {
			c.cfg.Webhooks[i].Env = func(k string) string { return secrets[k] }
		}
	}
	start := func() {
		if err := c.Start(context.Background(), deps); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}
	return c, router, ts, start
}

// issueOpenedBody is a minimal GitHub "issues opened" delivery.
func issueOpenedBody(t *testing.T, number int, login, repo string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action": "opened",
		"issue": map[string]any{
			"number":   number,
			"title":    fmt.Sprintf("issue %d", number),
			"body":     "please look",
			"html_url": fmt.Sprintf("https://github.com/%s/issues/%d", repo, number),
			"user":     map[string]any{"login": login},
		},
		"repository": map[string]any{"full_name": repo},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// commentCreatedBody is a minimal GitHub "issue_comment created" delivery.
func commentCreatedBody(t *testing.T, id int64, body, login, repo string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action": "created",
		"comment": map[string]any{
			"id":       id,
			"body":     body,
			"html_url": fmt.Sprintf("https://github.com/%s/issues/1#issuecomment-%d", repo, id),
			"user":     map[string]any{"login": login},
		},
		"issue":      map[string]any{"number": 1},
		"repository": map[string]any{"full_name": repo},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// post delivers a signed webhook to the router.
func post(t *testing.T, router *connector.Router, route, event string, body []byte, secret, delivery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", sign(secret, body))
	}
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// discardLogger is a slog logger that writes nowhere.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── signature verification (ADR 0029 decision 4) ────────────────────────

// TestWebhookRejectsBadSignature is the load-bearing test: an unsigned or
// wrongly-signed delivery is refused 401 and spawns no thought. It fails
// against an implementation that parses before verifying.
func TestWebhookRejectsBadSignature(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", Route: "/github", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 1, "alice", "acme/widgets")

	// No signature at all.
	rec := post(t, router, "/github", "issues", body, "", "d1")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned delivery: got %d, want 401", rec.Code)
	}
	// A wrong secret.
	rec = post(t, router, "/github", "issues", body, "wrong", "d2")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: got %d, want 401", rec.Code)
	}
	// A malformed header.
	req := httptest.NewRequest(http.MethodPost, "/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", "sha256=nothex")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("malformed signature: got %d, want 401", rec2.Code)
	}
	// A tampered body under a valid signature for different bytes.
	req = httptest.NewRequest(http.MethodPost, "/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", sign("s3cret", []byte("other")))
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, req)
	if rec3.Code != http.StatusUnauthorized {
		t.Errorf("tampered body: got %d, want 401", rec3.Code)
	}

	if ts.count() != 0 {
		t.Errorf("refused deliveries spawned %d thoughts, want 0", ts.count())
	}
}

// TestWebhookAcceptsValidSignature: a correctly signed declared event is
// accepted 202 and enqueues exactly one thought with the deterministic id.
func TestWebhookAcceptsValidSignature(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", Route: "/github", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 42, "alice", "acme/widgets")
	rec := post(t, router, "/github", "issues", body, "s3cret", "d1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid delivery: got %d, want 202", rec.Code)
	}
	if ts.count() != 1 {
		t.Fatalf("got %d thoughts, want 1", ts.count())
	}
	got := ts.get(0)
	if got.ID != "github-sub-issue-42" {
		t.Errorf("thought id = %q, want github-sub-issue-42", got.ID)
	}
	if !strings.Contains(got.Context, "acme/widgets") {
		t.Errorf("thought context missing repo: %q", got.Context)
	}
}

// TestWebhookSecretRotationOverlaps proves the SecretRefs array is what makes a
// rotation a config change: a delivery matching ANY configured secret is
// accepted (ADR 0029 decision 5).
func TestWebhookSecretRotationOverlaps(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"OLD", "NEW"}}},
	}, map[string]string{"OLD": "old-secret", "NEW": "new-secret"})
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 1, "alice", "acme/widgets")
	if rec := post(t, router, "/github", "issues", body, "old-secret", "d1"); rec.Code != http.StatusAccepted {
		t.Errorf("old secret during rotation: got %d, want 202", rec.Code)
	}
	body2 := issueOpenedBody(t, 2, "alice", "acme/widgets")
	if rec := post(t, router, "/github", "issues", body2, "new-secret", "d2"); rec.Code != http.StatusAccepted {
		t.Errorf("new secret during rotation: got %d, want 202", rec.Code)
	}
	if ts.count() != 2 {
		t.Errorf("got %d thoughts, want 2", ts.count())
	}
}

// TestWebhookUnresolvedSecretFailsClosed: a binding whose secret cannot be
// resolved refuses EVERY delivery rather than accepting unverified input, and
// the navi still runs (ADR 0029 decision 5, ADR 0020 decision 7).
func TestWebhookUnresolvedSecretFailsClosed(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"MISSING"}}},
	}, map[string]string{}) // the reference resolves to ""
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 1, "alice", "acme/widgets")
	rec := post(t, router, "/github", "issues", body, "anything", "d1")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unresolved secret: got %d, want 401", rec.Code)
	}
	if ts.count() != 0 {
		t.Errorf("unresolved secret accepted %d thoughts, want 0", ts.count())
	}
}

// ── dedupe (ADR 0029 decision 6) ────────────────────────────────────────

// TestWebhookDedupeRedelivery: the same normalized event identity delivered
// twice is acknowledged but spawns one thought, and the deterministic thought
// id is the second backstop.
func TestWebhookDedupeRedelivery(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 7, "alice", "acme/widgets")
	if rec := post(t, router, "/github", "issues", body, "s3cret", "d1"); rec.Code != http.StatusAccepted {
		t.Fatalf("first delivery: got %d, want 202", rec.Code)
	}
	// A redelivery with a DIFFERENT delivery id names the same underlying
	// event, so it is deduped (the delivery id is logged, not the key).
	if rec := post(t, router, "/github", "issues", body, "s3cret", "d2"); rec.Code != http.StatusAccepted {
		t.Fatalf("redelivery: got %d, want 202", rec.Code)
	}
	if ts.count() != 1 {
		t.Errorf("redelivery spawned %d thoughts, want 1", ts.count())
	}
}

// TestWebhookAndPollShareTheSeenSet is the cross-transport property: an event
// the poll loop already saw is deduped when the webhook delivers it, because
// both key the same seen-set on the same normalized identity (decision 6).
func TestWebhookAndPollShareTheSeenSet(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	// Simulate the poll loop having seen this event identity.
	c.seen.mark("sub", "issue-7", time.Now())

	body := issueOpenedBody(t, 7, "alice", "acme/widgets")
	rec := post(t, router, "/github", "issues", body, "s3cret", "d1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d, want 202", rec.Code)
	}
	if ts.count() != 0 {
		t.Errorf("webhook reprocessed an event the poll loop saw: %d thoughts, want 0", ts.count())
	}
}

// ── status table (ADR 0029 decision 9) ──────────────────────────────────

func TestWebhookStatusTable(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	valid := issueOpenedBody(t, 1, "alice", "acme/widgets")

	// Non-POST -> 405.
	req := httptest.NewRequest(http.MethodGet, "/github", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d, want 405", rec.Code)
	}

	// Body over the bound -> 413.
	big := make([]byte, maxWebhookBody+10)
	rec = post(t, router, "/github", "issues", big, "s3cret", "d1")
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: got %d, want 413", rec.Code)
	}

	// Malformed body -> 400 (signed, so it reaches the parse).
	bad := []byte("{not json")
	rec = post(t, router, "/github", "issues", bad, "s3cret", "d2")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: got %d, want 400", rec.Code)
	}

	// Undeclared event kind -> 202 (acknowledged and ignored, not refused).
	rec = post(t, router, "/github", "push", valid, "s3cret", "d3")
	if rec.Code != http.StatusAccepted {
		t.Errorf("undeclared event: got %d, want 202", rec.Code)
	}

	// Unknown route -> 404.
	rec = post(t, router, "/nope", "issues", valid, "s3cret", "d4")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown route: got %d, want 404", rec.Code)
	}

	if ts.count() != 0 {
		t.Errorf("status-table cases spawned %d thoughts, want 0", ts.count())
	}
}

// TestWebhookRateLimitsAndSheds: a source over its rate is refused 429 rather
// than queued (ADR 0029 decision 12).
func TestWebhookRateLimitsAndSheds(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}, Rate: 0.001, Burst: 2}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	// Burst 2: the first two are accepted, the third is shed. Distinct event
	// identities so the dedupe does not mask the rate limit.
	codes := make([]int, 0, 3)
	for i := 1; i <= 3; i++ {
		body := issueOpenedBody(t, i, "alice", "acme/widgets")
		rec := post(t, router, "/github", "issues", body, "s3cret", fmt.Sprintf("d%d", i))
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusAccepted || codes[1] != http.StatusAccepted {
		t.Errorf("first two deliveries: got %v, want both 202", codes[:2])
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Errorf("third delivery: got %d, want 429 (shed)", codes[2])
	}
	if ts.count() != 2 {
		t.Errorf("shed delivery still enqueued: got %d thoughts, want 2", ts.count())
	}
}

// ── composition (ADR 0029 decisions 2, 10) ──────────────────────────────

// TestWebhookBindingRequiresAListener: a connector declaring a webhook binding
// in a navi that composed no webhook listener fails Start naming the reason
// rather than silently serving nothing.
func TestWebhookBindingRequiresAListener(t *testing.T) {
	c := New(Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	})
	deps := connector.Deps{ThoughtSystem: &fakeTS{}, Log: discardLogger()} // Webhooks nil
	err := c.Start(context.Background(), deps)
	if err == nil {
		t.Fatal("Start with no webhook router: got nil, want an error")
	}
	if !errors.Is(err, connector.ErrNoWebhookRouter) {
		t.Errorf("error = %v, want ErrNoWebhookRouter", err)
	}
}

// TestWebhookBindingRequiresASubscription: a binding with no subscription of
// the same name is refused at composition, because the delivery would have no
// declared intent or principal (decision 10).
func TestWebhookBindingRequiresASubscription(t *testing.T) {
	c := New(Config{
		Webhooks: []WebhookBinding{{Name: "orphan", SecretRefs: []string{"GH_SECRET"}}},
	})
	err := c.Start(context.Background(), connector.Deps{ThoughtSystem: &fakeTS{}, Log: discardLogger(), Webhooks: connector.NewRouter()})
	if err == nil || !strings.Contains(err.Error(), "no subscription") {
		t.Fatalf("Start with an orphan binding: got %v, want a no-subscription error", err)
	}
}

// TestWebhookRouteRegisteredAndRemoved: Start registers the route and Stop
// removes it, so a stopped connector serves 404 rather than a handler over a
// cleared client.
func TestWebhookRouteRegisteredAndRemoved(t *testing.T) {
	c, router, _, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", Route: "/hooks/gh", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	start()

	if routes := router.Routes(); len(routes) != 1 || routes[0] != "/hooks/gh" {
		t.Fatalf("routes after Start = %v, want [/hooks/gh]", routes)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if routes := router.Routes(); len(routes) != 0 {
		t.Errorf("routes after Stop = %v, want none", routes)
	}
}

// TestWebhookDuplicateRouteRefused: two connectors cannot shadow each other's
// route.
func TestWebhookDuplicateRouteRefused(t *testing.T) {
	router := connector.NewRouter()
	if err := router.Handle("/github", http.NotFoundHandler()); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := router.Handle("/github", http.NotFoundHandler()); err == nil {
		t.Error("second Handle on the same route: got nil, want an error")
	}
}

// TestWebhookBindingValidatesDeclarations: a declaration the binding cannot
// serve is refused at composition.
func TestWebhookBindingValidatesDeclarations(t *testing.T) {
	cases := []struct {
		name string
		b    WebhookBinding
	}{
		{"no name", WebhookBinding{Route: "/x", SecretRefs: []string{"S"}}},
		{"no secret refs", WebhookBinding{Name: "n", Route: "/x"}},
		{"empty secret ref", WebhookBinding{Name: "n", Route: "/x", SecretRefs: []string{" "}}},
		{"unrooted route", WebhookBinding{Name: "n", Route: "x", SecretRefs: []string{"S"}}},
		{"negative rate", WebhookBinding{Name: "n", Route: "/x", SecretRefs: []string{"S"}, Rate: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateWebhookBinding(tc.b); err == nil {
				t.Errorf("validateWebhookBinding(%+v): got nil, want an error", tc.b)
			}
		})
	}
}

// ── the payload is stimulus, not configuration (decision 7) ─────────────

// TestWebhookCommentNeedsMention: a comment event only becomes a stimulus when
// it mentions the watched login, so the two transports agree on what an event
// IS — not only on its identity.
func TestWebhookCommentNeedsMention(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{
			Name: "sub", Repository: "acme/widgets",
			Events: []EventKind{EventIssueMention}, Mention: "navi",
		}},
		Webhooks: []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	// A comment that does not mention the login is not the declared stimulus.
	body := commentCreatedBody(t, 1, "just chatting", "alice", "acme/widgets")
	if rec := post(t, router, "/github", "issue_comment", body, "s3cret", "d1"); rec.Code != http.StatusAccepted {
		t.Fatalf("non-mention comment: got %d, want 202 (acknowledged, ignored)", rec.Code)
	}
	if ts.count() != 0 {
		t.Errorf("non-mention comment spawned %d thoughts, want 0", ts.count())
	}

	// A mention is.
	body = commentCreatedBody(t, 2, "@navi please fix", "alice", "acme/widgets")
	if rec := post(t, router, "/github", "issue_comment", body, "s3cret", "d2"); rec.Code != http.StatusAccepted {
		t.Fatalf("mention comment: got %d, want 202", rec.Code)
	}
	if ts.count() != 1 {
		t.Fatalf("mention comment spawned %d thoughts, want 1", ts.count())
	}
	if got := ts.get(0).ID; got != "github-sub-comment-2" {
		t.Errorf("thought id = %q, want github-sub-comment-2", got)
	}
}

// TestWebhookRepositoryNarrowing: a delivery for a repository the subscription
// does not declare is ignored, so runtime narrowing holds for the webhook
// transport too (ADR 0023 decision 1).
func TestWebhookRepositoryNarrowing(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 1, "alice", "evil/other")
	rec := post(t, router, "/github", "issues", body, "s3cret", "d1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("other repo: got %d, want 202 (acknowledged, ignored)", rec.Code)
	}
	if ts.count() != 0 {
		t.Errorf("delivery for an undeclared repository spawned %d thoughts, want 0", ts.count())
	}
}

// TestWebhookPrincipalAttribution: the thought acts as the subscription's
// declared service principal, never the sender's (ADR 0029 decision 8).
func TestWebhookPrincipalAttribution(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	body := issueOpenedBody(t, 1, "alice", "acme/widgets")
	post(t, router, "/github", "issues", body, "s3cret", "d1")
	if ts.count() != 1 {
		t.Fatalf("got %d thoughts, want 1", ts.count())
	}
	if got := ts.get(0).Metadata["principal"]; got != "connector:github" {
		t.Errorf("principal = %q, want connector:github (the default service principal)", got)
	}
}

// TestVerifySignatureUnit pins the verifier's edges.
func TestVerifySignatureUnit(t *testing.T) {
	body := []byte(`{"a":1}`)
	good := sign("k", body)
	if !verifySignature("k", body, good) {
		t.Error("valid signature rejected")
	}
	if verifySignature("k", body, "") {
		t.Error("empty signature accepted")
	}
	if verifySignature("k", body, "sha1=abc") {
		t.Error("wrong scheme accepted")
	}
	if verifySignature("other", body, good) {
		t.Error("wrong secret accepted")
	}
	if verifySignature("k", []byte(`{"a":2}`), good) {
		t.Error("tampered body accepted")
	}
}

// TestReadBoundedUnit pins the bound: exactly at the limit passes, one over
// fails.
func TestReadBoundedUnit(t *testing.T) {
	at := bytes.Repeat([]byte("x"), 10)
	if _, err := readBounded(bytes.NewReader(at), 10); err != nil {
		t.Errorf("exactly at bound: %v, want nil", err)
	}
	over := bytes.Repeat([]byte("x"), 11)
	if _, err := readBounded(bytes.NewReader(over), 10); err == nil {
		t.Error("over the bound: got nil, want an error")
	}
}

// TestSourceLimiterUnit pins the token bucket's refill.
func TestSourceLimiterUnit(t *testing.T) {
	l := newSourceLimiter(1, 2)
	now := time.Now()
	if !l.allow("a", now) || !l.allow("a", now) {
		t.Fatal("burst not allowed")
	}
	if l.allow("a", now) {
		t.Error("third immediate call allowed, want shed")
	}
	// After a second, one token has refilled.
	if !l.allow("a", now.Add(time.Second)) {
		t.Error("refilled token refused")
	}
	// A different source has its own bucket.
	if !l.allow("b", now) {
		t.Error("distinct source shares another's bucket")
	}
}

// TestClientSourceUnit pins the rate-limit key.
func TestClientSourceUnit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "10.0.0.9:5555"
	if got := clientSource(r); got != "10.0.0.9" {
		t.Errorf("clientSource = %q, want 10.0.0.9", got)
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	if got := clientSource(r); got != "203.0.113.7" {
		t.Errorf("forwarded clientSource = %q, want 203.0.113.7", got)
	}
}

// TestWebhookConcurrentDeliveries runs deliveries in parallel, so the shared
// seen-set, limiter, and router are exercised under -race on a capable host.
func TestWebhookConcurrentDeliveries(t *testing.T) {
	c, router, ts, start := webhookConnector(t, Config{
		Subscriptions: []Subscription{{Name: "sub", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}},
		Webhooks:      []WebhookBinding{{Name: "sub", SecretRefs: []string{"GH_SECRET"}, Burst: 1000}},
	}, map[string]string{"GH_SECRET": "s3cret"})
	defer c.Stop()
	start()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := issueOpenedBody(t, n, "alice", "acme/widgets")
			post(t, router, "/github", "issues", body, "s3cret", fmt.Sprintf("d%d", n))
		}(i)
	}
	wg.Wait()
	if ts.count() != 32 {
		t.Errorf("concurrent deliveries: got %d thoughts, want 32", ts.count())
	}
}

// fakeTS.get returns the queued thought at i.
func (f *fakeTS) get(i int) *thought.Thought {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.queued) {
		return nil
	}
	return f.queued[i]
}

// TestRepoMatchesCaseInsensitive: GitHub repository names are case-insensitive,
// so a declared "acme/widgets" matches a delivery naming "Acme/Widgets" rather
// than silently dropping it.
func TestRepoMatchesCaseInsensitive(t *testing.T) {
	if !repoMatches(Subscription{Repository: "acme/widgets"}, "Acme/Widgets") {
		t.Error("case-differing repository did not match")
	}
	if repoMatches(Subscription{Repository: "acme/widgets"}, "other/repo") {
		t.Error("different repository matched")
	}
	if !repoMatches(Subscription{}, "anything/at/all") {
		t.Error("an unscoped subscription should match any repository")
	}
}
