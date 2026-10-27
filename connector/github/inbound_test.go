package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the repo's pure-Go SQLite driver, for the persisted seen-set test

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// fakeTS is a full thought.ThoughtSystem that records what is enqueued. It is
// mutex-guarded because the inbound poll loop runs in a goroutine.
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

// commentServer serves one fixed issue-comment page.
func commentServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/issues/comments") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestInboundMentionEnqueuesThought is NAVI-394's acceptance criterion: a
// composed connector reacts to a declared inbound event by queueing a thought,
// verified against a test double rather than a live GitHub.
func TestInboundMentionEnqueuesThought(t *testing.T) {
	tok := testToken(t)
	ref := "GITHUB_TOKEN"
	srv := commentServer(t, `[{"id":42,"body":"hey @navi take a look","html_url":"https://github.com/acme/widgets/issues/7#issuecomment-42","user":{"login":"alice"}}]`)

	q := &fakeTS{}
	c := New(Config{
		BaseURL:      srv.URL,
		Credential:   ref,
		Env:          envWith(ref, tok),
		HTTPClient:   srv.Client(),
		PollInterval: 20 * time.Millisecond,
		Subscriptions: []Subscription{{
			Name:       "issues",
			Repository: "acme/widgets",
			Events:     []EventKind{EventIssueMention},
			Mention:    "navi",
			Principal:  principal.MustParse("navi:opennavi"),
		}},
	})
	if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: q}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop() }()

	th := waitForThought(t, q)
	if th.Metadata["connector"] != "github" {
		t.Errorf("metadata connector = %q, want github", th.Metadata["connector"])
	}
	if th.Metadata["event"] != string(EventIssueMention) {
		t.Errorf("metadata event = %q, want %q", th.Metadata["event"], EventIssueMention)
	}
	if th.Metadata["event_id"] != "comment-42" {
		t.Errorf("metadata event_id = %q, want comment-42", th.Metadata["event_id"])
	}
	if th.Metadata["principal"] != "navi:opennavi" {
		t.Errorf("metadata principal = %q, want navi:opennavi", th.Metadata["principal"])
	}
	if !strings.Contains(th.Context, "alice") || !strings.Contains(th.Context, "@navi") {
		t.Errorf("context missing the stimulus: %q", th.Context)
	}
	if th.Status != thought.ThoughtPending {
		t.Errorf("status = %v, want pending", th.Status)
	}
}

// TestInboundRedeliveryDoesNotDuplicate is the second acceptance criterion: a
// redelivered event (the same id, polled twice) produces exactly one thought.
func TestInboundRedeliveryDoesNotDuplicate(t *testing.T) {
	tok := testToken(t)
	ref := "GITHUB_TOKEN"
	srv := commentServer(t, `[{"id":99,"body":"@navi ping","user":{"login":"bob"}}]`)

	q := &fakeTS{}
	c := New(Config{
		BaseURL:    srv.URL,
		Credential: ref,
		Env:        envWith(ref, tok),
		HTTPClient: srv.Client(),
		Subscriptions: []Subscription{{
			Name:       "issues",
			Repository: "acme/widgets",
			Events:     []EventKind{EventIssueMention},
			Mention:    "navi",
		}},
	})
	// Drive the poll directly (deterministic): set the live client the way
	// Start would, without launching the loop.
	c.live.Set(c.newClient())
	seen, err := newSeenStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := connector.Deps{ThoughtSystem: q}

	c.pollOnce(context.Background(), deps, seen, time.Hour)
	c.pollOnce(context.Background(), deps, seen, time.Hour)

	if got := q.count(); got != 1 {
		t.Fatalf("enqueued %d thoughts for one redelivered event, want 1", got)
	}
}

