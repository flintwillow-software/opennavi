// Package http implements the http_request tool: a
// stateless outbound HTTP client for the LLM, restricted to CRUD
// methods, with a DNS-pinned SSRF guard, per-navi allow/deny lists,
// server-side credential references (stub, fail-closed), and
// response truncation at a byte cap with a partial-output notice.
//
// The tool is deliberately stateless — it registers through
// `registry.Register` (no store handle): access-level enforcement is
// `AccessExecute`, which later IAM work maps onto permissions. It is
// the substrate future author-domain tools (github_*, ...) build on.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
	"github.com/flintwillow-software/opennavi/tool/registry"
)

// DefaultMaxResponseBytes caps the response body returned to the LLM
// 256 KiB. Bodies over the cap are truncated and the
// output is marked partial with a truncation notice.
const DefaultMaxResponseBytes = 256 * 1024

// DefaultTimeout is the per-call timeout in seconds (default 30s).
// Per-call overrides are clamped to this ceiling — the bridge's
// registry envelope enforces the definition's Timeout anyway, so a
// longer per-call value could only silently shrink.
const DefaultTimeout = 30

// MaxRedirects bounds how many redirect hops http_request follows per
// call ("max 3"). Every hop is re-validated against the
// SSRF policy before it is dialed — both in the redirect hook and,
// authoritatively, at dial time via the pinned dialer.
const MaxRedirects = 3

// AllowedMethods is the CRUD method set the tool accepts.
// The input schema enforces it; the handler re-checks as
// defense in depth.
var AllowedMethods = map[string]bool{
	"GET":    true,
	"POST":   true,
	"PUT":    true,
	"PATCH":  true,
	"DELETE": true,
}

// AuthResolver resolves a credential reference (authRef) to a raw
// credential value at call time. (credential references) is
// planned but not landed, so the package defines only this interface
// stub with NO bundled implementation; the wiring step injects a
// resolver on compatible deployments. Until then every authRef call
// fails closed: without a resolver the tool refuses rather than leak
// or guess.
//
// The resolved value is injected server-side as the request's
// Authorization header; credentials never appear in the tool output.
type AuthResolver interface {
	Resolve(authRef string) (value string, err error)
}

// Options tunes http_request at registration. All fields are the
// config hook the orchestrator converges with runtime configuration
// (internal/config) in the NAVI-272 wiring step; zero/empty keeps the
// safe default.
type Options struct {
	// MaxResponseBytes caps the response body returned to the LLM;
	// longer bodies are truncated with a notice. Zero =
	// DefaultMaxResponseBytes.
	MaxResponseBytes int
	// Allowlist names per-navi internal services exempt from the
	// SSRF rules. An entry is a hostname (compared against the URL
	// host), an IP literal (exact address), or a CIDR range.
	Allowlist []string
	// Denylist entries remove hosts/addresses from the allowlist.
	// Deny always wins over allow.
	Denylist []string
	// Auth resolves authRef credentials server-side; nil fails
	// closed on any authRef call.
	Auth AuthResolver
}

// SchemaJSON is the strict input schema.
var SchemaJSON = json.RawMessage(`{
	"type": "object",
	"required": ["method", "url"],
	"properties": {
		"method": { "type": "string", "enum": ["GET", "POST", "PUT", "PATCH", "DELETE"] },
		"url": { "type": "string" },
		"headers": { "type": "object", "additionalProperties": { "type": "string" } },
		"query": { "type": "object", "additionalProperties": { "type": "string" } },
		"body": { "type": "string" },
		"authRef": { "type": "string" },
		"timeout": { "type": "integer", "minimum": 1 },
		"maxResponseBytes": { "type": "integer", "minimum": 1 }
	},
	"additionalProperties": false
}`)

// mergeOptions folds variadic Options into one effective value: zero
// fields and empty lists keep their defaults.
func mergeOptions(opts ...Options) Options {
	o := Options{}
	for _, x := range opts {
		if x.MaxResponseBytes > 0 {
			o.MaxResponseBytes = x.MaxResponseBytes
		}
		if len(x.Allowlist) > 0 {
			o.Allowlist = x.Allowlist
		}
		if len(x.Denylist) > 0 {
			o.Denylist = x.Denylist
		}
		if x.Auth != nil {
			o.Auth = x.Auth
		}
	}
	if o.MaxResponseBytes == 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	return o
}

