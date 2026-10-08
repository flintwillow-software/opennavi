package object_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flintwillow-software/opennavi/object"
)

func validType() object.Type {
	return object.Type{
		Name:  "thought",
		Label: "Thought",
		ID:    "id",
		Fields: []object.Field{
			{Name: "id", Kind: object.KindString},
			{Name: "prompt", Kind: object.KindText, Section: "core"},
			{Name: "status", Kind: object.KindEnum, Enum: []string{"pending", "running", "complete"}},
			{Name: "priority", Kind: object.KindInt, Section: "core"},
			{Name: "depends_on", Kind: object.KindList, ItemKind: object.KindRef, RefType: "thought"},
			{Name: "checkpoint", Kind: object.KindJSON, Sensitive: true, ReadOnly: true},
		},
		Relations: []object.Relation{{Name: "depends_on", Target: "thought", Kind: object.RelationMany}},
		Details:   []object.Detail{{Name: "transcript", Label: "Transcript", Type: "entry", Key: "thought_id"}},
		Actions: []object.Action{
			{Name: "steer", Label: "Steer", Args: json.RawMessage(`{"type":"object"}`)},
			{Name: "pause", Label: "Pause"},
		},
		Views: []object.View{
			{Kind: object.ViewList, Columns: []string{"prompt", "status", "priority"}},
			{Kind: object.ViewChat, Detail: "transcript"},
		},
	}
}

func TestValidateAcceptsAWellFormedType(t *testing.T) {
	if err := validType().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsBrokenDescriptors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*object.Type)
		want   string
	}{
		{"no name", func(ty *object.Type) { ty.Name = "" }, "requires a name"},
		{"no identity", func(ty *object.Type) { ty.ID = "" }, "requires an identity field"},
		{"identity not declared", func(ty *object.Type) { ty.ID = "nope" }, "not declared"},
		{"duplicate field", func(ty *object.Type) {
			ty.Fields = append(ty.Fields, object.Field{Name: "id", Kind: object.KindString})
		}, "duplicate field"},
		{"field without kind", func(ty *object.Type) { ty.Fields = append(ty.Fields, object.Field{Name: "x"}) }, "requires a kind"},
		{"ref without target", func(ty *object.Type) { ty.Fields = append(ty.Fields, object.Field{Name: "r", Kind: object.KindRef}) }, "requires a ref type"},
		{"duplicate detail", func(ty *object.Type) { ty.Details = append(ty.Details, ty.Details[0]) }, "duplicate detail"},
		{"duplicate action", func(ty *object.Type) { ty.Actions = append(ty.Actions, ty.Actions[0]) }, "duplicate action"},
		{"view column undeclared", func(ty *object.Type) { ty.Views[0].Columns = []string{"ghost"} }, "is not a declared field"},
		{"view detail undeclared", func(ty *object.Type) { ty.Views[1].Detail = "ghost" }, "undeclared detail"},
		{"view groupby undeclared", func(ty *object.Type) {
			ty.Views = append(ty.Views, object.View{Kind: object.ViewBoard, GroupBy: "ghost"})
		}, "groups by undeclared field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ty := validType()
			tc.mutate(&ty)
			err := ty.Validate()
			if err == nil {
				t.Fatal("Validate accepted a broken descriptor")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLookups(t *testing.T) {
	ty := validType()
	if f, ok := ty.Field("prompt"); !ok || f.Kind != object.KindText {
		t.Fatalf("Field(prompt) = %+v, %v", f, ok)
	}
	if _, ok := ty.Field("ghost"); ok {
		t.Fatal("Field(ghost) resolved")
	}
	if d, ok := ty.Detail("transcript"); !ok || d.Type != "entry" {
		t.Fatalf("Detail(transcript) = %+v, %v", d, ok)
	}
	if a, ok := ty.Action("steer"); !ok || len(a.Args) == 0 {
		t.Fatalf("Action(steer) = %+v, %v", a, ok)
	}
	if _, ok := ty.Action("ghost"); ok {
		t.Fatal("Action(ghost) resolved")
	}
}

func TestSentinelErrorsAreDistinct(t *testing.T) {
	for _, err := range []error{
		object.ErrUnknownType, object.ErrNotFound, object.ErrUnknownDetail,
		object.ErrUnknownAction, object.ErrUnsupported,
	} {
		if err == nil {
			t.Fatal("nil sentinel")
		}
		if errors.Is(object.ErrUnknownType, err) && err != object.ErrUnknownType {
			t.Fatalf("sentinel %v aliases ErrUnknownType", err)
		}
	}
}
