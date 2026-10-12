// Package github is the GitHub connector (ADR 0014): the integration through
// which a navi acts on GitHub and hears from it. The **egress** half (ADR 0020)
// is the tools through which a navi acts; the **inbound** half (ADR 0023) is the
// build-time subscription declaration and the poll loop that turns a declared
// event into a thought. Both halves share one client, one configuration, and one
// credential.
//
// Egress tools are built with connector.SendTool and are added explicitly by
// the navi author; nothing auto-registers them (ADR 0006/0014). The credential
// arrives through a TokenSource (ADR 0020): a static reference resolved at the
// call site (ADR 0008), or a source that mints and refreshes its own token —
// which is what a GitHub App installation needs, since its token expires in an
// hour.
package github

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
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

// TokenSource yields the bearer token for a request. It is an interface rather
// than a value because GitHub credentials are not all static: a personal
// access token is a string read from the environment, while a **GitHub App
// installation** holds a private key, signs an RS256 JWT, exchanges it for an
// installation access token that expires in an hour, and must re-mint it. A
// resolver returning a string cannot express that, and resolving per request
// would mint a token per call — so the source owns its own caching and
// refresh, and is asked only for a usable token.
//
// An implementation must never write the token it yields to a log, an error, or
// the transcript; a static source holds no value at all, and a minting source
// holds only its short-lived cache.
type TokenSource interface {
	// Token returns a usable bearer token. The context bounds any minting the
	// source has to do; a static source ignores it. The error must name what is
	// missing (a reference, an app ID) and never a secret value.
	Token(ctx context.Context) (string, error)
}

// TokenInvalidator is the optional extension to TokenSource that makes a
// rejected token recoverable. A source that caches a MINTED token implements
// it, and the egress path calls Invalidate when GitHub answers 401, because an
// installation token can be revoked before its hour is up — so a 401 is not
// necessarily "the credential is wrong", and one retry with a fresh token
// recovers the call.
//
// The token is passed so a 401 on a token that has since been replaced does not
// discard its replacement. A static source has nothing to discard and does not
// implement this, so its 401 is reported rather than retried.
type TokenInvalidator interface {
	Invalidate(token string)
}

// StaticToken is a TokenSource over a credential REFERENCE: it reads the value
// from Env at each call, so a rotated token is picked up without a restart and
// the value is never stored (ADR 0014 decision 6).
//
// This is the right source for a personal access token, and for the
// GITHUB_TOKEN a GitHub Actions workflow mints per run.
type StaticToken struct {
	// Ref names where the token lives — an environment variable or file path,
	// never the value (ADR 0008).
	Ref string
	// Env resolves Ref. Nil = os.Getenv.
	Env func(string) string
}

