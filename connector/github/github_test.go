package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/tool"
)

// testToken derives the token from the test name so the value is unique per
// test and cannot be confused with a real credential (ADR 0008: no secret
// literals in source or fixtures).
func testToken(t *testing.T) string {
	t.Helper()
	return "tkn-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
}

// envWith returns an Env that resolves only ref, so tests never read the real
// environment.
func envWith(ref, value string) func(string) string {
	return func(name string) string {
		if name == ref {
			return value
		}
		return ""
	}
}

// startedConnector builds, starts, and stops a connector against a test
// server, returning the tool the author would add to a navi.
func startedConnector(t *testing.T, srv *httptest.Server, cfg Config) (*Connector, tool.Tool) {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = srv.URL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = srv.Client()
	}
	c := New(cfg)
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	tools, err := c.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("SendTools = %d tools, want 1", len(tools))
	}
	return c, tools[0]
}

// TestEgressToolReachesTheLiveClient is the acceptance criterion that the
// mechanism works end to end: a tool built at authoring time reaches the
// client the connector published at Start, and posts what it was asked to.
func TestEgressToolReachesTheLiveClient(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	token := testToken(t)

	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id": 42, "html_url": "https://github.com/acme/widgets/issues/7#issuecomment-42"}`)
	}))
	defer srv.Close()

	_, tl := startedConnector(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	out, err := tl.Handler(context.Background(), map[string]any{"issue": float64(7), "body": "looked into it"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if gotPath != "/repos/acme/widgets/issues/7/comments" {
		t.Fatalf("path = %q, want the issue-comments endpoint", gotPath)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q, want the bearer token", gotAuth)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("request body %q is not JSON: %v", gotBody, err)
	}
	if sent["body"] != "looked into it" {
		t.Fatalf("posted body = %q", sent["body"])
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("tool output %q is not JSON: %v", out, err)
	}
	if result["commented"] != "acme/widgets#7" {
		t.Fatalf("result = %v, want the commented target", result)
	}
	if result["url"] != "https://github.com/acme/widgets/issues/7#issuecomment-42" {
		t.Fatalf("result url = %v", result["url"])
	}
}

// TestEgressToolFailsClosedBeforeStart is ADR 0014's consequence: the tool is
// bound at authoring time but the client arrives at Start, so a call before
// the connector serves must fail cleanly rather than dereference nil.
func TestEgressToolFailsClosedBeforeStart(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	c := New(Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, testToken(t))})
	tools, err := c.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}

	_, err = tools[0].Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
	if !errors.Is(err, connector.ErrNotStarted) {
		t.Fatalf("call before Start = %v, want ErrNotStarted", err)
	}

	// And again after Stop, so a released client is not reachable either.
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := tools[0].Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"}); !errors.Is(err, connector.ErrNotStarted) {
		t.Fatalf("call after Stop = %v, want ErrNotStarted", err)
	}
}

// TestMissingCredentialDisablesTheCapabilityNotTheNavi pins ADR 0020: an
// unconfigured integration must not abort the run (navi.serve fails the whole
// navi when a connector Start fails), so Start succeeds, warns once, and each
// egress call refuses — naming the REFERENCE, never a value (ADR 0008).
func TestMissingCredentialDisablesTheCapabilityNotTheNavi(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no reference declared", Config{}, "no credential declared"},
		{"reference is unset", Config{Credential: "GITHUB_TOKEN", Env: envWith("OTHER", "x")}, "GITHUB_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.cfg)
			var logged strings.Builder
			deps := connector.Deps{Log: slog.New(slog.NewTextHandler(&logged, nil))}
			if err := c.Start(context.Background(), deps); err != nil {
				t.Fatalf("Start = %v, want it to succeed so the navi still runs", err)
			}
			defer func() { _ = c.Stop() }()

			// The operator is warned early, and the warning names the reference.
			if !strings.Contains(logged.String(), tc.want) {
				t.Fatalf("Start did not warn about the credential: %q", logged.String())
			}

			// The capability itself refuses, at the call site.
			tools, err := c.SendTools()
			if err != nil {
				t.Fatalf("SendTools: %v", err)
			}
			_, err = tools[0].Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("call = %v, want a refusal naming %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "not started") {
				t.Fatalf("the connector IS serving; the refusal must be about the credential: %v", err)
			}
		})
	}
}

