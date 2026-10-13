package huly

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flintwillow-software/opennavi/connector"
)

// fakeHuly is a stand-in for a Huly instance: config.json, the accounts RPC,
// and the transactor's find-all/tx routes. It records what it was asked, so a
// test can assert the wire shape the connector produces.
type fakeHuly struct {
	srv *httptest.Server

	mu         sync.Mutex
	txs        []map[string]any
	loginCalls int
	requests   []string

	issues   []map[string]any
	statuses []map[string]any
	persons  []map[string]any
	project  map[string]any

	tags          []map[string]any
	tagRefs       []map[string]any
	tagCategories []map[string]any
}

func newFakeHuly(t *testing.T) *fakeHuly {
	t.Helper()
	f := &fakeHuly{
		issues: []map[string]any{
			{"_id": "6acd168268c57585aac3fa3f", "identifier": "NAVI-408", "title": "Huly connector",
				"status": "tracker:status:InProgress", "assignee": "6aa2a3038376b1c099c0403d",
				"priority": 2, "number": 408, "modifiedOn": 1000, "space": "space-1", "rank": "0|i001c7:"},
		},
		project: map[string]any{
			"_id": "space-1", "identifier": "NAVI", "name": "OpenNAVI", "sequence": 418,
			"defaultIssueStatus": "tracker:status:Backlog", "space": "core:space:Space",
		},
		statuses: []map[string]any{
			{"_id": "tracker:status:Backlog", "name": "Backlog", "category": "task:statusCategory:UnStarted"},
			{"_id": "tracker:status:InProgress", "name": "In Progress", "category": "task:statusCategory:Active"},
		},
		persons: []map[string]any{
			{"_id": "6aa2a3038376b1c099c0403d", "name": "bastilli"},
		},
		tagCategories: []map[string]any{
			{"_id": "tracker:category:Other", "targetClass": classIssue, "default": true, "label": "Other"},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/config.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ACCOUNTS_URL": f.srv.URL + "/_accounts"})
	})
	mux.HandleFunc("/_accounts", f.serveAccounts)
	mux.HandleFunc("/_transactor/api/v1/find-all/", f.serveFindAll)
	mux.HandleFunc("/_transactor/api/v1/account/", f.serveAccount)
	mux.HandleFunc("/_transactor/api/v1/tx/", f.serveTx)
	mux.HandleFunc("/_transactor/api/v1/search-fulltext/", f.serveSearch)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHuly) serveAccounts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		Params struct {
			Email     string `json:"email"`
			Password  string `json:"password"`
			Workspace string `json:"workspaceUrl"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	if req.Method == "login" {
		f.loginCalls++
	}
	f.requests = append(f.requests, "accounts:"+req.Method)
	f.mu.Unlock()

	switch req.Method {
	case "login":
		if req.Params.Email != "bot@example.com" || req.Params.Password != "hunter2" {
			writeJSON(w, map[string]any{"error": "bad credentials"})
			return
		}
		writeJSON(w, map[string]any{"result": map[string]any{"token": "account-token"}})
	case "selectWorkspace":
		ws := strings.Replace(f.srv.URL, "http", "ws", 1)
		writeJSON(w, map[string]any{"result": map[string]any{
			"endpoint":  ws + "/_transactor",
			"token":     "workspace-token",
			"workspace": "ws-1",
			"account":   "acct-1",
		}})
	default:
		writeJSON(w, map[string]any{"error": "unknown method " + req.Method})
	}
}

func (f *fakeHuly) serveFindAll(w http.ResponseWriter, r *http.Request) {
	class := r.URL.Query().Get("class")
	var query map[string]any
	if raw := r.URL.Query().Get("query"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &query)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, "find-all:"+class)

	data := []map[string]any{}
	switch class {
	case classIssue:
		data = f.issues
	case classIssueStatus:
		data = f.statuses
	case classPerson:
		data = f.persons
	case classProject:
		if f.project != nil {
			data = []map[string]any{f.project}
		}
	case classTagElement:
		data = f.tags
	case classTagReference:
		data = f.tagRefs
	case classTagCategory:
		data = f.tagCategories
	}
	writeJSON(w, map[string]any{"dataType": "TotalArray", "total": -1, "value": stripQueryFields(filterDocs(data, query), query)})
}

// stripQueryFields mimics the live server, which uses find-all's query as an
// exclusion projection: every top-level scalar field named in the query is
// absent from the returned documents. The connector compensates in
// restoreQueryFields, and this makes the fake able to catch a regression.
// Documents are copied, so the fake's own state is never mutated.
func stripQueryFields(docs []map[string]any, query map[string]any) []map[string]any {
	if len(query) == 0 {
		return docs
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		clone := make(map[string]any, len(d))
		for k, v := range d {
			clone[k] = v
		}
		for k, v := range query {
			if _, nested := v.(map[string]any); nested {
				continue
			}
			delete(clone, k)
		}
		out = append(out, clone)
	}
	return out
}

// serveSearch answers the full-text search route with Huly's `{docs, total}`
// envelope.
func (f *fakeHuly) serveSearch(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, "search:"+r.URL.Query().Get("query"))
	f.mu.Unlock()
	writeJSON(w, map[string]any{
		"total": 1,
		"docs": []map[string]any{{
			"id": "6acd168268c57585aac3fa3f", "title": "Huly connector",
			"shortTitle": "NAVI-408", "score": 12.5,
			"doc": map[string]any{"_id": "6acd168268c57585aac3fa3f", "_class": "tracker:class:Issue"},
		}},
	})
}

// serveAccount answers the account route, whose primarySocialId is the author
// a transaction must carry.
func (f *fakeHuly) serveAccount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"uuid": "acct-uuid", "primarySocialId": "social-1"})
}

func (f *fakeHuly) serveTx(w http.ResponseWriter, r *http.Request) {
	blob, _ := io.ReadAll(r.Body)
	var tx map[string]any
	if err := json.Unmarshal(blob, &tx); err != nil {
		http.Error(w, "bad tx", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	// A sequence bump is the create path's first transaction: answer with the
	// updated document, the way a `retrieve` transaction does.
	if tx["_class"] == classTxUpdateDoc {
		if ops, ok := tx["operations"].(map[string]any); ok {
			if inc, ok := ops["$inc"].(map[string]any); ok {
				if _, ok := inc["sequence"]; ok && f.project != nil {
					f.project["sequence"] = toInt(f.project["sequence"]) + 1
					f.txs = append(f.txs, tx)
					seq := f.project["sequence"]
					f.mu.Unlock()
					writeJSON(w, map[string]any{"result": map[string]any{"sequence": seq}})
					return
				}
			}
		}
	}
	f.txs = append(f.txs, tx)
	f.mu.Unlock()
	writeJSON(w, map[string]any{"result": []any{}})
}

// filterDocs applies the small query subset the connector uses: equality on a
// field, and `_id: {$in: [...]}`.
func filterDocs(docs []map[string]any, query map[string]any) []map[string]any {
	if len(query) == 0 {
		return docs
	}
	out := []map[string]any{}
	for _, d := range docs {
		if matches(d, query) {
			out = append(out, d)
		}
	}
	return out
}

func matches(doc, query map[string]any) bool {
	for k, v := range query {
		switch want := v.(type) {
		case map[string]any:
			in, ok := want["$in"].([]any)
			if !ok {
				return false
			}
			found := false
			for _, cand := range in {
				if doc[k] == cand {
					found = true
				}
			}
			if !found {
				return false
			}
		default:
			if doc[k] != want {
				return false
			}
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// connectFake builds a connector pointed at the fake, with a token reference.
func connectFake(t *testing.T, f *fakeHuly, env map[string]string) (*Connector, *connector.Live[*Client]) {
	t.Helper()
	cfg := Config{
		URL:        f.srv.URL,
		Workspace:  "flintwillowsoftware",
		Credential: "HULY_TOKEN",
		Env:        func(k string) string { return env[k] },
	}
	c := New(cfg)
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c, c.live
}

// TestIssueListResolvesStatusAndAssignee is the read path: the connector
// authenticates, finds issues, and resolves the status and assignee references
// to names.
func TestIssueListResolvesStatusAndAssignee(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "account-token"})
	client, err := live.Get()
	if err != nil {
		t.Fatalf("live client: %v", err)
	}

	out, err := client.issueList(context.Background(), map[string]any{"limit": 5})
	if err != nil {
		t.Fatalf("issueList: %v", err)
	}
	var got struct {
		Issues []map[string]any `json:"issues"`
		Count  int              `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode result: %v (%s)", err, out)
	}
	if got.Count != 1 || len(got.Issues) != 1 {
		t.Fatalf("issues = %s, want one", out)
	}
	row := got.Issues[0]
	if row["identifier"] != "NAVI-408" || row["statusName"] != "In Progress" || row["assigneeName"] != "bastilli" {
		t.Fatalf("row = %v, want resolved status and assignee", row)
	}
	// A token reference means no login RPC was needed.
	f.mu.Lock()
	logins := f.loginCalls
	f.mu.Unlock()
	if logins != 0 {
		t.Errorf("login RPC called %d times for a token reference, want 0", logins)
	}
}