// TestInboundSubscriptionValidation fails at composition for a declaration the
// binding cannot serve, rather than dropping events silently.
func TestInboundSubscriptionValidation(t *testing.T) {
	base := Subscription{Name: "s", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}}
	cases := map[string]Subscription{
		"no name":            {Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}},
		"bad repository":     {Name: "s", Repository: "widgets", Events: []EventKind{EventIssueOpened}},
		"unknown event":      {Name: "s", Repository: "acme/widgets", Events: []EventKind{"bogus"}},
		"mention no login":   {Name: "s", Repository: "acme/widgets", Events: []EventKind{EventIssueMention}},
		"unserved intent":    {Name: "s", Repository: "acme/widgets", Events: []EventKind{EventIssueOpened}, Intent: IntentSteer},
		"missing repository": {Name: "s", Events: []EventKind{EventIssueOpened}},
	}
	_ = base
	for name, sub := range cases {
		t.Run(name, func(t *testing.T) {
			c := New(Config{Subscriptions: []Subscription{sub}})
			if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: &fakeTS{}}); err == nil {
				t.Fatalf("Start accepted invalid subscription %+v", sub)
			}
		})
	}
	t.Run("duplicate name", func(t *testing.T) {
		c := New(Config{Subscriptions: []Subscription{base, base}})
		if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: &fakeTS{}}); err == nil {
			t.Fatal("Start accepted duplicate subscription names")
		}
	})
}

// TestSeenStorePersists proves the dedupe set survives a restart: a second store
// over the same DB sees the first store's marks.
func TestSeenStorePersists(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	first, err := newSeenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first.mark("issues", "comment-7", time.Now())
	if !first.has("issues", "comment-7") {
		t.Fatal("first store does not see its own mark")
	}

	second, err := newSeenStore(db) // simulates a restart over the same DB
	if err != nil {
		t.Fatal(err)
	}
	if !second.has("issues", "comment-7") {
		t.Fatal("dedupe set did not survive the restart")
	}
	if second.has("issues", "comment-8") {
		t.Fatal("dedupe set reports an unseen event as seen")
	}
}

// waitForThought returns the first enqueued thought, or fails after a bounded
// wait. The poll loop runs in a goroutine, so the test waits rather than sleeps.
func waitForThought(t *testing.T, q *fakeTS) *thought.Thought {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := q.GetThoughts(); len(got) > 0 {
			return got[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no thought enqueued within the deadline")
	return nil
}

// ── NAVI-406: polling durability and correctness ────────────────────────

// pagedCommentServer serves one issue-comment page per element, linking each
// page to the next with a Link header the way GitHub does and the last page to
// nothing. The requested page comes from the query, so following the header is
// what advances the fetch.
func pagedCommentServer(t *testing.T, pages ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/user") {
			http.NotFound(w, r)
			return
		}
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			page, _ = strconv.Atoi(p)
		}
		if page < 1 || page > len(pages) {
			http.NotFound(w, r)
			return
		}
		if page < len(pages) {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=%d>; rel="next"`, r.Host, r.URL.Path, page+1))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, pages[page-1])
	}))
	t.Cleanup(srv.Close)
	return srv
}

// inboundConnector builds a connector pointed at srv with one mention
// subscription, ready to be driven by pollOnce.
func inboundConnector(t *testing.T, srv *httptest.Server, cfg Config) (*Connector, *seenStore) {
	t.Helper()
	ref := "GITHUB_TOKEN"
	cfg.BaseURL = srv.URL
	cfg.Credential = ref
	cfg.Env = envWith(ref, testToken(t))
	cfg.HTTPClient = srv.Client()
	if len(cfg.Subscriptions) == 0 {
		cfg.Subscriptions = []Subscription{{
			Name:       "issues",
			Repository: "acme/widgets",
			Events:     []EventKind{EventIssueMention},
			Mention:    "navi",
		}}
	}
	c := New(cfg)
	c.live.Set(c.newClient())
	seen, err := newSeenStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, seen
}

// TestLinkNext pins the Link-header parse: the rel="next" target is returned,
// a last-page header yields nothing, and a malformed header is ignored.
func TestLinkNext(t *testing.T) {
	cases := map[string]string{
		`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=5>; rel="last"`: "https://api.github.com/x?page=2",
		`<https://api.github.com/x?page=5>; rel="last"`:                                                "",
		`<https://api.github.com/x?page=3>; rel="next"`:                                                "https://api.github.com/x?page=3",
		``: "",
	}
	for header, want := range cases {
		if got := linkNext(header); got != want {
			t.Errorf("linkNext(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestInboundFollowsPagination is the pagination fix: a burst larger than one
// page is followed via the Link header rather than silently truncated.
func TestInboundFollowsPagination(t *testing.T) {
	srv := pagedCommentServer(t,
		`[{"id":1,"body":"@navi first","user":{"login":"alice"}}]`,
		`[{"id":2,"body":"@navi second","user":{"login":"bob"}}]`,
	)
	q := &fakeTS{}
	c, seen := inboundConnector(t, srv, Config{})

	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: q}, seen, time.Hour)

	if got := q.count(); got != 2 {
		t.Fatalf("enqueued %d thoughts across two pages, want 2", got)
	}
	ids := map[string]bool{}
	for _, th := range q.GetThoughts() {
		ids[th.Metadata["event_id"]] = true
	}
	if !ids["comment-1"] || !ids["comment-2"] {
		t.Fatalf("enqueued event ids = %v, want comment-1 and comment-2", ids)
	}
}

// TestInboundPageCapIsLoud proves the page cap fails the poll rather than
// truncating: the watermark is not advanced, so the next cycle re-reads the
// window instead of dropping the tail.
func TestInboundPageCapIsLoud(t *testing.T) {
	pages := make([]string, maxPollPages+1)
	for i := range pages {
		pages[i] = fmt.Sprintf(`[{"id":%d,"body":"@navi x","user":{"login":"a"}}]`, i+1)
	}
	srv := pagedCommentServer(t, pages...)
	q := &fakeTS{}
	c, seen := inboundConnector(t, srv, Config{})
	clock := time.Unix(1_700_000_000, 0).UTC()
	c.now = func() time.Time { return clock }
	fallback := clock.Add(-time.Minute)

	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: q}, seen, time.Minute)

	if got := seen.watermark("issues", fallback); !got.Equal(fallback) {
		t.Fatalf("watermark = %v, want the fallback %v after a capped poll", got, fallback)
	}
}