// Register adds http_request to reg (stateless: plain Register, no
// store handle). opts override the response cap and SSRF lists.
func Register(reg *registry.Registry, opts ...Options) error {
	if reg == nil {
		return errors.New("http: registry is required")
	}
	o := mergeOptions(opts...)
	def, err := definition()
	if err != nil {
		return err
	}
	return reg.Register(def, requestHandler(o))
}

// definition builds the complete http_request descriptor.
func definition() (registry.ToolDefinition, error) {
	schema, err := registry.ParseSchema(SchemaJSON)
	if err != nil {
		return registry.ToolDefinition{}, fmt.Errorf("http: schema: %w", err)
	}
	return registry.ToolDefinition{
		Name:          "http_request",
		Description:   "Performs an outbound HTTP request (GET, POST, PUT, PATCH, DELETE) and returns the response status, filtered headers, and body. Non-2xx responses are returned as normal output — recovery is the caller's call. Bodies are truncated at maxResponseBytes with a notice. SSRF guard: private/loopback/link-local/multicast targets are denied unless allowlisted; redirects are re-validated per hop (max 3).",
		InputSchema:   schema,
		OutputFormat:  "json",
		Access:        registry.AccessExecute,
		Timeout:       30 * time.Second,
		Recovery:      "capture_response",
		Mode:          registry.ToolModeParallel,
		Tags:          []string{"http", "network", "utility"},
		PromptSnippet: "http_request: perform an outbound HTTP request and read the response",
		PromptGuidelines: []string{
			"Use authRef for credentialed services — never put secrets directly in headers.",
			"Non-2xx statuses come back as normal output; decide recovery yourself.",
			"Private/loopback addresses are denied by default; only allowlisted internal services are reachable.",
			"Redirects are followed and re-validated per hop (max 3).",
		},
		SourceInfo: registry.SourceInfo{Origin: registry.OriginSDK, Path: "tool/http/http.go"},
	}, nil
}

// Tool returns http_request as a public component tool — the collection
// form of Register for the navi composition root.
func Tool(opts ...Options) (tool.Tool, error) {
	o := mergeOptions(opts...)
	def, err := definition()
	if err != nil {
		return tool.Tool{}, err
	}
	tdef, err := registry.ToDef(def)
	if err != nil {
		return tool.Tool{}, fmt.Errorf("http: %w", err)
	}
	return tool.Tool{Def: tdef, Handler: tool.Handler(requestHandler(o))}, nil
}

// ── SSRF guard ─────────────────────────────────────────────────────────

// hostListed reports whether entry is a hostname-style allow/deny
// entry matching host (case-insensitive).
func hostListed(entries []string, host string) bool {
	for _, entry := range entries {
		if !isIPEntry(entry) && strings.EqualFold(entry, host) {
			return true
		}
	}
	return false
}

// isIPEntry reports whether entry is address-style (an IP literal or a
// CIDR range) rather than a bare hostname.
func isIPEntry(entry string) bool {
	if strings.Contains(entry, "/") {
		return true
	}
	return net.ParseIP(entry) != nil
}

// entryContains reports whether the address-style entry (IP literal or
// CIDR) contains ip.
func entryContains(entry string, ip net.IP) bool {
	if _, ipnet, err := net.ParseCIDR(entry); err == nil {
		return ip.Mask(ipnet.Mask).Equal(ipnet.IP)
	}
	lit := net.ParseIP(entry)
	return lit != nil && lit.Equal(ip)
}

// rangeListed reports whether ANY entry in entries (address-style)
// contains ip; hostname entries never match an address.
func rangeListed(entries []string, ip net.IP) bool {
	for _, entry := range entries {
		if isIPEntry(entry) && entryContains(entry, ip) {
			return true
		}
	}
	return false
}

// inDeniedRange reports whether ip falls in the SSRF-denied ranges:
// private (RFC 1918), loopback, link-local, multicast, and
// unspecified — the caller applies the allowlist exception.
func inDeniedRange(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified()
}

