package huly

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// This file is the Huly connector's inbound half (ADR 0023): the build-time
// subscription declaration and the poll loop that turns a declared event into a
// thought. It is the other half of ADR 0014's bidirectional connector — the
// egress half lives in huly.go.
//
// The governing decisions are ADR 0023's:
//
//   - Subscriptions are build-time declarations; runtime may narrow, never add.
//   - Delivery transport is a per-connector binding. Polling is the first
//     binding because it needs no public reachability (ADR 0023 d4); the
//     webhook binding lands on ADR 0029's seam.
//   - Idempotency is explicit and persisted: a redelivered event must not spawn
//     a second thought (ADR 0023 d5).
//   - The inbound loop consumes the SAME client the egress tools use (ADR 0023
//     d6).
//
// # The pick-up path
//
// `issue_assigned` is the path ADR 0031's claim model rests on: a human assigns
// an issue to this navi's Huly user and the navi picks it up. "This navi's Huly
// user" is the Person document the account resolves to — the id an issue's
// `assignee` references, which is neither the account uuid nor the social id
// (see lookupPersonID).

// DefaultPollInterval is how often a subscription polls when the connector does
// not declare one. It is deliberately unhurried: polling is the reachability-free
// binding, not a low-latency one.
const DefaultPollInterval = 60 * time.Second

// EventKind is a declared inbound event kind (ADR 0023 decision 1). Only the
// kinds a subscription names are ever polled; runtime cannot add one.
type EventKind string

const (
	// EventIssueAssigned fires for an issue assigned to this navi's Huly user —
	// the pick-up path.
	EventIssueAssigned EventKind = "issue_assigned"
	// EventIssueMentioned fires for a comment that mentions this navi's Huly
	// user.
	EventIssueMentioned EventKind = "issue_mentioned"
	// EventIssueStatusChanged fires when an issue the navi owns moves status.
	EventIssueStatusChanged EventKind = "issue_status_changed"
	// EventCommentOnMyIssue fires for a comment on an issue assigned to the
	// navi.
	EventCommentOnMyIssue EventKind = "comment_on_my_issue"
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
	// Project is the project identifier (e.g. "NAVI") whose issues the
	// assignment and status-change kinds poll. Empty = every project. A
	// mention or comment kind is not project-scoped by this binding: a mention
	// is a mention wherever it lands.
	Project string
	// Events are the event kinds to poll. Empty = no events (a no-op).
	Events []EventKind
	// Intent is what an event does to the queue. Empty = IntentSpawn.
	Intent Intent
	// Principal is the service principal the resulting thought acts as
	// (ADR 0023 decision 2, ADR 0021 decision 10). The zero Ref selects the
	// connector's own principal, connector:huly. Declare navi:<name> for work
	// that must act as the navi itself.
	Principal principal.Ref
	// Prompt is the thought prompt for an event. Empty = a generated prompt.
	Prompt string
	// Priority is the enqueued thought's priority (1-10; 0 = 3).
	Priority int
}

// declares reports whether the subscription names an event kind.
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
	Kind       EventKind
	ID         string
	IssueID    string
	Identifier string
	Title      string
	Body       string
	Author     string
	Status     string
}

// dedupeKey is the event identity the seen-set is keyed on (ADR 0023 decision
// 5). A status change includes the new status, so A→B and B→C are two events
// while a replay of the same change is one.
func (e inboundEvent) dedupeKey() string {
	switch e.Kind {
	case EventIssueAssigned:
		return "assigned:" + e.IssueID
	case EventIssueStatusChanged:
		return "status:" + e.IssueID + ":" + e.Status
	case EventIssueMentioned:
		return "mention:" + e.ID
	case EventCommentOnMyIssue:
		return "comment:" + e.ID
	default:
		return string(e.Kind) + ":" + e.ID
	}
}

