package forgejo

// Live smoke test (NAVI-413). It is SKIPPED unless FORGEJO_LIVE_SMOKE=1, and it
// writes to a real repository: it creates a scratch issue, exercises the egress
// paths, and deletes what it created. Run it by hand after a wire shape change —
// the fake-server tests cannot catch a wrong shape, which is exactly how the
// Huly connector's live bugs got through.
//
//	FORGEJO_LIVE_SMOKE=1 FORGEJO_TOKEN=... FORGEJO_REPO=owner/name \
//	  go test -run TestLiveSmoke -v ./connector/forgejo/
//
// FORGEJO_URL overrides the instance root (default DefaultBaseURL). The token
// needs write access to issues in the repository; the repository should be one
// where a transient scratch issue is acceptable.
//
// It exercises the divergences a GitHub-shaped connector gets wrong: labels are
// assigned by id (so a name is resolved first), pull requests share the issues
// endpoint (so a list must filter type=issues), and pagination is reported in
// the X-Total-Count header.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
)

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("FORGEJO_LIVE_SMOKE") != "1" {
		t.Skip("set FORGEJO_LIVE_SMOKE=1 to run the live Forgejo smoke test (it writes to a real repository)")
	}
	repo := strings.TrimSpace(os.Getenv("FORGEJO_REPO"))
	if repo == "" {
		t.Fatal("FORGEJO_REPO (owner/name) is required when FORGEJO_LIVE_SMOKE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c := New(Config{
		BaseURL:    strings.TrimSpace(os.Getenv("FORGEJO_URL")),
		Credential: "FORGEJO_TOKEN",
		Repository: repo,
	})
	if err := c.Start(ctx, connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop() }()
	cl, err := c.live.Get()
	if err != nil {
		t.Fatalf("live client: %v", err)
	}

	// ── reads ──────────────────────────────────────────────────────────
	if _, err := cl.repoGet(ctx, nil); err != nil {
		t.Fatalf("repoGet: %v", err)
	}
	labelsOut, err := cl.labelList(ctx, nil)
	if err != nil {
		t.Fatalf("labelList: %v", err)
	}
	var labels struct {
		Labels []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(labelsOut), &labels); err != nil {
		t.Fatalf("labelList output %q: %v", labelsOut, err)
	}
	t.Logf("repository %s has %d label(s)", repo, len(labels.Labels))
	if _, err := cl.pullList(ctx, nil); err != nil {
		t.Fatalf("pullList: %v", err)
	}

	// ── create ─────────────────────────────────────────────────────────
	title := fmt.Sprintf("navi connector smoke test — delete me (%d)", time.Now().Unix())
	createArgs := map[string]any{"title": title, "body": "opened by the navi connector live smoke test"}
	if len(labels.Labels) > 0 {
		// Exercise name→id resolution against a label the repo really has.
		createArgs["labels"] = []any{labels.Labels[0].Name}
	}
	createdOut, err := cl.issueCreate(ctx, createArgs)
	if err != nil {
		t.Fatalf("issueCreate: %v", err)
	}
	var created struct {
		Index int   `json:"index"`
		ID    int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(createdOut), &created); err != nil {
		t.Fatalf("issueCreate output %q: %v", createdOut, err)
	}
	if created.Index < 1 {
		t.Fatalf("issueCreate returned no index: %s", createdOut)
	}
	// Delete whatever was created, whatever happens next.
	defer func() {
		if _, _, derr := cl.do(context.Background(), http.MethodDelete,
			fmt.Sprintf("/repos/%s/issues/%d", repo, created.Index), nil); derr != nil {
			t.Errorf("cleanup: delete %s#%d: %v", repo, created.Index, derr)
			return
		}
		t.Logf("removed %s#%d", repo, created.Index)
	}()
	t.Logf("created %s#%d (id %d)", repo, created.Index, created.ID)

	// ── comment ────────────────────────────────────────────────────────
	if _, err := cl.issueComment(ctx, map[string]any{"index": float64(created.Index), "body": "smoke comment — delete me"}); err != nil {
		t.Fatalf("issueComment: %v", err)
	}

	// ── update ─────────────────────────────────────────────────────────
	updateArgs := map[string]any{"index": float64(created.Index), "state": "closed"}
	if len(labels.Labels) > 0 {
		updateArgs["labels"] = []any{labels.Labels[0].Name}
	}
	if _, err := cl.issueUpdate(ctx, updateArgs); err != nil {
		t.Fatalf("issueUpdate: %v", err)
	}

	// ── read back ──────────────────────────────────────────────────────
	gotOut, err := cl.issueGet(ctx, map[string]any{"index": float64(created.Index)})
	if err != nil {
		t.Fatalf("issueGet: %v", err)
	}
	var got struct {
		Issue struct {
			Index  int      `json:"index"`
			Title  string   `json:"title"`
			State  string   `json:"state"`
			Labels []string `json:"labels"`
		} `json:"issue"`
	}
	if err := json.Unmarshal([]byte(gotOut), &got); err != nil {
		t.Fatalf("issueGet output %q: %v", gotOut, err)
	}
	if got.Issue.Title != title {
		t.Fatalf("title = %q, want %q", got.Issue.Title, title)
	}
	if got.Issue.State != "closed" {
		t.Fatalf("state = %q, want closed", got.Issue.State)
	}
	if len(labels.Labels) > 0 && len(got.Issue.Labels) != 1 {
		t.Fatalf("labels = %v, want the one resolved label", got.Issue.Labels)
	}
	t.Logf("verified %s#%d: state=%s labels=%v", repo, got.Issue.Index, got.Issue.State, got.Issue.Labels)
}
