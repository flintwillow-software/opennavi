package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// started builds, starts, and stops a connector against a test server,
// returning the connector and its tools keyed by name.
func started(t *testing.T, srv *httptest.Server, cfg Config) (*Connector, map[string]tool.Tool) {
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
	byName := make(map[string]tool.Tool, len(tools))
	for _, tl := range tools {
		byName[tl.Def.Name] = tl
	}
	return c, byName
}

// TestIssueCommentReachesTheLiveClient is the acceptance criterion that the
// mechanism works end to end: a tool built at authoring time reaches the client
// the connector published at Start, and posts what it was asked to.
func TestIssueCommentReachesTheLiveClient(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id": 42, "html_url": "https://git.example.test/acme/widgets/issues/7#issuecomment-42"}`)
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	out, err := tools[ToolIssueComment].Handler(context.Background(),
		map[string]any{"index": float64(7), "body": "looked into it"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	if gotPath != "/api/v1/repos/acme/widgets/issues/7/comments" {
		t.Fatalf("path = %q, want the issue-comments endpoint", gotPath)
	}
	if gotAuth != "token "+token {
		t.Fatalf("Authorization = %q, want the token scheme", gotAuth)
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
	if result["url"] != "https://git.example.test/acme/widgets/issues/7#issuecomment-42" {
		t.Fatalf("result url = %v", result["url"])
	}
}

// TestEgressToolsFailClosedBeforeStart is ADR 0014's consequence: the tools are
// bound at authoring time but the client arrives at Start, so a call before the
// connector serves must fail cleanly rather than dereference nil.
func TestEgressToolsFailClosedBeforeStart(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	c := New(Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, testToken(t))})
	tools, err := c.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	for _, tl := range tools {
		if _, err := tl.Handler(context.Background(), map[string]any{"index": float64(1)}); !errors.Is(err, connector.ErrNotStarted) {
			t.Fatalf("%s before Start = %v, want ErrNotStarted", tl.Def.Name, err)
		}
	}

	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := tools[0].Handler(context.Background(), map[string]any{"index": float64(1)}); !errors.Is(err, connector.ErrNotStarted) {
		t.Fatalf("call after Stop = %v, want ErrNotStarted", err)
	}
}

// TestMissingCredentialDisablesTheCapabilityNotTheNavi pins ADR 0020 decision
// 7: an unconfigured integration must not abort the run, so Start succeeds,
// warns once naming the reference, and each egress call refuses (ADR 0008).
func TestMissingCredentialDisablesTheCapabilityNotTheNavi(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no reference declared", Config{Repository: "acme/widgets"}, "no credential declared"},
		{"reference is unset", Config{Credential: "FORGEJO_TOKEN", Repository: "acme/widgets", Env: envWith("OTHER", "x")}, "FORGEJO_TOKEN"},
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

			if !strings.Contains(logged.String(), tc.want) {
				t.Fatalf("Start did not warn about the credential: %q", logged.String())
			}

			tools, err := c.SendTools()
			if err != nil {
				t.Fatalf("SendTools: %v", err)
			}
			_, err = tools[0].Handler(context.Background(), map[string]any{"index": float64(1), "body": "hi"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("call = %v, want a refusal naming %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "not started") {
				t.Fatalf("the connector IS serving; the refusal must be about the credential: %v", err)
			}
		})
	}
}