// TestTheTokenNeverLeavesTheCallSite is ADR 0014 decision 6 and the issue's
// acceptance criterion: no credential value can appear in the tool output, in
// an error, or in anything that would be written to a transcript, the
// datastore, or a log.
func TestTheTokenNeverLeavesTheCallSite(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	token := testToken(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	c, tl := startedConnector(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	// A failed call: the error carries the status and the response body, never
	// the token.
	_, err := tl.Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
	if err == nil {
		t.Fatal("want a failure from the 403")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the token leaked into an error: %v", err)
	}

	// A successful call: the output is the created comment, not the request.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id": 1, "html_url": "https://example.test/c/1"}`)
	}))
	defer ok.Close()
	_, tlOK := startedConnector(t, ok, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})
	out, err := tlOK.Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if strings.Contains(out, token) {
		t.Fatalf("the token leaked into the tool output: %s", out)
	}

	// Structurally: the connector and its client hold the REFERENCE only, so a
	// dump of either — the shape a log line or a debug print produces — cannot
	// contain the value.
	for _, dumped := range []string{fmt.Sprintf("%+v", c), fmt.Sprintf("%+v", c.newClient())} {
		if strings.Contains(dumped, token) {
			t.Fatalf("the token leaked into a struct dump: %s", dumped)
		}
		if !strings.Contains(dumped, ref) {
			t.Fatalf("the reference should be present (it is not a secret): %s", dumped)
		}
	}
}

// TestRepositoryResolution pins that an egress call cannot silently act on the
// wrong repository, and that the configured default is overridable.
func TestRepositoryResolution(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	token := testToken(t)

	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"id": 1}`)
	}))
	defer srv.Close()

	_, tl := startedConnector(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	if _, err := tl.Handler(context.Background(), map[string]any{"issue": float64(3), "body": "x", "repository": "other/repo"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if path != "/repos/other/repo/issues/3/comments" {
		t.Fatalf("path = %q, want the per-call override", path)
	}

	// No configured default and no override is an error, not a guess.
	_, bare := startedConnector(t, srv, Config{Credential: ref, Env: envWith(ref, token)})
	if _, err := bare.Handler(context.Background(), map[string]any{"issue": float64(3), "body": "x"}); err == nil {
		t.Fatal("a call with no repository must fail")
	}
	// A repository that is not owner/name is refused before any request.
	if _, err := tl.Handler(context.Background(), map[string]any{"issue": float64(3), "body": "x", "repository": "widgets"}); err == nil {
		t.Fatal("a repository without an owner must fail")
	}
}

// TestSendToolsDeclaresAnAttributableShape pins the descriptor an egress tool
// must carry (ADR 0020 decision 5): a namespaced name, a real access level,
// and provenance that identifies the connector.
func TestSendToolsDeclaresAnAttributableShape(t *testing.T) {
	const ref = "GITHUB_TOKEN"
	tools, err := New(Config{Credential: ref, Repository: "acme/widgets"}).SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	def := tools[0].Def
	if def.Name != ToolIssueComment {
		t.Fatalf("name = %q, want %q", def.Name, ToolIssueComment)
	}
	if def.Namespace() != "github" {
		t.Fatalf("namespace = %q, want github", def.Namespace())
	}
	if def.Access != tool.AccessExecute {
		t.Fatalf("access = %v, want execute (egress reaches outside the process)", def.Access)
	}
	if def.SourceInfo.Origin != tool.OriginAuthor || def.SourceInfo.Path != "github" {
		t.Fatalf("provenance = %+v, want author/github", def.SourceInfo)
	}
	if def.Recovery != "capture_response" {
		t.Fatalf("recovery = %q, want capture_response: a retried POST would duplicate the comment", def.Recovery)
	}
	if len(def.InputSchema.Raw()) == 0 {
		t.Fatal("the descriptor must declare an input schema")
	}
}

// recordingTokenSource stands in for a credential that is not a static string
// (a GitHub App installation, which signs a JWT and mints a short-lived token).
// It records how it was called so the seam can be checked rather than assumed.
type recordingTokenSource struct {
	mu    sync.Mutex
	calls int
	ctxs  []context.Context
	value string
	err   error
}

func (s *recordingTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.ctxs = append(s.ctxs, ctx)
	return s.value, s.err
}

func (s *recordingTokenSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestTokenSourceIsUsedInsteadOfAStaticCredential pins the seam ADR 0020
// settles: the connector asks a TokenSource for its bearer token, so a
// credential that must be minted and refreshed is expressible at all. A string
// resolver could not do this, which is why a GitHub App installation is a
// source rather than a longer Config field.
func TestTokenSourceIsUsedInsteadOfAStaticCredential(t *testing.T) {
	src := &recordingTokenSource{value: testToken(t)}

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"id": 1}`)
	}))
	defer srv.Close()

	// No Credential at all: the source is the only credential.
	c := New(Config{Token: src, Repository: "acme/widgets", BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop() }()

	tools, err := c.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	if _, err := tools[0].Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gotAuth != "Bearer "+src.value {
		t.Fatalf("Authorization = %q, want the token the source yielded", gotAuth)
	}

	// The source is asked at the call site, not once at construction: Start
	// asks once (to surface a misconfiguration early) and the call asks again,
	// so a source that refreshes on its own schedule is consulted per request.
	if src.count() < 2 {
		t.Fatalf("source was asked %d time(s); it must be consulted per call, not cached by the connector", src.count())
	}
	// And it is handed a context, so a minting source can bound its own network
	// call and honour cancellation.
	for i, ctx := range src.ctxs {
		if ctx == nil {
			t.Fatalf("call %d got a nil context; a minting source needs one to bound its request", i)
		}
	}
}

