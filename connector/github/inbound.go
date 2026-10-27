package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the GitHub connector's inbound half (ADR 0023): the build-time
// subscription declaration and the poll loop that turns a declared event into a
// thought. It is the other half of ADR 0014's bidirectional connector — the
// egress half lives in github.go.
//
// The governing decisions are ADR 0023's:
//
//   - Subscriptions are build-time declarations; runtime may narrow, never add.
//   - Delivery transport is a per-connector binding. Polling is the first
//     GitHub binding because it needs no public reachability (ADR 0023 d4).
//   - Idempotency is explicit and persisted: a redelivered event must not spawn
//     a second thought (ADR 0023 d5).
//   - The inbound loop consumes the SAME client the egress tools use, published
//     into connector.Live at Start (ADR 0023 d6).

// DefaultPollInterval is how often a subscription polls when the connector does
// not declare one. It is deliberately unhurried: polling is the reachability-free
// binding, not a low-latency one.
const DefaultPollInterval = 60 * time.Second

// maxPollBody bounds how much of a poll response is read.
const maxPollBody = 1 << 20

// maxPollPages bounds how many pages one subscription poll follows, so a
// pathological Link chain cannot make a single cycle unbounded. Exceeding it is
// an error rather than a silent truncation: the subscription's watermark does
// not advance, so the next cycle re-reads the same window instead of dropping
// the tail.
const maxPollPages = 100

// EventKind is a declared inbound event kind (ADR 0023 decision 1). Only the
// kinds a subscription names are ever polled; runtime cannot add one.
type EventKind string

const (
	// EventIssueMention fires for a comment that mentions the configured login.
	EventIssueMention EventKind = "issue_mention"
	// EventIssueOpened fires for a newly opened issue.
	EventIssueOpened EventKind = "issue_opened"
)

// Intent is what an inbound event does to the queue (ADR 0023 decision 8). It
// reuses the lifecycle vocabulary rather than inventing one; this binding
// implements spawn, and refuses a declaration it cannot serve rather than
// silently dropping the event.
type Intent string

const (
	// IntentSpawn queues a new thought.
	IntentSpawn Intent = "spawn"
	// IntentAttach attaches an event to an in-flight thought.
	IntentAttach Intent = "attach"
	// IntentSteer steers a running thought.
	IntentSteer Intent = "steer"
)

// Subscription is one build-time inbound declaration (ADR 0023 decisions 1-2).
// It is declared where the connector is composed, visible in main.go the way
// egress tools are.
type Subscription struct {
	// Name uniquely identifies the subscription (also the dedupe key scope).
	Name string
	// Repository is the "owner/name" this subscription watches.
	Repository string
	// Events are the event kinds to poll. Empty = no events (a no-op).
	Events []EventKind
	// Intent is what an event does to the queue. Empty = IntentSpawn.
	Intent Intent
	// Principal is the service principal the resulting thought acts as
	// (ADR 0023 decision 2, ADR 0021 decision 10). A validated principal
	// reference. The zero Ref selects the connector's own principal,
	// connector:github: a connector may act as its own principal (ADR 0023
	// decision 2), which attributes inbound work to the connector rather
	// than laundering it into the navi's service principal. Declare
	// navi:<name> for work that must act as the navi itself (ADR 0021
	// decision 10); the runtime's own machinery (cron, the plan-closing
	// check) does not come through a subscription and acts as navi:<name>
	// regardless. A refused enqueue is logged and retried, never dropped
	// silently (see the poll loop).
	Principal principal.Ref
	// Prompt is the thought prompt for an event. Empty = a generated prompt.
	Prompt string
	// Priority is an ADVISORY priority hint for the thoughts this
	// subscription spawns (ADR 0035 decision 5, ADR 0036 decision 2). Nil
	// (unset) lands at connector.DefaultPriority; a value is used verbatim,
	// including a deliberate 0 for the urgent band. GitHub reports no
	// per-issue priority, so the subscription's hint is the whole signal; a
	// hint is a starting position, not a verdict — only the running thought
	// can see the queue and revise it.
	Priority connector.PriorityHint
	// Mention is the login (without "@") an issue_mention comment must name.
	// Required when Events includes EventIssueMention.
	Mention string
}