// TestSeenStoreWatermarkPersists proves the watermark survives a restart: a
// second store over the same DB resumes from it instead of a lookback window.
func TestSeenStoreWatermarkPersists(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	first, err := newSeenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()
	first.setWatermark("issues", at)
	fallback := at.Add(-time.Hour)
	if got := first.watermark("issues", fallback); !got.Equal(at) {
		t.Fatalf("watermark = %v, want %v", got, at)
	}

	second, err := newSeenStore(db) // simulates a restart over the same DB
	if err != nil {
		t.Fatal(err)
	}
	if got := second.watermark("issues", fallback); !got.Equal(at) {
		t.Fatalf("watermark did not survive the restart: %v, want %v", got, at)
	}
	if got := second.watermark("other", fallback); !got.Equal(fallback) {
		t.Fatalf("unknown subscription watermark = %v, want the fallback %v", got, fallback)
	}
}

// TestInboundWatermarkAdvancesOnSuccessOnly proves a transient poll failure does
// not move the watermark, so the failed window is re-read rather than dropped.
func TestInboundWatermarkAdvancesOnSuccessOnly(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/user") {
			http.NotFound(w, r)
			return
		}
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)

	c, seen := inboundConnector(t, srv, Config{})
	clock := time.Unix(1_700_000_000, 0).UTC()
	c.now = func() time.Time { return clock }
	fallback := clock.Add(-time.Minute)
	deps := connector.Deps{ThoughtSystem: &fakeTS{}}

	fail.Store(true)
	c.pollOnce(context.Background(), deps, seen, time.Minute)
	if got := seen.watermark("issues", fallback); !got.Equal(fallback) {
		t.Fatalf("watermark after a failed poll = %v, want %v", got, fallback)
	}

	fail.Store(false)
	clock = clock.Add(time.Minute)
	c.pollOnce(context.Background(), deps, seen, time.Minute)
	if got := seen.watermark("issues", fallback); !got.Equal(clock) {
		t.Fatalf("watermark after a successful poll = %v, want %v", got, clock)
	}
}

// failTS refuses every enqueue with an ordinary error.
type failTS struct{ fakeTS }

func (f *failTS) Enqueue(*thought.Thought) error { return errors.New("enqueue unavailable") }

