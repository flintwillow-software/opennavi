package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flintwillow-software/opennavi/tool/registry"
)

// registerHttp builds a registry with http_request registered (opts
// applied) and active.
func registerHttp(t *testing.T, opts ...Options) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := Register(reg, opts...); err != nil {
		t.Fatalf("http Register: %v", err)
	}
	if err := reg.SetActive([]string{"http_request"}); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	return reg
}

// allowLo registers against the loopback range so tests can talk to
// httptest servers (which bind 127.0.0.1) — the SSRF guard denies
// loopback by default.
func allowLo(t *testing.T, opts ...Options) *registry.Registry {
	merged := mergeOptions(opts...)
	merged.Allowlist = append([]string{"127.0.0.1"}, merged.Allowlist...)
	return registerHttp(t, merged)
}

// dispatch runs one http_request call and decodes the JSON output.
func dispatch(t *testing.T, reg *registry.Registry, args string) (map[string]any, error) {
	t.Helper()
	out, err := reg.Dispatch(context.Background(), "http_request", json.RawMessage(args))
	if err != nil {
		return map[string]any{}, err
	}
	var doc map[string]any
	if err := json.Unmarshal(json.RawMessage(out), &doc); err != nil {
		t.Fatalf("http_request output not a JSON object: %v (was %q)", err, out)
	}
	return doc, nil
}

// statusOf extracts the numeric status from a decoded output map
// (JSON numbers decode as float64 in this dialect).
func statusOf(doc map[string]any) int {
	s, _ := doc["status"].(float64)
	return int(s)
}

// ---------------------------------------------------------------------------
// Registration metadata.

func TestRegisterMetadata(t *testing.T) {
	reg := registerHttp(t)

	def, ok := reg.Get("http_request")
	if !ok {
		t.Fatal("http_request not registered")
	}
	if def.Name != "http_request" || def.OutputFormat != "json" {
		t.Errorf("metadata mismatch: %+v", def)
	}
	if def.Access != registry.AccessExecute {
		t.Errorf("access = %s, want execute", def.Access.String())
	}
	if def.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s", def.Timeout)
	}
	if def.Mode != registry.ToolModeParallel {
		t.Errorf("mode = %s, want parallel", def.Mode.String())
	}
	if def.Recovery != "capture_response" {
		t.Errorf("recovery = %q", def.Recovery)
	}
	if def.SourceInfo.Origin != registry.OriginSDK {
		t.Errorf("origin = %s, want sdk", def.SourceInfo.Origin.String())
	}
}

// ---------------------------------------------------------------------------
// Core request/response semantics.

func TestSuccessReturnsStatusHeadersBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Custom", "yes")
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if statusOf(doc) != 200 {
		t.Errorf("status = %v, want 200", doc["status"])
	}
	if doc["body"] != "hello world" {
		t.Errorf("body = %q", doc["body"])
	}
	if doc["truncated"] != false {
		t.Errorf("unexpected truncation: %+v", doc)
	}
	headers, ok := doc["headers"].(map[string]any)
	if !ok {
		t.Errorf("headers missing: %+v", doc["headers"])
	} else {
		lower := map[string]string{}
		for k, v := range headers {
			s, _ := v.(string)
			lower[strings.ToLower(k)] = s
		}
		if lower["content-type"] != "text/plain" || lower["x-custom"] != "yes" {
			t.Errorf("response headers wrong: %+v", headers)
		}
	}
}

func TestNon2xxIsNormalOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("404 should return as normal output, got error: %v", err)
	}
	if statusOf(doc) != 404 {
		t.Errorf("status = %v, want 404", doc["status"])
	}
	body, _ := doc["body"].(string)
	if !strings.Contains(body, "nope") {
		t.Errorf("404 body missing: %q", body)
	}
}

