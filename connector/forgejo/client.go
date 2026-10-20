package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// This file is the Forgejo REST transport: the shared client every egress tool
// calls through. It is native Go over the Gitea-compatible API (ADR 0014: the
// integration is service-specific, not a generic payload), so a tool handler
// names an endpoint and a shape rather than handing the model a URL.
//
// The credential is resolved at the moment of each request and never stored on
// the client (ADR 0014 decision 6): a dump of the connector or its client
// carries the REFERENCE, never the value.

// maxBody bounds how much of a response is read, so a hostile or broken server
// cannot exhaust memory.
const maxBody = 1 << 20

// client is the live service client an egress handler is handed. It holds no
// secret: the token comes from the TokenSource at the moment of the request.
type client struct {
	// apiBase is the instance root plus /api/v1.
	apiBase string
	// repository is the configured default "owner/name".
	repository string
	tokens     TokenSource
	http       *http.Client
}

// repositoryFor resolves the target repository: the call's override, else the
// configured default. It is required, so a tool cannot silently act on the
// wrong repository.
func (c *client) repositoryFor(args map[string]any) (string, error) {
	repo := strings.TrimSpace(c.repository)
	if v, ok := args["repository"].(string); ok && strings.TrimSpace(v) != "" {
		repo = strings.TrimSpace(v)
	}
	if repo == "" {
		return "", errors.New("forgejo: no repository; set Config.Repository or pass repository")
	}
	if !strings.Contains(repo, "/") {
		return "", fmt.Errorf("forgejo: repository %q must be owner/name", repo)
	}
	return repo, nil
}

// do performs one API request and returns the response body plus the total
// count the server reports (the X-Total-Count header, 0 when absent). The token
// is sent as a header and never appears in the URL or an error.
func (c *client) do(ctx context.Context, method, path string, in any) ([]byte, int, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, 0, err
	}
	var body io.Reader
	if in != nil {
		blob, err := json.Marshal(in)
		if err != nil {
			return nil, 0, fmt.Errorf("forgejo: encode request: %w", err)
		}
		body = bytes.NewReader(blob)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, body)
	if err != nil {
		return nil, 0, fmt.Errorf("forgejo: build request: %w", err)
	}
	// Forgejo/Gitea document the `token` scheme for access tokens; `Bearer` is
	// also accepted, but the documented one is used so a proxy that inspects the
	// scheme sees what the API expects.
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opennavi")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Deliberately not wrapping the request: a transport error must not be
		// able to echo the Authorization header into the transcript.
		return nil, 0, fmt.Errorf("forgejo: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, 0, fmt.Errorf("forgejo: %s %s: http %d: %s",
			method, path, resp.StatusCode, clip(respBody))
	}
	total := 0
	if v := resp.Header.Get("X-Total-Count"); v != "" {
		total, _ = strconv.Atoi(v)
	}
	return respBody, total, nil
}

// getJSON performs a GET and decodes the body into out, returning the reported
// total count.
func (c *client) getJSON(ctx context.Context, path string, out any) (int, error) {
	body, total, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, err
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return 0, fmt.Errorf("forgejo: decode response: %w", err)
		}
	}
	return total, nil
}

// clip bounds an error detail so a server's response cannot flood a transcript.
func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxErrorBody {
		return s[:maxErrorBody] + "…"
	}
	return s
}

// ── argument readers ───────────────────────────────────────────────────

// stringArg reads a string argument ("" when absent or not a string).
func stringArg(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return s
}

// intArg reads a positive integer argument. Validated JSON arrives as a
// float64, but an in-process caller may hand a Go int.
func intArg(args map[string]any, name string) (int, error) {
	switch v := args[name].(type) {
	case float64:
		if v < 1 || v != float64(int(v)) {
			return 0, fmt.Errorf("forgejo: %s must be a positive integer", name)
		}
		return int(v), nil
	case int:
		if v < 1 {
			return 0, fmt.Errorf("forgejo: %s must be a positive integer", name)
		}
		return v, nil
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 1 {
			return 0, fmt.Errorf("forgejo: %s must be a positive integer", name)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("forgejo: %s is required", name)
	}
}

// optionalInt reads an optional integer argument, returning def when absent and
// refusing a malformed value rather than silently defaulting it.
func optionalInt(args map[string]any, name string, def int) (int, error) {
	if _, ok := args[name]; !ok {
		return def, nil
	}
	return intArg(args, name)
}

// limitArg reads an optional `limit`, clamped to max.
func limitArg(args map[string]any, def, max int) (int, error) {
	n, err := optionalInt(args, "limit", def)
	if err != nil {
		return 0, err
	}
	if n > max {
		n = max
	}
	return n, nil
}

// stringSlice reads an array-of-strings argument. A non-array value is refused
// rather than ignored, so a malformed call cannot silently drop labels or
// assignees.
func stringSlice(args map[string]any, name string) ([]string, bool, error) {
	raw, ok := args[name]
	if !ok {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, fmt.Errorf("forgejo: %s must be an array of strings", name)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, true, fmt.Errorf("forgejo: %s must be an array of strings", name)
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, true, nil
}