// TestTokenSourceErrorIsReportedVerbatim guards the failure path: a source that
// cannot produce a token must surface its own error (which names the reference
// or the app), and the connector must not invent a different one or leak a
// value.
func TestTokenSourceErrorIsReportedVerbatim(t *testing.T) {
	sentinel := errors.New("github: app installation 42 is not configured")
	c := New(Config{Token: &recordingTokenSource{err: sentinel}, Repository: "acme/widgets"})
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop() }()

	tools, err := c.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	_, err = tools[0].Handler(context.Background(), map[string]any{"issue": float64(1), "body": "hi"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("call = %v, want the source's error preserved", err)
	}
}

// TestLiveGitHubIssueComment is the opt-in end-to-end check: it posts a REAL
// comment to a real issue through the connector, so the parts a test double
// cannot vouch for — the endpoint, the auth header GitHub actually accepts, the
// request shape — are exercised once against the live API.
//
// It is gated three ways so it can never fire by accident (CI environments
// frequently have GITHUB_TOKEN in the environment):
//
//	NAVI_LIVE_GITHUB=1                        # explicit opt-in
//	NAVI_TEST_GITHUB_REPO=owner/name          # where to post
//	NAVI_TEST_GITHUB_ISSUE=123                # which issue
//	GITHUB_TOKEN=<token with issues:write>    # the credential reference
//
// Run it with:
//
//	NAVI_LIVE_GITHUB=1 NAVI_TEST_GITHUB_REPO=acme/widgets NAVI_TEST_GITHUB_ISSUE=7 \
//	  GITHUB_TOKEN=... go test -v -run TestLiveGitHubIssueComment ./connector/github/
//
// It POSTS — it does not read a comment and delete it — so point it at a test
// issue you do not mind a comment on.
func TestLiveGitHubIssueComment(t *testing.T) {
	if os.Getenv("NAVI_LIVE_GITHUB") != "1" {
		t.Skip("live GitHub check is opt-in: set NAVI_LIVE_GITHUB=1 with NAVI_TEST_GITHUB_REPO, NAVI_TEST_GITHUB_ISSUE, and GITHUB_TOKEN")
	}
	repo := strings.TrimSpace(os.Getenv("NAVI_TEST_GITHUB_REPO"))
	rawIssue := strings.TrimSpace(os.Getenv("NAVI_TEST_GITHUB_ISSUE"))
	if repo == "" || rawIssue == "" {
		t.Fatal("NAVI_TEST_GITHUB_REPO and NAVI_TEST_GITHUB_ISSUE are required when NAVI_LIVE_GITHUB=1")
	}
	issue, err := strconv.Atoi(rawIssue)
	if err != nil || issue < 1 {
		t.Fatalf("NAVI_TEST_GITHUB_ISSUE = %q, want an issue number", rawIssue)
	}
	if strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) == "" {
		t.Fatal("GITHUB_TOKEN is unset; the connector will refuse (fail closed)")
	}

	// Exactly the composition the template uses: a static reference, the tool
	// added explicitly.
	gh := New(Config{Credential: "GITHUB_TOKEN", Repository: repo})
	if err := gh.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = gh.Stop() }()

	tools, err := gh.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := tools[0].Handler(ctx, map[string]any{
		"issue": float64(issue),
		"body":  "navi connector egress smoke test — safe to delete.",
	})
	if err != nil {
		t.Fatalf("posting a real comment failed: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("tool output %q is not JSON: %v", out, err)
	}
	if want := fmt.Sprintf("%s#%d", repo, issue); result["commented"] != want {
		t.Fatalf("result = %v, want %q", result["commented"], want)
	}
	t.Logf("posted: %v", result["url"])
	if strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "" && strings.Contains(out, os.Getenv("GITHUB_TOKEN")) {
		t.Fatal("the token appeared in the tool output")
	}
}
