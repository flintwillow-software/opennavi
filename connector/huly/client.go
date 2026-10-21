package huly

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This file is the Huly REST client: the transport the egress tools call
// through. It speaks Huly's own REST API — `GET /api/v1/find-all/{workspace}`
// and `POST /api/v1/tx/{workspace}` — so the connector is native Go, not a
// wrapper around another process (NAVI-408).
//
// The workspace session is established lazily and cached: a navi that starts
// while Huly is unreachable still serves, and the first call reports the failure
// rather than the process refusing to start.

// Client is a connected Huly workspace. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	mu   sync.Mutex
	sess *session
}

// session is the workspace-scoped connection the API calls need.
type session struct {
	// endpoint is the workspace's transactor root, http(s) scheme.
	endpoint string
	// workspace is the workspace id (a uuid) the routes carry.
	workspace string
	// token is the workspace-scoped bearer token.
	token string
	// socialID is the account's PRIMARY SOCIAL ID, which Huly stamps on a
	// transaction as its author (modifiedBy/createdBy). It is NOT the account
	// uuid: Huly's own client builds its transaction factory with
	// `me.primarySocialId`, and a transaction carrying the uuid is refused with
	// AccountMismatch.
	socialID string
	// personID is the account's `contact:class:Person` document id — the id an
	// issue's `assignee` field references. It is neither the account uuid nor
	// the social id, so the inbound half resolves it once at connect. Empty when
	// the instance has no Person for the account.
	personID string
	// collaborator is the http(s) root of the instance's collaborator service
	// (config.json's COLLABORATOR_URL), where rich text such as an issue's
	// description lives. Empty when the instance declares none.
	collaborator string
}

// maxBody bounds how much of a response is read, so a hostile or broken server
// cannot exhaust memory.
const maxBody = 8 << 20

func newClient(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{cfg: cfg, http: hc}
}

func defaultEnv(name string) string        { return os.Getenv(name) }
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// connect establishes the workspace session. Idempotent: a second call while a
// session is live is a no-op.
func (c *Client) connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil {
		return nil
	}
	s, err := c.open(ctx)
	if err != nil {
		return err
	}
	c.sess = s
	return nil
}

// session returns the live session, connecting on first use.
func (c *Client) session(ctx context.Context) (*session, error) {
	c.mu.Lock()
	if c.sess != nil {
		s := c.sess
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil, fmt.Errorf("huly: no session")
	}
	return c.sess, nil
}

// forget drops the cached session so the next call reconnects. It is how a
// refused token recovers: a workspace token can expire or be revoked, and one
// reconnect with a fresh one recovers the call.
func (c *Client) forget() {
	c.mu.Lock()
	c.sess = nil
	c.mu.Unlock()
}

