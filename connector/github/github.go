// Package github is the GitHub connector's **egress** half (ADR 0014,
// ADR 0020): the tools through which a navi acts on GitHub. It is the worked
// example for the egress seam, and deliberately minimal.
//
// The inbound half — which events the connector watches, and how a stimulus
// becomes a thought — is NOT here. ADR 0014 open question 3 owns that
// decision, and this package should be read as a partial connector until it
// lands rather than as the whole component.
//
// Egress tools are built with connector.SendTool and are added explicitly by
// the navi author; nothing auto-registers them (ADR 0006/0014). The
// credential is a REFERENCE (ADR 0008) resolved at the call site, so the token
// is never held in a field that could be logged or dumped.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/tool"
)

// DefaultBaseURL is GitHub's public API root. Compiled in as an endpoint
// default (ADR 0008 permits this); the operator overrides it at runtime.
const DefaultBaseURL = "https://api.github.com"

// ToolIssueComment is the name of the one egress tool this connector exposes.
const ToolIssueComment = "github_issue_comment"

// maxErrorBody bounds how much of a failed response reaches the model.
const maxErrorBody = 8 << 10

// Config declares the connector at build time (ADR 0013: what a component can
// reach is a composition decision, not a runtime one).
type Config struct {
	// Credential names where the token lives — an environment variable or file
	// path, never the value (ADR 0008). Required.
	Credential string
	// BaseURL overrides the API root (GitHub Enterprise, tests).
	// Empty = DefaultBaseURL.
	BaseURL string
	// Repository is the default "owner/name" the egress tools act on. A tool
	// call may override it.
	Repository string
	// HTTPClient is the transport. Nil = a client with a 20s timeout.
	HTTPClient *http.Client
	// Env resolves Credential. Nil = os.Getenv.
	Env func(string) string
}

// Connector is the GitHub integration. It implements connector.Connector for
// the lifecycle and exposes its outbound operations through SendTools.
type Connector struct {
	cfg  Config
	live *connector.Live[*client]
}

// New builds the connector. It performs no I/O and resolves no credential:
// the client arrives at Start.
func New(cfg Config) *Connector {
	return &Connector{cfg: cfg, live: connector.NewLive[*client]()}
}

// Start validates the credential reference and publishes a client, so an
// egress tool bound at authoring time can reach it. It starts no inbound loop:
// this is the egress half only.
//
// A missing credential fails Start with a clear error naming the REFERENCE
// (ADR 0008: credential resolution is fail-closed, and a value is never
// printed).
func (c *Connector) Start(_ context.Context, _ connector.Deps) error {
	if _, err := c.token(); err != nil {
		return err
	}
	c.live.Set(c.newClient())
	return nil
}

// Stop releases the client, so a call after Stop fails the same way a call
// before Start does.
func (c *Connector) Stop() error {
	c.live.Clear()
	return nil
}

