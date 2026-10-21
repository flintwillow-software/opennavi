package huly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the repo's pure-Go SQLite driver, for the persisted seen-set test

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/thought"
)

// fakeTS is a full thought.ThoughtSystem that records what is enqueued. It is
// mutex-guarded so a test can drive the poll loop directly or in a goroutine.
type fakeTS struct {
	mu     sync.Mutex
	queued []*thought.Thought
}

func (f *fakeTS) Enqueue(t *thought.Thought) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued = append(f.queued, t)
	return nil
}
func (f *fakeTS) RouteMessage(id, text string) error     { return nil }
func (f *fakeTS) Pause(id string) error                  { return nil }
func (f *fakeTS) Resume(id string) error                 { return nil }
func (f *fakeTS) Cancel(id string) error                 { return nil }
func (f *fakeTS) BumpPriority(id string, prio int) error { return nil }
func (f *fakeTS) Retry(id string) error                  { return nil }
func (f *fakeTS) Complete(id, result string) error       { return nil }
func (f *fakeTS) Fail(id, detail string) error           { return nil }
func (f *fakeTS) GetThought(id string) (*thought.Thought, error) {
	return nil, thought.ErrThoughtNotFound
}
func (f *fakeTS) GetThoughts() []*thought.Thought {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*thought.Thought(nil), f.queued...)
}
func (f *fakeTS) OnEvent(fn func(thought.Event)) {}

func (f *fakeTS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queued)
}

// inboundClient connects a connector to the fake without launching the poll
// loop, so a test can drive pollOnce synchronously and deterministically.
func inboundClient(t *testing.T, f *fakeHuly, cfg Config) *Connector {
	t.Helper()
	cfg.URL = f.srv.URL
	cfg.Workspace = "flintwillowsoftware"
	if cfg.Credential == "" {
		cfg.Credential = "HULY_TOKEN"
	}
	if cfg.Env == nil {
		cfg.Env = func(string) string { return "account-token" }
	}
	c := New(cfg)
	// A fixed clock keeps the comment window deterministic: the first poll looks
	// back one interval from it, so a test's comment timestamps are in or out of
	// the window by construction rather than by wall clock.
	c.now = func() time.Time { return time.UnixMilli(10_000) }
	client := newClient(c.cfg)
	if err := client.connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	c.live.Set(client)
	t.Cleanup(func() { _ = c.Stop() })
	return c
}

func pollOnceNow(t *testing.T, c *Connector, ts *fakeTS, seen *seenStore, interval time.Duration) {
	t.Helper()
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: ts}, seen, interval)
}

// TestInboundAssignedIssueEnqueuesThought is NAVI-561's critical path: a human
// assigns an issue to this navi's Huly user and the navi picks it up.
func TestInboundAssignedIssueEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	seen, err := newSeenStore(nil)
	if err != nil {
		t.Fatalf("newSeenStore: %v", err)
	}
	pollOnceNow(t, c, ts, seen, time.Hour)

	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1", ts.count())
	}
	got := ts.GetThoughts()[0]
	if want := "huly-assigned-issue_assigned-6acd168268c57585aac3fa3f"; got.ID != want {
		t.Errorf("thought id = %q, want %q", got.ID, want)
	}
	if !strings.Contains(got.Prompt, "assigned to you") || !strings.Contains(got.Prompt, "NAVI-408") {
		t.Errorf("prompt = %q, want the assignment and identifier", got.Prompt)
	}
	if got.Metadata["event"] != string(EventIssueAssigned) {
		t.Errorf("event = %q, want issue_assigned", got.Metadata["event"])
	}
	if got.Metadata["identifier"] != "NAVI-408" {
		t.Errorf("identifier = %q, want NAVI-408", got.Metadata["identifier"])
	}
	if got.Metadata["principal"] != "connector:huly" {
		t.Errorf("principal = %q, want the connector's own", got.Metadata["principal"])
	}
}

// TestInboundDedupesAcrossPolls pins ADR 0023 decision 5: the same assigned
// issue is not a second thought on the next poll.
func TestInboundDedupesAcrossPolls(t *testing.T) {
	f := newFakeHuly(t)
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour)
	pollOnceNow(t, c, ts, seen, time.Hour)
	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts across two polls, want 1", ts.count())
	}
}

