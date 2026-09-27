package navi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/llm"
	"github.com/flintwillow-software/opennavi/model"
	"github.com/flintwillow-software/opennavi/thought"
)

// The live tests drive the whole path against a REAL provider: declare,
// run, enqueue, model reply, transcript. They skip rather than fail when
// a provider is not available, so CI stays hermetic while a developer
// with a local model gets the real thing.
//
// Ollama is first because it needs no credential — it validates the loop
// without dragging secret plumbing into the picture. OpenRouter is
// second, and it is what exercises credential resolution through the
// descriptor's CredentialRef.
//
// Enable with:
//
//	OPENNAVI_LIVE_OLLAMA_MODEL=qwen3.8:27b go test ./navi/ -run Live -v
//	OPENROUTER_API_KEY=... go test ./navi/ -run Live -v

const livePrompt = "Reply with exactly one word: pong"

func liveOllamaModel(t *testing.T) string {
	t.Helper()
	modelName := os.Getenv("OPENNAVI_LIVE_OLLAMA_MODEL")
	if modelName == "" {
		t.Skip("OPENNAVI_LIVE_OLLAMA_MODEL not set — name a local model to run live Ollama tests")
	}
	host := llm.OllamaHostFromEnv()
	if !reachable(host) {
		t.Skipf("ollama not reachable at %s", host)
	}
	return modelName
}

func liveOpenRouterModel(t *testing.T) string {
	t.Helper()
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — skipping live OpenRouter tests")
	}
	if m := os.Getenv("OPENNAVI_LIVE_OPENROUTER_MODEL"); m != "" {
		return m
	}
	return "z-ai/glm-5.3-flash"
}

// TestLiveOllamaThinking is the loop check: a thought reaches a local
// model through the full navi path and comes back complete, with the
// exchange in its session.
func TestLiveOllamaThinking(t *testing.T) {
	modelName := liveOllamaModel(t)

	n := New("live-ollama", WithDefaultModel("ollama"), WithWorkers(1))
	if err := n.Models.Add(model.Ollama(modelName)); err != nil {
		t.Fatal(err)
	}

	reply := runLiveThought(t, n)
	if strings.TrimSpace(reply) == "" {
		// Ollama models are not reasoning-only under a normal budget, so
		// an empty visible reply here means something is actually wrong.
		t.Fatalf("ollama returned no visible content for %q", livePrompt)
	}
	t.Logf("ollama (%s) replied: %q", modelName, strings.TrimSpace(reply))
}

// TestLiveOpenRouterThinking exercises the credential path: the
// descriptor carries only a CredentialRef, and the key must reach the
// adapter through environment resolution.
func TestLiveOpenRouterThinking(t *testing.T) {
	modelName := liveOpenRouterModel(t)

	n := New("live-openrouter", WithDefaultModel("openrouter"), WithWorkers(1))
	if err := n.Models.Add(model.OpenRouter(modelName)); err != nil {
		t.Fatal(err)
	}

	reply := runLiveThought(t, n)
	// Content is NOT asserted non-empty: reasoning-capable models can
	// spend a small budget on thinking tokens before any visible text
	// (the llm live tests carry the same note). Completion plus a
	// recorded turn is the assertion.
	t.Logf("openrouter (%s) replied: %q", modelName, strings.TrimSpace(reply))
}

// TestLiveDefaultProviderWithoutCredentialFailsClosed is the negative
// half: with the key absent, a navi whose default is OpenRouter must
// refuse to run rather than serve work it cannot complete.
func TestLiveDefaultProviderWithoutCredentialFailsClosed(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")

	n := New("live-fail-closed", WithDefaultModel("openrouter"))
	if err := n.Models.Add(model.OpenRouter("z-ai/glm-5.3-flash")); err != nil {
		t.Fatal(err)
	}
	err := n.Run(t.Context())
	if err == nil {
		t.Fatal("a default provider with no credential must fail the run")
	}
	if !strings.Contains(err.Error(), string(model.DefaultOpenRouterCredential)) {
		t.Fatalf("the error should name the missing credential reference: %v", err)
	}
}

// TestOllamaPathAgainstAFakeDaemon closes the coverage gap left when no
// local ollama is running — which is the normal case in CI. It drives
// the same path as the live test (declare, resolve, call, record) against
// a stand-in for the daemon: TestLiveOllamaThinking is for a developer
// with a real model, this is what can be checked on every commit.
func TestOllamaPathAgainstAFakeDaemon(t *testing.T) {
	var gotModel string
	var sawStreaming bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = req.Model
		sawStreaming = req.Stream

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"pong"},"done":true}`))
	}))
	defer srv.Close()

	n := New("fake-ollama", WithDefaultModel("ollama"), WithWorkers(1))
	if err := n.Models.Add(model.Ollama("test-model", model.WithEndpoint(srv.URL))); err != nil {
		t.Fatal(err)
	}

	reply := runLiveThought(t, n)
	if strings.TrimSpace(reply) != "pong" {
		t.Fatalf("reply = %q, want %q", reply, "pong")
	}
	if gotModel != "test-model" {
		t.Fatalf("descriptor model did not reach the provider: %q", gotModel)
	}
	if !sawStreaming {
		t.Fatal("the agent runner must request a streaming response")
	}
}

// runLiveThought runs the navi, enqueues one thought, and returns the
// assistant reply recorded in its transcript.
func runLiveThought(t *testing.T, n *Navi) string {
	t.Helper()

	cancel, done := startServing(t, n)
	defer stopServing(t, cancel, done)

	if err := n.Thoughts.Enqueue(&thought.Thought{
		ID: "live-1", Prompt: livePrompt, Priority: 5,
	}); err != nil {
		t.Fatal(err)
	}
	waitStatusWithin(t, n, "live-1", "complete", 2*time.Minute)

	sess, err := n.SessionStore().LoadSession(SessionIDFor("live-1"))
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	text := sessionText(sess)
	if len(text) < 2 {
		t.Fatalf("expected a prompt turn and a reply turn, got %v", text)
	}
	return text[len(text)-1]
}

// reachable reports whether something is listening at a provider base
// URL, so a live test can skip instead of failing when nothing is there.
func reachable(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Host
	if host == "" {
		host = raw
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "11434")
	}
	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
