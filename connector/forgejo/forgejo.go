// Package forgejo is the Forgejo connector (ADR 0014): the integration through
// which a navi acts in a self-hosted Forgejo instance. This slice is the
// **egress** half (ADR 0020) — the tools a navi calls to read and act — built
// natively over the Forgejo/Gitea REST API, not through an MCP server, so the
// capability is an ordinary tool.Tool and the package stays contract-pure
// (ADR 0004).
//
// Egress tools are built with connector.SendTool and are added explicitly by
// the navi author; nothing auto-registers them (ADR 0006/0014).
//
// # Credentials (ADR 0008)
//
// The credential is a REFERENCE — an environment variable name or a file path
// — never a value, and the value is read at connect time and never stored on
// the tool. A Forgejo connector authenticates with an **access token**
// (Config.Credential), sent as the `token` authorization scheme the API
// documents. The instance base URL (Config.BaseURL) is a compiled endpoint
// default, not a secret.
//
// # The Forgejo/Gitea REST API
//
// The API root is `<instance>/api/v1`. Issues and pull requests are addressed
// by their repository `index` (the `#N` in the web UI); a pull request is also
// an issue, so comments are shared. Reads are `GET`, writes are `POST` (create)
// and `PATCH` (edit). Labels are assigned by **id**, not by name, so a tool
// that accepts a label name resolves it against the repository's label list
// first — a divergence from GitHub worth handling rather than exposing.
package forgejo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/tool"
)

// DefaultBaseURL is the workspace's self-hosted Forgejo instance. Forgejo is
// self-hosted, so there is no public default the way api.github.com is one;
// this compiled default names the instance this connector was written for and
// the operator points Config.BaseURL at any other instance (ADR 0008 permits a
// compiled endpoint default).
const DefaultBaseURL = "https://git.flintwillow.software"

// Egress tool names. The namespace is the connector identity, so every one is
// `forgejo_*` (ADR 0020 decision 5).
const (
	// ToolIssueList lists issues in a repository.
	ToolIssueList = "forgejo_issue_list"
	// ToolIssueGet reads one issue (or pull request) by index.
	ToolIssueGet = "forgejo_issue_get"
	// ToolIssueCreate opens an issue.
	ToolIssueCreate = "forgejo_issue_create"
	// ToolIssueUpdate edits an issue's state, title, body, labels, or assignees.
	ToolIssueUpdate = "forgejo_issue_update"
	// ToolIssueComment posts a comment on an issue or pull request.
	ToolIssueComment = "forgejo_issue_comment"
	// ToolPullList lists pull requests in a repository.
	ToolPullList = "forgejo_pull_list"
	// ToolPullCreate opens a pull request.
	ToolPullCreate = "forgejo_pull_create"
	// ToolRepoGet reads one repository's metadata.
	ToolRepoGet = "forgejo_repo_get"
	// ToolLabelList lists a repository's labels, with their ids.
	ToolLabelList = "forgejo_label_list"
)

// maxErrorBody bounds how much of a failed response reaches the model.
const maxErrorBody = 8 << 10

// maxLabelPages bounds the label-list walk when resolving label names.
const maxLabelPages = 10

// TokenSource yields the bearer token for a request. It is an interface rather
// than a plain string for the same reason GitHub's is (ADR 0020 decision 8): a
// credential that must be minted (an OAuth2 token exchange) is expressible
// without changing the tools, and a test can inject one.
//
// An implementation must never write the token it yields to a log, an error, or
// the transcript.
type TokenSource interface {
	// Token returns a usable access token. The context bounds any minting the
	// source has to do; a static source ignores it. The error must name what is
	// missing (a reference) and never a secret value.
	Token(ctx context.Context) (string, error)
}

// StaticToken is a TokenSource over a credential REFERENCE: it reads the value
// from Env at each call, so a rotated token is picked up without a restart and
// the value is never stored (ADR 0014 decision 6). This is the right source for
// a Forgejo access token.
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
		return "", errors.New("forgejo: no credential declared; set Config.Credential to the reference (ADR 0008)")
	}
	env := s.Env
	if env == nil {
		env = os.Getenv
	}
	if v := strings.TrimSpace(env(ref)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("forgejo: credential %q is unset; the connector is disabled (fail closed)", ref)
}