// declares reports whether the subscription names an event kind. The webhook
// binding uses it too, so both transports agree on what a subscription serves.
func (s Subscription) declares(k EventKind) bool {
	for _, e := range s.Events {
		if e == k {
			return true
		}
	}
	return false
}

// inboundEvent is one polled stimulus, normalized across event kinds.
type inboundEvent struct {
	ID     string
	Repo   string
	Title  string
	Body   string
	URL    string
	Author string
}

func (e inboundEvent) contextText() string {
	var b strings.Builder
	if e.Repo != "" {
		fmt.Fprintf(&b, "repository: %s\n", e.Repo)
	}
	if e.Author != "" {
		fmt.Fprintf(&b, "author: %s\n", e.Author)
	}
	if e.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", e.Title)
	}
	if e.URL != "" {
		fmt.Fprintf(&b, "url: %s\n", e.URL)
	}
	if e.Body != "" {
		fmt.Fprintf(&b, "\n%s", e.Body)
	}
	return strings.TrimSpace(b.String())
}

// validateSubscription rejects a declaration this binding cannot serve, at
// composition time rather than by silently dropping events at runtime.
func validateSubscription(s Subscription) error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("subscription requires a name")
	}
	if s.Priority != nil && !connector.ValidPriority(*s.Priority) {
		return fmt.Errorf("subscription %q: priority %d is outside the 0..10 scale", s.Name, *s.Priority)
	}
	if !strings.Contains(s.Repository, "/") {
		return fmt.Errorf("subscription %q: repository %q must be owner/name", s.Name, s.Repository)
	}
	intent := s.Intent
	if intent == "" {
		intent = IntentSpawn
	}
	if intent != IntentSpawn {
		return fmt.Errorf("subscription %q: intent %q is declared but not served by the polling binding", s.Name, intent)
	}
	for _, k := range s.Events {
		switch k {
		case EventIssueMention:
			if strings.TrimSpace(s.Mention) == "" {
				return fmt.Errorf("subscription %q: event %q requires Mention", s.Name, k)
			}
		case EventIssueOpened:
		default:
			return fmt.Errorf("subscription %q: unknown event kind %q", s.Name, k)
		}
	}
	return nil
}

// startInbound validates the declared subscriptions and, when there are any,
// launches the poll loop bound to the navi's thought system. A declaration the
// binding cannot serve fails Start (and thus the navi's run), so a
// misconfiguration is visible at composition rather than as missing work.
func (c *Connector) startInbound(ctx context.Context, deps connector.Deps, seen *seenStore) error {
	if len(c.cfg.Subscriptions) == 0 {
		return nil
	}
	if deps.ThoughtSystem == nil {
		return fmt.Errorf("github: inbound subscriptions require a thought system")
	}
	interval := c.cfg.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	pollCtx, cancel := context.WithCancel(ctx)
	c.pollCancel = cancel
	c.pollWG.Add(1)
	go func() {
		defer c.pollWG.Done()
		c.pollLoop(pollCtx, deps, seen, interval)
	}()
	return nil
}

// pollLoop polls immediately, then on the interval, until ctx is cancelled.
func (c *Connector) pollLoop(ctx context.Context, deps connector.Deps, seen *seenStore, interval time.Duration) {
	c.pollOnce(ctx, deps, seen, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.pollOnce(ctx, deps, seen, interval)
		}
	}
}