func TestMethodRestriction(t *testing.T) {
	reg := allowLo(t)
	for _, method := range []string{"TRACE", "OPTIONS", "HEAD", "get"} {
		if _, err := dispatch(t, reg, fmt.Sprintf(`{"method":%q,"url":"http://127.0.0.1/"}`, method)); err == nil {
			t.Errorf("method %q accepted", method)
		}
	}
}

func TestQueryMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("a") != "1" || q.Get("b") != "x y" {
			t.Errorf("query not merged: %q", r.URL.RawQuery)
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q,"query":{"a":"1","b":"x y"}}`, srv.URL))
	if err != nil {
		t.Fatalf("GET with query: %v", err)
	}
	if statusOf(doc) != 200 {
		t.Errorf("status = %v", doc["status"])
	}
}

func TestHeadersForwardedAndFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "tok" {
			t.Errorf("request header not forwarded: %q", r.Header.Get("X-Token"))
		}
		w.Header().Set("Set-Cookie", "sid=1")
		w.Header().Set("Authorization", "Bearer LEAK")
		w.Header().Set("X-Real", "yes")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q,"headers":{"X-Token":"tok"}}`, srv.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	headers, ok := doc["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing: %+v", doc)
	}
	lower := map[string]string{}
	for k, v := range headers {
		s, _ := v.(string)
		lower[strings.ToLower(k)] = s
	}
	if _, ok := lower["authorization"]; ok {
		t.Error("Authorization echoed in output")
	}
	if _, ok := lower["set-cookie"]; ok {
		t.Error("Set-Cookie echoed in output")
	}
	if lower["x-real"] != "yes" {
		t.Errorf("X-Real lost: %+v", headers)
	}
}

func TestBodySent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "payload-123" {
			t.Errorf("body = %q (err %v)", string(body), err)
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"POST","url":%q,"body":"payload-123"}`, srv.URL))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if statusOf(doc) != 200 {
		t.Errorf("status = %v", doc["status"])
	}
}

// repeat returns a string of n copies of s (tests only).
func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// ---------------------------------------------------------------------------
// Truncation (response truncation).

func TestTruncationWithNotice(t *testing.T) {
	payload := repeat("z", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	reg := allowLo(t, Options{MaxResponseBytes: 64})
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, ok := doc["body"].(string)
	if !ok || len(body) != 64 {
		t.Errorf("body not truncated at cap: len=%d (ok=%v)", len(body), ok)
	}
	if doc["truncated"] != true {
		t.Errorf("truncation notice missing: %+v", doc)
	}

	// Per-call maxResponseBytes overrides the registration cap.
	doc, err = dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q,"maxResponseBytes":16}`, srv.URL))
	if err != nil {
		t.Fatalf("GET with per-call cap: %v", err)
	}
	if b, _ := doc["body"].(string); len(b) != 16 {
		t.Errorf("per-call cap ignored: len=%d", len(b))
	}
	if doc["truncated"] != true {
		t.Errorf("per-call truncation notice missing")
	}
}

// ---------------------------------------------------------------------------
// SSRF guard.

func TestSSRFDeniesPrivateRanges(t *testing.T) {
	// No allowlist: loopback registry — every denied range must be
	// rejected before any dial.
	reg := registerHttp(t)
	for _, target := range []string{
		"http://127.0.0.1/",
		"http://10.0.0.7/",
		"http://192.168.2.3/",
		"http://172.16.5.5/",
		"http://169.254.169.254/",
		"http://224.0.0.250/",
		"http://0.0.0.0/",
		"http://[::1]/",
	} {
		if _, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, target)); err == nil {
			t.Errorf("SSRF guard allowed %q", target)
		} else if !strings.Contains(err.Error(), "SSRF") {
			t.Errorf("denial for %q not an SSRF error: %v", target, err)
		}
	}
}

func TestSSRFAllowlistPermitsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	reg := allowLo(t) // allowlist 127.0.0.1
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL))
	if err != nil {
		t.Fatalf("allowlisted loopback should dial: %v", err)
	}
	if statusOf(doc) != 200 {
		t.Errorf("status = %v", doc["status"])
	}
}

