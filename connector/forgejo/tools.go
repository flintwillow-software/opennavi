package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the egress half of the connector (ADR 0020): the tools a navi
// calls to read and act in Forgejo. Each is an ordinary tool.Tool built with
// connector.SendTool, so it is added explicitly by the navi author and is absent
// from a binary whose author did not add it (ADR 0006).
//
// Forgejo's API is Gitea-compatible: issues and pull requests are addressed by
// their repository `index` (the `#N` in the web UI), a pull request is also an
// issue (so comments are shared), and labels are assigned by id rather than by
// name — which is why a label name is resolved against the repository's label
// list before a write.

// ── wire shapes ────────────────────────────────────────────────────────

type apiUser struct {
	Login string `json:"login"`
}

type apiLabel struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type apiIssue struct {
	ID        int64      `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	HTMLURL   string     `json:"html_url"`
	User      apiUser    `json:"user"`
	Labels    []apiLabel `json:"labels"`
	Assignees []apiUser  `json:"assignees"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type apiRef struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

type apiPull struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	User      apiUser   `json:"user"`
	Head      apiRef    `json:"head"`
	Base      apiRef    `json:"base"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type apiRepo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Empty         bool   `json:"empty"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Stars         int    `json:"stars_count"`
	Forks         int    `json:"forks_count"`
	OpenIssues    int    `json:"open_issues_count"`
	OpenPulls     int    `json:"open_pr_counter"`
}

// ── tool definitions ───────────────────────────────────────────────────