// pollOnce runs one poll over every declared subscription and event kind.
//
// The watermark advances per subscription only when that subscription's whole
// cycle succeeded: a failed fetch or a failed enqueue leaves it in place, so
// the next poll re-reads the same window and a transient failure does not drop
// it (a failed event is not marked seen either, so it is retried). interval is
// the fallback lookback for a subscription that has never polled successfully —
// a first run, or a restart whose persisted watermark is absent — so downtime
// longer than one interval is recovered from the stored watermark, not lost.
func (c *Connector) pollOnce(ctx context.Context, deps connector.Deps, seen *seenStore, interval time.Duration) {
	now := c.clock()
	cl, err := c.live.Get()
	if err != nil {
		return // not serving; the loop stops when the connector stops
	}
	self := c.selfLogin(ctx, cl, deps)
	for _, sub := range c.cfg.Subscriptions {
		since := seen.watermark(sub.Name, now.Add(-interval))
		ok := true
		for _, kind := range sub.Events {
			events, perr := c.pollSubscription(ctx, cl, sub, kind, since)
			if perr != nil {
				ok = false
				if deps.Log != nil {
					deps.Log.Warn("github: poll failed", "subscription", sub.Name, "event", string(kind), "err", perr.Error())
				}
				continue
			}
			for _, ev := range events {
				if self != "" && strings.EqualFold(ev.Author, self) {
					continue // the navi's own egress cannot re-enter as an event
				}
				if seen.has(sub.Name, ev.ID) {
					continue
				}
				if eerr := c.enqueueEvent(deps, sub, kind, ev, now); eerr != nil {
					if errors.Is(eerr, thought.ErrDuplicateID) {
						// The deterministic thought id already exists: the seen-set
						// was lost but the queue survived, so the event has been
						// handled. Mark it seen rather than retrying it every poll.
						seen.mark(sub.Name, ev.ID, now)
						continue
					}
					ok = false
					if deps.Log != nil {
						deps.Log.Error("github: enqueue failed", "subscription", sub.Name, "event_id", ev.ID, "err", eerr.Error())
					}
					continue // not marked seen: retried next poll
				}
				seen.mark(sub.Name, ev.ID, now)
			}
		}
		if ok {
			seen.setWatermark(sub.Name, now)
		}
	}
}

// selfLogin resolves the login this connector posts as, so a comment the navi
// itself wrote (via egress) that mentions the watched login cannot re-enter as
// an event and loop. Config.SelfLogin wins; otherwise the account's own login
// is read from GET /user once and cached. A resolution failure is not fatal —
// polling continues with the filter off, warned once — and is retried on the
// next poll so a transient failure does not disable the filter for the run.
func (c *Connector) selfLogin(ctx context.Context, cl *client, deps connector.Deps) string {
	c.selfMu.Lock()
	if c.selfResolved {
		login := c.selfLoginVal
		c.selfMu.Unlock()
		return login
	}
	c.selfMu.Unlock()

	if login := strings.TrimSpace(c.cfg.SelfLogin); login != "" {
		c.selfMu.Lock()
		c.selfLoginVal, c.selfResolved = login, true
		c.selfMu.Unlock()
		return login
	}
	resolved, err := cl.myLogin(ctx)
	if err != nil {
		c.selfMu.Lock()
		warn := !c.selfWarned
		c.selfWarned = true
		c.selfMu.Unlock()
		if warn && deps.Log != nil {
			deps.Log.Warn("github: cannot resolve the connector's own login; self-filter is off until it resolves (set Config.SelfLogin)", "err", err.Error())
		}
		return ""
	}
	c.selfMu.Lock()
	c.selfLoginVal, c.selfResolved = resolved, true
	c.selfMu.Unlock()
	return resolved
}

