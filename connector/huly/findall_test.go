package huly

import (
	"context"
	"testing"
)

// TestFindAllRestoresQueriedFields pins the compensation for a live Huly
// behavior the fake now reproduces: find-all uses the query as an exclusion
// projection, so the field a document was matched on is absent from the
// response. Without the client's restore, `findProject` loses the project
// identifier (creating `-419` instead of `NAVI-419`) and `findIssue` loses the
// issue id (so a comment or label attaches to nothing).
func TestFindAllRestoresQueriedFields(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	project, err := client.findProject(context.Background(), "NAVI")
	if err != nil {
		t.Fatalf("findProject: %v", err)
	}
	if got := stringField(project, "identifier"); got != "NAVI" {
		t.Errorf("project identifier = %q, want NAVI (the server strips the queried field)", got)
	}

	issue, err := client.findIssue(context.Background(), "NAVI-408")
	if err != nil {
		t.Fatalf("findIssue by identifier: %v", err)
	}
	if got := stringField(issue, "identifier"); got != "NAVI-408" {
		t.Errorf("issue identifier = %q, want NAVI-408", got)
	}

	// Addressing by raw id queries `_id`, which the server strips in turn.
	byID, err := client.findIssue(context.Background(), "6acd168268c57585aac3fa3f")
	if err != nil {
		t.Fatalf("findIssue by id: %v", err)
	}
	if got := stringField(byID, "_id"); got != "6acd168268c57585aac3fa3f" {
		t.Errorf("issue _id = %q, want the raw id restored", got)
	}

	// A nested `$in` value is not stripped by the server and is left alone.
	persons, err := client.findAll(context.Background(), classPerson,
		map[string]any{"_id": map[string]any{"$in": []any{"6aa2a3038376b1c099c0403d"}}}, nil)
	if err != nil {
		t.Fatalf("find persons: %v", err)
	}
	if len(persons) != 1 || stringField(persons[0], "_id") != "6aa2a3038376b1c099c0403d" {
		t.Errorf("persons = %v, want the server's own _id", persons)
	}
}