func (e inboundEvent) contextText() string {
	var b strings.Builder
	if e.Identifier != "" {
		fmt.Fprintf(&b, "issue: %s\n", e.Identifier)
	}
	if e.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", e.Title)
	}
	if e.Status != "" {
		fmt.Fprintf(&b, "status: %s\n", e.Status)
	}
	if e.Author != "" {
		fmt.Fprintf(&b, "author: %s\n", e.Author)
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
	intent := s.Intent
	if intent == "" {
		intent = IntentSpawn
	}
	if intent != IntentSpawn {
		return fmt.Errorf("subscription %q: intent %q is declared but not served by the polling binding", s.Name, intent)
	}
	for _, k := range s.Events {
		switch k {
		case EventIssueAssigned, EventIssueMentioned, EventIssueStatusChanged, EventCommentOnMyIssue:
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
		return fmt.Errorf("huly: inbound subscriptions require a thought system")
	}
	names := map[string]bool{}
	for _, s := range c.cfg.Subscriptions {
		if names[s.Name] {
			return fmt.Errorf("huly: duplicate subscription %q", s.Name)
		}
		names[s.Name] = true
		if err := validateSubscription(s); err != nil {
			return fmt.Errorf("huly: %w", err)
		}
	}
	seen, err := newSeenStore(c.stateDB)
	if err != nil {
		return err
	}
	if c.stateDB == nil && deps.Log != nil {
		deps.Log.Warn("huly: inbound dedupe is in-memory only; a restart may replay a redelivery (wire WithStateStore)")
	}
	// An account with no Person record has no "my issues" to poll; surface it
	// once rather than as a silent no-op every cycle.
	if cl, err := c.live.Get(); err == nil {
		if pid, _ := cl.myPersonID(ctx); pid == "" && deps.Log != nil {
			deps.Log.Warn("huly: the account has no Person record; inbound has no \"my issues\" to poll")
		}
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
	// The first comment window looks back one interval so work that arrived
	// while the navi was down is picked up, not skipped.
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

// pollOnce runs one poll over every declared subscription.
func (c *Connector) pollOnce(ctx context.Context, deps connector.Deps, seen *seenStore, since *time.Time) {
	now := c.clock()
	cl, err := c.live.Get()
	if err != nil {
		return // not serving; the loop stops when the connector stops
	}
	personID, err := cl.myPersonID(ctx)
	if err != nil || personID == "" {
		return // reported at start; nothing to poll
	}
	for _, sub := range c.cfg.Subscriptions {
		events, perr := c.pollSubscription(ctx, cl, seen, sub, personID, *since)
		if perr != nil {
			if deps.Log != nil {
				deps.Log.Warn("huly: poll failed", "subscription", sub.Name, "err", perr.Error())
			}
			continue
		}
		for _, ev := range events {
			if !sub.declares(ev.Kind) {
				continue
			}
			key := ev.dedupeKey()
			if seen.has(sub.Name, key) {
				continue
			}
			if eerr := c.enqueueEvent(deps, sub, ev, now); eerr != nil {
				if deps.Log != nil {
					deps.Log.Error("huly: enqueue failed", "subscription", sub.Name, "event", string(ev.Kind), "err", eerr.Error())
				}
				continue // not marked seen: retried next poll
			}
			seen.mark(sub.Name, key, now)
		}
	}
	*since = now
}

// pollSubscription fetches the events of one subscription. Assigned issues are
// polled once per subscription because they feed three kinds: a first sighting
// is an assignment, a changed status is a status change, and the set is what
// "my issue" means for a comment.
func (c *Connector) pollSubscription(ctx context.Context, cl *Client, seen *seenStore, sub Subscription, personID string, since time.Time) ([]inboundEvent, error) {
	var out []inboundEvent

	issues, err := cl.listAssignedIssues(ctx, personID, sub.Project)
	if err != nil {
		return nil, err
	}
	mine := make(map[string]bool, len(issues))
	for _, is := range issues {
		id := stringField(is, "_id")
		mine[id] = true
		status := stringField(is, "status")
		prev, known := seen.statusOf(id)
		if !known {
			seen.setStatus(id, status, c.clock())
			out = append(out, issueEvent(EventIssueAssigned, is))
			continue
		}
		if prev != status {
			seen.setStatus(id, status, c.clock())
			out = append(out, issueEvent(EventIssueStatusChanged, is))
		}
	}

	if sub.declares(EventIssueMentioned) || sub.declares(EventCommentOnMyIssue) {
		comments, err := cl.listCommentsSince(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, cm := range comments {
			raw := stringField(cm, "message")
			text := proseMirrorText(raw)
			author := stringField(cm, "createdBy")
			issueID := stringField(cm, "attachedTo")
			if sub.declares(EventIssueMentioned) && mentionsPerson(raw, personID) {
				out = append(out, commentEvent(EventIssueMentioned, cm, text, author))
			}
			if sub.declares(EventCommentOnMyIssue) && mine[issueID] {
				out = append(out, commentEvent(EventCommentOnMyIssue, cm, text, author))
			}
		}
	}
	return out, nil
}

// issueEvent projects an issue document to a normalized event.
func issueEvent(kind EventKind, is map[string]any) inboundEvent {
	ev := inboundEvent{
		Kind:       kind,
		ID:         stringField(is, "_id"),
		IssueID:    stringField(is, "_id"),
		Identifier: stringField(is, "identifier"),
		Title:      stringField(is, "title"),
		Status:     stringField(is, "status"),
	}
	if kind == EventIssueStatusChanged {
		// Two different status changes on one issue are two events.
		ev.ID = stringField(is, "_id") + ":" + ev.Status
	}
	return ev
}

// commentEvent projects a comment document to a normalized event.
func commentEvent(kind EventKind, cm map[string]any, text, author string) inboundEvent {
	return inboundEvent{
		Kind:    kind,
		ID:      stringField(cm, "_id"),
		IssueID: stringField(cm, "attachedTo"),
		Body:    text,
		Author:  author,
	}
}

// mentionsPerson reports whether a comment's raw ProseMirror markup names the
// person. A Huly mention embeds the person's document id in the markup (the UI
// resolves it from there), so the id is the reliable signal — the display name
// is not, and the exact node shape is not part of the contract.
func mentionsPerson(raw, personID string) bool {
	if strings.TrimSpace(personID) == "" {
		return false
	}
	return strings.Contains(raw, personID)
}

// enqueueEvent turns one stimulus into a thought (ADR 0023 decision 8) and
// enqueues it. The thought id is deterministic from the subscription, the kind,
// and the event id, so a redelivery that slips past the seen-set still collides
// on the queue rather than spawning a second thought.
func (c *Connector) enqueueEvent(deps connector.Deps, sub Subscription, ev inboundEvent, now time.Time) error {
	prompt := strings.TrimSpace(sub.Prompt)
	if prompt == "" {
		prompt = defaultPrompt(ev)
	}
	actor := sub.Principal
	if actor.IsZero() {
		ref, err := principal.New(principal.KindConnector, "huly")
		if err != nil {
			return fmt.Errorf("huly: default service principal: %w", err)
		}
		actor = ref
	}
	t := &thought.Thought{
		ID:        fmt.Sprintf("huly-%s-%s-%s", sub.Name, ev.Kind, ev.ID),
		Prompt:    prompt,
		Context:   ev.contextText(),
		Priority:  sub.Priority,
		Status:    thought.ThoughtPending,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Metadata: map[string]string{
			"connector":    "huly",
			"subscription": sub.Name,
			"event":        string(ev.Kind),
			"principal":    actor.String(),
		},
	}
	if ev.IssueID != "" {
		t.Metadata["issue_id"] = ev.IssueID
	}
	if ev.Identifier != "" {
		t.Metadata["identifier"] = ev.Identifier
	}
	return deps.ThoughtSystem.Enqueue(t)
}

// defaultPrompt is the thought prompt for an event when the subscription does
// not declare one.
func defaultPrompt(ev inboundEvent) string {
	name := ev.Identifier
	if name == "" {
		name = ev.IssueID
	}
	switch ev.Kind {
	case EventIssueAssigned:
		return fmt.Sprintf("Huly issue assigned to you: %s — %s", name, ev.Title)
	case EventIssueStatusChanged:
		return fmt.Sprintf("Huly issue %s moved to status %s", name, ev.Status)
	case EventIssueMentioned:
		return fmt.Sprintf("You were mentioned in Huly on %s", name)
	case EventCommentOnMyIssue:
		return fmt.Sprintf("New comment on your Huly issue %s", name)
	default:
		return fmt.Sprintf("Huly event %s on %s", ev.Kind, name)
	}
}

// ── the persisted seen-set and status baseline (ADR 0023 decision 5) ────

// seenStore dedupes redelivered events and remembers the last status seen for
// an issue. With a state store it is SQLite shared with the navi's session DB;
// without one it is in memory, and a restart may replay a redelivery.
type seenStore struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string]struct{}
	// statuses remembers each issue's last-seen status so a change is a
	// transition, not a first sighting.
	statuses map[string]string
}

func newSeenStore(db *sql.DB) (*seenStore, error) {
	s := &seenStore{db: db, mem: map[string]struct{}{}, statuses: map[string]string{}}
	if db != nil {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS huly_seen_events (
			subscription TEXT NOT NULL,
			event_id TEXT NOT NULL,
			seen_at DATETIME NOT NULL,
			PRIMARY KEY (subscription, event_id)
		)`); err != nil {
			return nil, fmt.Errorf("huly: create huly_seen_events: %w", err)
		}
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS huly_issue_status (
			issue_id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			updated_at DATETIME NOT NULL
		)`); err != nil {
			return nil, fmt.Errorf("huly: create huly_issue_status: %w", err)
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
	err := s.db.QueryRow(`SELECT 1 FROM huly_seen_events WHERE subscription = ? AND event_id = ?`, sub, id).Scan(&one)
	return err == nil
}

func (s *seenStore) mark(sub, id string, at time.Time) {
	if s.db == nil {
		s.mu.Lock()
		s.mem[sub+"\x00"+id] = struct{}{}
		s.mu.Unlock()
		return
	}
	_, _ = s.db.Exec(`INSERT OR IGNORE INTO huly_seen_events (subscription, event_id, seen_at) VALUES (?, ?, ?)`,
		sub, id, at.UTC().Format(time.RFC3339Nano))
}

// statusOf returns the last status seen for an issue, and whether one is known.
func (s *seenStore) statusOf(issueID string) (string, bool) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		st, ok := s.statuses[issueID]
		return st, ok
	}
	var st string
	err := s.db.QueryRow(`SELECT status FROM huly_issue_status WHERE issue_id = ?`, issueID).Scan(&st)
	return st, err == nil
}

// setStatus records the last status seen for an issue.
func (s *seenStore) setStatus(issueID, status string, at time.Time) {
	if s.db == nil {
		s.mu.Lock()
		s.statuses[issueID] = status
		s.mu.Unlock()
		return
	}
	_, _ = s.db.Exec(`INSERT INTO huly_issue_status (issue_id, status, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(issue_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		issueID, status, at.UTC().Format(time.RFC3339Nano))
}

// ── read-side API calls (the shared client, ADR 0023 decision 6) ────────

// myPersonID is the Person document id the account resolves to.
func (c *Client) myPersonID(ctx context.Context) (string, error) {
	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	return s.personID, nil
}

// listAssignedIssues returns the issues assigned to personID, optionally
// narrowed to one project. It is polled in full (no `since` window) because the
// persisted seen-set and status baseline are what make it idempotent: an issue
// assigned while the navi was down is picked up, not skipped.
func (c *Client) listAssignedIssues(ctx context.Context, personID, project string) ([]map[string]any, error) {
	query := map[string]any{"assignee": personID}
	if p := strings.TrimSpace(project); p != "" {
		proj, err := c.findProject(ctx, p)
		if err != nil {
			return nil, err
		}
		query["space"] = stringField(proj, "_id")
	}
	return c.findAll(ctx, classIssue, query, map[string]any{
		"limit": 200,
		"sort":  map[string]any{"modifiedOn": -1},
	})
}

// listCommentsSince returns comments created after t, oldest first.
func (c *Client) listCommentsSince(ctx context.Context, since time.Time) ([]map[string]any, error) {
	return c.findAll(ctx, classChatMessage,
		map[string]any{"createdOn": map[string]any{"$gt": since.UnixMilli()}},
		map[string]any{"limit": 100, "sort": map[string]any{"createdOn": 1}})
}
