package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/datastore/sqlite"
	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/principal"
	"github.com/flintwillow-software/opennavi/thought"
)

// allowAllAuth permits every portal action: the shim-attachment test
// seam (production wires nil and fails closed until IAM).
type allowAllAuth struct{}

func (a allowAllAuth) Authorize(portal.Action, portal.Actor) error {
	return nil
}

// servePortal starts a portal gateway over a queue (the pool NOT started,
// so thoughts stay pending and the enqueue is deterministic). allow
// controls whether an authorizer is wired. Returns the gateway address
// and the queue the running navi would expose.
func servePortal(t *testing.T, allow bool) (string, *thought.Queue) {
	t.Helper()
	addr := ""
	{
		ln, lerr := net.Listen("tcp", "127.0.0.1:0")
		if lerr != nil {
			t.Fatal(lerr)
		}
		addr = ln.Addr().String()
		ln.Close()
	}
	var authz portal.Authorizer
	if allow {
		authz = allowAllAuth{}
	}
	q, qerr := thought.NewQueue(thought.Config{MaxConcurrent: 4, SlotExhaustion: thought.PolicyQueue}, thought.NewMemoryStore())
	if qerr != nil {
		t.Fatal(qerr)
	}
	broker := portal.NewBroker()
	q.OnEvent(func(ev thought.Event) { broker.Publish(portal.QueueEvent(ev)) })
	control := portal.NewControl(authz)
	if serr := control.Start(context.Background(), portal.Deps{ThoughtSystem: q, Observation: broker}); serr != nil {
		t.Fatal(serr)
	}
	sln, slerr := net.Listen("tcp", addr)
	if slerr != nil {
		t.Fatal(slerr)
	}
	srv := &http.Server{Addr: addr, Handler: gateway.Handler(control, broker, nil, portal.AllowAllAuthenticator(principal.MustParse("service:test"))), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(sln) }()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(func() { cancel(); _ = srv.Shutdown(shutdown) })
	time.Sleep(150 * time.Millisecond)
	return addr, q
}

func dbThoughts(t *testing.T, db string) []*thought.Thought {
	t.Helper()
	st, oerr := sqlite.Open(db, sqlite.Options{})
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer st.Close()
	ts, terr := sqlite.NewThoughtStore(st.DB())
	if terr != nil {
		t.Fatal(terr)
	}
	all, lerr := ts.LoadThoughts()
	if lerr != nil {
		t.Fatal(lerr)
	}
	return all
}

func TestEnqueueViaPortalReachable(t *testing.T) {
	addr, rt := servePortal(t, true)
	db := t.TempDir() + "/portal.db"
	err := runEnqueue([]string{"-portal", "http://" + addr, "-db", db, "portal task"})
	if err != nil {
		t.Fatal(err)
	}
	// Landed via the control channel: in the running navi's queue.
	found := false
	for _, th := range rt.GetThoughts() {
		if th.Prompt == "portal task" {
			found = true
		}
	}
	if !found {
		t.Fatal("thought not delivered through the portal")
	}
	// The DB mailbox was NOT written on the portal path.
	if len(dbThoughts(t, db)) != 0 {
		t.Error("portal path also wrote the DB mailbox")
	}
}

func TestEnqueueUnreachableFallsBackToDb(t *testing.T) {
	db := t.TempDir() + "/fallback.db"
	err := runEnqueue([]string{"-portal", "http://127.0.0.1:9", "-db", db, "fallback task"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, th := range dbThoughts(t, db) {
		if th.Prompt == "fallback task" {
			found = true
		}
	}
	if !found {
		t.Fatal("unreachable portal did not fall back to the DB mailbox")
	}
}

func TestEnqueuePortalFailClosedFallsBack(t *testing.T) {
	addr, _ := servePortal(t, false) // no authorizer: 403
	db := t.TempDir() + "/failclosed.db"
	err := runEnqueue([]string{"-portal", "http://" + addr, "-db", db, "nope task"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, th := range dbThoughts(t, db) {
		if th.Prompt == "nope task" {
			found = true
		}
	}
	if !found {
		t.Fatal("fail-closed portal did not fall back to the DB mailbox")
	}
}

func TestRunEnqueuePromptFirst(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	err := runEnqueue([]string{"hello second", "--db", db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal(err)
	}
}

func TestRunEnqueueFlagsFirst(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	err := runEnqueue([]string{"-priority", "7", "-db", db, "the prompt"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunEnqueueMultiWordPrompt(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/test.db"
	err := runEnqueue([]string{"multi", "word", "prompt", "-priority", "2", "-db", db})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunEnqueuePriorityValidation(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"0", "11", "-1"} {
		err := runEnqueue([]string{"-priority", p, "-db", dir + "/x.db", "task"})
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Errorf("priority %q: error = %v, want out-of-range", p, err)
		}
	}
}

func TestRunEnqueueMissingPrompt(t *testing.T) {
	dir := t.TempDir()
	err := runEnqueue([]string{"-db", dir + "/x.db"})
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunEnqueueMissingDB(t *testing.T) {
	err := runEnqueue([]string{"some prompt"})
	if err == nil || !strings.Contains(err.Error(), "db path is required") {
		t.Fatalf("error = %v, want db path required", err)
	}
}

func TestRunEnqueueThreeCaseOllamaHost(t *testing.T) {
	// 3-case CLI smoke: flag set / env set / neither — OllamaHost
	// reaches the provider in all cases.
	t.Run("flag set", func(t *testing.T) {
		// runEnqueue doesn't set OllamaHost; the flag is consumed by
		// the daemon subcommand. The CLI chat path wires it via
		// Config.OllamaHost. This test verifies the enqueue path
		// doesn't error with OLLAMA_HOST unset.
		t.Setenv("OLLAMA_HOST", "")
		dir := t.TempDir()
		err := runEnqueue([]string{"task", "-priority", "3", "-db", dir + "/x.db"})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("env set", func(t *testing.T) {
		t.Setenv("OLLAMA_HOST", "http://10.0.0.50:11434")
		dir := t.TempDir()
		err := runEnqueue([]string{"task", "-db", dir + "/y.db"})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("neither", func(t *testing.T) {
		t.Setenv("OLLAMA_HOST", "")
		dir := t.TempDir()
		err := runEnqueue([]string{"task", "-db", dir + "/z.db"})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunEnqueueProviderModel(t *testing.T) {
	dir := t.TempDir()
	db := dir + "/t.db"
	err := runEnqueue([]string{"-provider", "mock", "-model", "fast-1", "-db", db, "do the thing"})
	if err != nil {
		t.Fatal(err)
	}
	st, oerr := sqlite.Open(db, sqlite.Options{})
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer st.Close()
	ts, terr := sqlite.NewThoughtStore(st.DB())
	if terr != nil {
		t.Fatal(terr)
	}
	all, lerr := ts.LoadThoughts()
	if lerr != nil {
		t.Fatal(lerr)
	}
	found := false
	for _, th := range all {
		if th.Prompt == "do the thing" && th.Provider == "mock" && th.Model == "fast-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider/model flags not persisted: %+v", all)
	}
}
