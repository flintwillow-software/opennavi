// Package view turns the typed-object surface into framework-agnostic view
// models (ADR 0017 decision 7): a portal author implements each view kind
// once per UI framework instead of writing per-type code, and a new type is
// visible to every portal the moment the navi registers it.
//
// Nothing here renders. A view model is data — resolved columns, labelled
// sections, formatted cells, conversation turns, action descriptors — so a
// terminal UI, a web UI, and a test all consume the same shape, and the
// descriptor is the only thing that decides what appears. Presentation hints
// are optional: a type with no hints renders as a list of its non-sensitive
// fields and a detail view grouped by section.
//
// The package depends only on the object contract.
package view

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flintwillow-software/opennavi/object"
)

// ── list ────────────────────────────────────────────────────────────────

// Column is one list column: a declared field, its label, and its kind.
type Column struct {
	Field string
	Label string
	Kind  object.Kind
}

// Cell is one object's value in a column: the display string plus the raw
// value, so a renderer that wants a different presentation (an age instead of
// a timestamp, a coloured enum) does not have to re-read the object.
type Cell struct {
	Field string
	Value string
	Raw   any
}

// Row is one object in a list view.
type Row struct {
	ID    string
	Cells []Cell
}

// List is one type's objects as a table.
type List struct {
	Type    object.Type
	Kind    object.ViewKind
	Columns []Column
	Rows    []Row
	// Next is the surface's cursor for the page after this one ("" at the
	// end), so a renderer can page without knowing the type.
	Next string
}

// ReadList reads one bounded page and resolves it against the type.
func ReadList(s object.Surface, typeName string, q object.Query) (List, error) {
	t, err := typeOf(s, typeName)
	if err != nil {
		return List{}, err
	}
	objs, next, err := s.List(typeName, q)
	if err != nil {
		return List{}, err
	}
	return ListOf(t, objs, next), nil
}

// ListOf resolves already-read objects into a list view.
func ListOf(t object.Type, objs []object.Object, next string) List {
	cols := Columns(t)
	return List{
		Type:    t,
		Kind:    Primary(t),
		Columns: cols,
		Rows:    Rows(t, objs, cols),
		Next:    next,
	}
}

