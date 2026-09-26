// Package tool is the public tool contract (ADR 0004: contract
// packages are public and dependency-light; ADR 0010: tools are
// agent-callable actions that run inside a thought). Stdlib-only: it
// must never import store/sqlite, llm/* adapters, internal/*, or navi
// — an extension author imports THIS package, not the runtime.
//
// Def mirrors tools/registry's ToolDefinition FIELD-FOR-FIELD (the
// M4 catalog port must be mechanical): Name, Description, InputSchema,
// OutputFormat, Access, Timeout, Recovery, Mode, Tags, Version,
// PromptSnippet, PromptGuidelines, SourceInfo{Origin, Path}.
// DELIBERATE OMISSION — prepareArguments: ToolDefinition carries an
// unexported argument-migration hook set via the
// WithPrepareArguments registration Option; it is a registration
// concern, not descriptor ABI. Def leaves it out (the public
// descriptor is the 13 fields below); the M4 port expresses argument
// preparation through the registry's registration option, keeping the
// contract ABI free of an implementation migration detail.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolAccess is the access level a tool call requires (S130000 §01).
// The zero value is "any" for filters; definitions must set a real level.
type ToolAccess int

const (
	// AccessAny matches any access level in filters (zero value).
	AccessAny ToolAccess = iota
	// AccessReadOnly tools observe state without mutating it.
	AccessReadOnly
	// AccessWrite tools mutate state.
	AccessWrite
	// AccessExecute tools run commands or reach outside the process.
	AccessExecute
)

// String returns the spec-facing access name.
func (a ToolAccess) String() string {
	switch a {
	case AccessReadOnly:
		return "read-only"
	case AccessWrite:
		return "write"
	case AccessExecute:
		return "execute"
	default:
		return "any"
	}
}

// ToolMode is the execution capability a tool declares: whether its
// calls may be batched concurrently or must run one at a time.
type ToolMode int

const (
	// ModeAny matches any mode in filters (zero value).
	ModeAny ToolMode = iota
	// ToolModeParallel tools may run concurrently with other parallel tools.
	ToolModeParallel
	// ToolModeSequential tools must run one at a time, in order.
	ToolModeSequential
)

// String returns the spec-facing mode name.
func (m ToolMode) String() string {
	switch m {
	case ToolModeParallel:
		return "parallel"
	case ToolModeSequential:
		return "sequential"
	default:
		return "any"
	}
}

// ToolOrigin records who provided a tool (S130400 §01 provenance).
type ToolOrigin int

const (
	// OriginAny matches any origin in filters (zero value).
	OriginAny ToolOrigin = iota
	// OriginBuiltin tools are compiled into the binary as part of the catalog.
	OriginBuiltin
	// OriginSDK tools ship with the public SDK.
	OriginSDK
	// OriginAuthor tools are registered by a navi author.
	OriginAuthor
	// OriginExtension tools arrive via the extension contract (S000600).
	OriginExtension
)

// String returns the spec-facing origin name.
func (o ToolOrigin) String() string {
	switch o {
	case OriginBuiltin:
		return "builtin"
	case OriginSDK:
		return "sdk"
	case OriginAuthor:
		return "author"
	case OriginExtension:
		return "extension"
	default:
		return "any"
	}
}

// SourceInfo records a tool's provenance (S130400 §01): where the
// tool came from plus optional location metadata for audit and
// filtering.
type SourceInfo struct {
	// Origin is the provenance class; required on every definition.
	Origin ToolOrigin
	// Path is an optional source location (file path, extension id, ...).
	Path string
}

// PrepareArguments migrates older argument shapes to the current
// schema before validation (S130400 §06). The contract types the hook;
// the M4 port attaches it by registration option (see the package
// note on the Def omission).
type PrepareArguments func(args map[string]any) (map[string]any, error)

// Schema is the JSON Schema tool-call arguments are validated against
// before dispatch (S130400 §06) — a pragmatic stdlib-encoded subset,
// constructed via ParseSchema (mirrors tools/registry.Schema).
type Schema struct {
	raw json.RawMessage
}

// ParseSchema validates raw as a JSON Schema object and wraps it. It
// must decode to a JSON object (typically `{"type": "object", ...}`).
func ParseSchema(raw json.RawMessage) (Schema, error) {
	if len(raw) == 0 {
		return Schema{}, fmt.Errorf("schema is empty")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Schema{}, fmt.Errorf("schema is not a JSON object: %w", err)
	}
	return Schema{raw: append(json.RawMessage(nil), raw...)}, nil
}

// Raw returns the schema's underlying JSON document.
func (s Schema) Raw() json.RawMessage {
	return append(json.RawMessage(nil), s.raw...)
}

// Handler executes a tool call. Args arrive pre-validated against the
// definition's InputSchema as a decoded JSON object.
type Handler func(ctx context.Context, args map[string]any) (string, error)

// Def is the complete self-describing metadata descriptor for one
// tool — the field-for-field mirror of the runtime ToolDefinition's
// public fields (see the package note for prepareArguments).
type Def struct {
	// Name is unique per binary and follows the namespace_action
	// pattern (`file_read`, `github_issue_read`).
	Name string
	// Description states, in LLM-readable terms, what the tool does.
	Description string
	// InputSchema is the JSON Schema tool-call arguments are
	// validated against before dispatch.
	InputSchema Schema
	// OutputFormat is the rendering hint for results: one of
	// "text", "json", "diff", "markdown".
	OutputFormat string
	// Access is the access level enforcement is based on.
	Access ToolAccess
	// Timeout is the per-call timeout applied by the runtime.
	Timeout time.Duration
	// Recovery is the recovery strategy id applied when a call fails
	// or times out.
	Recovery string
	// Mode declares parallel or sequential execution capability.
	Mode ToolMode
	// Tags are discovery keywords beyond the name.
	Tags []string
	// Version is the tool schema/behavior version for author and
	// extension tools.
	Version string
	// PromptSnippet is the one-line LLM-facing entry for the
	// "available tools" prompt section.
	PromptSnippet string
	// PromptGuidelines are tool-specific bullets included in the
	// system prompt only while the tool is active.
	PromptGuidelines []string
	// SourceInfo records provenance (origin is required).
	SourceInfo SourceInfo
}

// Namespace returns the namespace of the tool name: the segment before
// the first underscore (S130400 §04).
func (d Def) Namespace() string {
	name, _, _ := strings.Cut(d.Name, "_")
	return name
}

// Tool pairs a complete descriptor with its handler — the unit a
// catalog or author registers (M4 wires it into the runtime).
type Tool struct {
	Def     Def
	Handler Handler
}