// open runs the three-step connection: read the instance's config for its
// accounts URL, authenticate against it, and select the workspace, which
// answers with the transactor endpoint and a workspace token.
func (c *Client) open(ctx context.Context) (*session, error) {
	base := strings.TrimRight(strings.TrimSpace(c.cfg.URL), "/")
	if base == "" {
		base = DefaultURL
	}
	ws := strings.TrimSpace(c.cfg.Workspace)
	if ws == "" {
		return nil, fmt.Errorf("huly: no workspace declared (Config.Workspace)")
	}
	env := c.cfg.Env
	if env == nil {
		env = defaultEnv
	}

	var conf struct {
		AccountsURL     string `json:"ACCOUNTS_URL"`
		CollaboratorURL string `json:"COLLABORATOR_URL"`
	}
	if err := c.getJSON(ctx, base+"/config.json", "", &conf); err != nil {
		return nil, fmt.Errorf("huly: read config.json: %w", err)
	}
	accounts := strings.TrimRight(strings.TrimSpace(conf.AccountsURL), "/")
	if accounts == "" {
		return nil, fmt.Errorf("huly: %s/config.json declares no ACCOUNTS_URL", base)
	}

	// A token reference takes precedence over a login (ADR 0008).
	accountToken := resolveRef(c.cfg.Credential, env)
	if accountToken == "" {
		email := resolveRef(c.cfg.Email, env)
		password := resolveRef(c.cfg.Password, env)
		if email == "" || password == "" {
			return nil, fmt.Errorf("huly: no credential declared; set Config.Credential (an account-token reference) or Config.Email and Config.Password (ADR 0008)")
		}
		var login struct {
			Token string `json:"token"`
		}
		if err := c.rpc(ctx, accounts, "", map[string]any{
			"method": "login",
			"params": map[string]any{"email": email, "password": password},
		}, &login); err != nil {
			return nil, fmt.Errorf("huly: login: %w", err)
		}
		accountToken = strings.TrimSpace(login.Token)
	}
	if accountToken == "" {
		return nil, fmt.Errorf("huly: authentication produced no token")
	}

	var sel struct {
		Endpoint  string `json:"endpoint"`
		Token     string `json:"token"`
		Workspace string `json:"workspace"`
		Account   string `json:"account"`
	}
	if err := c.rpc(ctx, accounts, accountToken, map[string]any{
		"method": "selectWorkspace",
		"params": map[string]any{"workspaceUrl": ws, "kind": "external", "externalRegions": []any{}},
	}, &sel); err != nil {
		return nil, fmt.Errorf("huly: select workspace %q: %w", ws, err)
	}
	if strings.TrimSpace(sel.Endpoint) == "" || strings.TrimSpace(sel.Token) == "" {
		return nil, fmt.Errorf("huly: workspace %q answered with no endpoint or token", ws)
	}
	endpoint := strings.Replace(sel.Endpoint, "ws", "http", 1)
	collaborator := strings.Replace(strings.TrimSpace(conf.CollaboratorURL), "ws", "http", 1)

	// A transaction's author is the account's PRIMARY SOCIAL ID, not its uuid:
	// Huly's own client builds its transaction factory from `me.primarySocialId`,
	// and a transaction carrying the uuid is refused with AccountMismatch.
	var account struct {
		UUID            string `json:"uuid"`
		PrimarySocialID string `json:"primarySocialId"`
	}
	accountURL := fmt.Sprintf("%s/api/v1/account/%s", strings.TrimRight(endpoint, "/"), url.PathEscape(sel.Workspace))
	if err := c.getJSON(ctx, accountURL, sel.Token, &account); err != nil {
		return nil, fmt.Errorf("huly: read account: %w", err)
	}
	socialID := strings.TrimSpace(account.PrimarySocialID)
	if socialID == "" {
		return nil, fmt.Errorf("huly: account has no primary social id")
	}
	personID, err := c.lookupPersonID(ctx, endpoint, sel.Workspace, sel.Token, strings.TrimSpace(account.UUID))
	if err != nil {
		return nil, fmt.Errorf("huly: resolve the account's person: %w", err)
	}
	return &session{
		// Huly answers with the WebSocket endpoint; the REST routes live at the
		// same host with the http scheme.
		endpoint:     endpoint,
		workspace:    sel.Workspace,
		token:        sel.Token,
		socialID:     socialID,
		personID:     personID,
		collaborator: collaborator,
	}, nil
}