// TestTheTokenNeverLeavesTheCallSite is ADR 0014 decision 6: no credential value
// can appear in the tool output, in an error, or in a struct dump.
func TestTheTokenNeverLeavesTheCallSite(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	c, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	_, err := tools[ToolIssueComment].Handler(context.Background(),
		map[string]any{"index": float64(1), "body": "hi"})
	if err == nil {
		t.Fatal("want a failure from the 403")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the token leaked into an error: %v", err)
	}

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
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"id": 1, "number": 3}`)
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	if _, err := tools[ToolIssueComment].Handler(context.Background(),
		map[string]any{"index": float64(3), "body": "x", "repository": "other/repo"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if path != "/api/v1/repos/other/repo/issues/3/comments" {
		t.Fatalf("path = %q, want the per-call override", path)
	}

	_, bare := started(t, srv, Config{Credential: ref, Env: envWith(ref, token)})
	if _, err := bare[ToolIssueComment].Handler(context.Background(),
		map[string]any{"index": float64(3), "body": "x"}); err == nil {
		t.Fatal("a call with no repository must fail")
	}
	if _, err := tools[ToolIssueComment].Handler(context.Background(),
		map[string]any{"index": float64(3), "body": "x", "repository": "widgets"}); err == nil {
		t.Fatal("a repository without an owner must fail")
	}
}

// TestSendToolsDeclareAnAttributableShape pins ADR 0020 decision 5: every egress
// tool is namespaced, declares a real access level, and carries provenance that
// identifies the connector.
func TestSendToolsDeclareAnAttributableShape(t *testing.T) {
	tools, err := New(Config{Credential: "FORGEJO_TOKEN", Repository: "acme/widgets"}).SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	if len(tools) != 9 {
		t.Fatalf("SendTools = %d tools, want 9", len(tools))
	}
	writes := map[string]bool{
		ToolIssueCreate: true, ToolIssueUpdate: true,
		ToolIssueComment: true, ToolPullCreate: true,
	}
	for _, tl := range tools {
		def := tl.Def
		if def.Namespace() != "forgejo" {
			t.Errorf("%s namespace = %q, want forgejo", def.Name, def.Namespace())
		}
		if def.SourceInfo.Origin != tool.OriginAuthor || def.SourceInfo.Path != "forgejo" {
			t.Errorf("%s provenance = %+v, want author/forgejo", def.Name, def.SourceInfo)
		}
		if len(def.InputSchema.Raw()) == 0 {
			t.Errorf("%s must declare an input schema", def.Name)
		}
		if def.Recovery == "" {
			t.Errorf("%s must declare a recovery strategy", def.Name)
		}
		want := tool.AccessReadOnly
		if writes[def.Name] {
			want = tool.AccessExecute
		}
		if def.Access != want {
			t.Errorf("%s access = %v, want %v", def.Name, def.Access, want)
		}
	}
}

// TestIssueListExcludesPullsAndReportsTotal pins the Forgejo/GitHub divergence
// the connector must handle: pull requests share the issues endpoint, so a
// `type=issues` filter is required, and pagination is reported through the
// X-Total-Count header rather than a response envelope.
func TestIssueListExcludesPullsAndReportsTotal(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("X-Total-Count", "137")
		fmt.Fprint(w, `[{"number": 3, "id": 9, "title": "a bug", "state": "open",
			"user": {"login": "ada"}, "labels": [{"id": 1, "name": "bug"}],
			"assignees": [{"login": "grace"}]}]`)
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	out, err := tools[ToolIssueList].Handler(context.Background(),
		map[string]any{"state": "open", "limit": float64(10), "page": float64(2)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	for _, want := range []string{"type=issues", "state=open", "limit=10", "page=2"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q does not contain %q", query, want)
		}
	}

	var result struct {
		Count  int `json:"count"`
		Total  int `json:"total"`
		Issues []struct {
			Index     int      `json:"index"`
			Labels    []string `json:"labels"`
			Assignees []string `json:"assignees"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	if result.Count != 1 || result.Total != 137 {
		t.Fatalf("count/total = %d/%d, want 1/137", result.Count, result.Total)
	}
	if result.Issues[0].Index != 3 || result.Issues[0].Labels[0] != "bug" || result.Issues[0].Assignees[0] != "grace" {
		t.Fatalf("row = %+v, want the projected issue", result.Issues[0])
	}
}