// Config declares the connector at build time (ADR 0013: what a component can
// reach is a composition decision, not a runtime one).
type Config struct {
	// BaseURL is the Forgejo instance root (e.g.
	// https://git.flintwillow.software). Empty = DefaultBaseURL.
	BaseURL string
	// Credential names where an access token lives — an environment variable
	// or file path, never the value (ADR 0008). Required unless Token is set.
	Credential string
	// Token overrides Credential with a token source, for a credential that is
	// not a static string. Nil means StaticToken{Credential, Env}.
	Token TokenSource
	// Repository is the default "owner/name" the egress tools act on. A tool
	// call may override it.
	Repository string
	// HTTPClient is the transport. Nil = a client with a 20s timeout.
	HTTPClient *http.Client
	// Env resolves Credential. Nil = os.Getenv.
	Env func(string) string
}

// Connector is the Forgejo integration. It implements connector.Connector for
// the lifecycle and exposes its outbound operations through SendTools.
type Connector struct {
	cfg  Config
	live *connector.Live[*client]
}

// New builds the connector. It performs no I/O and resolves no credential: the
// client arrives at Start.
func New(cfg Config) *Connector {
	return &Connector{cfg: cfg, live: connector.NewLive[*client]()}
}

// Start publishes a client so an egress tool bound at authoring time can reach
// it. A missing credential does NOT fail Start: the connector is declared at
// build time (ADR 0014 decision 4), so the service exists whether or not the
// operator configured a token; what an unset credential disables is the
// *capability*, not the navi (ADR 0020 decision 7). Each egress call refuses at
// the call site, naming the reference (ADR 0008), and Start warns once so the
// operator sees it without waiting for a failed call.
func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
	if _, err := c.tokenSource().Token(ctx); err != nil && deps.Log != nil {
		deps.Log.Warn("forgejo connector has no usable credential; its egress tools will refuse", "err", err)
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
//	fj := forgejo.New(forgejo.Config{Credential: "FORGEJO_TOKEN", Repository: "acme/widgets"})
//	n.Connectors.Add(fj)
//	for _, t := range mustTools(fj.SendTools()) { n.Tools.Add(t) }
//
// It returns an error rather than panicking because the descriptor is validated
// when the tool is built — a misdeclared egress tool fails at composition time,
// not at the first model call.
func (c *Connector) SendTools() ([]tool.Tool, error) {
	defs := []struct {
		def  tool.Def
		call func(ctx context.Context, cl *client, args map[string]any) (string, error)
	}{
		{issueListDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.issueList(ctx, args)
		}},
		{issueGetDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.issueGet(ctx, args)
		}},
		{issueCreateDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.issueCreate(ctx, args)
		}},
		{issueUpdateDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.issueUpdate(ctx, args)
		}},
		{issueCommentDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.issueComment(ctx, args)
		}},
		{pullListDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.pullList(ctx, args)
		}},
		{pullCreateDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.pullCreate(ctx, args)
		}},
		{repoGetDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.repoGet(ctx, args)
		}},
		{labelListDef(), func(ctx context.Context, cl *client, args map[string]any) (string, error) {
			return cl.labelList(ctx, args)
		}},
	}
	tools := make([]tool.Tool, 0, len(defs))
	for _, d := range defs {
		t, err := connector.SendTool(d.def, c.live, d.call)
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// tokenSource is the configured source, or the static one over Credential.
func (c *Connector) tokenSource() TokenSource {
	if c.cfg.Token != nil {
		return c.cfg.Token
	}
	return StaticToken{Ref: c.cfg.Credential, Env: c.cfg.Env}
}

// newClient builds the service client. It carries the credential REFERENCE,
// never a value.
func (c *Connector) newClient() *client {
	root := strings.TrimRight(strings.TrimSpace(c.cfg.BaseURL), "/")
	if root == "" {
		root = DefaultBaseURL
	}
	hc := c.cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &client{
		apiBase:    root + "/api/v1",
		repository: strings.TrimSpace(c.cfg.Repository),
		tokens:     c.tokenSource(),
		http:       hc,
	}
}