// TestInboundEnqueueFailureHoldsWatermark proves a failed enqueue is retried
// rather than dropped: the event is not marked seen and the watermark does not
// advance past it.
func TestInboundEnqueueFailureHoldsWatermark(t *testing.T) {
	srv := commentServer(t, `[{"id":42,"body":"@navi ping","user":{"login":"bob"}}]`)
	c, seen := inboundConnector(t, srv, Config{})
	clock := time.Unix(1_700_000_000, 0).UTC()
	c.now = func() time.Time { return clock }
	fallback := clock.Add(-time.Minute)

	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: &failTS{}}, seen, time.Minute)
	if seen.has("issues", "comment-42") {
		t.Fatal("a failed enqueue must not be marked seen")
	}
	if got := seen.watermark("issues", fallback); !got.Equal(fallback) {
		t.Fatalf("watermark after a failed enqueue = %v, want %v", got, fallback)
	}

	q := &fakeTS{}
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: q}, seen, time.Minute)
	if q.count() != 1 {
		t.Fatalf("retry enqueued %d thoughts, want 1", q.count())
	}
	if got := seen.watermark("issues", fallback); !got.Equal(clock) {
		t.Fatalf("watermark after a successful retry = %v, want %v", got, clock)
	}
}

// dupTS reports every enqueue as a duplicate, as the queue does when the
// deterministic thought id already exists.
type dupTS struct {
	fakeTS
	calls int
}

func (d *dupTS) Enqueue(t *thought.Thought) error {
	d.calls++
	return fmt.Errorf("thought: %w %s", thought.ErrDuplicateID, t.ID)
}

// TestInboundDuplicateIDIsMarkedSeen is the duplicate-enqueue fix: the queue
// already holds the deterministic thought, so the event is treated as handled
// (marked seen, watermark advanced) instead of retried every poll.
func TestInboundDuplicateIDIsMarkedSeen(t *testing.T) {
	srv := commentServer(t, `[{"id":42,"body":"@navi ping","user":{"login":"bob"}}]`)
	q := &dupTS{}
	c, seen := inboundConnector(t, srv, Config{})
	clock := time.Unix(1_700_000_000, 0).UTC()
	c.now = func() time.Time { return clock }
	fallback := clock.Add(-time.Minute)
	deps := connector.Deps{ThoughtSystem: q}

	c.pollOnce(context.Background(), deps, seen, time.Minute)
	if !seen.has("issues", "comment-42") {
		t.Fatal("a duplicate-id enqueue must be treated as already-seen")
	}
	if got := seen.watermark("issues", fallback); !got.Equal(clock) {
		t.Fatalf("watermark = %v, want %v (a duplicate must not hold it back)", got, clock)
	}

	c.pollOnce(context.Background(), deps, seen, time.Minute)
	if q.calls != 1 {
		t.Fatalf("Enqueue called %d times, want 1 (the duplicate is not retried)", q.calls)
	}
}

// TestInboundSelfCommentIsIgnored is the self-filter: a comment the connector's
// own login authored is the navi's egress, not a new stimulus, so it cannot
// loop back as an event — while another author's mention still lands.
func TestInboundSelfCommentIsIgnored(t *testing.T) {
	srv := commentServer(t, `[
		{"id":7,"body":"@navi working on it","user":{"login":"navi"}},
		{"id":8,"body":"@navi please look","user":{"login":"alice"}}
	]`)
	q := &fakeTS{}
	c, seen := inboundConnector(t, srv, Config{SelfLogin: "navi"})

	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: q}, seen, time.Hour)

	if q.count() != 1 {
		t.Fatalf("enqueued %d thoughts, want 1 (the self comment is filtered)", q.count())
	}
	if got := q.GetThoughts()[0].Metadata["event_id"]; got != "comment-8" {
		t.Fatalf("enqueued event_id = %q, want comment-8", got)
	}
}

