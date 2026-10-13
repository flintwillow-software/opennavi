// Package huly is the Huly connector (ADR 0014): the integration through which
// a navi acts in Huly, the workspace's canonical tracker. This slice is the
// **egress** half (ADR 0020) — the tools a navi calls to read and act — built
// natively over Huly's REST API, not through an MCP server, so the capability is
// an ordinary tool.Tool and the package stays contract-pure (ADR 0004).
//
// Egress tools are built with connector.SendTool and are added explicitly by
// the navi author; nothing auto-registers them (ADR 0006/0014).
//
// # Credentials (ADR 0008)
//
// Every credential is a REFERENCE — an environment variable name or a file path
// — never a value, and the value is read at connect time and never stored on the
// tool. A Huly connector authenticates with an **account token** (Config.Credential)
// or with email and password references (Config.Email / Config.Password); the
// token takes precedence. The workspace slug (Config.Workspace) is a compiled
// declaration, not a secret.
//
// # The Huly REST API
//
// A connection is three steps: read the instance's `config.json` for its
// accounts URL, authenticate against it (a token, or a login RPC), and select
// the workspace, which answers with the workspace's transactor endpoint and a
// workspace-scoped token. Reads are `GET /api/v1/find-all/{workspace}` and
// writes are `POST /api/v1/tx/{workspace}` with a single transaction object —
// Huly's own client library speaks exactly this.
package huly

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/connector"
	"github.com/flintwillow-software/opennavi/tool"
)

// DefaultURL is the hosted Huly instance. A self-hosted deployment sets
// Config.URL; a compiled endpoint default is permitted (ADR 0008).
const DefaultURL = "https://huly.app"

// Config declares the connector at build time (ADR 0013: what a component can
// reach is a composition decision, not a runtime one).
type Config struct {
	// URL is the Huly instance root (e.g. https://huly.flintwillow.software).
	// Empty = DefaultURL.
	URL string
	// Workspace is the workspace slug — the segment in the workbench URL
	// (https://huly.app/workbench/<slug>). Required.
	Workspace string
	// Credential names where an ACCOUNT token lives: an environment variable
	// or a file path, never the value (ADR 0008). Preferred over Email/Password.
	Credential string
	// Email and Password name where login credentials live, used when no
	// Credential is declared. They are references too.
	Email string
	// Password names where the password lives (a reference).
	Password string
	// HTTPClient overrides the HTTP client (tests, a custom transport).
	HTTPClient *http.Client
	// Env resolves a credential reference. Nil = os.Getenv.
	Env func(string) string
}

// Connector is the Huly connector component. It satisfies connector.Connector:
// Start connects (best-effort — a call retries) and publishes the live client,
// Stop clears it, so a call before Start or after Stop fails with
// connector.ErrNotStarted rather than dereferencing nothing.
type Connector struct {
	cfg  Config
	live *connector.Live[*Client]
}

// New builds the connector. It performs no I/O: the workspace session is
// established at Start (or lazily at the first call).
func New(cfg Config) *Connector {
	return &Connector{cfg: cfg, live: connector.NewLive[*Client]()}
}

// Start connects to Huly and publishes the client. A connection failure is
// warned, not fatal: the client retries at the call, so a navi whose tracker is
// briefly unreachable still starts and its egress tools report the failure
// cleanly instead of the process refusing to serve.
func (c *Connector) Start(ctx context.Context, deps connector.Deps) error {
	client := newClient(c.cfg)
	if err := client.connect(ctx); err != nil && deps.Log != nil {
		deps.Log.Warn("huly connector could not connect; its egress tools will retry", "err", err)
	}
	c.live.Set(client)
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
//	h := huly.New(huly.Config{Workspace: "flintwillowsoftware", Credential: "HULY_TOKEN"})
//	n.Connectors.Add(h)
//	for _, t := range mustTools(h.SendTools()) { n.Tools.Add(t) }
//
// It returns an error rather than panicking because the descriptor is validated
// when the tool is built — a misdeclared egress tool fails at composition time,
// not at the first model call.
func (c *Connector) SendTools() ([]tool.Tool, error) {
	list, err := connector.SendTool(issueListDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueList(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	comment, err := connector.SendTool(issueCommentDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueComment(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	update, err := connector.SendTool(issueUpdateDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueUpdate(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	search, err := connector.SendTool(issueSearchDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueSearch(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	create, err := connector.SendTool(issueCreateDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueCreate(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	label, err := connector.SendTool(issueLabelDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueLabel(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	relation, err := connector.SendTool(issueRelationDef(), c.live, func(ctx context.Context, cl *Client, args map[string]any) (string, error) {
		return cl.issueRelation(ctx, args)
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{list, comment, update, search, create, label, relation}, nil
}

// resolveRef resolves a credential REFERENCE (ADR 0008): an environment
// variable, or a file path whose contents are the value. An empty reference is
// an empty value (the caller decides whether that is fatal).
func resolveRef(ref string, env func(string) string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if env == nil {
		env = defaultEnv
	}
	if v := strings.TrimSpace(env(ref)); v != "" {
		return v
	}
	// A reference may name a file; a missing or unreadable file is an empty
	// value, reported by the caller's own "credential is unset" error.
	if b, err := readFile(ref); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// defaultTimeout bounds a Huly API call.
const defaultTimeout = 20 * time.Second
