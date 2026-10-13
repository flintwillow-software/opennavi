package huly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/tool"
)

// This file is the rich-text description egress tools (NAVI-418). An issue's
// `description` field is NOT the text: it is a REFERENCE
// (`<issueId>-description-<timestamp>`) to a document held by Huly's
// **collaborator** service, a separate RPC at the instance's COLLABORATOR_URL.
// The transactor stores the reference; the collaborator stores the ProseMirror
// document the reference points at.
//
// So reading a description is two steps — read the issue for its reference,
// then ask the collaborator for the document — and writing one is three: upload
// the document (createContent on the first write, updateContent after), then
// set the reference on the issue with an update transaction.
//
// The collaborator speaks `POST {COLLABORATOR_URL}/rpc/{documentId}` with a
// `{method, payload}` body, where documentId is
// `<workspace>|<objectClass>|<objectId>|<objectAttr>`. The reference read/write
// pair is `getContent` / `createContent` / `updateContent`, all keyed on the
// object attribute (`description`).
//
// # Fidelity
//
// The stored document is ProseMirror JSON, which can carry rich formatting.
// This connector reads the plain text out of it and writes plain text back —
// one paragraph per non-empty line — so a description round-trips as text, not
// as formatting. A richer markdown conversion is a separate, larger piece.

const (
	// ToolIssueDescription reads an issue's description as text.
	ToolIssueDescription = "huly_issue_description"
	// ToolIssueDescriptionSet replaces an issue's description.
	ToolIssueDescriptionSet = "huly_issue_description_set"
)

// descriptionAttr is the issue attribute the collaborator document is keyed on.
const descriptionAttr = "description"

// ── tool definitions ───────────────────────────────────────────────────

func issueDescriptionDef() tool.Def {
	return tool.Def{
		Name: ToolIssueDescription,
		Description: "Reads a Huly issue's description as plain text, addressed by identifier (NAVI-408) or id. " +
			"Huly keeps the rich text on a separate service; this returns its text content. Read-only.",
		InputSchema:   parseSchema(issueDescriptionSchema),
		OutputFormat:  "json",
		Access:        tool.AccessReadOnly,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"huly", "egress", "issue", "description"},
		Version:       "1.0.0",
		PromptSnippet: "huly_issue_description: read a Huly issue's description as text",
		PromptGuidelines: []string{
			"Use this when the issue body matters; huly_issue_list returns titles, not descriptions.",
			"Formatting is not preserved — the result is the text content of the stored document.",
		},
	}
}

func issueDescriptionSetDef() tool.Def {
	return tool.Def{
		Name: ToolIssueDescriptionSet,
		Description: "Replaces a Huly issue's description, addressed by identifier (NAVI-408) or id. The text is " +
			"stored as plain-text paragraphs (one per non-empty line); existing formatting is replaced. " +
			"The credential is never an argument.",
		InputSchema:   parseSchema(issueDescriptionSetSchema),
		OutputFormat:  "json",
		Access:        tool.AccessExecute,
		Timeout:       20 * time.Second,
		Recovery:      "capture_response",
		Mode:          tool.ToolModeSequential,
		Tags:          []string{"huly", "egress", "issue", "description"},
		Version:       "1.0.0",
		PromptSnippet: "huly_issue_description_set: replace a Huly issue's description",
		PromptGuidelines: []string{
			"The description is plain text: each non-empty line becomes a paragraph, and any existing formatting is replaced.",
			"Prefer a comment when the detail is a running discussion; use the description for the issue's settled statement.",
			"Never pass a token or credential as an argument — the connector resolves it itself.",
		},
	}
}

const issueDescriptionSchema = `{
	"type": "object",
	"properties": {
		"issue": {"type": "string", "description": "The issue identifier (NAVI-408) or its raw id."}
	},
	"required": ["issue"],
	"additionalProperties": false
}`

const issueDescriptionSetSchema = `{
	"type": "object",
	"properties": {
		"issue": {"type": "string", "description": "The issue identifier (NAVI-408) or its raw id."},
		"text": {"type": "string", "description": "The new description; each non-empty line becomes a paragraph."}
	},
	"required": ["issue", "text"],
	"additionalProperties": false
}`

// ── handlers ───────────────────────────────────────────────────────────

// issueDescription reads the issue's description as text.
func (c *Client) issueDescription(ctx context.Context, args map[string]any) (string, error) {
	issue, err := c.issueFromArgs(ctx, args)
	if err != nil {
		return "", err
	}
	text, err := c.readDescription(ctx, issue)
	if err != nil {
		return "", err
	}
	return encodeResult(map[string]any{
		"ok": true, "issue": stringField(issue, "identifier"),
		"description": text, "empty": text == "",
	})
}

// issueDescriptionSet replaces the issue's description with the given text.
func (c *Client) issueDescriptionSet(ctx context.Context, args map[string]any) (string, error) {
	issue, err := c.issueFromArgs(ctx, args)
	if err != nil {
		return "", err
	}
	text := asString(args["text"])
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("huly: text is required")
	}
	if err := c.writeDescription(ctx, issue, text); err != nil {
		return "", err
	}
	return encodeResult(map[string]any{
		"ok": true, "issue": stringField(issue, "identifier"),
		"description": text,
	})
}

// issueFromArgs resolves the required issue argument.
func (c *Client) issueFromArgs(ctx context.Context, args map[string]any) (map[string]any, error) {
	ref := strings.TrimSpace(stringArg(args, "issue"))
	if ref == "" {
		return nil, fmt.Errorf("huly: issue is required")
	}
	return c.findIssue(ctx, ref)
}

