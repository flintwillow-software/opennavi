// Package object is the navi's typed-object contract (ADR 0017): a portal
// renders the navi through declared types instead of a typed handle per
// concern, so a portal author implements each view kind once per UI
// framework and never writes per-type code.
//
// A Type is a descriptor over a shared struct: the fields a portal may
// render, which of them are sensitive, the relations and named detail
// collections between objects, the actions that may be performed on them,
// and optional presentation hints. The descriptor is the portal-facing ABI;
// the Go struct remains the in-process representation.
//
// A Surface serves objects of every declared type: Types lists the
// descriptors, List/Get/Detail read them with a bounded cursor, and — as the
// contract grows — Act performs declared actions and Watch delivers live
// object deltas. The same Surface is handed to an in-process portal and
// framed over the wire by the gateway.
//
// The package is stdlib-only: an out-of-repo portal or provider can depend on
// the contract without pulling runtime internals (ADR 0004).
package object

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors a Surface reports. Callers test with errors.Is.
var (
	// ErrUnknownType is returned when a type name is not exposed.
	ErrUnknownType = errors.New("object: unknown type")
	// ErrNotFound is returned when an object id does not exist.
	ErrNotFound = errors.New("object: not found")
	// ErrUnknownDetail is returned when a detail collection is not declared.
	ErrUnknownDetail = errors.New("object: unknown detail")
	// ErrUnknownAction is returned when an action is not declared.
	ErrUnknownAction = errors.New("object: unknown action")
	// ErrInvalidArgs is returned when an action's arguments do not satisfy
	// the action's declared JSON Schema.
	ErrInvalidArgs = errors.New("object: invalid action arguments")
	// ErrUnsupported is returned by a Surface method the implementation does
	// not serve yet.
	ErrUnsupported = errors.New("object: unsupported")
)

// Kind is a field's value kind. It tells a renderer what widget and
// formatting to use; it is not a Go type.
type Kind string

const (
	KindString   Kind = "string"
	KindText     Kind = "text" // multi-line, rendered verbatim
	KindInt      Kind = "int"
	KindFloat    Kind = "float"
	KindBool     Kind = "bool"
	KindTime     Kind = "time"
	KindDuration Kind = "duration"
	KindEnum     Kind = "enum"
	KindRef      Kind = "ref"  // id of another object
	KindList     Kind = "list" // list of ItemKind
	KindJSON     Kind = "json" // opaque structured value
)

// Field describes one renderable field of a type.
type Field struct {
	// Name is the stable wire name, independent of the Go field name.
	Name string `json:"name"`
	// Label is the human label a renderer shows.
	Label string `json:"label,omitempty"`
	// Kind is the value kind.
	Kind Kind `json:"kind"`
	// Section groups fields in a detail view (empty = the default section).
	Section string `json:"section,omitempty"`
	// Order sorts fields within their section; ties keep declaration order.
	Order int `json:"order,omitempty"`
	// ReadOnly marks a field a portal must not write.
	ReadOnly bool `json:"readOnly,omitempty"`
	// Sensitive marks a field the surface omits unless the caller is granted
	// it. Nothing sensitive is exposed by default (ADR 0006/0008).
	Sensitive bool `json:"sensitive,omitempty"`
	// Optional marks a field that may be absent.
	Optional bool `json:"optional,omitempty"`
	// Enum lists the allowed values when Kind is KindEnum.
	Enum []string `json:"enum,omitempty"`
	// RefType names the referenced type when Kind is KindRef.
	RefType string `json:"refType,omitempty"`
	// ItemKind is the element kind when Kind is KindList.
	ItemKind Kind `json:"itemKind,omitempty"`
}

// RelationKind describes a relation's cardinality from the owning type.
type RelationKind string

const (
	// RelationOne is a single reference to another object.
	RelationOne RelationKind = "one"
	// RelationMany is a collection of references.
	RelationMany RelationKind = "many"
)

// Relation is a declared edge between two types (a thought's depends_on, a
// plan's stages). A renderer may show it as a list or a graph.
type Relation struct {
	// Name is the stable wire name.
	Name string `json:"name"`
	// Label is the human label.
	Label string `json:"label,omitempty"`
	// Target names the related type.
	Target string `json:"target"`
	// Kind is the cardinality from the owning type.
	Kind RelationKind `json:"kind"`
}

// Detail is a named sub-collection of another type, read through
// Surface.Detail. A thought's transcript entries are a detail collection.
type Detail struct {
	// Name is the stable wire name, used in the Detail route.
	Name string `json:"name"`
	// Label is the human label.
	Label string `json:"label,omitempty"`
	// Type names the type of the detail's objects.
	Type string `json:"type"`
	// Key is the field on the detail objects that references the parent
	// object's id.
	Key string `json:"key,omitempty"`
}