// TestInboundStatusChangeEnqueuesThought pins the status-change kind: the first
// sighting is an assignment, a later move is a status change.
func TestInboundStatusChangeEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "s", Project: "NAVI",
		Events: []EventKind{EventIssueAssigned, EventIssueStatusChanged},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour) // assignment baseline

	f.mu.Lock()
	f.issues[0]["status"] = "tracker:status:Done"
	f.mu.Unlock()
	pollOnceNow(t, c, ts, seen, time.Hour)

	if ts.count() != 2 {
		t.Fatalf("enqueued %d thoughts, want 2 (assigned + status change)", ts.count())
	}
	got := ts.GetThoughts()[1]
	if got.Metadata["event"] != string(EventIssueStatusChanged) {
		t.Errorf("event = %q, want issue_status_changed", got.Metadata["event"])
	}
	if !strings.Contains(got.Prompt, "tracker:status:Done") {
		t.Errorf("prompt = %q, want the new status", got.Prompt)
	}
}

// TestInboundMentionEnqueuesThought pins the mention kind: a comment whose
// markup names the navi's person id is a mention.
func TestInboundMentionEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{{
		"_id": "cm-1", "attachedTo": "6acd168268c57585aac3fa3f", "createdBy": "social-9",
		"createdOn": int64(5000),
		"message": `{"type":"doc","content":[{"type":"paragraph","content":[` +
			`{"type":"text","text":"hey "},` +
			`{"type":"mention","attrs":{"id":"6aa2a3038376b1c099c0403d"}}]}]}`,
	}}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Events: []EventKind{EventIssueMentioned},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour)

	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1", ts.count())
	}
	got := ts.GetThoughts()[0]
	if got.Metadata["event"] != string(EventIssueMentioned) {
		t.Errorf("event = %q, want issue_mentioned", got.Metadata["event"])
	}
	if !strings.Contains(got.Context, "hey") {
		t.Errorf("context = %q, want the comment text", got.Context)
	}
}

// TestInboundCommentOnMyIssueEnqueuesThought pins the comment kind: a comment
// on an issue assigned to the navi is work, even without a mention.
func TestInboundCommentOnMyIssueEnqueuesThought(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{{
		"_id": "cm-2", "attachedTo": "6acd168268c57585aac3fa3f", "createdBy": "social-9",
		"createdOn": int64(6000),
		"message":   `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"any update?"}]}]}`,
	}}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "c", Project: "NAVI", Events: []EventKind{EventCommentOnMyIssue},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour)

	if ts.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1", ts.count())
	}
	if got := ts.GetThoughts()[0]; got.Metadata["event"] != string(EventCommentOnMyIssue) {
		t.Errorf("event = %q, want comment_on_my_issue", got.Metadata["event"])
	}
}

