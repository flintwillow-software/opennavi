package navi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/connector/github"
)

// This file proves the navi-side half of ADR 0029: the navi owns ONE webhook
// listener and a router, hands that router to connectors through Deps, and the
// listener serves the connector's route — nothing else.

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestWebhookListenerServesConnectorRoute is the end-to-end property: a navi
// that declares a webhook listener and a GitHub connector with a webhook
// binding serves a signed delivery on the declared route, over a real socket.
func TestWebhookListenerServesConnectorRoute(t *testing.T) {
	n := New("test", WithWebhooks("127.0.0.1:0"))
	gh := github.New(github.Config{
		Subscriptions: []github.Subscription{{
			Name: "sub", Repository: "acme/widgets", Events: []github.EventKind{github.EventIssueOpened},
		}},
		Webhooks: []github.WebhookBinding{{
			Name: "sub", Route: "/github", SecretRefs: []string{"GH_SECRET"},
			Env: func(string) string { return "s3cret" },
		}},
	})
	// The listener is what the navi composes; start it with the router the
	// connector will register on, which is exactly what serve does.
	ln := &webhookListener{addr: "127.0.0.1:0", router: n.webhooks}
	deps := connector.Deps{ThoughtSystem: n.Thoughts, Log: discardLog(), Webhooks: n.webhooks}
	if err := ln.Start(context.Background(), deps); err != nil {
		t.Fatalf("listener Start: %v", err)
	}
	defer ln.Stop()
	if err := gh.Start(context.Background(), deps); err != nil {
		t.Fatalf("connector Start: %v", err)
	}
	defer gh.Stop()

	body, _ := json.Marshal(map[string]any{
		"action": "opened",
		"issue": map[string]any{
			"number": 5, "title": "boom", "body": "help",
			"html_url": "https://github.com/acme/widgets/issues/5",
			"user":     map[string]any{"login": "alice"},
		},
		"repository": map[string]any{"full_name": "acme/widgets"},
	})
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	url := fmt.Sprintf("http://%s/github", ln.BoundAddr())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Delivery", "d1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("delivery over the socket: got %d, want 202", resp.StatusCode)
	}
	// The thought reached the navi's queue with the deterministic id.
	if got, err := n.Thoughts.GetThought("github-sub-issue-5"); err != nil {
		t.Fatalf("thought not enqueued: %v", err)
	} else if got.Status != "pending" {
		t.Errorf("thought status = %q, want pending", got.Status)
	}
}

// TestWebhookListenerNotFoundForUnknownRoute: the listener serves the router
// and nothing else — an unregistered path is 404, not a portal or object route.
func TestWebhookListenerNotFoundForUnknownRoute(t *testing.T) {
	ln := &webhookListener{addr: "127.0.0.1:0", router: connector.NewRouter()}
	if err := ln.Start(context.Background(), connector.Deps{Log: discardLog()}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ln.Stop()

	for _, path := range []string{"/", "/portal/stream", "/_types", "/thought"} {
		resp, err := http.Post(fmt.Sprintf("http://%s%s", ln.BoundAddr(), path), "application/json", nil)
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404 (the webhook listener serves only the connector router)", path, resp.StatusCode)
		}
	}
}

// TestWebhookListenerBindFailure: a bind failure is a Start error, so it is
// reported through the same health path as every other door.
func TestWebhookListenerBindFailure(t *testing.T) {
	first := &webhookListener{addr: "127.0.0.1:0", router: connector.NewRouter()}
	if err := first.Start(context.Background(), connector.Deps{Log: discardLog()}); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer first.Stop()

	second := &webhookListener{addr: first.BoundAddr(), router: connector.NewRouter()}
	if err := second.Start(context.Background(), connector.Deps{Log: discardLog()}); err == nil {
		second.Stop()
		t.Error("second listener on a taken port: got nil, want an error")
	}
}

// TestWithWebhooksBuildsARouter: declaring a listener is what gives connectors
// a router to register on, and a navi with no listener builds none.
func TestWithWebhooksBuildsARouter(t *testing.T) {
	n := New("test", WithWebhooks("127.0.0.1:0"))
	if n.webhooks == nil {
		t.Fatal("WithWebhooks did not build a router")
	}
	if routes := n.WebhookRoutes(); len(routes) != 0 {
		t.Errorf("fresh navi routes = %v, want none", routes)
	}
	plain := New("plain")
	if plain.webhookListenerFor() != nil {
		t.Error("a navi with no webhook address built a listener")
	}
}

// TestWebhookRoutesDiagnostic: the routes a navi serves are answerable from the
// composition (ADR 0029, consequences).
func TestWebhookRoutesDiagnostic(t *testing.T) {
	router := connector.NewRouter()
	n := New("test", WithWebhookRouter(router))
	if err := router.Handle("/github", http.NotFoundHandler()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if routes := n.WebhookRoutes(); len(routes) != 1 || routes[0] != "/github" {
		t.Errorf("WebhookRoutes = %v, want [/github]", routes)
	}
}