// Action is an operation a portal may perform on an object of this type.
// Control is not a separate capability surface: a portal renders the
// actions it is granted instead of hardcoding verbs.
type Action struct {
	// Name is the stable wire name (steer, pause, retry, ...).
	Name string `json:"name"`
	// Label is the human label.
	Label string `json:"label,omitempty"`
	// Args is a JSON Schema for the action's arguments (tool.ParseSchema
	// shape); nil means no arguments.
	Args json.RawMessage `json:"args,omitempty"`
	// Confirm asks a renderer to confirm before invoking.
	Confirm bool `json:"confirm,omitempty"`
	// ReadOnly marks an action that does not mutate state (a query).
	ReadOnly bool `json:"readOnly,omitempty"`
}

// ViewKind is a presentation hint: which generic view renders a type.
type ViewKind string

const (
	// ViewList renders objects as a table or list.
	ViewList ViewKind = "list"
	// ViewDetail renders one object's fields in sections.
	ViewDetail ViewKind = "detail"
	// ViewChat renders a detail collection as a conversation.
	ViewChat ViewKind = "chat"
	// ViewGraph renders a relation as a graph or tree.
	ViewGraph ViewKind = "graph"
	// ViewBoard renders objects grouped by a field.
	ViewBoard ViewKind = "board"
)

// Section is a named group of fields in a detail view.
type Section struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
}

// View is one presentation hint. Hints are data — they serialize over the
// gateway and never call back into Go — and they are optional: a type with
// no hints renders as a list of its non-sensitive fields and a detail view
// grouped by section.
type View struct {
	// Kind selects the generic view.
	Kind ViewKind `json:"kind"`
	// Name is the stable wire name.
	Name string `json:"name,omitempty"`
	// Label is the human label.
	Label string `json:"label,omitempty"`
	// Columns lists the field names a list/board view shows, in order.
	Columns []string `json:"columns,omitempty"`
	// GroupBy names the field a board/list view groups by.
	GroupBy string `json:"groupBy,omitempty"`
	// Detail names the detail collection a chat/graph view renders.
	Detail string `json:"detail,omitempty"`
	// Sections orders the sections of a detail view.
	Sections []Section `json:"sections,omitempty"`
}

// Type is the descriptor for one object kind. It is declared at the navi
// level, like a tool or a model, so what a portal can see is visible in
// main.go.
type Type struct {
	// Name is the stable wire name (thought, todo, note, memory).
	Name string `json:"name"`
	// Label is the human label.
	Label string `json:"label,omitempty"`
	// ID names the field that carries the object's identity.
	ID string `json:"id"`
	// Fields are the renderable fields.
	Fields []Field `json:"fields,omitempty"`
	// Relations are the declared edges to other types.
	Relations []Relation `json:"relations,omitempty"`
	// Details are the named sub-collections read through Surface.Detail.
	Details []Detail `json:"details,omitempty"`
	// Actions are the operations a portal may perform.
	Actions []Action `json:"actions,omitempty"`
	// Views are optional presentation hints.
	Views []View `json:"views,omitempty"`
	// Scope, when set, is the type's ROW-level scoping rule (ADR 0025 decision
	// 3): which objects of the type a principal may see. Nil means the type is
	// not row-scoped.
	Scope *Scope `json:"scope,omitempty"`
}

// Scope is a type's row-level scoping rule (ADR 0025 decision 3, ADR 0028).
// It is closed and referential — it names a field the type already declares —
// and the one rule is "that field equals the acting principal's reference",
// so there is no expression language (ADR 0017).
//
// A scope NARROWS: the surface ANDs it with the caller's query, so a caller may
// narrow further but can never widen, and the provider applies the result. A
// type with no scope is not row-scoped — every row the read gate admits is
// visible.
type Scope struct {
	// Field is the declared field compared to the acting principal. Its value
	// is the principal's reference ("user:alice"), so a type exposes the field
	// it is scoped on.
	Field string `json:"field"`
}