// pollSubscription fetches the events of one kind for one subscription.
func (c *Connector) pollSubscription(ctx context.Context, cl *client, sub Subscription, kind EventKind, since time.Time) ([]inboundEvent, error) {
	switch kind {
	case EventIssueMention:
		comments, err := cl.listIssueComments(ctx, sub.Repository, since)
		if err != nil {
			return nil, err
		}
		var out []inboundEvent
		for _, cm := range comments {
			if !mentions(cm.Body, sub.Mention) {
				continue
			}
			out = append(out, inboundEvent{
				ID:     fmt.Sprintf("comment-%d", cm.ID),
				Repo:   sub.Repository,
				Body:   cm.Body,
				URL:    cm.HTMLURL,
				Author: cm.User.Login,
			})
		}
		return out, nil
	case EventIssueOpened:
		issues, err := cl.listIssues(ctx, sub.Repository, since)
		if err != nil {
			return nil, err
		}
		var out []inboundEvent
		for _, is := range issues {
			if !is.CreatedAt.After(since) {
				continue // only newly opened, not merely updated
			}
			out = append(out, inboundEvent{
				ID:     fmt.Sprintf("issue-%d", is.Number),
				Repo:   sub.Repository,
				Title:  is.Title,
				Body:   is.Body,
				URL:    is.HTMLURL,
				Author: is.User.Login,
			})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("github: unknown event kind %q", kind)
	}
}

// mentions reports whether a comment body names login as a word: `@alice`
// matches `@alice` and `@alice,` but not `@alice-bot` or `bob@alice`, and a
// mention inside a fenced code block, an inline code span, or a block quote is
// not a mention at all (it is quoted text, not a request to the navi).
func mentions(body, login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	text := strings.ToLower(stripMentionNoise(body))
	for i := 0; ; {
		j := strings.IndexByte(text[i:], '@')
		if j < 0 {
			return false
		}
		i += j
		// The @ must start a word: an @ preceded by a login character is an
		// email address or a longer handle, not a mention.
		if i > 0 && isLoginChar(text[i-1]) {
			i++
			continue
		}
		rest := text[i+1:]
		if !strings.HasPrefix(rest, login) {
			i++
			continue
		}
		// The login must end at a word boundary: a following login character
		// means this is a longer handle (`@alice-bot`).
		if len(rest) > len(login) && isLoginChar(rest[len(login)]) {
			i++
			continue
		}
		return true
	}
}

// isLoginChar reports whether b can appear in a GitHub login. A mention ends at
// any other byte, which is what makes the match word-bounded.
func isLoginChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-'
}

// stripMentionNoise blanks the regions of a Markdown comment where an @-token
// is not a mention: fenced code blocks (``` or ~~~), inline code spans, and
// block quotes. Removed regions are replaced with spaces so the words around
// them stay separated.
func stripMentionNoise(body string) string {
	var b strings.Builder
	b.Grow(len(body))
	inFence := false
	fence := ""
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			b.WriteByte('\n')
			continue
		}
		if marker := fenceMarker(trimmed); marker != "" {
			inFence, fence = true, marker
			b.WriteByte('\n')
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			b.WriteByte('\n') // a quoted mention is not a mention
			continue
		}
		b.WriteString(stripInlineCode(line))
		b.WriteByte('\n')
	}
	return b.String()
}

// fenceMarker returns the fence marker a line opens a code block with ("```"
// or "~~~"), or "" when the line is not a fence.
func fenceMarker(trimmed string) string {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```"
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~"
	}
	return ""
}

// stripInlineCode blanks `code spans` in one line.
func stripInlineCode(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	inCode := false
	for i := 0; i < len(line); i++ {
		if line[i] == '`' {
			inCode = !inCode
			b.WriteByte(' ')
			continue
		}
		if inCode {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(line[i])
	}
	return b.String()
}