// TestInboundSelfLoginResolvedFromAPI proves the self-filter works without an
// explicit Config.SelfLogin: the account's own login is read from GET /user.
func TestInboundSelfLoginResolvedFromAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/user"):
			_, _ = io.WriteString(w, `{"login":"navi-bot"}`)
		case strings.Contains(r.URL.Path, "/issues/comments"):
			_, _ = io.WriteString(w, `[{"id":9,"body":"@navi done","user":{"login":"navi-bot"}}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	q := &fakeTS{}
	c, seen := inboundConnector(t, srv, Config{})
	c.pollOnce(context.Background(), connector.Deps{ThoughtSystem: q}, seen, time.Hour)
	if q.count() != 0 {
		t.Fatalf("enqueued %d thoughts, want 0 (the resolved self login is filtered)", q.count())
	}
}

// TestMentionsIsWordBoundedAndIgnoresNoise pins the mention matcher: a word
// boundary (`@alice` does not match `@alice-bot` or `bob@alice`) and no match
// inside fenced code, inline code, or a block quote.
func TestMentionsIsWordBoundedAndIgnoresNoise(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"plain":            {"hey @navi look", true},
		"start of body":    {"@navi look", true},
		"punctuation":      {"cc @navi, please", true},
		"end of body":      {"thanks @navi", true},
		"case-insensitive": {"@NaVi", true},
		"longer handle":    {"cc @navi-bot", false},
		"email":            {"bob@navi.example", false},
		"no mention":       {"hello", false},
		"fenced code":      {"```\n@navi\n```", false},
		"tilde fence":      {"~~~\n@navi\n~~~", false},
		"inline code":      {"use `@navi` here", false},
		"block quote":      {"> @navi said hi", false},
		"fence then real":  {"```\n@navi\n```\n@navi now", true},
		"underscore after": {"@navi_b", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mentions(tc.body, "navi"); got != tc.want {
				t.Errorf("mentions(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
	if mentions("anything", "") {
		t.Error("an empty login must not match")
	}
}

// TestInboundPriorityHint pins NAVI-579 for the GitHub connector: the declared
// hint reaches the thought, and an unset hint lands at the planned-work default
// rather than the urgent band.
func TestInboundPriorityHint(t *testing.T) {
	cases := []struct {
		name string
		hint connector.PriorityHint
		want int
	}{
		{"unset is the default", nil, connector.DefaultPriority},
		{"a declared hint is verbatim", connector.PriorityValue(1), 1},
		{"deliberate urgent survives", connector.PriorityValue(0), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := testToken(t)
			ref := "GITHUB_TOKEN"
			srv := commentServer(t, `[{"id":42,"body":"hey @navi take a look","html_url":"https://github.com/acme/widgets/issues/7#issuecomment-42","user":{"login":"alice"}}]`)
			q := &fakeTS{}
			c := New(Config{
				BaseURL:      srv.URL,
				Credential:   ref,
				Env:          envWith(ref, tok),
				HTTPClient:   srv.Client(),
				PollInterval: 20 * time.Millisecond,
				Subscriptions: []Subscription{{
					Name: "issues", Repository: "acme/widgets",
					Events: []EventKind{EventIssueMention}, Mention: "navi",
					Priority: tc.hint,
				}},
			})
			if err := c.Start(context.Background(), connector.Deps{ThoughtSystem: q}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer func() { _ = c.Stop() }()

			th := waitForThought(t, q)
			if th.Priority != tc.want {
				t.Fatalf("priority = %d (effective %d), want %d", th.Priority, th.EffectivePriority(), tc.want)
			}
		})
	}
}

// TestGithubSubscriptionPriorityOutOfRangeIsRejected mirrors the Huly check: a
// hint outside the queue's scale is a composition error.
func TestGithubSubscriptionPriorityOutOfRangeIsRejected(t *testing.T) {
	base := Subscription{
		Name: "x", Repository: "acme/widgets",
		Events: []EventKind{EventIssueMention}, Mention: "navi",
	}
	for _, p := range []int{-1, 11} {
		s := base
		s.Priority = connector.PriorityValue(p)
		err := validateSubscription(s)
		if err == nil || !strings.Contains(err.Error(), "0..10") {
			t.Errorf("priority %d: error = %v, want an out-of-range rejection", p, err)
		}
	}
	s := base
	s.Priority = connector.PriorityValue(0)
	if err := validateSubscription(s); err != nil {
		t.Fatalf("a deliberate urgent hint must validate: %v", err)
	}
}