// SendTools returns the connector's egress tools for the author to add
// explicitly:
//
//	gh := github.New(github.Config{Credential: "GITHUB_TOKEN", Repository: "acme/widgets"})
//	n.Connectors.Add(gh)
//	for _, t := range mustTools(gh.SendTools()) { n.Tools.Add(t) }
//
// It returns an error rather than panicking because the descriptor is
// validated when the tool is built — a misdeclared egress tool should fail at
// composition time, not at the first model call.
func (c *Connector) SendTools() ([]tool.Tool, error) {
	comment, err := connector.SendTool(commentDef(), c.live, func(ctx context.Context, cl *client, args map[string]any) (string, error) {
		return cl.comment(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{comment}, nil
}

// token resolves the credential reference to its value. It is called at the
// call site and the result is never stored (ADR 0014 decision 6), so a token
// cannot leak through a struct dump, a log line, or the transcript.
func (c *Connector) token() (string, error) {
	ref := strings.TrimSpace(c.cfg.Credential)
	if ref == "" {
		return "", errors.New("github: no credential declared; set Config.Credential to the reference (ADR 0008)")
	}
	env := c.cfg.Env
	if env == nil {
		env = os.Getenv
	}
	if v := strings.TrimSpace(env(ref)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("github: credential %q is unset; the connector is disabled (fail closed)", ref)
}

// newClient builds the service client. It carries the credential REFERENCE,
// never a value.
func (c *Connector) newClient() *client {
	base := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	hc := c.cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &client{
		base:       base,
		repository: strings.TrimSpace(c.cfg.Repository),
		credential: strings.TrimSpace(c.cfg.Credential),
		env:        c.cfg.Env,
		http:       hc,
	}
}

// client is the live service client an egress handler is handed. It holds no
// secret: the token is resolved per request.
type client struct {
	base       string
	repository string
	credential string
	env        func(string) string
	http       *http.Client
}

// comment posts a comment on an issue (or pull request — GitHub treats them
// alike).
func (c *client) comment(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	issue, err := intArg(args, "issue")
	if err != nil {
		return "", err
	}
	body, _ := args["body"].(string)
	if strings.TrimSpace(body) == "" {
		return "", errors.New("github: issue comment needs a non-empty body")
	}
	token, err := c.token()
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", fmt.Errorf("github: encode comment: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.base, repo, issue)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("github: build comment request: %w", err)
	}
	// The token is used here and nowhere else. It is sent as a header, never a
	// query parameter, so it cannot reach a URL that ends up in a log or an
	// error message.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opennavi")

	resp, err := c.http.Do(req)
	if err != nil {
		// Deliberately not wrapping the request: a transport error must not be
		// able to echo the Authorization header into the transcript.
		return "", fmt.Errorf("github: comment on %s#%d: %w", repo, issue, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("github: comment on %s#%d: http %d: %s",
			repo, issue, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	out := map[string]any{"commented": fmt.Sprintf("%s#%d", repo, issue)}
	var created struct {
		HTMLURL string `json:"html_url"`
		ID      int64  `json:"id"`
	}
	if json.Unmarshal(respBody, &created) == nil {
		if created.HTMLURL != "" {
			out["url"] = created.HTMLURL
		}
		if created.ID != 0 {
			out["id"] = created.ID
		}
	}
	blob, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("github: encode result: %w", err)
	}
	return string(blob), nil
}

// token resolves the credential at the call site.
func (c *client) token() (string, error) {
	if c.credential == "" {
		return "", errors.New("github: no credential declared; set Config.Credential to the reference (ADR 0008)")
	}
	env := c.env
	if env == nil {
		env = os.Getenv
	}
	if v := strings.TrimSpace(env(c.credential)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("github: credential %q is unset; the connector is disabled (fail closed)", c.credential)
}

// repositoryFor resolves the target repository: the call's override, else the
// configured default. It is a required argument, so a tool cannot silently
// act on the wrong repository.
func (c *client) repositoryFor(args map[string]any) (string, error) {
	repo := strings.TrimSpace(c.repository)
	if v, ok := args["repository"].(string); ok && strings.TrimSpace(v) != "" {
		repo = strings.TrimSpace(v)
	}
	if repo == "" {
		return "", errors.New("github: no repository; set Config.Repository or pass repository")
	}
	if !strings.Contains(repo, "/") {
		return "", fmt.Errorf("github: repository %q must be owner/name", repo)
	}
	return repo, nil
}

// intArg reads a positive integer argument. Validated JSON arrives as a
// float64, but an in-process caller may hand a Go int.
func intArg(args map[string]any, name string) (int, error) {
	switch v := args[name].(type) {
	case float64:
		if v < 1 || v != float64(int(v)) {
			return 0, fmt.Errorf("github: %s must be a positive integer", name)
		}
		return int(v), nil
	case int:
		if v < 1 {
			return 0, fmt.Errorf("github: %s must be a positive integer", name)
		}
		return v, nil
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 1 {
			return 0, fmt.Errorf("github: %s must be a positive integer", name)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("github: %s is required", name)
	}
}

// commentDef is the descriptor for github_issue_comment. Recovery is
// capture_response, not a retry: a retried POST would post the comment twice,
// so the outcome of the call is the response and nothing retries it for the
// model.
func commentDef() tool.Def {
	schema, err := tool.ParseSchema(json.RawMessage(`{
		"type": "object",
		"required": ["issue", "body"],
		"properties": {
			"issue": { "type": "integer", "minimum": 1, "description": "Issue or pull request number." },
			"body": { "type": "string", "minLength": 1, "description": "Comment body, in GitHub-flavored Markdown." },
			"repository": { "type": "string", "description": "owner/name; defaults to the configured repository." }
		},
		"additionalProperties": false
	}`))
	if err != nil {
		// The schema is a compiled-in literal: a parse failure is a programming
		// error, and SendTool would refuse the empty schema anyway.
		panic(fmt.Sprintf("github: comment schema: %v", err))
	}
	return tool.Def{
		Name:         ToolIssueComment,
		Description:  "Posts a comment on a GitHub issue or pull request and returns the created comment's URL. Requires the connector's credential; the token is never an argument.",
		InputSchema:  schema,
		OutputFormat: "json",
		// Egress reaches outside the process, so it is execute-level: a navi
		// that cannot execute cannot talk to GitHub at all.
		Access:        tool.AccessExecute,
		Timeout:       15 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"github", "egress", "issue"},
		Version:       "1.0.0",
		PromptSnippet: "github_issue_comment: post a comment on a GitHub issue",
		PromptGuidelines: []string{
			"Never pass a token or credential as an argument — the connector resolves it itself.",
			"Comments are public on a public repository: write what you would post by hand.",
			"Post once. A retry posts a second comment, so recovery is not automatic.",
		},
	}
}
