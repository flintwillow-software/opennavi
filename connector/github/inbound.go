package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
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
	// (ADR 0023 decision 2, ADR 0021 decision 10). Until the principal model
	// lands this is the recorded identity string; empty = "connector:github".
	Principal string
	// Prompt is the thought prompt for an event. Empty = a generated prompt.
	Prompt string
	// Priority is the enqueued thought's priority (1-10; 0 = 3).
	Priority int
	// Mention is the login (without "@") an issue_mention comment must name.
	// Required when Events includes EventIssueMention.
	Mention string
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
func (c *Connector) startInbound(ctx context.Context, deps connector.Deps) error {
	if len(c.cfg.Subscriptions) == 0 {
		return nil
	}
	if deps.ThoughtSystem == nil {
		return fmt.Errorf("github: inbound subscriptions require a thought system")
	}
	names := map[string]bool{}
	for _, s := range c.cfg.Subscriptions {
		if names[s.Name] {
			return fmt.Errorf("github: duplicate subscription %q", s.Name)
		}
		names[s.Name] = true
		if err := validateSubscription(s); err != nil {
			return fmt.Errorf("github: %w", err)
		}
	}
	seen, err := newSeenStore(c.stateDB)
	if err != nil {
		return err
	}
	if c.stateDB == nil && deps.Log != nil {
		deps.Log.Warn("github: inbound dedupe is in-memory only; a restart may replay a redelivery (wire WithStateStore)")
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
	// The first window looks back one interval so work that arrived while the
	// navi was down is picked up, not skipped.
	since := c.clock().Add(-interval)
	c.pollOnce(ctx, deps, seen, &since)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.pollOnce(ctx, deps, seen, &since)
		}
	}
}

// pollOnce runs one poll over every declared subscription and event kind.
func (c *Connector) pollOnce(ctx context.Context, deps connector.Deps, seen *seenStore, since *time.Time) {
	now := c.clock()
	cl, err := c.live.Get()
	if err != nil {
		return // not serving; the loop stops when the connector stops
	}
	for _, sub := range c.cfg.Subscriptions {
		for _, kind := range sub.Events {
			events, perr := c.pollSubscription(ctx, cl, sub, kind, *since)
			if perr != nil {
				if deps.Log != nil {
					deps.Log.Warn("github: poll failed", "subscription", sub.Name, "event", string(kind), "err", perr.Error())
				}
				continue
			}
			for _, ev := range events {
				if seen.has(sub.Name, ev.ID) {
					continue
				}
				if eerr := c.enqueueEvent(deps, sub, kind, ev, now); eerr != nil {
					if deps.Log != nil {
						deps.Log.Error("github: enqueue failed", "subscription", sub.Name, "event_id", ev.ID, "err", eerr.Error())
					}
					continue // not marked seen: retried next poll
				}
				seen.mark(sub.Name, ev.ID, now)
			}
		}
	}
	*since = now
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

// mentions reports whether a comment body names login (case-insensitively).
func mentions(body, login string) bool {
	if login == "" {
		return false
	}
	return strings.Contains(strings.ToLower(body), "@"+strings.ToLower(login))
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
	principal := strings.TrimSpace(sub.Principal)
	if principal == "" {
		principal = "connector:github"
	}
	t := &thought.Thought{
		ID:        fmt.Sprintf("github-%s-%s", sub.Name, ev.ID),
		Prompt:    prompt,
		Context:   ev.contextText(),
		Priority:  sub.Priority,
		Status:    thought.ThoughtPending,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Metadata: map[string]string{
			"connector":    "github",
			"subscription": sub.Name,
			"event":        string(kind),
			"repo":         ev.Repo,
			"event_id":     ev.ID,
			"principal":    principal,
		},
	}
	if ev.URL != "" {
		t.Metadata["url"] = ev.URL
	}
	return deps.ThoughtSystem.Enqueue(t)
}

// ── the persisted seen-set (ADR 0023 decision 5) ────────────────────────

// seenStore dedupes redelivered events. With a state store it is a SQLite table
// shared with the navi's session DB (the cron connector's pattern); without one
// it is an in-memory set, and a restart may replay a redelivery.
type seenStore struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string]struct{}
}

func newSeenStore(db *sql.DB) (*seenStore, error) {
	s := &seenStore{db: db, mem: map[string]struct{}{}}
	if db != nil {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS github_seen_events (
			subscription TEXT NOT NULL,
			event_id TEXT NOT NULL,
			seen_at DATETIME NOT NULL,
			PRIMARY KEY (subscription, event_id)
		)`); err != nil {
			return nil, fmt.Errorf("github: create github_seen_events: %w", err)
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

// listIssueComments returns repository comments updated since t (one page).
func (c *client) listIssueComments(ctx context.Context, repo string, since time.Time) ([]githubComment, error) {
	u := fmt.Sprintf("%s/repos/%s/issues/comments?per_page=100&sort=updated&direction=asc&since=%s",
		c.base, repo, url.QueryEscape(since.UTC().Format(time.RFC3339)))
	var out []githubComment
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// listIssues returns repository issues updated since t (one page), newest last.
func (c *client) listIssues(ctx context.Context, repo string, since time.Time) ([]githubIssue, error) {
	u := fmt.Sprintf("%s/repos/%s/issues?per_page=100&state=all&sort=updated&direction=asc&since=%s",
		c.base, repo, url.QueryEscape(since.UTC().Format(time.RFC3339)))
	var out []githubIssue
	if err := c.getJSON(ctx, u, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getJSON performs an authenticated GET and decodes the response. The token is
// sent as a header and never appears in the URL or an error; the response body
// is not echoed into an error, since it may contain repository content.
func (c *client) getJSON(ctx context.Context, url string, out any) error {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("github: build poll request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "opennavi")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: poll: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPollBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("github: poll: http %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("github: decode poll response: %w", err)
	}
	return nil
}