func issueListDef() tool.Def {
	return tool.Def{
		Name: ToolIssueList,
		Description: "Lists Forgejo issues in a repository, newest first, with their state, labels, and " +
			"assignees. Pull requests are excluded (use forgejo_pull_list). Read-only; the credential is " +
			"never an argument.",
		InputSchema:  parseSchema(issueListSchema),
		OutputFormat: "json",
		Access:       tool.AccessReadOnly,
		Timeout:      20 * time.Second,
		Recovery:     "retry_once",
		Mode:         tool.ToolModeParallel,
		Tags:         []string{"forgejo", "egress", "issue"},
		Version:      "1.0.0",
		PromptSnippet: "forgejo_issue_list: list Forgejo issues (state, labels, assignees) " +
			"with optional filters",
		PromptGuidelines: []string{
			"Prefer a state or label filter over fetching everything.",
			"The result is bounded (limit, default 20, max 50); use page to continue.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueGetDef() tool.Def {
	return tool.Def{
		Name:          ToolIssueGet,
		Description:   "Reads one Forgejo issue (or pull request) by its repository index (#N), with its body, state, labels, and assignees. Read-only.",
		InputSchema:   parseSchema(issueGetSchema),
		OutputFormat:  "json",
		Access:        tool.AccessReadOnly,
		Timeout:       20 * time.Second,
		Recovery:      "retry_once",
		Mode:          tool.ToolModeParallel,
		Tags:          []string{"forgejo", "egress", "issue"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_issue_get: read one Forgejo issue or pull request by #N",
		PromptGuidelines: []string{
			"An issue and a pull request share one index space; a pull request's index also works here.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueCreateDef() tool.Def {
	return tool.Def{
		Name: ToolIssueCreate,
		Description: "Opens a Forgejo issue in a repository and returns its index and URL. Labels may be " +
			"names (resolved against the repository's labels) or ids. The credential is never an argument.",
		InputSchema:  parseSchema(issueCreateSchema),
		OutputFormat: "json",
		// Egress reaches outside the process, so it is execute-level.
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"forgejo", "egress", "issue"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_issue_create: open a Forgejo issue",
		PromptGuidelines: []string{
			"Write the title and body as you would file the issue by hand.",
			"Labels are names or ids; a name the repository does not have is refused, not dropped.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueUpdateDef() tool.Def {
	return tool.Def{
		Name: ToolIssueUpdate,
		Description: "Edits a Forgejo issue's state, title, body, labels, or assignees by index (#N). " +
			"Only the fields given are changed. The credential is never an argument.",
		InputSchema:   parseSchema(issueUpdateSchema),
		OutputFormat:  "json",
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"forgejo", "egress", "issue"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_issue_update: change a Forgejo issue's state, title, body, labels, or assignees",
		PromptGuidelines: []string{
			"Give a state as \"open\" or \"closed\"; closing an issue is how it is marked done.",
			"Labels replace the issue's whole label set when given; omit the field to leave labels alone.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func issueCommentDef() tool.Def {
	return tool.Def{
		Name:          ToolIssueComment,
		Description:   "Posts a comment on a Forgejo issue or pull request by index (#N) and returns the created comment's id and URL. The credential is never an argument.",
		InputSchema:   parseSchema(issueCommentSchema),
		OutputFormat:  "json",
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"forgejo", "egress", "issue", "comment"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_issue_comment: post a comment on a Forgejo issue or pull request",
		PromptGuidelines: []string{
			"Write the comment as you would post it by hand — it is visible to everyone with repository access.",
			"Post once. A retry posts a second comment, so recovery is not automatic.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func pullListDef() tool.Def {
	return tool.Def{
		Name:          ToolPullList,
		Description:   "Lists Forgejo pull requests in a repository, newest first, with their source and target branches and state. Read-only.",
		InputSchema:   parseSchema(pullListSchema),
		OutputFormat:  "json",
		Access:        tool.AccessReadOnly,
		Timeout:       20 * time.Second,
		Recovery:      "retry_once",
		Mode:          tool.ToolModeParallel,
		Tags:          []string{"forgejo", "egress", "pull"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_pull_list: list Forgejo pull requests",
		PromptGuidelines: []string{
			"The result is bounded (limit, default 20, max 50); use page to continue.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func pullCreateDef() tool.Def {
	return tool.Def{
		Name:          ToolPullCreate,
		Description:   "Opens a Forgejo pull request from a source branch into a target branch and returns its index and URL. The credential is never an argument.",
		InputSchema:   parseSchema(pullCreateSchema),
		OutputFormat:  "json",
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"forgejo", "egress", "pull"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_pull_create: open a Forgejo pull request",
		PromptGuidelines: []string{
			"head is the source branch; for a fork, spell it owner:branch.",
			"base is the branch the change should merge into.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func repoGetDef() tool.Def {
	return tool.Def{
		Name:          ToolRepoGet,
		Description:   "Reads one Forgejo repository's metadata: full name, description, visibility, default branch, and open issue and pull counts. Read-only.",
		InputSchema:   parseSchema(repoGetSchema),
		OutputFormat:  "json",
		Access:        tool.AccessReadOnly,
		Timeout:       20 * time.Second,
		Recovery:      "retry_once",
		Mode:          tool.ToolModeParallel,
		Tags:          []string{"forgejo", "egress", "repository"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_repo_get: read a Forgejo repository's metadata",
		PromptGuidelines: []string{
			"Use this to learn the default branch before proposing a change.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

func labelListDef() tool.Def {
	return tool.Def{
		Name:          ToolLabelList,
		Description:   "Lists a Forgejo repository's labels with their ids and colours, so a label name can be resolved before a write. Read-only.",
		InputSchema:   parseSchema(labelListSchema),
		OutputFormat:  "json",
		Access:        tool.AccessReadOnly,
		Timeout:       20 * time.Second,
		Recovery:      "retry_once",
		Mode:          tool.ToolModeParallel,
		Tags:          []string{"forgejo", "egress", "label"},
		Version:       "1.0.0",
		PromptSnippet: "forgejo_label_list: list a Forgejo repository's labels and their ids",
		PromptGuidelines: []string{
			"Labels are assigned by id in Forgejo; this lists the ids a name resolves to.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

// ── schemas ────────────────────────────────────────────────────────────

const repoProp = `"repository": {"type": "string", "description": "owner/name; defaults to the configured repository."}`

const issueListSchema = `{
	"type": "object",
	"properties": {
		` + repoProp + `,
		"state": {"type": "string", "enum": ["open", "closed", "all"], "description": "Issue state filter (default open)."},
		"labels": {"type": "string", "description": "Comma-separated label names to filter by."},
		"assignee": {"type": "string", "description": "Filter by assignee login."},
		"query": {"type": "string", "description": "Free-text search over title and body."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum issues per page (default 20)."},
		"page": {"type": "integer", "minimum": 1, "description": "Page number (default 1)."}
	},
	"additionalProperties": false
}`

const issueGetSchema = `{
	"type": "object",
	"required": ["index"],
	"properties": {
		` + repoProp + `,
		"index": {"type": "integer", "minimum": 1, "description": "Issue or pull request index (#N)."}
	},
	"additionalProperties": false
}`

const issueCreateSchema = `{
	"type": "object",
	"required": ["title"],
	"properties": {
		` + repoProp + `,
		"title": {"type": "string", "minLength": 1, "description": "Issue title."},
		"body": {"type": "string", "description": "Issue body, in Markdown."},
		"labels": {"type": "array", "items": {"type": "string"}, "description": "Label names or ids."},
		"assignees": {"type": "array", "items": {"type": "string"}, "description": "Assignee logins."}
	},
	"additionalProperties": false
}`

const issueUpdateSchema = `{
	"type": "object",
	"required": ["index"],
	"properties": {
		` + repoProp + `,
		"index": {"type": "integer", "minimum": 1, "description": "Issue index (#N)."},
		"state": {"type": "string", "enum": ["open", "closed"], "description": "New state."},
		"title": {"type": "string", "minLength": 1, "description": "New title."},
		"body": {"type": "string", "description": "New body, in Markdown."},
		"labels": {"type": "array", "items": {"type": "string"}, "description": "Replacement label set (names or ids)."},
		"assignees": {"type": "array", "items": {"type": "string"}, "description": "Replacement assignee logins."}
	},
	"additionalProperties": false
}`

const issueCommentSchema = `{
	"type": "object",
	"required": ["index", "body"],
	"properties": {
		` + repoProp + `,
		"index": {"type": "integer", "minimum": 1, "description": "Issue or pull request index (#N)."},
		"body": {"type": "string", "minLength": 1, "description": "Comment body, in Markdown."}
	},
	"additionalProperties": false
}`

const pullListSchema = `{
	"type": "object",
	"properties": {
		` + repoProp + `,
		"state": {"type": "string", "enum": ["open", "closed", "all"], "description": "Pull request state filter (default open)."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum pull requests per page (default 20)."},
		"page": {"type": "integer", "minimum": 1, "description": "Page number (default 1)."}
	},
	"additionalProperties": false
}`

const pullCreateSchema = `{
	"type": "object",
	"required": ["title", "head", "base"],
	"properties": {
		` + repoProp + `,
		"title": {"type": "string", "minLength": 1, "description": "Pull request title."},
		"head": {"type": "string", "minLength": 1, "description": "Source branch (owner:branch for a fork)."},
		"base": {"type": "string", "minLength": 1, "description": "Target branch."},
		"body": {"type": "string", "description": "Pull request body, in Markdown."}
	},
	"additionalProperties": false
}`

const repoGetSchema = `{
	"type": "object",
	"properties": {
		` + repoProp + `
	},
	"additionalProperties": false
}`

const labelListSchema = `{
	"type": "object",
	"properties": {
		` + repoProp + `,
		"limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum labels per page (default 50)."},
		"page": {"type": "integer", "minimum": 1, "description": "Page number (default 1)."}
	},
	"additionalProperties": false
}`

// parseSchema parses a compiled-in schema literal. A failure is a programming
// error: SendTool refuses an empty schema anyway, and failing at composition
// time is the point.
func parseSchema(raw string) tool.Schema {
	s, err := tool.ParseSchema(json.RawMessage(strings.TrimSpace(raw)))
	if err != nil {
		panic(fmt.Sprintf("forgejo: tool schema: %v", err))
	}
	return s
}

// ── egress handlers ────────────────────────────────────────────────────

// issueList reads issues and projects them to a readable row.
func (c *client) issueList(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	state, err := stateArg(args, "open", true)
	if err != nil {
		return "", err
	}
	limit, err := limitArg(args, 20, 50)
	if err != nil {
		return "", err
	}
	page, err := optionalInt(args, "page", 1)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	// type=issues excludes pull requests, which share the issues endpoint.
	q.Set("type", "issues")
	q.Set("state", state)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("page", strconv.Itoa(page))
	if v := strings.TrimSpace(stringArg(args, "labels")); v != "" {
		q.Set("labels", v)
	}
	if v := strings.TrimSpace(stringArg(args, "assignee")); v != "" {
		q.Set("assignee", v)
	}
	if v := strings.TrimSpace(stringArg(args, "query")); v != "" {
		q.Set("q", v)
	}

	var issues []apiIssue
	total, err := c.getJSON(ctx, fmt.Sprintf("/repos/%s/issues?%s", repo, q.Encode()), &issues)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(issues))
	for _, is := range issues {
		rows = append(rows, issueRow(is))
	}
	return encode(map[string]any{"issues": rows, "count": len(rows), "total": total})
}

// issueGet reads one issue or pull request by index.
func (c *client) issueGet(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	index, err := intArg(args, "index")
	if err != nil {
		return "", err
	}
	var is apiIssue
	if _, err := c.getJSON(ctx, fmt.Sprintf("/repos/%s/issues/%d", repo, index), &is); err != nil {
		return "", err
	}
	return encode(map[string]any{"issue": issueRow(is), "body": is.Body})
}

// issueCreate opens an issue.
func (c *client) issueCreate(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	if title == "" {
		return "", fmt.Errorf("forgejo: title is required")
	}
	labels, _, err := stringSlice(args, "labels")
	if err != nil {
		return "", err
	}
	ids, err := c.resolveLabels(ctx, repo, labels)
	if err != nil {
		return "", err
	}
	assignees, _, err := stringSlice(args, "assignees")
	if err != nil {
		return "", err
	}

	body := map[string]any{"title": title}
	if v := strings.TrimSpace(stringArg(args, "body")); v != "" {
		body["body"] = v
	}
	if len(assignees) > 0 {
		body["assignees"] = assignees
	}
	if len(ids) > 0 {
		body["labels"] = ids
	}
	blob, _, err := c.do(ctx, "POST", fmt.Sprintf("/repos/%s/issues", repo), body)
	if err != nil {
		return "", err
	}
	var created apiIssue
	if err := json.Unmarshal(blob, &created); err != nil {
		return "", fmt.Errorf("forgejo: decode created issue: %w", err)
	}
	return encode(map[string]any{
		"ok": true, "index": created.Number, "id": created.ID, "url": created.HTMLURL,
	})
}

// issueUpdate edits an issue's state, title, body, labels, or assignees. Only
// the fields given are touched.
func (c *client) issueUpdate(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	index, err := intArg(args, "index")
	if err != nil {
		return "", err
	}
	body := map[string]any{}
	var changed []string

	if _, ok := args["state"]; ok {
		state, err := stateArg(args, "", false)
		if err != nil {
			return "", err
		}
		body["state"] = state
		changed = append(changed, "state")
	}
	if _, ok := args["title"]; ok {
		title := strings.TrimSpace(stringArg(args, "title"))
		if title == "" {
			return "", fmt.Errorf("forgejo: title cannot be empty")
		}
		body["title"] = title
		changed = append(changed, "title")
	}
	if _, ok := args["body"]; ok {
		body["body"] = stringArg(args, "body")
		changed = append(changed, "body")
	}
	if labels, present, err := stringSlice(args, "labels"); err != nil {
		return "", err
	} else if present {
		ids, err := c.resolveLabels(ctx, repo, labels)
		if err != nil {
			return "", err
		}
		if ids == nil {
			ids = []int64{}
		}
		body["labels"] = ids
		changed = append(changed, "labels")
	}
	if assignees, present, err := stringSlice(args, "assignees"); err != nil {
		return "", err
	} else if present {
		if assignees == nil {
			assignees = []string{}
		}
		body["assignees"] = assignees
		changed = append(changed, "assignees")
	}
	if len(body) == 0 {
		return "", fmt.Errorf("forgejo: nothing to update (give a state, title, body, labels, or assignees)")
	}

	blob, _, err := c.do(ctx, "PATCH", fmt.Sprintf("/repos/%s/issues/%d", repo, index), body)
	if err != nil {
		return "", err
	}
	var updated apiIssue
	if err := json.Unmarshal(blob, &updated); err != nil {
		return "", fmt.Errorf("forgejo: decode updated issue: %w", err)
	}
	return encode(map[string]any{
		"ok": true, "index": updated.Number, "state": updated.State, "updated": changed,
	})
}

// issueComment posts a comment on an issue or pull request.
func (c *client) issueComment(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	index, err := intArg(args, "index")
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(stringArg(args, "body"))
	if body == "" {
		return "", fmt.Errorf("forgejo: body is required")
	}
	blob, _, err := c.do(ctx, "POST",
		fmt.Sprintf("/repos/%s/issues/%d/comments", repo, index),
		map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	var created struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.Unmarshal(blob, &created)
	out := map[string]any{"ok": true, "commented": fmt.Sprintf("%s#%d", repo, index)}
	if created.ID != 0 {
		out["id"] = created.ID
	}
	if created.HTMLURL != "" {
		out["url"] = created.HTMLURL
	}
	return encode(out)
}

// pullList lists pull requests.
func (c *client) pullList(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	state, err := stateArg(args, "open", true)
	if err != nil {
		return "", err
	}
	limit, err := limitArg(args, 20, 50)
	if err != nil {
		return "", err
	}
	page, err := optionalInt(args, "page", 1)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("state", state)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("page", strconv.Itoa(page))

	var pulls []apiPull
	total, err := c.getJSON(ctx, fmt.Sprintf("/repos/%s/pulls?%s", repo, q.Encode()), &pulls)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(pulls))
	for _, pr := range pulls {
		rows = append(rows, map[string]any{
			"index":      pr.Number,
			"id":         pr.ID,
			"title":      pr.Title,
			"state":      pr.State,
			"author":     pr.User.Login,
			"head":       pr.Head.Label,
			"base":       pr.Base.Label,
			"url":        pr.HTMLURL,
			"created_at": pr.CreatedAt,
			"updated_at": pr.UpdatedAt,
		})
	}
	return encode(map[string]any{"pulls": rows, "count": len(rows), "total": total})
}

// pullCreate opens a pull request.
func (c *client) pullCreate(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	head := strings.TrimSpace(stringArg(args, "head"))
	base := strings.TrimSpace(stringArg(args, "base"))
	if title == "" || head == "" || base == "" {
		return "", fmt.Errorf("forgejo: title, head, and base are required")
	}
	body := map[string]any{"title": title, "head": head, "base": base}
	if v := strings.TrimSpace(stringArg(args, "body")); v != "" {
		body["body"] = v
	}
	blob, _, err := c.do(ctx, "POST", fmt.Sprintf("/repos/%s/pulls", repo), body)
	if err != nil {
		return "", err
	}
	var created apiPull
	if err := json.Unmarshal(blob, &created); err != nil {
		return "", fmt.Errorf("forgejo: decode created pull request: %w", err)
	}
	return encode(map[string]any{
		"ok": true, "index": created.Number, "id": created.ID, "url": created.HTMLURL,
	})
}

// repoGet reads one repository's metadata.
func (c *client) repoGet(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	var r apiRepo
	if _, err := c.getJSON(ctx, fmt.Sprintf("/repos/%s", repo), &r); err != nil {
		return "", err
	}
	return encode(map[string]any{
		"repository":     r.FullName,
		"description":    r.Description,
		"private":        r.Private,
		"fork":           r.Fork,
		"empty":          r.Empty,
		"default_branch": r.DefaultBranch,
		"url":            r.HTMLURL,
		"stars":          r.Stars,
		"forks":          r.Forks,
		"open_issues":    r.OpenIssues,
		"open_pulls":     r.OpenPulls,
	})
}

// labelList lists a repository's labels.
func (c *client) labelList(ctx context.Context, args map[string]any) (string, error) {
	repo, err := c.repositoryFor(args)
	if err != nil {
		return "", err
	}
	limit, err := limitArg(args, 50, 50)
	if err != nil {
		return "", err
	}
	page, err := optionalInt(args, "page", 1)
	if err != nil {
		return "", err
	}
	var labels []apiLabel
	total, err := c.getJSON(ctx,
		fmt.Sprintf("/repos/%s/labels?limit=%d&page=%d", repo, limit, page), &labels)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(labels))
	for _, l := range labels {
		rows = append(rows, map[string]any{"id": l.ID, "name": l.Name, "color": l.Color})
	}
	return encode(map[string]any{"labels": rows, "count": len(rows), "total": total})
}

// ── helpers ────────────────────────────────────────────────────────────

// issueRow projects an issue to the readable shape the tools return.
func issueRow(is apiIssue) map[string]any {
	labels := make([]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		labels = append(labels, l.Name)
	}
	assignees := make([]string, 0, len(is.Assignees))
	for _, a := range is.Assignees {
		assignees = append(assignees, a.Login)
	}
	return map[string]any{
		"index":      is.Number,
		"id":         is.ID,
		"title":      is.Title,
		"state":      is.State,
		"author":     is.User.Login,
		"labels":     labels,
		"assignees":  assignees,
		"url":        is.HTMLURL,
		"created_at": is.CreatedAt,
		"updated_at": is.UpdatedAt,
	}
}

// stateArg validates a state filter. update=true allows "all" (a list filter);
// a write allows only open/closed, since "all" is not a state.
func stateArg(args map[string]any, def string, list bool) (string, error) {
	raw, ok := args["state"]
	if !ok {
		return def, nil
	}
	state := strings.ToLower(strings.TrimSpace(asString(raw)))
	if state == "" {
		return "", fmt.Errorf("forgejo: state cannot be empty")
	}
	switch state {
	case "open", "closed":
		return state, nil
	case "all":
		if list {
			return state, nil
		}
		return "", fmt.Errorf("forgejo: state must be open or closed")
	default:
		return "", fmt.Errorf("forgejo: unknown state %q (want open, closed, or all)", state)
	}
}

// resolveLabels turns label names (or numeric ids) into the ids a Forgejo write
// requires. A name the repository does not have is refused rather than dropped,
// so a call cannot silently lose a label.
func (c *client) resolveLabels(ctx context.Context, repo string, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}
	byName := map[string]int64{}
	for page := 1; page <= maxLabelPages; page++ {
		var labels []apiLabel
		total, err := c.getJSON(ctx,
			fmt.Sprintf("/repos/%s/labels?limit=50&page=%d", repo, page), &labels)
		if err != nil {
			return nil, err
		}
		for _, l := range labels {
			byName[strings.ToLower(l.Name)] = l.ID
		}
		if len(labels) < 50 || (total > 0 && page*50 >= total) {
			break
		}
	}
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		if id, err := strconv.ParseInt(name, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
			continue
		}
		id, ok := byName[strings.ToLower(name)]
		if !ok {
			return nil, fmt.Errorf("forgejo: no label named %q in %s", name, repo)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// asString renders an argument as a string (JSON numbers arrive as float64).
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return fmt.Sprintf("%g", t)
	default:
		return ""
	}
}

// encode marshals a tool result, mapping an encode failure to a clean error.
func encode(v any) (string, error) {
	blob, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("forgejo: encode result: %w", err)
	}
	return string(blob), nil
}
