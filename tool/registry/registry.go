// Package registry implements the public tool registry: the
// single, in-binary record of every tool available to an agent. Each
// tool — catalog, author, or extension — registers a self-describing
// ToolDefinition plus its handler; the registry stores both and exposes
// discovery queries (get, list, find, active set) used by the agent
// runtime to build the LLM-facing tool list and to enforce execution
// metadata.
//
// The registry is purely local metadata and dispatch validation: it is
// not a sharing or marketplace component.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ToolAccess is the access level a tool call requires.
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

// String returns the canonical access name.
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

// String returns the canonical mode name.
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

// ToolOrigin records who provided a tool (provenance).
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
	// OriginExtension tools arrive via the extension interface.
	OriginExtension
)

// String returns the canonical origin name.
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

// SourceInfo records a tool's provenance: where the tool
// came from plus optional location metadata for audit and filtering.
type SourceInfo struct {
	// Origin is the provenance class; required on every definition.
	Origin ToolOrigin
	// Path is an optional source location (file path, extension id, ...).
	Path string
}

// ToolDefinition is the complete, self-describing metadata descriptor
// every tool declares. Registration IS the metadata
// declaration: the descriptor is the single source of truth for the
// LLM-facing tool list, system-prompt assembly, and execution
// enforcement.
type ToolDefinition struct {
	// Name is unique per binary and follows the namespace_action
	// pattern (`file_read`, `github_issue_read`); the namespace is
	// the segment before the first underscore.
	Name string
	// Description states, in LLM-readable terms, what the tool does
	// and its subject domain.
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
	// Recovery is the recovery strategy id from applied when
	// a call fails or times out.
	Recovery string
	// Mode declares parallel or sequential execution capability.
	Mode ToolMode
	// Tags are discovery keywords beyond the name; ALL must match in
	// a Find filter.
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

	// prepareArguments migrates older argument shapes to the current
	// schema before validation; set via WithPrepareArguments.
	prepareArguments PrepareArguments
}

// Option adjusts a registration. Options are applied to the stored
// descriptor, never to the caller's value.
type Option func(*registration)

// WithPrepareArguments attaches an argument-preparation hook that runs
// before schema validation on every dispatch — used to
// migrate arguments from older schema versions (e.g. resumed sessions).
func WithPrepareArguments(prepare PrepareArguments) Option {
	return func(reg *registration) { reg.def.prepareArguments = prepare }
}

// Namespace returns the namespace of the tool name: the segment before
// the first underscore. Derived, never set directly.
func (d ToolDefinition) Namespace() string {
	name, _, _ := strings.Cut(d.Name, "_")
	return name
}

// Handler executes a tool call. Args arrive pre-validated against the
// definition's InputSchema as a decoded JSON object.
type Handler func(ctx context.Context, args map[string]any) (string, error)

// PrepareArguments migrates older argument shapes to the current schema
// before validation — e.g. arguments stored by an old
// session. Declared per tool; the public schema stays strict and
// compatibility lives in the hook.
type PrepareArguments func(args map[string]any) (map[string]any, error)

// registration pairs the descriptor with its implementation.
type registration struct {
	def     ToolDefinition
	handler Handler
}

// Registry is the in-binary tool registry. It is safe
// for concurrent use. The zero value is not usable; construct with New.
type Registry struct {
	mu            sync.RWMutex
	byName        map[string]*registration
	order         []string // registration order
	active        map[string]bool
	activeOrder   []string
	schemaChecker *schemaChecker
}

// New creates an empty registry.
func New() *Registry {
	return &Registry{
		byName:        make(map[string]*registration),
		active:        make(map[string]bool),
		schemaChecker: newSchemaChecker(),
	}
}

// ErrDuplicateTool is returned when a registration reuses an existing
// tool name; the original registration remains intact.
var ErrDuplicateTool = errors.New("tool already registered")

// Register stores a tool's metadata descriptor and binds its handler
// The descriptor is validated for completeness first;
// an incomplete definition is rejected with an error naming the missing
// fields. Duplicate names are rejected and the original stays intact.
func (r *Registry) Register(def ToolDefinition, handler Handler, opts ...Option) error {
	if handler == nil {
		return fmt.Errorf("register %q: handler is required", def.Name)
	}
	if err := validateDefinition(def); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[def.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, def.Name)
	}
	stored := &registration{def: def, handler: handler}
	for _, opt := range opts {
		opt(stored)
	}
	r.byName[def.Name] = stored
	r.order = append(r.order, def.Name)
	return nil
}

// validateDefinition enforces the complete metadata descriptor
// requirement ("Minimal descriptor incompleteness").
func validateDefinition(def ToolDefinition) error {
	var missing []string
	if def.Name == "" {
		missing = append(missing, "name")
	} else if !validToolName(def.Name) {
		return fmt.Errorf("register %q: name must follow the namespace_action pattern (first segment is the namespace)", def.Name)
	}
	if def.Description == "" {
		missing = append(missing, "description")
	}
	if def.InputSchema.raw == nil {
		missing = append(missing, "inputSchema")
	}
	if def.OutputFormat == "" {
		missing = append(missing, "outputFormat")
	} else if !validOutputFormat(def.OutputFormat) {
		return fmt.Errorf("register %q: outputFormat must be one of text, json, diff, markdown", def.Name)
	}
	if def.Access == AccessAny {
		missing = append(missing, "access")
	}
	if def.Timeout <= 0 {
		missing = append(missing, "timeout")
	}
	if def.Recovery == "" {
		missing = append(missing, "recovery")
	}
	if def.Mode == ModeAny {
		missing = append(missing, "mode")
	}
	if def.PromptSnippet == "" {
		missing = append(missing, "promptSnippet")
	}
	if def.SourceInfo.Origin == OriginAny {
		missing = append(missing, "sourceInfo.origin")
	}
	if len(missing) > 0 {
		return fmt.Errorf("register %q: incomplete metadata descriptor, missing: %s", def.Name, strings.Join(missing, ", "))
	}
	return nil
}