// enqueueEvent turns one stimulus into a thought (ADR 0023 decision 8) and
// enqueues it. The thought id is deterministic from the subscription and the
// event id, so a redelivery that slips past the seen-set still collides on the
// queue rather than spawning a second thought.
func (c *Connector) enqueueEvent(deps connector.Deps, sub Subscription, kind EventKind, ev inboundEvent, now time.Time) error {
	prompt := strings.TrimSpace(sub.Prompt)
	if prompt == "" {
		if ev.Title != "" {
			prompt = fmt.Sprintf("GitHub %s in %s: %s", kind, ev.Repo, ev.Title)
		} else {
			prompt = fmt.Sprintf("GitHub %s in %s", kind, ev.Repo)
		}
	}
	actor := sub.Principal
	if actor.IsZero() {
		ref, err := principal.New(principal.KindConnector, "github")
		if err != nil {
			return fmt.Errorf("github: default service principal: %w", err)
		}
		actor = ref
	}
	t := &thought.Thought{
		ID:     fmt.Sprintf("github-%s-%s", sub.Name, ev.ID),
		Prompt: prompt,
		// GitHub states no per-issue priority, so the subscription's hint is
		// the signal (ADR 0035 decision 5). Advisory: the triaging thought
		// may revise it once it has seen the queue.
		Priority:  connector.ResolvePriority(sub.Priority),
		Context:   ev.contextText(),
		Status:    thought.ThoughtPending,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Metadata: map[string]string{
			"connector":    "github",
			"subscription": sub.Name,
			"event":        string(kind),
			"repo":         ev.Repo,
			"event_id":     ev.ID,
			"principal":    actor.String(),
		},
	}
	if ev.URL != "" {
		t.Metadata["url"] = ev.URL
	}
	return deps.ThoughtSystem.Enqueue(t)
}

// ── the persisted seen-set (ADR 0023 decision 5) ────────────────────────

// seenStore dedupes redelivered events and remembers each subscription's
// watermark. With a state store it is a SQLite table shared with the navi's
// session DB (the cron connector's pattern); without one it is an in-memory
// set, and a restart resumes from the last interval rather than from the
// watermark.
type seenStore struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string]struct{}
	// watermarks remembers each subscription's last successful poll time so a
	// restart resumes from it instead of re-scanning a fixed lookback window.
	watermarks map[string]time.Time
}

func newSeenStore(db *sql.DB) (*seenStore, error) {
	s := &seenStore{db: db, mem: map[string]struct{}{}, watermarks: map[string]time.Time{}}
	if db != nil {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS github_seen_events (
			subscription TEXT NOT NULL,
			event_id TEXT NOT NULL,
			seen_at DATETIME NOT NULL,
			PRIMARY KEY (subscription, event_id)
		)`); err != nil {
			return nil, fmt.Errorf("github: create github_seen_events: %w", err)
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS github_poll_watermark (
			subscription TEXT PRIMARY KEY,
			since DATETIME NOT NULL
		)`); err != nil {
			return nil, fmt.Errorf("github: create github_poll_watermark: %w", err)
		}
	}
	return s, nil
}

func (s *seenStore) has(sub, id string) bool {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.mem[sub+"\x00"+id]
		return ok
	}
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM github_seen_events WHERE subscription = ? AND event_id = ?`, sub, id).Scan(&one)
	return err == nil
}

func (s *seenStore) mark(sub, id string, at time.Time) {
	if s.db == nil {
		s.mu.Lock()
		s.mem[sub+"\x00"+id] = struct{}{}
		s.mu.Unlock()
		return
	}
	_, _ = s.db.Exec(`INSERT OR IGNORE INTO github_seen_events (subscription, event_id, seen_at) VALUES (?, ?, ?)`,
		sub, id, at.UTC().Format(time.RFC3339Nano))
}

// watermark returns the subscription's last successful poll time, or fallback
// when it has never polled successfully (a first run, or a restart before the
// first success).
func (s *seenStore) watermark(sub string, fallback time.Time) time.Time {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if at, ok := s.watermarks[sub]; ok {
			return at
		}
		return fallback
	}
	var raw any
	if err := s.db.QueryRow(`SELECT since FROM github_poll_watermark WHERE subscription = ?`, sub).Scan(&raw); err != nil {
		return fallback
	}
	switch v := raw.(type) {
	case time.Time:
		return v.UTC()
	case string:
		if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return at.UTC()
		}
	case []byte:
		if at, err := time.Parse(time.RFC3339Nano, string(v)); err == nil {
			return at.UTC()
		}
	}
	return fallback
}

// setWatermark records a successful poll. A write failure is not fatal to the
// poll: the next cycle falls back to the lookback window rather than losing
// events.
func (s *seenStore) setWatermark(sub string, at time.Time) {
	at = at.UTC()
	if s.db == nil {
		s.mu.Lock()
		s.watermarks[sub] = at
		s.mu.Unlock()
		return
	}
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO github_poll_watermark (subscription, since) VALUES (?, ?)`,
		sub, at.Format(time.RFC3339Nano))
}