// readDescription fetches the collaborator document the issue's description
// reference points at and returns its text. An issue with no description has no
// reference, so there is nothing to fetch.
func (c *Client) readDescription(ctx context.Context, issue map[string]any) (string, error) {
	source := stringField(issue, descriptionAttr)
	if source == "" {
		return "", nil
	}
	s, err := c.session(ctx)
	if err != nil {
		return "", err
	}
	markup, err := c.collaboratorContent(ctx, s, classIssue, stringField(issue, "_id"), descriptionAttr,
		"getContent", map[string]any{"source": source})
	if err != nil {
		return "", err
	}
	return proseMirrorText(markup), nil
}

// writeDescription uploads the text to the collaborator and, on the first
// write, points the issue's description reference at it. A later write updates
// the existing document, so the reference does not change.
func (c *Client) writeDescription(ctx context.Context, issue map[string]any, text string) error {
	s, err := c.session(ctx)
	if err != nil {
		return err
	}
	issueID := stringField(issue, "_id")
	markup := markupParagraphs(text)
	content := map[string]any{"content": map[string]any{descriptionAttr: markup}}

	if stringField(issue, descriptionAttr) != "" {
		_, err := c.collaboratorContent(ctx, s, classIssue, issueID, descriptionAttr, "updateContent", content)
		return err
	}
	ref, err := c.collaboratorContent(ctx, s, classIssue, issueID, descriptionAttr, "createContent", content)
	if err != nil {
		return err
	}
	if ref == "" {
		return fmt.Errorf("huly: the collaborator returned no description reference")
	}
	// The issue's `description` is the reference, set with a plain field map —
	// Huly has no `$set` operator.
	transaction := map[string]any{
		"_id": newID(), "_class": classTxUpdateDoc, "space": spaceTx,
		"objectId": issueID, "objectClass": classIssue,
		"objectSpace": stringField(issue, "space"),
		"modifiedOn":  time.Now().UnixMilli(), "modifiedBy": s.socialID,
		"operations": map[string]any{descriptionAttr: ref}, "retrieve": false,
	}
	return c.tx(ctx, transaction)
}

// collaboratorContent calls the collaborator service for one issue attribute.
// documentId is `<workspace>|<objectClass>|<objectId>|<objectAttr>`, URL-escaped
// into the route, and the body is `{method, payload}`. The answer carries the
// attribute's content (a ProseMirror document on a read, the new reference on
// createContent).
func (c *Client) collaboratorContent(ctx context.Context, s *session, objectClass, objectID, objectAttr, method string, payload map[string]any) (string, error) {
	if strings.TrimSpace(s.collaborator) == "" {
		return "", fmt.Errorf("huly: the instance declares no COLLABORATOR_URL, so descriptions are unavailable")
	}
	documentID := strings.Join([]string{s.workspace, objectClass, objectID, objectAttr}, "|")
	target := fmt.Sprintf("%s/rpc/%s", strings.TrimRight(s.collaborator, "/"), url.PathEscape(documentID))

	var envelope struct {
		Content map[string]string `json:"content"`
		Error   json.RawMessage   `json:"error"`
	}
	if err := c.postJSON(ctx, target, s.token, map[string]any{"method": method, "payload": payload}, &envelope); err != nil {
		return "", err
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		return "", fmt.Errorf("huly: collaborator %s: %s", method, clip(envelope.Error))
	}
	return envelope.Content[objectAttr], nil
}

// ── plain text <-> ProseMirror ─────────────────────────────────────────

// markupParagraphs renders plain text as a ProseMirror document with one
// paragraph per non-empty line. Huly stores a description's rich text as this
// JSON, so a plain-text write produces a document Huly renders as paragraphs.
func markupParagraphs(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	content := []any{}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		content = append(content, map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": line, "marks": []any{}},
			},
		})
	}
	b, _ := json.Marshal(map[string]any{"type": "doc", "content": content})
	return string(b)
}

// proseMirrorText extracts the plain text of a ProseMirror document: the text
// nodes in order, with a newline after each leaf block. A value that is not a
// ProseMirror document is returned as-is, so a version difference degrades to
// showing the raw content rather than nothing.
func proseMirrorText(markup string) string {
	var doc any
	if err := json.Unmarshal([]byte(markup), &doc); err != nil {
		return strings.TrimSpace(markup)
	}
	return strings.TrimSpace(proseMirrorTextOf(doc))
}

func proseMirrorTextOf(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if text, ok := t["text"].(string); ok {
			return text
		}
		var b strings.Builder
		if children, ok := t["content"].([]any); ok {
			for _, child := range children {
				b.WriteString(proseMirrorTextOf(child))
			}
		}
		if isBlockNode(stringField(t, "type")) {
			b.WriteByte('\n')
		}
		return b.String()
	case []any:
		var b strings.Builder
		for _, child := range t {
			b.WriteString(proseMirrorTextOf(child))
		}
		return b.String()
	}
	return ""
}

// isBlockNode reports whether a ProseMirror node ends a line of text. Only the
// leaf blocks that hold text count: a container such as listItem or blockquote
// wraps a paragraph, which already ends the line, so counting the container too
// would double the newline.
func isBlockNode(typ string) bool {
	switch typ {
	case "paragraph", "heading", "codeBlock":
		return true
	}
	return false
}