func TestSSRFDenyOverridesAllow(t *testing.T) {
	reg := registerHttp(t, Options{Allowlist: []string{"127.0.0.1"}, Denylist: []string{"127.0.0.1"}})
	if _, err := dispatch(t, reg, `{"method":"GET","url":"http://127.0.0.1/"}`); err == nil {
		t.Error("denylist did not win over allowlist")
	} else if !strings.Contains(err.Error(), "denylist") && !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("deny error unclear: %v", err)
	}
}

func TestSSRFHostnameAllowlistEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// localhost resolves to a loopback address; the hostname allowlist
	// entry permits the named internal service without exposing the
	// whole loopback range.
	reg := registerHttp(t, Options{Allowlist: []string{"localhost"}})
	host := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, host))
	if err != nil {
		t.Fatalf("hostname allowlist should dial: %v", err)
	}
	if statusOf(doc) != 200 {
		t.Errorf("status = %v", doc["status"])
	}

	// Same host WITHOUT the allowlist entry is denied.
	deny := registerHttp(t)
	if _, err := dispatch(t, deny, fmt.Sprintf(`{"method":"GET","url":%q}`, host)); err == nil {
		t.Error("localhost without allowlist accepted")
	}
}

func TestRedirectRevalidatedPerHop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte("fine"))
		case "/private":
			w.Header().Set("Location", "http://10.1.2.3/")
			w.WriteHeader(http.StatusFound)
			w.Write([]byte(""))
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	reg := allowLo(t)

	// Direct redirect target re-validation: the intermediate hop is
	// allowlisted, the destination is not — denied before dialing.
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL+"/private"))
	if err == nil {
		t.Fatal("redirect to private range accepted")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("redirect denial not an SSRF error: %v", err)
	}

	// Control: a non-redirecting path still works.
	doc, err = dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL+"/ok"))
	if err != nil || statusOf(doc) != 200 {
		t.Errorf("control request failed: %v %+v", err, doc)
	}
}

func TestRedirectHopLimit(t *testing.T) {
	// /a -> /b -> /c -> /d is 3 redirects: allowed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Header().Set("Location", "/b")
			w.WriteHeader(http.StatusFound)
		case "/b":
			w.Header().Set("Location", "/c")
			w.WriteHeader(http.StatusFound)
		case "/c":
			w.Header().Set("Location", "/d")
			w.WriteHeader(http.StatusFound)
		case "/d":
			w.Write([]byte("end"))
		default:
			w.Header().Set("Location", "/nope")
			w.WriteHeader(http.StatusFound)
		}
		w.Write([]byte(""))
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, srv.URL+"/a"))
	if err != nil {
		t.Fatalf("3-hop chain should succeed: %v", err)
	}
	if statusOf(doc) != 200 || doc["body"] != "end" {
		t.Errorf("chain result wrong: %+v", doc)
	}

	// An un-ending chain (every path redirects somewhere new) exceeds
	// the hop budget and must be denied.
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := strings.TrimPrefix(r.URL.Path, "/")
		w.Header().Set("Location", "/"+n+"x")
		w.WriteHeader(http.StatusFound)
		w.Write([]byte(""))
	}))
	defer loop.Close()

	_, err = dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q}`, loop.URL+"/p"))
	if err == nil {
		t.Fatal("redirect loop accepted")
	}
	if !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("hop limit error unclear: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Credential references (stub, fails closed).

func TestAuthRefFailsClosedWithoutResolver(t *testing.T) {
	// An allowlisted (guard-passing) target so the request reaches the
	// resolver step; with no resolver configured every authRef call
	// must fail closed BEFORE any dial.
	reg := registerHttp(t, Options{Allowlist: []string{"10.2.3.4"}})
	_, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q,"authRef":"api-key-1"}`, "http://10.2.3.4/"))
	if err == nil {
		t.Error("authRef without a resolver accepted")
	}
	if !strings.Contains(err.Error(), "resolver") {
		t.Errorf("fail-closed message unclear: %v", err)
	}
}