// lookupPersonID resolves the account's Person document id — the id an issue's
// `assignee` references — by matching the Person's `personUuid` against the
// account's uuid. It is a direct REST call rather than findAll because `open`
// runs under the client lock, before a session exists. It returns "" when the
// instance has no Person for the account (a service account with no contact
// record), and the inbound half then has no "my issues" to poll.
func (c *Client) lookupPersonID(ctx context.Context, endpoint, workspace, token, personUUID string) (string, error) {
	if strings.TrimSpace(personUUID) == "" {
		return "", nil
	}
	params := url.Values{}
	params.Set("class", classPerson)
	params.Set("query", fmt.Sprintf(`{"personUuid":%q}`, personUUID))
	params.Set("options", `{"limit":1}`)
	target := fmt.Sprintf("%s/api/v1/find-all/%s?%s",
		strings.TrimRight(endpoint, "/"), url.PathEscape(workspace), params.Encode())
	body, err := c.getRaw(ctx, target, token)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Value []map[string]any `json:"value"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("decode person lookup: %w", err)
	}
	if len(envelope.Value) == 0 {
		return "", nil
	}
	return stringField(envelope.Value[0], "_id"), nil
}

// findAll reads documents of one class. Huly answers with a TotalArray envelope
// (`{"value": [...]}`); a bare array is also accepted, so a version difference
// does not break a read.
func (c *Client) findAll(ctx context.Context, class string, query, options map[string]any) ([]map[string]any, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("class", class)
	if len(query) > 0 {
		b, err := json.Marshal(query)
		if err != nil {
			return nil, fmt.Errorf("huly: encode query: %w", err)
		}
		params.Set("query", string(b))
	}
	if len(options) > 0 {
		b, err := json.Marshal(options)
		if err != nil {
			return nil, fmt.Errorf("huly: encode options: %w", err)
		}
		params.Set("options", string(b))
	}
	target := fmt.Sprintf("%s/api/v1/find-all/%s?%s",
		strings.TrimRight(s.endpoint, "/"), url.PathEscape(s.workspace), params.Encode())

	body, err := c.getRaw(ctx, target, s.token)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Value []map[string]any `json:"value"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Value != nil {
		restoreQueryFields(envelope.Value, query)
		return envelope.Value, nil
	}
	var bare []map[string]any
	if err := json.Unmarshal(body, &bare); err != nil {
		return nil, fmt.Errorf("huly: decode find-all response: %w", err)
	}
	restoreQueryFields(bare, query)
	return bare, nil
}

// maxKeysetPages bounds a keyset walk. Huly's find-all has no offset or cursor
// (FindOptions carries only limit, sort, lookup, projection and total), so
// reading past one page means re-querying with the sort key tightened. The walk
// stops here with an error rather than truncating: a window that needs more
// pages is either genuinely huge or not draining, and silently dropping the
// tail is the bug this exists to prevent.
const maxKeysetPages = 100

// findAllKeyset reads every document matching query by walking the sort key.
//
// The endpoint cannot be paged by offset, so each page is fetched with the sort
// key bounded inclusively at the last key read — the newest for an ascending
// walk, the oldest for a descending one — and documents already read are dropped
// by id. An inclusive bound with local dedupe is what makes a page boundary that
// lands inside a group sharing one key value (a millisecond, for createdOn or
// modifiedOn) safe: `$gt` would skip the rest of the group. A full page that
// yields nothing new means more than one page shares a single key value, which
// find-all cannot page past; that is an error, not a truncation. start bounds
// the first page (nil = unbounded), and the page size comes from options' limit.
func (c *Client) findAllKeyset(ctx context.Context, class, key string, descending bool, start *int64, query, options map[string]any) ([]map[string]any, error) {
	pageSize := intField(options, "limit")
	if pageSize <= 0 {
		pageSize = 200
	}
	opts := make(map[string]any, len(options)+1)
	for k, v := range options {
		opts[k] = v
	}
	order := 1
	if descending {
		order = -1
	}
	opts["sort"] = map[string]any{key: order}

	bounded := start != nil
	var cursor int64
	if start != nil {
		cursor = *start
	}
	read := map[string]bool{}
	var out []map[string]any
	for page := 0; ; page++ {
		if page >= maxKeysetPages {
			return nil, fmt.Errorf("huly: find-all %s: more than %d pages; the %s window did not drain", class, maxKeysetPages, key)
		}
		q := make(map[string]any, len(query)+1)
		for k, v := range query {
			q[k] = v
		}
		if bounded {
			op := "$gte"
			if descending {
				op = "$lte"
			}
			q[key] = map[string]any{op: cursor}
		}
		batch, err := c.findAll(ctx, class, q, opts)
		if err != nil {
			return nil, err
		}
		fresh := 0
		for _, doc := range batch {
			id := stringField(doc, "_id")
			if read[id] {
				continue
			}
			read[id] = true
			out = append(out, doc)
			fresh++
		}
		if len(batch) < pageSize {
			return out, nil
		}
		if fresh == 0 {
			return nil, fmt.Errorf("huly: find-all %s: a full page of %d documents shares %s %d; find-all has no offset to page past them", class, pageSize, key, cursor)
		}
		cursor = edgeKey(batch, key, descending)
		bounded = true
	}
}