// Columns resolves the columns for a type: the list view's declared columns
// when it has a hint, otherwise every non-sensitive field in declaration
// order.
func Columns(t object.Type) []Column {
	if v, ok := Hint(t, object.ViewList); ok && len(v.Columns) > 0 {
		out := make([]Column, 0, len(v.Columns))
		for _, name := range v.Columns {
			if f, ok := t.Field(name); ok {
				out = append(out, Column{Field: f.Name, Label: label(f), Kind: f.Kind})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	out := make([]Column, 0, len(t.Fields))
	for _, f := range t.Fields {
		if f.Sensitive {
			continue
		}
		out = append(out, Column{Field: f.Name, Label: label(f), Kind: f.Kind})
	}
	return out
}

// Rows projects objects onto the columns. A field the object does not carry
// (the surface omits sensitive and absent fields) renders as an empty cell
// rather than an error: a list must render a partial object.
func Rows(t object.Type, objs []object.Object, cols []Column) []Row {
	out := make([]Row, 0, len(objs))
	for _, obj := range objs {
		row := Row{ID: obj.ID, Cells: make([]Cell, 0, len(cols))}
		for _, c := range cols {
			raw, ok := obj.Fields[c.Field]
			if !ok {
				// The identity is always known even when a provider does not
				// duplicate it into the field map.
				if c.Field == t.ID {
					row.Cells = append(row.Cells, Cell{Field: c.Field, Value: obj.ID, Raw: obj.ID})
					continue
				}
				row.Cells = append(row.Cells, Cell{Field: c.Field})
				continue
			}
			row.Cells = append(row.Cells, Cell{Field: c.Field, Value: Format(c.Kind, raw), Raw: raw})
		}
		out = append(out, row)
	}
	return out
}

// ── detail ──────────────────────────────────────────────────────────────

// Field is one labelled value in a detail section.
type Field struct {
	Name     string
	Label    string
	Kind     object.Kind
	Value    string
	Raw      any
	ReadOnly bool
}

// Section groups detail fields. Sections come from the type's detail hint when
// it declares them, otherwise from the fields' own Section names in first-seen
// order.
type Section struct {
	Name   string
	Label  string
	Fields []Field
}

// Action is a declared action a renderer may dispatch through Surface.Act.
type Action struct {
	Name    string
	Label   string
	Confirm bool
	Args    json.RawMessage
}

// Detail is one object rendered as labelled sections plus the actions it
// grants.
type Detail struct {
	Type     object.Type
	Object   object.Object
	Title    string
	Sections []Section
	Actions  []Action
}

// ReadDetail reads one object and resolves it against the type.
func ReadDetail(s object.Surface, typeName, id string) (Detail, error) {
	t, err := typeOf(s, typeName)
	if err != nil {
		return Detail{}, err
	}
	obj, err := s.Get(typeName, id)
	if err != nil {
		return Detail{}, err
	}
	return DetailOf(t, obj), nil
}

// DetailOf resolves an already-read object into a detail view.
func DetailOf(t object.Type, obj object.Object) Detail {
	return Detail{
		Type:     t,
		Object:   obj,
		Title:    Title(t, obj),
		Sections: Sections(t, obj),
		Actions:  Actions(t),
	}
}

// Sections resolves an object's fields into ordered sections, skipping
// sensitive fields and fields the object does not carry.
func Sections(t object.Type, obj object.Object) []Section {
	order, labels := sectionOrder(t)
	var names []string
	seen := map[string]bool{}
	for _, f := range t.Fields {
		if f.Sensitive {
			continue
		}
		if _, ok := obj.Fields[f.Name]; !ok {
			continue
		}
		if !seen[f.Section] {
			seen[f.Section] = true
			names = append(names, f.Section)
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		oi, iok := order[names[i]]
		oj, jok := order[names[j]]
		if iok != jok {
			return iok // a declared section sorts before an undeclared one
		}
		if iok && oi != oj {
			return oi < oj
		}
		return false // keep first-seen order
	})

	out := make([]Section, 0, len(names))
	for _, name := range names {
		fields := fieldsIn(t, obj, name)
		if len(fields) == 0 {
			continue
		}
		out = append(out, Section{Name: name, Label: sectionLabel(name, labels), Fields: fields})
	}
	return out
}

// fieldsIn returns one section's fields, ordered by the declared Order (ties
// keep declaration order).
func fieldsIn(t object.Type, obj object.Object, section string) []Field {
	type ordered struct {
		f   object.Field
		pos int
	}
	var picked []ordered
	for i, f := range t.Fields {
		if f.Sensitive || f.Section != section {
			continue
		}
		if _, ok := obj.Fields[f.Name]; !ok {
			continue
		}
		picked = append(picked, ordered{f: f, pos: i})
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].f.Order < picked[j].f.Order })
	out := make([]Field, 0, len(picked))
	for _, p := range picked {
		raw := obj.Fields[p.f.Name]
		out = append(out, Field{
			Name:     p.f.Name,
			Label:    label(p.f),
			Kind:     p.f.Kind,
			Value:    Format(p.f.Kind, raw),
			Raw:      raw,
			ReadOnly: p.f.ReadOnly,
		})
	}
	return out
}

// Actions lists the actions a type declares, in declaration order.
func Actions(t object.Type) []Action {
	out := make([]Action, 0, len(t.Actions))
	for _, a := range t.Actions {
		label := a.Label
		if label == "" {
			label = prettify(a.Name)
		}
		out = append(out, Action{Name: a.Name, Label: label, Confirm: a.Confirm, Args: a.Args})
	}
	return out
}

// ── chat ────────────────────────────────────────────────────────────────

// Turn is one conversation turn. A tool call carries Tool+Input; a tool
// result carries Tool+Output.
type Turn struct {
	Role   string
	Text   string
	Tool   string
	Input  string
	Output string
	At     time.Time
}

// Chat is a detail collection rendered as a conversation.
type Chat struct {
	Type   object.Type
	ID     string
	Detail string
	Turns  []Turn
	Next   string
}

// ReadChat reads a bounded page of a detail collection and resolves it as a
// conversation.
func ReadChat(s object.Surface, typeName, id, detail string, q object.Query) (Chat, error) {
	t, err := typeOf(s, typeName)
	if err != nil {
		return Chat{}, err
	}
	entries, next, err := s.Detail(typeName, id, detail, q)
	if err != nil {
		return Chat{}, err
	}
	return ChatOf(t, id, detail, entries, next), nil
}

// ChatOf resolves already-read detail entries into a conversation.
func ChatOf(t object.Type, id, detail string, entries []object.Object, next string) Chat {
	turns := make([]Turn, 0, len(entries))
	for _, e := range entries {
		turns = append(turns, turnOf(e))
	}
	return Chat{Type: t, ID: id, Detail: detail, Turns: turns, Next: next}
}

// turnOf maps one entry object onto a turn: a tool result is an output turn, a
// tool call is an input turn, and everything else is a message turn.
func turnOf(e object.Object) Turn {
	role := str(e, "role")
	message := str(e, "message")
	tool := str(e, "tool_name")
	at, _ := e.Fields["time"].(time.Time)
	switch {
	case role == "tool":
		return Turn{Role: role, Tool: tool, Output: message, At: at}
	case tool != "" && message == "":
		return Turn{Role: role, Tool: tool, Input: str(e, "tool_input"), At: at}
	default:
		return Turn{Role: role, Text: message, Tool: tool, At: at}
	}
}

// ── board ───────────────────────────────────────────────────────────────

// Group is one board column: the group's value and the rows in it.
type Group struct {
	Key   string
	Label string
	Rows  []Row
}

// Board is a list grouped by one field.
type Board struct {
	Type    object.Type
	Columns []Column
	GroupBy string
	Groups  []Group
}

// BoardOf groups objects by the type's board hint (or its first list hint's
// GroupBy), preserving the objects' order within a group and the groups'
// first-seen order.
func BoardOf(t object.Type, objs []object.Object) Board {
	groupBy := ""
	if v, ok := Hint(t, object.ViewBoard); ok {
		groupBy = v.GroupBy
	}
	if groupBy == "" {
		if v, ok := Hint(t, object.ViewList); ok {
			groupBy = v.GroupBy
		}
	}
	cols := Columns(t)
	board := Board{Type: t, Columns: cols, GroupBy: groupBy, Groups: []Group{}}
	if groupBy == "" {
		board.Groups = append(board.Groups, Group{Rows: Rows(t, objs, cols)})
		return board
	}
	index := map[string]int{}
	for _, obj := range objs {
		key := Format(fieldKind(t, groupBy), obj.Fields[groupBy])
		i, ok := index[key]
		if !ok {
			i = len(board.Groups)
			index[key] = i
			board.Groups = append(board.Groups, Group{Key: key, Label: key})
		}
		board.Groups[i].Rows = append(board.Groups[i].Rows, Rows(t, []object.Object{obj}, cols)...)
	}
	return board
}

// ── graph ───────────────────────────────────────────────────────────────

// Node is one object in a graph.
type Node struct {
	Type  string
	ID    string
	Label string
}

// Edge is a declared relation's resolved reference.
type Edge struct {
	From string
	To   string
	Name string
}

// Graph is the objects of one type plus the references their declared
// relations resolve to. A reference to an object outside the read set is still
// an edge (and a node), so a renderer can show what is missing rather than
// silently dropping it.
type Graph struct {
	Type  object.Type
	Nodes []Node
	Edges []Edge
}

// GraphOf builds a graph from the declared relations of one type: each
// relation's same-named field on each object becomes an edge.
func GraphOf(t object.Type, objs []object.Object) Graph {
	g := Graph{Type: t}
	if len(t.Relations) == 0 {
		return g
	}
	known := map[string]bool{}
	for _, obj := range objs {
		if !known[obj.ID] {
			known[obj.ID] = true
			g.Nodes = append(g.Nodes, Node{Type: t.Name, ID: obj.ID, Label: Title(t, obj)})
		}
	}
	for _, obj := range objs {
		for _, rel := range t.Relations {
			for _, ref := range refs(obj.Fields[rel.Name]) {
				if !known[ref] {
					known[ref] = true
					g.Nodes = append(g.Nodes, Node{Type: rel.Target, ID: ref, Label: ref})
				}
				g.Edges = append(g.Edges, Edge{From: obj.ID, To: ref, Name: rel.Name})
			}
		}
	}
	return g
}

// refs reads a reference value: a single id or a list of ids.
func refs(v any) []string {
	switch value := v.(type) {
	case string:
		if value == "" {
			return nil
		}
		return []string{value}
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// ── formatting ──────────────────────────────────────────────────────────

// Format renders a value by its declared kind. It is deliberately plain: a
// renderer decorates, the toolkit reports.
func Format(kind object.Kind, v any) string {
	if v == nil {
		return ""
	}
	switch kind {
	case object.KindTime:
		if t, ok := v.(time.Time); ok {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("2006-01-02 15:04:05Z")
		}
	case object.KindDuration:
		switch d := v.(type) {
		case time.Duration:
			return d.String()
		case float64:
			return time.Duration(d * float64(time.Second)).String()
		}
	case object.KindBool:
		if b, ok := v.(bool); ok {
			return strconv.FormatBool(b)
		}
	case object.KindList:
		if list := listValues(v); list != nil {
			parts := make([]string, 0, len(list))
			for _, item := range list {
				parts = append(parts, Format(object.KindString, item))
			}
			return strings.Join(parts, ", ")
		}
	case object.KindJSON:
		if blob, err := json.Marshal(v); err == nil {
			return string(blob)
		}
	case object.KindInt:
		switch n := v.(type) {
		case int:
			return strconv.Itoa(n)
		case int64:
			return strconv.FormatInt(n, 10)
		case float64:
			return strconv.FormatFloat(n, 'f', -1, 64)
		}
	case object.KindFloat:
		switch n := v.(type) {
		case float64:
			return strconv.FormatFloat(n, 'g', -1, 64)
		case float32:
			return strconv.FormatFloat(float64(n), 'g', -1, 32)
		case int:
			return strconv.Itoa(n)
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// listValues normalizes a list value (native slice or decoded array).
func listValues(v any) []any {
	switch value := v.(type) {
	case []any:
		return value
	case []string:
		out := make([]any, 0, len(value))
		for _, s := range value {
			out = append(out, s)
		}
		return out
	}
	return nil
}

// Title picks a human title for an object: the first declared field among
// title/name/prompt/label that the object carries, else its id.
func Title(t object.Type, obj object.Object) string {
	for _, name := range []string{"title", "name", "prompt", "label"} {
		if s, ok := obj.Fields[name].(string); ok && s != "" {
			return s
		}
	}
	return obj.ID
}

// ── hints ───────────────────────────────────────────────────────────────

// Hint returns a type's declared view of one kind.
func Hint(t object.Type, kind object.ViewKind) (object.View, bool) {
	for _, v := range t.Views {
		if v.Kind == kind {
			return v, true
		}
	}
	return object.View{}, false
}

// Primary returns the view kind a type should open with: its first declared
// hint, else a list.
func Primary(t object.Type) object.ViewKind {
	if len(t.Views) > 0 && t.Views[0].Kind != "" {
		return t.Views[0].Kind
	}
	return object.ViewList
}

// ── helpers ─────────────────────────────────────────────────────────────

// typeOf resolves a type descriptor from the surface, so a caller never has to
// hold the descriptor set itself.
func typeOf(s object.Surface, typeName string) (object.Type, error) {
	types, err := s.Types()
	if err != nil {
		return object.Type{}, err
	}
	for _, t := range types {
		if t.Name == typeName {
			return t, nil
		}
	}
	return object.Type{}, fmt.Errorf("%w: %s", object.ErrUnknownType, typeName)
}

// sectionOrder reads the detail hint's declared section order and labels.
func sectionOrder(t object.Type) (map[string]int, map[string]string) {
	order := map[string]int{}
	labels := map[string]string{}
	v, ok := Hint(t, object.ViewDetail)
	if !ok {
		return order, labels
	}
	for i, s := range v.Sections {
		order[s.Name] = i
		labels[s.Name] = s.Label
	}
	return order, labels
}

func sectionLabel(name string, labels map[string]string) string {
	if l, ok := labels[name]; ok && l != "" {
		return l
	}
	if name == "" {
		return "Detail"
	}
	return prettify(name)
}

// label is a field's declared label, else a prettified name.
func label(f object.Field) string {
	if f.Label != "" {
		return f.Label
	}
	return prettify(f.Name)
}

// prettify turns a wire name into a human label: underscores to spaces, first
// letter capitalized.
func prettify(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func str(o object.Object, name string) string {
	s, _ := o.Fields[name].(string)
	return s
}

// fieldKind reports a field's declared kind (KindString when undeclared).
func fieldKind(t object.Type, name string) object.Kind {
	if f, ok := t.Field(name); ok {
		return f.Kind
	}
	return object.KindString
}