// validateTarget applies the SSRF policy to one outbound
// target. Deny hits: denylisted host or address (deny wins), or an
// address in a denied range not carved out by the allowlist. The host
// is resolved through DNS and EVERY resolved address is checked — one
// denied address denies the request (conservative; a strict guard
// never dials a partially-private target).
func validateTarget(target *url.URL, o Options) error {
	if target == nil {
		return errors.New("http_request: invalid target")
	}
	scheme := target.Scheme
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("http_request: unsupported scheme %q (want http or https)", scheme)
	}
	host := target.Hostname()

	if hostListed(o.Denylist, host) {
		return fmt.Errorf("http_request: SSRF guard: host %q is denylisted", host)
	}
	if hostListed(o.Allowlist, host) {
		return nil // named internal service — dialed as configured
	}

	addrs := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		addrs = append(addrs, ip)
	} else {
		var err error
		addrs, err = net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("http_request: resolve %q: %w", host, err)
		}
	}
	if len(addrs) == 0 {
		return fmt.Errorf("http_request: SSRF guard: %q resolved to no addresses", host)
	}
	for _, ip := range addrs {
		if rangeListed(o.Denylist, ip) {
			return fmt.Errorf("http_request: SSRF guard: target %s is denylisted", ip.String())
		}
		if !rangeListed(o.Allowlist, ip) && inDeniedRange(ip) {
			return fmt.Errorf("http_request: SSRF guard: target %q resolves to a denied address (%s); set an allowlist entry to permit internal services", host, ip.String())
		}
	}
	return nil
}

// redirectHook builds the client's CheckRedirect policy: every hop is
// re-validated against the SSRF policy (fail fast, with a clear
// error), and the hop count is bounded at MaxRedirects. The dial-time
// check in the pinned Transport is the authoritative backstop for both
// the initial request and every redirect hop.
func redirectHook(o Options) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects+1 {
			return fmt.Errorf("http_request: too many redirects (max %d)", MaxRedirects)
		}
		return validateTarget(req.URL, o)
	}
}

// ── pinned dialer (SSRF backstop) ───────────────────────────────────────

// pinDialer is the standard net dialer used to place the connection
// AFTER the dial-time policy check. Dialing a literal IP means the
// connection target is the validated address — DNS cannot be queried
// a second time between check and connect.
var pinDialer = &net.Dialer{}

// dialNet dials addr through the standard net dialer.
func dialNet(ctx context.Context, network, addr string) (net.Conn, error) {
	return pinDialer.DialContext(ctx, network, addr)
}

// dialHook builds the Transport.DialContext for http_request: the
// SSRF backstop that closes the validate-then-dial TOCTOU window.
// Resolution happens INSIDE the dial path and the connection is pinned
// to a validated address, so a rebinding nameserver cannot return a
// public address to validation and a private one to the dial.
func dialHook(o Options) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialHookWithLookup(o, net.LookupIP, dialNet)
}

// dialHookWithLookup is dialHook with the resolution and final-dial
// steps injectable (the test seam: a rebinding-style resolver and a
// recording dialer prove the address pinning without touching the
// network).
func dialHookWithLookup(o Options, lookup func(host string) ([]net.IP, error), dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("http_request: SSRF guard: dial %q: %w", addr, err)
		}
		if hostListed(o.Denylist, host) {
			return nil, fmt.Errorf("http_request: SSRF guard: dial %q denied (host denylisted)", addr)
		}
		if hostListed(o.Allowlist, host) {
			// Named internal service: dial the hostname as configured
			// (an allowlisted internal host legitimately uses a
			// private address).
			return dial(ctx, network, addr)
		}

		ips := []net.IP{}
		if ip := net.ParseIP(host); ip != nil {
			ips = append(ips, ip)
		} else {
			var lerr error
			ips, lerr = lookup(host)
			if lerr != nil {
				return nil, fmt.Errorf("http_request: SSRF guard: dial %q: resolve %q: %w", addr, host, lerr)
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("http_request: SSRF guard: dial %q: %q resolved to no addresses", addr, host)
		}
		for _, ip := range ips {
			if rangeListed(o.Denylist, ip) {
				return nil, fmt.Errorf("http_request: SSRF guard: dial %q denied (address %s denylisted)", addr, ip.String())
			}
			if !rangeListed(o.Allowlist, ip) && inDeniedRange(ip) {
				return nil, fmt.Errorf("http_request: SSRF guard: dial %q denied (address %s is not permitted)", addr, ip.String())
			}
		}
		// Pin the connection to the validated address: the dial
		// target is the resolved IP, never the raw hostname, so a
		// DNS answer cannot change between check and connect.
		return dial(ctx, network, ips[0].String()+":"+port)
	}
}