// edgeKey returns the value a keyset walk advances to: the largest key in the
// page for an ascending walk, the smallest for a descending one.
func edgeKey(docs []map[string]any, key string, descending bool) int64 {
	var edge int64
	first := true
	for _, doc := range docs {
		v := int64(intField(doc, key))
		if first || (!descending && v > edge) || (descending && v < edge) {
			edge = v
			first = false
		}
	}
	return edge
}

// restoreQueryFields puts back the scalar fields Huly's REST find-all omits
// from the documents it returns. The server uses the query as an EXCLUSION
// projection as well as a filter: a document fetched with
// `{"identifier": "NAVI"}` comes back without `identifier`, and one fetched
// with `{"_id": id}` comes back without `_id`, so a caller that needs the
// matched field would read an empty string — which is how the create path lost
// the project identifier and the label path lost the issue id. A document
// matched the query, so the queried value is true of it. A nested value
// (`{"$in": [...]}`) is left alone: the server does not strip it, and the
// matching element is not known client-side.
func restoreQueryFields(docs []map[string]any, query map[string]any) {
	if len(query) == 0 {
		return
	}
	for _, doc := range docs {
		for key, want := range query {
			if _, nested := want.(map[string]any); nested {
				continue
			}
			if _, present := doc[key]; !present {
				doc[key] = want
			}
		}
	}
}

// tx applies one transaction. Huly's own client posts a single transaction
// object here, not an array.
func (c *Client) tx(ctx context.Context, transaction map[string]any) error {
	_, err := c.txRaw(ctx, transaction)
	return err
}

// txRaw applies a transaction and returns the server's answer, which a
// `retrieve` transaction uses to hand back the updated document.
func (c *Client) txRaw(ctx context.Context, transaction map[string]any) ([]byte, error) {
	s, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	target := fmt.Sprintf("%s/api/v1/tx/%s", strings.TrimRight(s.endpoint, "/"), url.PathEscape(s.workspace))
	return c.postRaw(ctx, target, s.token, transaction)
}

// rpc calls the accounts service: a `{method, params}` body answering
// `{result}` or `{error}`.
func (c *Client) rpc(ctx context.Context, target, token string, body map[string]any, out any) error {
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := c.postJSON(ctx, target, token, body, &envelope); err != nil {
		return err
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return fmt.Errorf("huly: %s", clip(envelope.Error))
	}
	if out != nil && len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, target, token string, out any) error {
	body, err := c.getRaw(ctx, target, token)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) getRaw(ctx context.Context, target, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("huly: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req)
}

func (c *Client) postJSON(ctx context.Context, target, token string, in, out any) error {
	body, err := c.postRaw(ctx, target, token, in)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) postRaw(ctx context.Context, target, token string, in any) ([]byte, error) {
	blob, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("huly: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(blob))
	if err != nil {
		return nil, fmt.Errorf("huly: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req)
}

// do performs a request and returns its body, mapping a refusal and a transport
// failure to a clean error. A 401/403 drops the cached session, so a revoked or
// expired workspace token recovers on the next call.
func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("huly: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		c.forget()
		return nil, fmt.Errorf("huly: %s %s: %s (credential refused)", req.Method, req.URL.Path, resp.Status)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("huly: %s %s: %s: %s", req.Method, req.URL.Path, resp.Status, clip(body))
	}
	return body, nil
}

// idCounter makes an id unique within a process even when two are generated in
// the same second.
var idCounter uint32

// newID builds a Huly document id: 24 hex characters — the unix second, ten
// random hex digits, and a per-process counter, which is the shape Huly's own
// client generates.
func newID() string {
	secs := uint32(time.Now().Unix())
	return fmt.Sprintf("%08x%06x%04x%06x",
		secs, randHex(1<<24), randHex(1<<16), atomic.AddUint32(&idCounter, 1)&0xFFFFFF)
}

// randHex returns a random value below n as hex.
func randHex(n int64) int64 {
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0
	}
	return v.Int64()
}

// clip bounds an error detail so a server's response cannot flood a transcript.
func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
