package huly

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	// Priority is an ADVISORY priority hint for the thoughts this
	// subscription spawns (ADR 0035 decision 5, ADR 0036 decision 2). Nil
	// (unset) lands at connector.DefaultPriority; a value is used verbatim,
	// including a deliberate 0 for the urgent band. An issue's OWN priority
	// wins over this hint when Huly states one — the hint is the fallback for
	// events that carry no priority of their own (a mention, a comment). A
	// hint is a starting position, not a verdict: only the running thought
	// can see the queue and revise it.
	Priority connector.PriorityHint
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
	// Priority is the ISSUE's own priority name (Huly's scale), when the
	// event came from an issue. Empty for an event that has none (a mention
	// or a comment), so the subscription's hint is the fallback.
	Priority string
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
	if s.Priority != nil && !connector.ValidPriority(*s.Priority) {
		return fmt.Errorf("subscription %q: priority %d is outside the 0..10 scale", s.Name, *s.Priority)
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
		deps.Log.Warn("huly: inbound dedupe and poll watermark are in-memory only; a restart may replay a redelivery (wire WithStateStore)")
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

// pollOnce runs one poll over every declared subscription.
//
// The watermark advances per subscription only when that subscription's whole
// cycle succeeded: a failed fetch or enqueue leaves it in place, so the next
// poll re-reads the same window and a transient failure does not drop it (a
// failed event is not marked seen either, so it is retried). interval is the
// fallback lookback for a subscription that has never polled successfully — a
// first run, or a restart whose persisted watermark is absent — so downtime
// longer than one interval is recovered from the stored watermark, not lost.
func (c *Connector) pollOnce(ctx context.Context, deps connector.Deps, seen *seenStore, interval time.Duration) {
	now := c.clock()
	cl, err := c.live.Get()
	if err != nil {
		return // not serving; the loop stops when the connector stops
	}
	sess, err := cl.session(ctx)
	if err != nil {
		return // reported at start; the next poll retries
	}
	personID, self := sess.personID, sess.socialID
	if personID == "" {
		return // reported at start; nothing to poll
	}
	for _, sub := range c.cfg.Subscriptions {
		since := seen.watermark(sub.Name, now.Add(-interval))
		events, perr := c.pollSubscription(ctx, cl, seen, sub, personID, since)
		if perr != nil {
			if deps.Log != nil {
				deps.Log.Warn("huly: poll failed", "subscription", sub.Name, "err", perr.Error())
			}
			continue
		}
		ok := true
		for _, ev := range events {
			if !sub.declares(ev.Kind) {
				continue
			}
			if self != "" && ev.Author == self {
				continue // the navi's own egress cannot re-enter as an event
			}
			key := ev.dedupeKey()
			if seen.has(sub.Name, key) {
				continue
			}
			if eerr := c.enqueueEvent(deps, sub, ev, now); eerr != nil {
				if errors.Is(eerr, thought.ErrDuplicateID) {
					// The deterministic thought id already exists: the seen-set was
					// lost but the queue survived, so the event has been handled.
					// Mark it seen rather than retrying it every poll.
					seen.mark(sub.Name, key, now)
					continue
				}
				ok = false
				if deps.Log != nil {
					deps.Log.Error("huly: enqueue failed", "subscription", sub.Name, "event", string(ev.Kind), "err", eerr.Error())
				}
				continue // not marked seen: retried next poll
			}
			seen.mark(sub.Name, key, now)
		}
		if ok {
			seen.setWatermark(sub.Name, now)
		}
	}
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

			// ONE comment is ONE event, however many declared kinds describe
			// it. A comment that both mentions the navi and sits on an issue
			// assigned to it matches two kinds, and emitting both spawned two
			// thoughts for one stimulus (NAVI-585) — the dedupe keys and the
			// deterministic thought ids are per-KIND, so neither idempotency
			// layer could collapse them.
			//
			// The kinds are precedence-ordered, most specific first: a mention
			// is the sharper signal, and "a comment on my issue" is the weaker
			// description of the same thing. The first match wins, so exactly
			// one event is emitted and the two layers agree by construction.
			// Precedence only chooses between kinds that BOTH match, so a
			// subscription declaring one of them is unaffected.
			var kind EventKind
			switch {
			case sub.declares(EventIssueMentioned) && mentionsPerson(raw, personID):
				kind = EventIssueMentioned
			case sub.declares(EventCommentOnMyIssue) && mine[issueID]:
				kind = EventCommentOnMyIssue
			default:
				continue
			}
			out = append(out, commentEvent(kind, cm, text, author))
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
		Priority:   issuePriorityName(is),
	}
	if kind == EventIssueStatusChanged {
		// Two different status changes on one issue are two events.
		ev.ID = stringField(is, "_id") + ":" + ev.Status
	}
	return ev
}