func TestAuthRefInjectsCredentialServerSide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sekret" {
			t.Errorf("credential not injected: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Authorization", "Bearer leak-me")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resolver := testResolver{value: "Bearer sekret"}
	reg := allowLo(t, Options{Auth: resolver})
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q,"authRef":"navi-github"}`, srv.URL))
	if err != nil {
		t.Fatalf("GET with authRef: %v", err)
	}
	headers, ok := doc["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing: %+v", doc)
	}
	for k := range headers {
		if strings.EqualFold(k, "authorization") {
			t.Error("Authorization header echoed despite authRef")
		}
	}
}

type testResolver struct {
	value string
}

func (r testResolver) Resolve(authRef string) (string, error) {
	return r.value, nil
}

// dialRecorder captures the address handed to the final dial, the way
// a recording net dialer would — it never opens a socket.
type dialRecorder struct {
	last string
}

func (r *dialRecorder) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	r.last = addr
	return nil, errors.New("stub dial not executed")
}

// ---------------------------------------------------------------------------
// Dial-time SSRF backstop (DNS rebinding / TOCTOU): resolution happens
// inside the dial path and the connection is pinned to the validated
// address, so a nameserver answer cannot change between validation and
// connect.

func TestPinnedDialDeniesRebindingToPrivateRange(t *testing.T) {
	// Rebind scenario: the SSRF guard's pre-dial validation sees a
	// PUBLIC answer; by dial time the same hostname resolves PRIVATE.
	// The dial-time check must deny the private answer before any
	// connection is placed.
	var rebinding func(host string) ([]net.IP, error) = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.9.8.7")}, nil
	}
	rec := &dialRecorder{}
	hook := dialHookWithLookup(Options{MaxResponseBytes: 64}, rebinding, rec.dial)

	_, err := hook(context.Background(), "tcp", "attacker.example.com:80")
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Errorf("rebinding dial not denied: %v", err)
	}
	if rec.last != "" {
		t.Errorf("dial placed despite private resolution: %q", rec.last)
	}
}

func TestPinnedDialConnectsToValidatedAddress(t *testing.T) {
	// Public answer: the hook must dial the VALIDATED IP literal —
	// never the raw hostname — so the connection is pinned to what was
	// checked.
	var pub func(host string) ([]net.IP, error) = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	rec := &dialRecorder{}
	hook := dialHookWithLookup(Options{}, pub, rec.dial)

	_, _ = hook(context.Background(), "tcp", "public.example.com:443")
	if rec.last != "8.8.8.8:443" {
		t.Errorf("dial target %q, want pinned 8.8.8.8:443", rec.last)
	}
}

func TestPinnedDialHostnameAllowlistDialedAsConfigured(t *testing.T) {
	// A hostname allowlist entry names an internal service: the dial
	// proceeds against the hostname itself (which may legitimately
	// resolve to a private address) — scope-limited to that entry.
	rec := &dialRecorder{}
	hook := dialHookWithLookup(Options{Allowlist: []string{"inner-svc"}}, net.LookupIP, rec.dial)

	_, _ = hook(context.Background(), "tcp", "inner-svc:8080")
	if rec.last != "inner-svc:8080" {
		t.Errorf("dial target %q, want allowlisted hostname as configured", rec.last)
	}
}

func TestPerCallTimeoutClampedSmoke(t *testing.T) {
	// Per-call timeout above the 30s definition cap is clamped, not
	// honored verbatim (the bridge envelope would cap it anyway).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	reg := allowLo(t)
	doc, err := dispatch(t, reg, fmt.Sprintf(`{"method":"GET","url":%q,"timeout":60}`, srv.URL))
	if err != nil {
		t.Fatalf("GET with oversized per-call timeout: %v", err)
	}
	if statusOf(doc) != 200 {
		t.Errorf("status = %v", doc["status"])
	}
}