// validToolName enforces the namespace_action pattern:
// at least one underscore; namespace and action are non-empty segments.
func validToolName(name string) bool {
	ns, action, ok := strings.Cut(name, "_")
	return ok && ns != "" && action != "" && !strings.ContainsAny(name, " \t\n")
}

func validOutputFormat(f string) bool {
	switch f {
	case "text", "json", "diff", "markdown":
		return true
	}
	return false
}

// Get returns the definition registered under name.
func (r *Registry) Get(name string) (ToolDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byName[name]
	if !ok {
		return ToolDefinition{}, false
	}
	return reg.def, true
}

// List returns all registered tools in registration order.
func (r *Registry) List() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name].def)
	}
	return out
}

// AllNames returns the names of all registered tools in registration
// order — useful for LLM-facing tool lists.
func (r *Registry) AllNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// FindFilter selects tools by namespace, access, mode, tags, and/or
// origin. Zero-value enum fields match anything; all
// Tags must match. MaxAccess additionally caps the access level
// returned ("Discovery Result Consistency"): an agent with
// read-only permission sets MaxAccess to AccessReadOnly and never sees
// write/execute tools.
type FindFilter struct {
	Namespace string
	Access    ToolAccess // zero value = any
	Mode      ToolMode   // zero value = any
	Tags      []string   // ALL must match
	Origin    ToolOrigin // zero value = any
	MaxAccess ToolAccess // zero value = unbounded
}

// Find returns tools matching the filter, in registration order
func (r *Registry) Find(filter FindFilter) []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolDefinition
	for _, name := range r.order {
		def := r.byName[name].def
		if filter.matches(def) {
			out = append(out, def)
		}
	}
	return out
}

func (f FindFilter) matches(def ToolDefinition) bool {
	if f.Namespace != "" && def.Namespace() != f.Namespace {
		return false
	}
	if f.Access != AccessAny && def.Access != f.Access {
		return false
	}
	if f.Mode != ModeAny && def.Mode != f.Mode {
		return false
	}
	if f.Origin != OriginAny && def.SourceInfo.Origin != f.Origin {
		return false
	}
	if f.MaxAccess != AccessAny && def.Access > f.MaxAccess {
		return false
	}
	if len(f.Tags) > 0 {
		have := make(map[string]bool, len(def.Tags))
		for _, t := range def.Tags {
			have[t] = true
		}
		for _, t := range f.Tags {
			if !have[t] {
				return false
			}
		}
	}
	return true
}

// Active returns the current active set — the tools offered to the LLM
// — in registration order.
func (r *Registry) Active() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolDefinition
	for _, name := range r.order {
		if r.active[name] {
			out = append(out, r.byName[name].def)
		}
	}
	return out
}

// ActiveNames returns the active set's names in registration order.
func (r *Registry) ActiveNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, name := range r.order {
		if r.active[name] {
			out = append(out, name)
		}
	}
	return out
}

// SetActive adjusts the active set at runtime without re-registration
// Names are sorted internally so membership, not call
// order, determines the result. Unknown names are rejected: the active
// set may only reference registered tools.
func (r *Registry) SetActive(names []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		if _, ok := r.byName[name]; !ok {
			return fmt.Errorf("set active: unknown tool %q", name)
		}
	}
	r.active = make(map[string]bool, len(names))
	for _, name := range names {
		r.active[name] = true
	}
	sort.Strings(names) // deterministic; membership is what matters
	r.activeOrder = append([]string(nil), names...)
	return nil
}

// Dispatch validates args against the named tool's input schema
// (running its PrepareArguments hook first, when declared) and only
// then invokes the handler. Inactive tools are not
// dispatchable through the registry — dispatch requires active
// membership, matching the LLM-facing surface.
func (r *Registry) Dispatch(ctx context.Context, name string, rawArgs json.RawMessage) (string, error) {
	r.mu.RLock()
	reg, ok := r.byName[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("dispatch %q: unknown tool", name)
	}
	if !r.isActive(name) {
		return "", fmt.Errorf("dispatch %q: tool is not active", name)
	}

	args := map[string]any{}
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("dispatch %q: arguments are not a JSON object: %w", name, err)
		}
	}
	if reg.def.prepareArguments != nil {
		migrated, err := reg.def.prepareArguments(args)
		if err != nil {
			return "", fmt.Errorf("dispatch %q: argument preparation failed: %w", name, err)
		}
		args = migrated
	}
	if err := r.schemaChecker.Validate(reg.def.InputSchema, args); err != nil {
		return "", fmt.Errorf("dispatch %q: invalid arguments: %w", name, err)
	}
	return reg.handler(ctx, args)
}

func (r *Registry) isActive(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active[name]
}