// issuePriorityName reads an issue's own priority as its Huly name, so the
// priority hint can be derived from the issue rather than the subscription
// (ADR 0035 decision 5). Huly stores it as a number on its own scale
// (urgent=1 .. low=4, no-priority=0); an unset or unrecognised value returns
// "", which the hint resolver reads as "no opinion" and falls back to the
// subscription's declared hint.
func issuePriorityName(is map[string]any) string {
	v, ok := is["priority"]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case float64: // JSON numbers
		return priorityToString(int(t))
	case string:
		return t
	default:
		return ""
	}
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

// commentDerived reports whether an event kind is derived from a COMMENT, as
// opposed to from an issue. The two comment kinds are two descriptions of one
// stimulus, so a comment-derived thought is identified by the comment, not by
// the kind (NAVI-585).
func commentDerived(kind EventKind) bool {
	return kind == EventIssueMentioned || kind == EventCommentOnMyIssue
}

// mentionsPerson reports whether a comment's raw ProseMirror markup names the
// person outside code and quoted text. A Huly mention embeds the person's
// document id in the markup (the UI resolves it from there), so the id is the
// reliable signal — the display name is not, and the exact node shape is not
// part of the contract. A code block, a block quote, or inline code carries the
// id too but is not a request to the navi, so those subtrees are dropped before
// the search. Markup that is not the JSON object Huly stores falls back to a
// whole-string search rather than dropping a possible mention.
func mentionsPerson(raw, personID string) bool {
	if strings.TrimSpace(personID) == "" {
		return false
	}
	if stripped, ok := stripMarkupNoise(raw); ok {
		return strings.Contains(stripped, personID)
	}
	return strings.Contains(raw, personID)
}

// stripMarkupNoise removes ProseMirror code-block, block-quote, and inline-code
// subtrees from a comment's markup, returning the remaining JSON. ok is false
// when the markup is not the JSON object Huly stores.
func stripMarkupNoise(raw string) (string, bool) {
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "", false
	}
	blob, err := json.Marshal(pruneNoiseNodes(doc))
	if err != nil {
		return "", false
	}
	return string(blob), true
}