// TestLabelNameResolvesToID pins the second Forgejo divergence: a write assigns
// labels by id, so a name is resolved against the repository's label list.
func TestLabelNameResolvesToID(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var posted []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/labels"):
			fmt.Fprint(w, `[{"id": 5, "name": "bug"}, {"id": 6, "name": "urgent"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues"):
			posted, _ = io.ReadAll(r.Body)
			fmt.Fprint(w, `{"number": 11, "id": 99, "html_url": "https://git.example.test/acme/widgets/issues/11"}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	out, err := tools[ToolIssueCreate].Handler(context.Background(), map[string]any{
		"title":  "a bug",
		"labels": []any{"bug", "6"},
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var sent struct {
		Labels []int64 `json:"labels"`
	}
	if err := json.Unmarshal(posted, &sent); err != nil {
		t.Fatalf("posted %q is not JSON: %v", posted, err)
	}
	if len(sent.Labels) != 2 || sent.Labels[0] != 5 || sent.Labels[1] != 6 {
		t.Fatalf("labels = %v, want [5 6] (a name resolved, an id passed through)", sent.Labels)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output %q is not JSON: %v", out, err)
	}
	if result["index"] != float64(11) {
		t.Fatalf("result = %v, want index 11", result)
	}
}

// TestUnknownLabelIsRefused pins fail-closed label handling: a name the
// repository does not have is an error, not a silently dropped label, and no
// create request is sent.
func TestUnknownLabelIsRefused(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[]`)
			return
		}
		created = true
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	_, err := tools[ToolIssueCreate].Handler(context.Background(), map[string]any{
		"title":  "a bug",
		"labels": []any{"no-such-label"},
	})
	if err == nil || !strings.Contains(err.Error(), "no-such-label") {
		t.Fatalf("call = %v, want a refusal naming the label", err)
	}
	if created {
		t.Fatal("a create request was sent despite an unresolvable label")
	}
}

// TestIssueUpdateSendsOnlyGivenFields pins that an edit touches only what was
// asked, and validates the state vocabulary.
func TestIssueUpdateSendsOnlyGivenFields(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var method, path string
	var posted []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		posted, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"number": 4, "state": "closed"}`)
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	if _, err := tools[ToolIssueUpdate].Handler(context.Background(),
		map[string]any{"index": float64(4), "state": "closed"}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if method != http.MethodPatch || path != "/api/v1/repos/acme/widgets/issues/4" {
		t.Fatalf("%s %s, want PATCH /api/v1/repos/acme/widgets/issues/4", method, path)
	}
	var sent map[string]any
	if err := json.Unmarshal(posted, &sent); err != nil {
		t.Fatalf("posted %q is not JSON: %v", posted, err)
	}
	if len(sent) != 1 || sent["state"] != "closed" {
		t.Fatalf("posted %v, want only state", sent)
	}

	// "all" is a list filter, not a state a write may set.
	if _, err := tools[ToolIssueUpdate].Handler(context.Background(),
		map[string]any{"index": float64(4), "state": "all"}); err == nil {
		t.Fatal("state=all on a write must be refused")
	}
	// Nothing to update is an error, not an empty PATCH.
	if _, err := tools[ToolIssueUpdate].Handler(context.Background(),
		map[string]any{"index": float64(4)}); err == nil {
		t.Fatal("an update with no fields must be refused")
	}
}

// TestReadToolsHitTheirEndpoints pins each read tool's endpoint, which is the
// class of bug a fake server catches and a live shape does not need to.
func TestReadToolsHitTheirEndpoints(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"), strings.HasSuffix(r.URL.Path, "/labels"), strings.HasSuffix(r.URL.Path, "/issues"):
			fmt.Fprint(w, `[]`)
		case strings.Contains(r.URL.Path, "/issues/"):
			fmt.Fprint(w, `{"number": 4}`)
		default:
			fmt.Fprint(w, `{"full_name": "acme/widgets"}`)
		}
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{ToolRepoGet, nil, "/api/v1/repos/acme/widgets"},
		{ToolLabelList, nil, "/api/v1/repos/acme/widgets/labels"},
		{ToolPullList, nil, "/api/v1/repos/acme/widgets/pulls"},
		{ToolIssueGet, map[string]any{"index": float64(4)}, "/api/v1/repos/acme/widgets/issues/4"},
		{ToolIssueList, nil, "/api/v1/repos/acme/widgets/issues"},
	}
	for _, tc := range cases {
		if _, err := tools[tc.name].Handler(context.Background(), tc.args); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if path != tc.want {
			t.Errorf("%s path = %q, want %q", tc.name, path, tc.want)
		}
	}
}

// TestPullCreate pins the pull-request wire shape.
func TestPullCreate(t *testing.T) {
	const ref = "FORGEJO_TOKEN"
	token := testToken(t)

	var path string
	var posted []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		posted, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"number": 12, "id": 77, "html_url": "https://git.example.test/acme/widgets/pulls/12"}`)
	}))
	defer srv.Close()

	_, tools := started(t, srv, Config{Credential: ref, Repository: "acme/widgets", Env: envWith(ref, token)})

	if _, err := tools[ToolPullCreate].Handler(context.Background(), map[string]any{
		"title": "add a thing", "head": "feature/thing", "base": "main",
	}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if path != "/api/v1/repos/acme/widgets/pulls" {
		t.Fatalf("path = %q, want the pulls endpoint", path)
	}
	var sent map[string]string
	if err := json.Unmarshal(posted, &sent); err != nil {
		t.Fatalf("posted %q is not JSON: %v", posted, err)
	}
	if sent["head"] != "feature/thing" || sent["base"] != "main" {
		t.Fatalf("posted %v, want head/base", sent)
	}
}