// ── read-side API calls (the shared client, ADR 0023 decision 6) ────────

type githubUser struct {
	Login string `json:"login"`
}

type githubComment struct {
	ID      int64      `json:"id"`
	Body    string     `json:"body"`
	HTMLURL string     `json:"html_url"`
	User    githubUser `json:"user"`
}

type githubIssue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	HTMLURL   string     `json:"html_url"`
	User      githubUser `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
}

// listIssueComments returns repository comments updated since t, following
// pagination so a burst larger than one page is not silently truncated.
func (c *client) listIssueComments(ctx context.Context, repo string, since time.Time) ([]githubComment, error) {
	next := fmt.Sprintf("%s/repos/%s/issues/comments?per_page=100&sort=updated&direction=asc&since=%s",
		c.base, repo, url.QueryEscape(since.UTC().Format(time.RFC3339)))
	var out []githubComment
	for page := 0; next != ""; page++ {
		if page >= maxPollPages {
			return nil, fmt.Errorf("github: poll %s: more than %d pages of comments since %s",
				repo, maxPollPages, since.UTC().Format(time.RFC3339))
		}
		var chunk []githubComment
		link, err := c.getJSONPage(ctx, next, &chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		next = link
	}
	return out, nil
}

// listIssues returns repository issues updated since t, newest last, following
// pagination so a burst larger than one page is not silently truncated.
func (c *client) listIssues(ctx context.Context, repo string, since time.Time) ([]githubIssue, error) {
	next := fmt.Sprintf("%s/repos/%s/issues?per_page=100&state=all&sort=updated&direction=asc&since=%s",
		c.base, repo, url.QueryEscape(since.UTC().Format(time.RFC3339)))
	var out []githubIssue
	for page := 0; next != ""; page++ {
		if page >= maxPollPages {
			return nil, fmt.Errorf("github: poll %s: more than %d pages of issues since %s",
				repo, maxPollPages, since.UTC().Format(time.RFC3339))
		}
		var chunk []githubIssue
		link, err := c.getJSONPage(ctx, next, &chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		next = link
	}
	return out, nil
}

// myLogin is the login the connector's credential authenticates as. It is the
// self-filter signal: an event this account authored is the navi's own egress,
// not a new stimulus.
func (c *client) myLogin(ctx context.Context) (string, error) {
	var u githubUser
	if _, err := c.getJSONPage(ctx, c.base+"/user", &u); err != nil {
		return "", err
	}
	if strings.TrimSpace(u.Login) == "" {
		return "", errors.New("github: GET /user returned no login")
	}
	return u.Login, nil
}

// getJSONPage performs an authenticated GET and decodes one page, returning the
// Link header's rel="next" URL (empty when this is the last page). The token is
// sent as a header and never appears in the URL or an error; the response body
// is not echoed into an error, since it may contain repository content.
func (c *client) getJSONPage(ctx context.Context, url string, out any) (string, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("github: build poll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "opennavi")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: poll: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPollBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("github: poll: http %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("github: decode poll response: %w", err)
	}
	return linkNext(resp.Header.Get("Link")), nil
}

// linkNext returns the rel="next" URL from a GitHub Link header, or "" when
// there is no next page. The header is a comma-separated list of
// `<url>; rel="name"` entries.
func linkNext(header string) string {
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(entry, ";")
		if len(parts) < 2 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, attr := range parts[1:] {
			attr = strings.TrimSpace(attr)
			if attr == `rel="next"` || attr == "rel=next" {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}