// TestInboundSeenSetPersistsAcrossRestart pins the persisted half of ADR 0023
// decision 5: a new seen-store over the same database suppresses the event.
func TestInboundSeenSetPersistsAcrossRestart(t *testing.T) {
	f := newFakeHuly(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	seen1, _ := newSeenStore(db)
	pollOnceNow(t, c, ts, seen1, time.Hour)
	if ts.count() != 1 {
		t.Fatalf("first poll enqueued %d, want 1", ts.count())
	}

	// A fresh store over the same database stands in for a restart.
	seen2, _ := newSeenStore(db)
	pollOnceNow(t, c, ts, seen2, time.Hour)
	if ts.count() != 1 {
		t.Fatalf("after a restart the same event enqueued a second thought (%d total)", ts.count())
	}
}

// TestInboundRejectsUnservedSubscriptions pins composition-time validation: a
// declaration the binding cannot serve fails Start rather than dropping events
// silently.
func TestInboundRejectsUnservedSubscriptions(t *testing.T) {
	f := newFakeHuly(t)
	base := func() Config {
		return Config{
			URL: f.srv.URL, Workspace: "flintwillowsoftware", Credential: "HULY_TOKEN",
			Env: func(string) string { return "account-token" },
		}
	}
	cases := []struct {
		name string
		subs []Subscription
		want string
	}{
		{"unknown event", []Subscription{{Name: "x", Events: []EventKind{"nope"}}}, "unknown event kind"},
		{"unserved intent", []Subscription{{Name: "x", Events: []EventKind{EventIssueAssigned}, Intent: IntentAttach}}, "not served"},
		{"no name", []Subscription{{Events: []EventKind{EventIssueAssigned}}}, "requires a name"},
		{"duplicate", []Subscription{{Name: "x", Events: []EventKind{EventIssueAssigned}}, {Name: "x", Events: []EventKind{EventIssueAssigned}}}, "duplicate subscription"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.Subscriptions = tc.subs
			c := New(cfg)
			err := c.Start(context.Background(), connector.Deps{ThoughtSystem: &fakeTS{}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}

	// Inbound with no thought system is refused: there is nowhere to put work.
	cfg := base()
	cfg.Subscriptions = []Subscription{{Name: "x", Events: []EventKind{EventIssueAssigned}}}
	if err := New(cfg).Start(context.Background(), connector.Deps{}); err == nil {
		t.Fatal("inbound without a thought system must fail")
	}
}

// ── NAVI-564: inbound durability and correctness ────────────────────────

// failTS refuses every enqueue with an ordinary error.
type failTS struct{ fakeTS }

func (f *failTS) Enqueue(*thought.Thought) error { return errors.New("enqueue unavailable") }

// dupTS reports every enqueue as a duplicate, as the queue does when the
// deterministic thought id already exists.
type dupTS struct {
	fakeTS
	calls int
}

func (d *dupTS) Enqueue(t *thought.Thought) error {
	d.calls++
	return fmt.Errorf("huly: %w %s", thought.ErrDuplicateID, t.ID)
}

// countRequests counts recorded find-all requests with the given prefix.
func (f *fakeHuly) countRequests(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// mentionMarkup is a one-paragraph ProseMirror message whose only content is a
// mention node carrying personID.
func mentionMarkup(personID string) string {
	return fmt.Sprintf(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"id":%q}}]}]}`, personID)
}

// mentionComment is a comment document the poll can react to.
func mentionComment(id, author string, createdOn int64, personID string) map[string]any {
	return map[string]any{
		"_id": id, "attachedTo": "6acd168268c57585aac3fa3f", "createdBy": author,
		"createdOn": createdOn, "message": mentionMarkup(personID),
	}
}

// TestInboundCommentsFollowKeysetPaging proves the comment window is read whole.
// Huly's find-all has no offset, so a burst larger than one page must be walked
// by the sort key rather than truncated.
func TestInboundCommentsFollowKeysetPaging(t *testing.T) {
	const personID = "6aa2a3038376b1c099c0403d"
	f := newFakeHuly(t)
	const total = 250
	f.comments = make([]map[string]any, 0, total)
	for i := 0; i < total; i++ {
		f.comments = append(f.comments, mentionComment(fmt.Sprintf("cm-%03d", i), "social-9", int64(1000+i), personID))
	}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Events: []EventKind{EventIssueMentioned},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour)

	if got := ts.count(); got != total {
		t.Fatalf("enqueued %d thoughts, want %d (a burst larger than one page must not truncate)", got, total)
	}
	if got := f.countRequests("find-all:" + classChatMessage); got < 2 {
		t.Fatalf("the comment window took %d find-all requests, want more than one page", got)
	}
}

// TestInboundAssignedIssuesFollowKeysetPaging proves the pick-up set is read
// whole too: a navi with more assigned issues than one page still sees them all.
func TestInboundAssignedIssuesFollowKeysetPaging(t *testing.T) {
	const personID = "6aa2a3038376b1c099c0403d"
	f := newFakeHuly(t)
	const total = 250
	f.issues = make([]map[string]any, 0, total)
	for i := 0; i < total; i++ {
		f.issues = append(f.issues, map[string]any{
			"_id": fmt.Sprintf("issue-%03d", i), "identifier": fmt.Sprintf("NAVI-%d", i),
			"title": "assigned work", "status": "tracker:status:InProgress", "assignee": personID,
			"modifiedOn": int64(1000 + i), "space": "space-1",
		})
	}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "assigned", Project: "NAVI", Events: []EventKind{EventIssueAssigned},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour)

	if got := ts.count(); got != total {
		t.Fatalf("enqueued %d thoughts, want %d (the assigned set must not truncate)", got, total)
	}
	if got := f.countRequests("find-all:" + classIssue); got < 2 {
		t.Fatalf("the assigned set took %d find-all requests, want more than one page", got)
	}
}

// TestListCommentsSinceFailsLoudlyWhenAPageCannotAdvance proves a window that
// cannot be paged is an error, not a silent truncation: when more comments share
// one createdOn than a page holds, find-all has no offset to reach the rest.
func TestListCommentsSinceFailsLoudlyWhenAPageCannotAdvance(t *testing.T) {
	const personID = "6aa2a3038376b1c099c0403d"
	f := newFakeHuly(t)
	f.comments = make([]map[string]any, 0, commentPageSize+50)
	for i := 0; i < commentPageSize+50; i++ {
		f.comments = append(f.comments, mentionComment(fmt.Sprintf("cm-%03d", i), "social-9", 5000, personID))
	}
	c := inboundClient(t, f, Config{})
	cl, err := c.live.Get()
	if err != nil {
		t.Fatalf("live client: %v", err)
	}
	if _, err := cl.listCommentsSince(context.Background(), time.UnixMilli(0)); err == nil {
		t.Fatal("a page that cannot advance the keyset must be an error, not a silent truncation")
	}
}

// TestSeenStoreWatermarkPersists proves the watermark survives a restart: a
// second store over the same DB resumes from it instead of a lookback window.
func TestSeenStoreWatermarkPersists(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	first, err := newSeenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1_700_000_000_000).UTC()
	first.setWatermark("m", at)
	fallback := at.Add(-time.Hour)
	if got := first.watermark("m", fallback); !got.Equal(at) {
		t.Fatalf("watermark = %v, want %v", got, at)
	}

	second, err := newSeenStore(db) // simulates a restart over the same DB
	if err != nil {
		t.Fatal(err)
	}
	if got := second.watermark("m", fallback); !got.Equal(at) {
		t.Fatalf("watermark did not survive the restart: %v, want %v", got, at)
	}
	if got := second.watermark("other", fallback); !got.Equal(fallback) {
		t.Fatalf("unknown subscription watermark = %v, want the fallback %v", got, fallback)
	}
}

// TestInboundWatermarkAdvancesOnSuccessOnly proves a transient poll failure does
// not move the watermark, so the failed window is re-read rather than dropped.
func TestInboundWatermarkAdvancesOnSuccessOnly(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{mentionComment("cm-1", "social-9", 5000, "6aa2a3038376b1c099c0403d")}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Events: []EventKind{EventIssueMentioned},
	}}})
	clock := time.UnixMilli(10_000)
	c.now = func() time.Time { return clock }
	seen, _ := newSeenStore(nil)
	fallback := clock.Add(-time.Minute)

	f.mu.Lock()
	f.failChatMessages = true
	f.mu.Unlock()
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: ts}, seen, time.Minute)
	if got := seen.watermark("m", fallback); !got.Equal(fallback) {
		t.Fatalf("watermark after a failed poll = %v, want %v", got, fallback)
	}

	f.mu.Lock()
	f.failChatMessages = false
	f.mu.Unlock()
	clock = clock.Add(time.Minute)
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: ts}, seen, time.Minute)
	if got := seen.watermark("m", fallback); !got.Equal(clock) {
		t.Fatalf("watermark after a successful poll = %v, want %v", got, clock)
	}
}

// TestInboundEnqueueFailureHoldsWatermark proves a failed enqueue is retried
// rather than dropped: the event is not marked seen and the watermark does not
// advance past it.
func TestInboundEnqueueFailureHoldsWatermark(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{mentionComment("cm-1", "social-9", 5000, "6aa2a3038376b1c099c0403d")}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Events: []EventKind{EventIssueMentioned},
	}}})
	clock := time.UnixMilli(10_000)
	c.now = func() time.Time { return clock }
	seen, _ := newSeenStore(nil)
	fallback := clock.Add(-time.Minute)

	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: &failTS{}}, seen, time.Minute)
	if seen.has("m", "mention:cm-1") {
		t.Fatal("a failed enqueue must not be marked seen")
	}
	if got := seen.watermark("m", fallback); !got.Equal(fallback) {
		t.Fatalf("watermark after a failed enqueue = %v, want %v", got, fallback)
	}

	ts := &fakeTS{}
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: ts}, seen, time.Minute)
	if ts.count() != 1 {
		t.Fatalf("retry enqueued %d thoughts, want 1", ts.count())
	}
	if got := seen.watermark("m", fallback); !got.Equal(clock) {
		t.Fatalf("watermark after a successful retry = %v, want %v", got, clock)
	}
}

// TestInboundDuplicateIDIsMarkedSeen is the duplicate-enqueue fix: the queue
// already holds the deterministic thought, so the event is treated as handled
// (marked seen, watermark advanced) instead of retried every poll.
func TestInboundDuplicateIDIsMarkedSeen(t *testing.T) {
	f := newFakeHuly(t)
	f.comments = []map[string]any{mentionComment("cm-1", "social-9", 5000, "6aa2a3038376b1c099c0403d")}
	q := &dupTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Events: []EventKind{EventIssueMentioned},
	}}})
	clock := time.UnixMilli(10_000)
	c.now = func() time.Time { return clock }
	seen, _ := newSeenStore(nil)
	fallback := clock.Add(-time.Minute)
	deps := connector.Deps{ThoughtSystem: q}

	c.pollOnce(context.Background(), deps, seen, time.Minute)
	if !seen.has("m", "mention:cm-1") {
		t.Fatal("a duplicate-id enqueue must be treated as already-seen")
	}
	if got := seen.watermark("m", fallback); !got.Equal(clock) {
		t.Fatalf("watermark = %v, want %v (a duplicate must not hold it back)", got, clock)
	}

	c.pollOnce(context.Background(), deps, seen, time.Minute)
	if q.calls != 1 {
		t.Fatalf("Enqueue called %d times, want 1 (the duplicate is not retried)", q.calls)
	}
}

// TestInboundSelfCommentsAreIgnored is the self-filter: a comment the connector's
// own account authored is the navi's egress, not a new stimulus, so it cannot
// loop back — for a mention or as a comment on the navi's own issue.
func TestInboundSelfCommentsAreIgnored(t *testing.T) {
	const personID = "6aa2a3038376b1c099c0403d"
	f := newFakeHuly(t)
	f.comments = []map[string]any{
		mentionComment("self", "social-1", 5000, personID),
		mentionComment("other", "social-9", 6000, personID),
	}
	ts := &fakeTS{}
	c := inboundClient(t, f, Config{Subscriptions: []Subscription{{
		Name: "m", Project: "NAVI",
		Events: []EventKind{EventIssueMentioned, EventCommentOnMyIssue},
	}}})
	seen, _ := newSeenStore(nil)
	pollOnceNow(t, c, ts, seen, time.Hour)

	if got := ts.count(); got != 2 {
		t.Fatalf("enqueued %d thoughts, want 2 (the self comment is filtered for both kinds)", got)
	}
	for _, th := range ts.GetThoughts() {
		if strings.Contains(th.ID, "self") {
			t.Errorf("a self-authored comment re-entered as %s", th.ID)
		}
	}
}

// TestMentionsPersonIgnoresCodeAndQuotes pins the noise half of the mention
// matcher: the person id is a mention only outside code blocks, block quotes,
// and inline code, since Huly resolves a mention from the node carrying the id
// and quoted or code text carries it without being a request.
func TestMentionsPersonIgnoresCodeAndQuotes(t *testing.T) {
	const personID = "6aa2a3038376b1c099c0403d"
	cases := map[string]struct {
		markup string
		want   bool
	}{
		"plain mention": {mentionMarkup(personID), true},
		"mention in a code block": {fmt.Sprintf(
			`{"type":"doc","content":[{"type":"codeBlock","content":[{"type":"text","text":%q}]}]}`, personID), false},
		"mention in a block quote": {fmt.Sprintf(
			`{"type":"doc","content":[{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"id":%q}}]}]}]}`, personID), false},
		"id as inline code": {fmt.Sprintf(
			`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":%q,"marks":[{"type":"code"}]}]}]}`, personID), false},
		"mention after a quote": {fmt.Sprintf(
			`{"type":"doc","content":[{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"old"}]}]},{"type":"paragraph","content":[{"type":"mention","attrs":{"id":%q}}]}]}`, personID), true},
		"not JSON falls back": {"hey @navi " + personID, true},
		"another person":      {mentionMarkup("000000000000000000000000"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mentionsPerson(tc.markup, personID); got != tc.want {
				t.Errorf("mentionsPerson(%s) = %v, want %v", tc.markup, got, tc.want)
			}
		})
	}
	if mentionsPerson("anything", "") {
		t.Error("an empty person id must not match")
	}
}