// ── handler ─────────────────────────────────────────────────────────────

// requestHandler returns the http_request handler bound to Options.
func requestHandler(o Options) registry.Handler {
	return func(ctx context.Context, args map[string]any) (string, error) {
		method, ok := args["method"].(string)
		if !ok || !AllowedMethods[method] {
			return "", fmt.Errorf("http_request: method %q is not allowed", method)
		}
		urlStr, ok := args["url"].(string)
		if !ok || urlStr == "" {
			return "", errors.New("http_request: url is required")
		}

		timeout := DefaultTimeout
		if v, ok := args["timeout"].(float64); ok && int(v) > 0 {
			timeout = int(v)
			if timeout > DefaultTimeout {
				timeout = DefaultTimeout // registry envelope caps at 30s regardless
			}
		}
		maxBytes := o.MaxResponseBytes
		if v, ok := args["maxResponseBytes"].(float64); ok && int(v) > 0 {
			maxBytes = int(v)
		}

		u, err := url.Parse(urlStr)
		if err != nil {
			return "", fmt.Errorf("http_request: invalid url: %w", err)
		}
		if q, ok := args["query"].(map[string]any); ok && len(q) > 0 {
			parts := []string{}
			if u.RawQuery != "" {
				parts = append(parts, u.RawQuery)
			}
			for k, v := range q {
				if s, ok := v.(string); ok {
					parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(s))
				}
			}
			u.RawQuery = strings.Join(parts, "&")
			urlStr = u.String()
		}
		if err := validateTarget(u, o); err != nil {
			return "", err
		}

		bodyText := ""
		if v, ok := args["body"].(string); ok {
			bodyText = v
		}
		req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader([]byte(bodyText)))
		if err != nil {
			return "", fmt.Errorf("http_request: build request: %w", err)
		}

		if h, ok := args["headers"].(map[string]any); ok {
			for k, v := range h {
				if s, ok := v.(string); ok {
					req.Header.Set(k, s)
				}
			}
		}

		authRef := ""
		if v, ok := args["authRef"].(string); ok {
			authRef = v
		}
		if authRef != "" {
			if o.Auth == nil {
				return "", fmt.Errorf("http_request: authRef %q requires a credential resolver; none is configured (fail closed)", authRef)
			}
			cred, rerr := o.Auth.Resolve(authRef)
			if rerr != nil {
				return "", fmt.Errorf("http_request: resolve authRef %q: %w", authRef, rerr)
			}
			// Server-side injection: the resolved secret becomes the
			// Authorization header and is never echoed back.
			req.Header.Set("Authorization", cred)
		}

		// Direct dials only (no environment proxies): the pinned
		// Transport is the SSRF backstop, and every dial goes through
		// it — initial request and redirect hops alike.
		client := &http.Client{
			Transport:     &http.Transport{DialContext: dialHook(o)},
			CheckRedirect: redirectHook(o),
			Timeout:       time.Duration(float64(timeout) * float64(time.Second)),
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("http_request: %w", err)
		}
		defer resp.Body.Close()

		data, rerr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
		if rerr != nil {
			return "", fmt.Errorf("http_request: read response: %w", rerr)
		}
		truncated := len(data) > maxBytes
		if truncated {
			data = data[:maxBytes]
		}

		// Filtered headers: credential and cookie echoes never reach
		// the LLM.
		headers := map[string]string{}
		for k, vals := range resp.Header {
			lk := strings.ToLower(k)
			if lk == "authorization" || lk == "set-cookie" {
				continue
			}
			headers[k] = strings.Join(vals, ", ")
		}

		blob, merr := json.Marshal(map[string]any{
			"status":    resp.StatusCode,
			"headers":   headers,
			"body":      string(data),
			"truncated": truncated,
		})
		if merr != nil {
			return "", fmt.Errorf("http_request: encode output: %w", merr)
		}
		return string(blob), nil
	}
}