// Field returns the named field descriptor.
func (t Type) Field(name string) (Field, bool) {
	for _, f := range t.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Detail returns the named detail descriptor.
func (t Type) Detail(name string) (Detail, bool) {
	for _, d := range t.Details {
		if d.Name == name {
			return d, true
		}
	}
	return Detail{}, false
}

// Action returns the named action descriptor.
func (t Type) Action(name string) (Action, bool) {
	for _, a := range t.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

// Validate checks the descriptor's shape: a name and identity field, unique
// field/detail/action names, and references that resolve within the type.
// A type is rejected at registration rather than producing a broken portal
// at render time.
func (t Type) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("%w: type requires a name", ErrUnknownType)
	}
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("object: type %q requires an identity field", t.Name)
	}
	if _, ok := t.Field(t.ID); !ok {
		return fmt.Errorf("object: type %q: identity field %q is not declared", t.Name, t.ID)
	}
	fields := map[string]bool{}
	for i, f := range t.Fields {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("object: type %q: field %d requires a name", t.Name, i)
		}
		if fields[f.Name] {
			return fmt.Errorf("object: type %q: duplicate field %q", t.Name, f.Name)
		}
		fields[f.Name] = true
		if f.Kind == "" {
			return fmt.Errorf("object: type %q: field %q requires a kind", t.Name, f.Name)
		}
		if f.Kind == KindRef && strings.TrimSpace(f.RefType) == "" {
			return fmt.Errorf("object: type %q: ref field %q requires a ref type", t.Name, f.Name)
		}
	}
	if t.Scope != nil {
		if strings.TrimSpace(t.Scope.Field) == "" {
			return fmt.Errorf("object: type %q: scope needs a field", t.Name)
		}
		if !fields[t.Scope.Field] {
			return fmt.Errorf("object: type %q: scope field %q is not a declared field", t.Name, t.Scope.Field)
		}
	}
	details := map[string]bool{}
	for i, d := range t.Details {
		if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Type) == "" {
			return fmt.Errorf("object: type %q: detail %d requires a name and type", t.Name, i)
		}
		if details[d.Name] {
			return fmt.Errorf("object: type %q: duplicate detail %q", t.Name, d.Name)
		}
		details[d.Name] = true
	}
	actions := map[string]bool{}
	for i, a := range t.Actions {
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("object: type %q: action %d requires a name", t.Name, i)
		}
		if actions[a.Name] {
			return fmt.Errorf("object: type %q: duplicate action %q", t.Name, a.Name)
		}
		actions[a.Name] = true
	}
	for i, v := range t.Views {
		if v.Kind == "" {
			return fmt.Errorf("object: type %q: view %d requires a kind", t.Name, i)
		}
		for _, c := range v.Columns {
			if !fields[c] {
				return fmt.Errorf("object: type %q: view %q column %q is not a declared field", t.Name, v.Kind, c)
			}
		}
		if v.Detail != "" && !details[v.Detail] {
			return fmt.Errorf("object: type %q: view %q names undeclared detail %q", t.Name, v.Kind, v.Detail)
		}
		if v.GroupBy != "" && !fields[v.GroupBy] {
			return fmt.Errorf("object: type %q: view %q groups by undeclared field %q", t.Name, v.Kind, v.GroupBy)
		}
	}
	return nil
}

// Object is one instance of a type: its identity, a monotonic version for
// delta ordering, and the granted field values. Fields the caller may not
// read are absent, not null.
type Object struct {
	Type    string         `json:"type"`
	ID      string         `json:"id"`
	Version int64          `json:"version,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// Query selects objects. Filter is a declared per-type filter (not an
// expression language); Cursor is opaque and returned by the previous page;
// Limit caps the page size (0 = the backend default).
type Query struct {
	Filter map[string]any
	Cursor string
	Limit  int
}

// Op is a live-delta operation.
type Op string

const (
	// OpCreate is a new object.
	OpCreate Op = "create"
	// OpUpdate is a changed object (Fields carries the changed fields).
	OpUpdate Op = "update"
	// OpDelete is a removed object.
	OpDelete Op = "delete"
	// OpAppend appends to a detail collection (Detail names it).
	OpAppend Op = "append"
)

// Delta is one live object change on the surface.
type Delta struct {
	Type   string         `json:"type"`
	ID     string         `json:"id"`
	Op     Op             `json:"op"`
	Fields map[string]any `json:"fields,omitempty"`
	Detail string         `json:"detail,omitempty"`
	At     time.Time      `json:"at"`
}

// Provider serves one type's objects. A navi registers a provider with its
// type descriptor; the built-in types are served by providers the navi
// builds over its own views. A provider reads through the view its
// component was bound to (ADR 0017 decision 8).
type Provider interface {
	// List returns a bounded page of objects and the next cursor ("" when
	// the page is the last).
	List(q Query) ([]Object, string, error)
	// Get returns one object.
	Get(id string) (Object, error)
	// Detail returns a bounded page of a named detail collection's objects.
	Detail(id, detail string, q Query) ([]Object, string, error)
}

// Surface serves objects of every exposed type. It is what a portal consumes:
// the in-process portal and the gateway's remote binding are two bindings of
// the same contract (ADR 0012 decision 4).
//
// Act and Watch are the write and live paths (ADR 0017 decisions 4 and 6): a
// Surface authorizes and validates a declared action before performing it,
// and reports live changes as deltas so a portal keeps objects current
// without knowing queue or agent event kinds. A Surface that does not serve
// them returns ErrUnsupported.
type Surface interface {
	// Types returns every exposed type descriptor. It fails when the
	// descriptors cannot be read (a stream can fail mid-flight; answering
	// "no types" would be a silent fail-open — ADR 0019 decision 8).
	Types() ([]Type, error)
	// List returns a bounded page of one type's objects.
	List(typeName string, q Query) ([]Object, string, error)
	// Get returns one object of one type.
	Get(typeName, id string) (Object, error)
	// Detail returns a bounded page of a named detail collection.
	Detail(typeName, id, detail string, q Query) ([]Object, string, error)
	// Act performs a declared action on one object.
	Act(typeName, id, action string, args map[string]any) error
	// Watch returns the deltas after a cursor, the next cursor, and whether
	// deltas before the cursor were dropped. It fails when the deltas cannot
	// be read, so a live path never mistakes a broken stream for "no
	// changes" (ADR 0019 decision 8).
	Watch(after int) ([]Delta, int, bool, error)
}
