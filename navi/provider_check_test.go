package navi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/internal/llm"
	"github.com/flintwillow-software/opennavi/model"
)

// fakeOllamaTags serves Ollama's /api/tags — what Ping queries — and returns
// its base URL.
func fakeOllamaTags(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3.8:27b"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestCheckProvidersReportsUsableOllama pins the point of NAVI-606's provider
// check: a declared, reachable provider is reported usable, and the report
// names the credential REFERENCE rather than a value (ADR 0008).
func TestCheckProvidersReportsUsableOllama(t *testing.T) {
	clearProviderEnv(t)
	n := New("providers", WithWorkers(1))
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		t.Fatal(err)
	}

	// A reachable ollama: point OLLAMA_HOST at a fake /api/tags responder.
	srv := fakeOllamaTags(t)
	t.Setenv("OLLAMA_HOST", srv)

	got, err := n.CheckProviders(context.Background())
	if err != nil {
		t.Fatalf("CheckProviders: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("statuses = %d, want 1", len(got))
	}
	if !got[0].Usable {
		t.Fatalf("ollama not usable: %+v", got[0])
	}
	if !got[0].Default || !got[0].Local {
		t.Fatalf("ollama flags wrong: %+v", got[0])
	}
}

// TestCheckProvidersReportsMissingCredential pins the fail-closed path: a
// provider whose credential reference is unset is reported unusable, with the
// reference named and the value never present (ADR 0008).
func TestCheckProvidersReportsMissingCredential(t *testing.T) {
	clearProviderEnv(t)
	n := New("providers", WithWorkers(1))
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		t.Fatal(err)
	}
	if err := n.Models.Add(model.OpenRouter("anthropic/claude-sonnet-4")); err != nil {
		t.Fatal(err)
	}

	got, err := n.CheckProviders(context.Background())
	if err != nil {
		t.Fatalf("CheckProviders: %v", err)
	}
	var or *ProviderStatus
	for i := range got {
		if got[i].Name == "openrouter" {
			or = &got[i]
		}
	}
	if or == nil {
		t.Fatal("openrouter not reported")
	}
	if or.Usable {
		t.Fatalf("openrouter reported usable without a credential: %+v", *or)
	}
	if !strings.Contains(or.Reason, string(model.DefaultOpenRouterCredential)) {
		t.Fatalf("reason does not name the reference: %q", or.Reason)
	}
	if or.Credential != string(model.DefaultOpenRouterCredential) {
		t.Fatalf("credential ref = %q", or.Credential)
	}
}

// TestCheckProvidersWithoutDeclarations keeps the report honest: a navi with
// nothing to check says so rather than returning an empty success.
func TestCheckProvidersWithoutDeclarations(t *testing.T) {
	clearProviderEnv(t)
	n := New("providers", WithWorkers(1))
	if _, err := n.CheckProviders(context.Background()); err == nil {
		t.Fatal("expected an error with no providers declared")
	}
}

// TestCheckProvidersUsesOverrideSeam covers the test seam: an overridden
// provider is checked instead of the declared descriptors.
func TestCheckProvidersUsesOverrideSeam(t *testing.T) {
	clearProviderEnv(t)
	mock := llm.NewMockProvider().WithPingError(errors.New("nope"))
	n := New("providers", WithProvider(mock), WithWorkers(1))
	if err := n.Models.Add(model.Ollama("qwen3.8:27b", model.Default())); err != nil {
		t.Fatal(err)
	}
	got, err := n.CheckProviders(context.Background())
	if err != nil {
		t.Fatalf("CheckProviders: %v", err)
	}
	if len(got) != 1 || got[0].Usable {
		t.Fatalf("override ping error not reported: %+v", got)
	}
	if !strings.Contains(got[0].Reason, "nope") {
		t.Fatalf("reason = %q", got[0].Reason)
	}
}