// pruneNoiseNodes drops the node subtrees a mention must not be read from:
// code blocks, block quotes, and text carrying a `code` mark. It returns nil
// for a dropped node (or a JSON null), and the parent omits it.
func pruneNoiseNodes(node any) any {
	switch v := node.(type) {
	case map[string]any:
		switch stringField(v, "type") {
		case "codeBlock", "code_block", "blockquote":
			return nil
		case "text":
			if hasCodeMark(v) {
				return nil
			}
		}
		out := make(map[string]any, len(v))
		for k, child := range v {
			if pruned := pruneNoiseNodes(child); pruned != nil {
				out[k] = pruned
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			if pruned := pruneNoiseNodes(child); pruned != nil {
				out = append(out, pruned)
			}
		}
		return out
	default:
		return node
	}
}

// hasCodeMark reports whether a ProseMirror text node is inline code.
func hasCodeMark(node map[string]any) bool {
	marks, _ := node["marks"].([]any)
	for _, mark := range marks {
		if m, ok := mark.(map[string]any); ok && stringField(m, "type") == "code" {
			return true
		}
	}
	return false
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
		ID:     thoughtID(sub.Name, ev),
		Prompt: prompt,
		// An issue's own priority wins; the subscription's hint is the
		// fallback for events with no priority of their own (ADR 0035
		// decision 5, ADR 0036 decision 2). Advisory: the triaging thought
		// may revise it once it has seen the queue.
		Priority:  connector.HintForEventPriority(ev.Priority, sub.Priority),
		Context:   ev.contextText(),
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

// thoughtID is the deterministic thought id for an event: the second
// idempotency backstop ADR 0023 decision 5 describes, so a redelivery that
// slips past the seen-set still collides on the queue rather than spawning a
// second thought.
//
// A comment-derived event is keyed on the COMMENT, not the kind (NAVI-585).
// The two comment kinds are two descriptions of one stimulus, so a
// kind-bearing id would let the same comment enqueue twice under two ids —
// which is exactly what happened before the precedence rule, and is why this
// backstop did not catch it. Issue-derived kinds keep their kind in the id,
// because an issue legitimately produces several distinct events (assigned,
// then a status change).
func thoughtID(subName string, ev inboundEvent) string {
	if commentDerived(ev.Kind) {
		return fmt.Sprintf("huly-%s-comment-%s", subName, ev.ID)
	}
	return fmt.Sprintf("huly-%s-%s-%s", subName, ev.Kind, ev.ID)
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

// seenStore dedupes redelivered events, remembers the last status seen for an
// issue, and remembers each subscription's poll watermark. With a state store it
// is SQLite shared with the navi's session DB; without one it is in memory, and
// a restart replays the last interval rather than resuming from the watermark.
type seenStore struct {
	db  *sql.DB
	mu  sync.Mutex
	mem map[string]struct{}
	// statuses remembers each issue's last-seen status so a change is a
	// transition, not a first sighting.
	statuses map[string]string
	// watermarks remembers each subscription's last successful poll time so a
	// restart resumes from it instead of re-scanning a fixed lookback window.
	watermarks map[string]time.Time
}

func newSeenStore(db *sql.DB) (*seenStore, error) {
	s := &seenStore{db: db, mem: map[string]struct{}{}, statuses: map[string]string{}, watermarks: map[string]time.Time{}}
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
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS huly_poll_watermark (
			subscription TEXT PRIMARY KEY,
			since DATETIME NOT NULL
		)`); err != nil {
			return nil, fmt.Errorf("huly: create huly_poll_watermark: %w", err)
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
	if err := s.db.QueryRow(`SELECT since FROM huly_poll_watermark WHERE subscription = ?`, sub).Scan(&raw); err != nil {
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
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO huly_poll_watermark (subscription, since) VALUES (?, ?)`,
		sub, at.Format(time.RFC3339Nano))
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

// commentPageSize and issuePageSize bound one find-all request, not the result:
// findAllKeyset follows the sort key past them so a burst is read whole.
const (
	commentPageSize = 100
	issuePageSize   = 200
)

// listAssignedIssues returns every issue assigned to personID, optionally
// narrowed to one project, walking the keyset so a set larger than one page is
// read whole rather than truncated. It is polled in full (no `since` window)
// because the persisted seen-set and status baseline are what make it
// idempotent: an issue assigned while the navi was down is picked up, not
// skipped.
func (c *Client) listAssignedIssues(ctx context.Context, personID, project string) ([]map[string]any, error) {
	query := map[string]any{"assignee": personID}
	if p := strings.TrimSpace(project); p != "" {
		proj, err := c.findProject(ctx, p)
		if err != nil {
			return nil, err
		}
		query["space"] = stringField(proj, "_id")
	}
	return c.findAllKeyset(ctx, classIssue, "modifiedOn", true, nil, query,
		map[string]any{"limit": issuePageSize})
}

// listCommentsSince returns every comment created at or after t, oldest first,
// walking the keyset so a burst larger than one page is read whole rather than
// truncated. The bound is inclusive so a comment written in the same millisecond
// as the watermark is re-read (and deduped) rather than skipped.
func (c *Client) listCommentsSince(ctx context.Context, since time.Time) ([]map[string]any, error) {
	start := since.UnixMilli()
	return c.findAllKeyset(ctx, classChatMessage, "createdOn", false, &start, nil,
		map[string]any{"limit": commentPageSize})
}