// TestIssueListFiltersByStatusName pins the status-name resolution: a filter by
// display name becomes the status reference Huly queries on.
func TestIssueListFiltersByStatusName(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueList(context.Background(), map[string]any{"status": "In Progress"}); err != nil {
		t.Fatalf("issueList: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, r := range f.requests {
		if r == "find-all:"+classIssueStatus {
			found = true
		}
	}
	if !found {
		t.Errorf("status filter did not resolve statuses: %v", f.requests)
	}
}

// TestIssueCommentPostsACollectionTransaction is the write path: a comment is an
// attached ChatMessage in the issue's comments collection, and the transaction
// carries the parent and the collection.
func TestIssueCommentPostsACollectionTransaction(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueComment(context.Background(), map[string]any{
		"issue": "NAVI-408", "body": "picking this up",
	})
	if err != nil {
		t.Fatalf("issueComment: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("result = %s, want ok", out)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.txs) != 1 {
		t.Fatalf("posted %d transactions, want 1", len(f.txs))
	}
	tx := f.txs[0]
	if tx["_class"] != classTxCreateDoc || tx["objectClass"] != classChatMessage {
		t.Errorf("tx classes = %v / %v, want a ChatMessage create", tx["_class"], tx["objectClass"])
	}
	if tx["modifiedBy"] != "social-1" || tx["createdBy"] != "social-1" {
		t.Errorf("author = %v/%v, want the account's primary social id", tx["modifiedBy"], tx["createdBy"])
	}
	if tx["collection"] != collectionComments || tx["attachedTo"] != "6acd168268c57585aac3fa3f" ||
		tx["attachedToClass"] != classIssue {
		t.Errorf("tx attachment = %v/%v/%v, want the issue's comments collection",
			tx["collection"], tx["attachedTo"], tx["attachedToClass"])
	}
	if tx["space"] != spaceTx || tx["objectSpace"] != "space-1" {
		t.Errorf("tx spaces = %v / %v, want core:space:Tx and the issue space", tx["space"], tx["objectSpace"])
	}
	attrs, _ := tx["attributes"].(map[string]any)
	message, _ := attrs["message"].(string)
	if !strings.Contains(message, `"type":"doc"`) || !strings.Contains(message, "picking this up") {
		t.Errorf("comment message = %q, want a ProseMirror document", message)
	}
}

// TestLoginWhenNoTokenReference is the credential half: email and password
// references log in, and the token reference skips it.
func TestLoginWhenNoTokenReference(t *testing.T) {
	f := newFakeHuly(t)
	cfg := Config{
		URL: f.srv.URL, Workspace: "flintwillowsoftware",
		Email: "HULY_EMAIL", Password: "HULY_PASSWORD",
		Env: func(k string) string {
			return map[string]string{"HULY_EMAIL": "bot@example.com", "HULY_PASSWORD": "hunter2"}[k]
		},
	}
	c := New(cfg)
	if err := c.Start(context.Background(), connector.Deps{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	client, err := c.live.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.issueList(context.Background(), nil); err != nil {
		t.Fatalf("issueList: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loginCalls != 1 {
		t.Errorf("login RPC called %d times, want 1", f.loginCalls)
	}
}

// TestEgressToolsRefuseBeforeStart is the lifecycle gate (ADR 0014): a tool
// bound at authoring time is callable before Start only to be refused cleanly.
func TestEgressToolsRefuseBeforeStart(t *testing.T) {
	f := newFakeHuly(t)
	c := New(Config{URL: f.srv.URL, Workspace: "ws", Credential: "T", Env: func(string) string { return "t" }})
	tools, err := c.SendTools()
	if err != nil {
		t.Fatalf("SendTools: %v", err)
	}
	if len(tools) != 7 {
		t.Fatalf("got %d tools, want 7", len(tools))
	}
	for _, tl := range tools {
		if _, err := tl.Handler(context.Background(), nil); !errors.Is(err, connector.ErrNotStarted) {
			t.Fatalf("%s before Start = %v, want ErrNotStarted", tl.Def.Name, err)
		}
	}
}

// TestNoCredentialIsRefused pins the fail-closed credential path: a connector
// with no reference resolves nothing and says so rather than calling anonymously.
func TestNoCredentialIsRefused(t *testing.T) {
	f := newFakeHuly(t)
	c := New(Config{URL: f.srv.URL, Workspace: "ws", Env: func(string) string { return "" }})
	client := newClient(c.cfg)
	if err := client.connect(context.Background()); err == nil {
		t.Fatal("connect with no credential succeeded, want a refusal")
	}
}

// TestResolveRefReadsEnvThenFile pins ADR 0008's two reference shapes.
func TestResolveRefReadsEnvThenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveRef("MY_TOKEN", func(k string) string {
		if k == "MY_TOKEN" {
			return " from-env "
		}
		return ""
	}); got != "from-env" {
		t.Errorf("env reference = %q, want from-env", got)
	}
	if got := resolveRef(path, func(string) string { return "" }); got != "from-file" {
		t.Errorf("file reference = %q, want from-file", got)
	}
	if got := resolveRef("MISSING", func(string) string { return "" }); got != "" {
		t.Errorf("missing reference = %q, want empty", got)
	}
}

// TestIssueUpdateSetsStatusAssigneeAndPriority pins the update transaction: a
// single TxUpdateDoc carrying only the fields given, with the status and
// assignee references resolved.
func TestIssueUpdateSetsStatusAssigneeAndPriority(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueUpdate(context.Background(), map[string]any{
		"issue": "NAVI-408", "status": "In Progress", "assignee": "bastilli", "priority": "high",
	})
	if err != nil {
		t.Fatalf("issueUpdate: %v", err)
	}
	if !strings.Contains(out, `"updated":["status","assignee","priority"]`) {
		t.Errorf("result = %s, want the updated fields", out)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.txs) != 1 {
		t.Fatalf("posted %d transactions, want 1", len(f.txs))
	}
	tx := f.txs[0]
	if tx["_class"] != classTxUpdateDoc || tx["objectId"] != "6acd168268c57585aac3fa3f" ||
		tx["objectClass"] != classIssue {
		t.Fatalf("tx = %v, want a TxUpdateDoc on the issue", tx)
	}
	if tx["modifiedBy"] != "social-1" {
		t.Errorf("author = %v, want the account's primary social id", tx["modifiedBy"])
	}
	set, _ := tx["operations"].(map[string]any)
	if set["status"] != "tracker:status:InProgress" {
		t.Errorf("status = %v, want the resolved reference", set["status"])
	}
	if set["assignee"] != "6aa2a3038376b1c099c0403d" {
		t.Errorf("assignee = %v, want the resolved person id", set["assignee"])
	}
	if set["priority"] != float64(2) {
		t.Errorf("priority = %v, want 2 (high)", set["priority"])
	}
}

// TestIssueUpdateClearsAssignee pins the empty-assignee path: it clears the
// field rather than resolving a person named "".
func TestIssueUpdateClearsAssignee(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueUpdate(context.Background(), map[string]any{"issue": "NAVI-408", "assignee": ""}); err != nil {
		t.Fatalf("issueUpdate: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	set, _ := f.txs[0]["operations"].(map[string]any)
	if v, present := set["assignee"]; !present || v != nil {
		t.Errorf("assignee = %v (present %v), want an explicit null", v, present)
	}
}

// TestIssueUpdateRefusesNothingToDo pins the guard: an update with no fields is
// refused rather than posting an empty transaction.
func TestIssueUpdateRefusesNothingToDo(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()
	if _, err := client.issueUpdate(context.Background(), map[string]any{"issue": "NAVI-408"}); err == nil {
		t.Fatal("an update with no fields succeeded, want a refusal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.txs) != 0 {
		t.Errorf("posted %d transactions for a no-op, want 0", len(f.txs))
	}
}

// TestIssueSearchReturnsDocuments pins the search path: the full-text route,
// mapped to the documents Huly returns.
func TestIssueSearchReturnsDocuments(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueSearch(context.Background(), map[string]any{"query": "Huly connector", "limit": 5})
	if err != nil {
		t.Fatalf("issueSearch: %v", err)
	}
	var got struct {
		Results []map[string]any `json:"results"`
		Count   int              `json:"count"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if got.Count != 1 || got.Results[0]["identifier"] != "NAVI-408" ||
		got.Results[0]["class"] != "tracker:class:Issue" {
		t.Fatalf("results = %s, want the issue", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, r := range f.requests {
		if r == "search:Huly connector" {
			found = true
		}
	}
	if !found {
		t.Errorf("search route not called: %v", f.requests)
	}
}

// TestPriorityScale pins Huly's priority numbering: no-priority is 0 and a
// LOWER number is a HIGHER priority.
func TestPriorityScale(t *testing.T) {
	for name, want := range map[string]int{"no-priority": 0, "urgent": 1, "high": 2, "medium": 3, "low": 4} {
		if got, ok := priorityFromString(name); !ok || got != want {
			t.Errorf("priorityFromString(%q) = %d (ok %v), want %d", name, got, ok, want)
		}
	}
	if priorityToString(2) != "high" {
		t.Errorf("priorityToString(2) = %q, want high", priorityToString(2))
	}
	if _, ok := priorityFromString("catastrophic"); ok {
		t.Error("an unknown priority was accepted")
	}
}

// TestRankNext pins the LexoRank successor: the middle for a project with no
// issues, then +8 (the gap Huly's own client leaves), and strict monotonicity
// over a run.
func TestRankNext(t *testing.T) {
	for prev, want := range map[string]string{
		"":          rankMiddle,
		"0|hzzzzz:": "0|i00007:",
		"0|i00007:": "0|i0000f:",
		"0|i001c7:": "0|i001cf:",
	} {
		got, err := rankNext(prev)
		if err != nil || got != want {
			t.Errorf("rankNext(%q) = %q (%v), want %q", prev, got, err, want)
		}
	}
	prev := rankMiddle
	for i := 0; i < 64; i++ {
		next, err := rankNext(prev)
		if err != nil {
			t.Fatalf("rankNext(%q): %v", prev, err)
		}
		if next <= prev {
			t.Fatalf("rankNext(%q) = %q, which does not sort after it", prev, next)
		}
		prev = next
	}
}

// TestIssueCreateCreatesATopLevelIssue pins the whole create flow: the sequence
// bump, the rank placed after the project's last issue, and the document
// attached to NoParent.
func TestIssueCreateCreatesATopLevelIssue(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueCreate(context.Background(), map[string]any{
		"project": "NAVI", "title": "a new issue", "status": "In Progress", "priority": "high",
	})
	if err != nil {
		t.Fatalf("issueCreate: %v", err)
	}
	if !strings.Contains(out, `"identifier":"NAVI-419"`) {
		t.Errorf("result = %s, want NAVI-419", out)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.txs) != 2 {
		t.Fatalf("posted %d transactions, want a sequence bump and a create", len(f.txs))
	}
	inc := f.txs[0]
	if inc["_class"] != classTxUpdateDoc || inc["objectClass"] != classProject {
		t.Fatalf("first transaction = %v, want the project sequence bump", inc)
	}
	if inc["modifiedBy"] != "social-1" {
		t.Errorf("the sequence bump's author = %v, want the primary social id", inc["modifiedBy"])
	}
	create := f.txs[1]
	if create["modifiedBy"] != "social-1" || create["createdBy"] != "social-1" {
		t.Errorf("the create's author = %v/%v, want the primary social id", create["modifiedBy"], create["createdBy"])
	}
	if create["_class"] != classTxCreateDoc || create["objectClass"] != classIssue ||
		create["objectSpace"] != "space-1" {
		t.Fatalf("create transaction = %v, want an issue in the project space", create)
	}
	if create["attachedTo"] != noParent || create["attachedToClass"] != classIssue ||
		create["collection"] != collectionSubIssues {
		t.Errorf("attachment = %v/%v/%v, want a top-level issue",
			create["attachedTo"], create["attachedToClass"], create["collection"])
	}
	attrs, _ := create["attributes"].(map[string]any)
	if attrs["identifier"] != "NAVI-419" || attrs["number"] != float64(419) {
		t.Errorf("identity = %v/%v, want NAVI-419", attrs["identifier"], attrs["number"])
	}
	if attrs["status"] != "tracker:status:InProgress" || attrs["priority"] != float64(2) {
		t.Errorf("status/priority = %v/%v, want In Progress / 2", attrs["status"], attrs["priority"])
	}
	if attrs["rank"] != "0|i001cf:" {
		t.Errorf("rank = %v, want the successor of the last issue's rank", attrs["rank"])
	}
	if v, present := attrs["description"]; !present || v != nil {
		t.Errorf("description = %v, want null (rich text is a separate service)", v)
	}
	if attrs["kind"] != taskTypeIssue {
		t.Errorf("kind = %v, want %s", attrs["kind"], taskTypeIssue)
	}
}

// TestIssueCreateAsASubIssue pins the parent path: the issue is attached to its
// parent and carries the parent chain.
func TestIssueCreateAsASubIssue(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueCreate(context.Background(), map[string]any{
		"project": "NAVI", "title": "child", "parent": "NAVI-408",
	}); err != nil {
		t.Fatalf("issueCreate: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	create := f.txs[1]
	if create["attachedTo"] != "6acd168268c57585aac3fa3f" || create["attachedToClass"] != classIssue {
		t.Errorf("attachment = %v, want the parent issue", create["attachedTo"])
	}
	attrs, _ := create["attributes"].(map[string]any)
	parents, _ := attrs["parents"].([]any)
	if len(parents) != 1 {
		t.Fatalf("parents = %v, want one entry", attrs["parents"])
	}
	entry, _ := parents[0].(map[string]any)
	if entry["identifier"] != "NAVI-408" {
		t.Errorf("parent entry = %v, want NAVI-408", entry)
	}
}

// TestIssueCreateRefusesAnUnknownProject pins the fail-closed lookup.
func TestIssueCreateRefusesAnUnknownProject(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()
	if _, err := client.issueCreate(context.Background(), map[string]any{"project": "NOPE", "title": "x"}); err == nil {
		t.Fatal("create in an unknown project succeeded, want a refusal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.txs) != 0 {
		t.Errorf("posted %d transactions for an unknown project, want 0", len(f.txs))
	}
}
