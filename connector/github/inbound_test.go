package github

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the repo's pure-Go SQLite driver, for the persisted seen-set test

	"github.com/flintwillow-software/opennavi/connector"
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
			Principal:  "navi:opennavi",
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
	since := time.Now().Add(-time.Hour)

	c.pollOnce(context.Background(), deps, seen, &since)
	c.pollOnce(context.Background(), deps, seen, &since)

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