// Token implements TokenSource.
func (s StaticToken) Token(context.Context) (string, error) {
	ref := strings.TrimSpace(s.Ref)
	if ref == "" {
		return "", errors.New("github: no credential declared; set Config.Credential to the reference (ADR 0008)")
	}
	env := s.Env
	if env == nil {
		env = os.Getenv
	}
	if v := strings.TrimSpace(env(ref)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("github: credential %q is unset; the connector is disabled (fail closed)", ref)
}

// Config declares the connector at build time (ADR 0013: what a component can
// reach is a composition decision, not a runtime one).
type Config struct {
	// Credential names where a static token lives — an environment variable or
	// file path, never the value (ADR 0008). Required unless Token is set.
	Credential string
	// Token overrides Credential with a token source, for a credential that is
	// not a static string (a GitHub App installation, which mints and refreshes
	// its own tokens). Nil means StaticToken{Credential, Env}.
	Token TokenSource
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
	// Subscriptions are the build-time inbound declarations (ADR 0023). Empty
	// = the connector has no inbound half and Start launches no poll loop.
	Subscriptions []Subscription
	// PollInterval is how often the inbound loop polls. Zero =
	// DefaultPollInterval.
	PollInterval time.Duration
}

// Connector is the GitHub integration. It implements connector.Connector for
// the lifecycle and exposes its outbound operations through SendTools.
type Connector struct {
	cfg  Config
	live *connector.Live[*client]

	// stateDB persists the inbound seen-set (ADR 0023 decision 5). Nil =
	// in-memory dedupe only.
	stateDB *sql.DB
	// now overrides the poll clock (tests). Nil = time.Now.
	now func() time.Time

	// pollCancel / pollWG control the inbound poll loop.
	pollCancel context.CancelFunc
	pollWG     sync.WaitGroup
}

// Option configures the connector.
type Option func(*Connector)

// WithStateStore persists the inbound dedupe set in the shared SQLite
// connection, so a redelivered event is suppressed across a restart (ADR 0023
// decision 5). Without it the seen-set is in memory only, and a restart may
// replay a redelivery.
func WithStateStore(db *sql.DB) Option {
	return func(c *Connector) { c.stateDB = db }
}

// New builds the connector. It performs no I/O and resolves no credential:
// the client arrives at Start.
func New(cfg Config, opts ...Option) *Connector {
	c := &Connector{cfg: cfg, live: connector.NewLive[*client]()}
	for _, o := range opts {
		o(c)
	}
	return c
}

// clock is the poll clock, overridable in tests.
func (c *Connector) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Start publishes a client so an egress tool bound at authoring time can reach
// it, and — when inbound subscriptions are declared — launches the poll loop
// (ADR 0023). Both halves consume the same client.
//
// A missing credential does NOT fail Start. The connector is declared at build
// time (ADR 0014 decision 4), so the service exists whether or not the operator
// configured a token; what an unset credential disables is the *capability*, not
// the navi — a navi whose connector Start fails aborts the whole run
// (navi.serve), which would make one unconfigured integration take down every
// other capability. Each egress call refuses at the call site, naming the
// reference (ADR 0008: fail closed, never silently degrade), and Start warns
// once so the operator sees it without waiting for a failed call.
func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
	// Ask the source for a token once so a misconfiguration is visible at start
	// rather than at the first call. A minting source may reach the network
	// here; a failure is warned, not fatal (see above), and retried at the call.
	if _, err := c.tokenSource().Token(ctx); err != nil && deps.Log != nil {
		deps.Log.Warn("github connector has no usable credential; its egress tools will refuse", "err", err)
	}
	c.live.Set(c.newClient())
	// The inbound half (ADR 0023) reuses this same client; it starts no second
	// connection and resolves no second credential.
	return c.startInbound(ctx, deps)
}

// Stop releases the client, so a call after Stop fails the same way a call
// before Start does.
func (c *Connector) Stop() error {
	if c.pollCancel != nil {
		c.pollCancel()
		c.pollCancel = nil
		c.pollWG.Wait()
	}
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

// tokenSource is the configured source, or the static one over Credential.
// Building it once keeps a minting source's cache in one place rather than
// giving every client its own.
func (c *Connector) tokenSource() TokenSource {
	if c.cfg.Token != nil {
		return c.cfg.Token
	}
	return StaticToken{Ref: c.cfg.Credential, Env: c.cfg.Env}
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
		tokens:     c.tokenSource(),
		http:       hc,
	}
}

// client is the live service client an egress handler is handed. It holds no
// secret: the token comes from the TokenSource at the moment of the request.
type client struct {
	base       string
	repository string
	tokens     TokenSource
	http       *http.Client
}

// comment posts a comment on an issue (or pull request — GitHub treats them
// alike).
func (c *client) comment(ctx context.Context, args map[string]any) (string, error) {
	// The credential is checked first: without one no call can succeed, so a
	// missing credential is reported as itself rather than masked by whatever
	// else is wrong with the arguments.
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return "", err
	}
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

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", fmt.Errorf("github: encode comment: %w", err)
	}
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", c.base, repo, issue)

	// One bounded attempt loop, for exactly one reason: a minted installation
	// token can be revoked before its hour is up, so a 401 does not necessarily
	// mean the credential is wrong. A source that caches a minted token can be
	// told the token it handed out was rejected (TokenInvalidator), and one
	// retry with a fresh token recovers the call. A static source has nothing to
	// discard, so its 401 is reported as itself. There is no other retry: the
	// comment POST is not idempotent.
	var resp *http.Response
	for attempt := 0; ; attempt++ {
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

		resp, err = c.http.Do(req)
		if err != nil {
			// Deliberately not wrapping the request: a transport error must not be
			// able to echo the Authorization header into the transcript.
			return "", fmt.Errorf("github: comment on %s#%d: %w", repo, issue, err)
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			break
		}
		inv, ok := c.tokens.(TokenInvalidator)
		if !ok {
			break
		}
		resp.Body.Close()
		inv.Invalidate(token)
		if token, err = c.tokens.Token(ctx); err != nil {
			return "", err
		}
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
