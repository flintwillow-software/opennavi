package huly

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// withDescription seeds an issue's description reference and the collaborator
// document it points at, the state a read or an in-place update starts from.
func withDescription(f *fakeHuly, ref, markup string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues[0]["description"] = ref
	f.collabRefs["ws-1|"+classIssue+"|"+testIssueID+"|"+descriptionAttr] = ref
	f.collabDocs[ref] = markup
}

func collabCalls(f *fakeHuly) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.collabCalls...)
}

// TestIssueDescriptionSetCreatesTheDocumentAndReference pins the first write:
// the collaborator creates the document, and one transaction points the issue's
// description reference at it.
func TestIssueDescriptionSetCreatesTheDocumentAndReference(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueDescriptionSet(context.Background(), map[string]any{
		"issue": "NAVI-408", "text": "first line\n\nsecond line",
	})
	if err != nil {
		t.Fatalf("issueDescriptionSet: %v", err)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("result = %s, want ok", out)
	}

	calls := collabCalls(f)
	if len(calls) != 1 || calls[0]["method"] != "createContent" {
		t.Fatalf("collaborator calls = %v, want one createContent", calls)
	}
	wantDoc := "ws-1|" + classIssue + "|" + testIssueID + "|" + descriptionAttr
	if calls[0]["documentId"] != wantDoc {
		t.Errorf("document id = %v, want %s", calls[0]["documentId"], wantDoc)
	}

	txs := txsOf(f)
	if len(txs) != 1 {
		t.Fatalf("posted %d transactions, want one setting the reference", len(txs))
	}
	tx := txs[0]
	if tx["_class"] != classTxUpdateDoc || tx["objectId"] != testIssueID || tx["objectSpace"] != "space-1" {
		t.Fatalf("tx = %v, want an update on the issue", tx)
	}
	set, _ := tx["operations"].(map[string]any)
	ref, _ := set[descriptionAttr].(string)
	if !strings.HasPrefix(ref, testIssueID+"-"+descriptionAttr+"-") {
		t.Errorf("reference = %q, want <issueId>-description-<timestamp>", ref)
	}

	f.mu.Lock()
	stored := f.collabDocs[ref]
	f.mu.Unlock()
	if !strings.Contains(stored, `"first line"`) || !strings.Contains(stored, `"second line"`) {
		t.Errorf("stored markup = %s, want both lines as text", stored)
	}
}

// TestIssueDescriptionSetUpdatesInPlace pins the later write: the reference
// stays, so the document is updated and no transaction is posted.
func TestIssueDescriptionSetUpdatesInPlace(t *testing.T) {
	f := newFakeHuly(t)
	withDescription(f, "ref-1", `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"old"}]}]}`)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	if _, err := client.issueDescriptionSet(context.Background(), map[string]any{
		"issue": "NAVI-408", "text": "replaced",
	}); err != nil {
		t.Fatalf("issueDescriptionSet: %v", err)
	}
	calls := collabCalls(f)
	if len(calls) != 1 || calls[0]["method"] != "updateContent" {
		t.Fatalf("collaborator calls = %v, want one updateContent", calls)
	}
	if txs := txsOf(f); len(txs) != 0 {
		t.Errorf("posted %d transactions, want none for an in-place update", len(txs))
	}
	f.mu.Lock()
	stored := f.collabDocs["ref-1"]
	f.mu.Unlock()
	if !strings.Contains(stored, "replaced") {
		t.Errorf("stored markup = %s, want the replacement", stored)
	}
}

// TestIssueDescriptionReadsText pins the read: the issue's reference is the
// collaborator `source`, and the ProseMirror document comes back as text.
func TestIssueDescriptionReadsText(t *testing.T) {
	f := newFakeHuly(t)
	withDescription(f, "ref-1", `{"type":"doc","content":[`+
		`{"type":"paragraph","content":[{"type":"text","text":"hello"}]},`+
		`{"type":"paragraph","content":[{"type":"text","text":"world"}]}]}`)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueDescription(context.Background(), map[string]any{"issue": "NAVI-408"})
	if err != nil {
		t.Fatalf("issueDescription: %v", err)
	}
	var got struct {
		Description string `json:"description"`
		Empty       bool   `json:"empty"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if got.Description != "hello\nworld" || got.Empty {
		t.Errorf("description = %q (empty %v), want two lines", got.Description, got.Empty)
	}
	calls := collabCalls(f)
	if len(calls) != 1 || calls[0]["method"] != "getContent" {
		t.Fatalf("collaborator calls = %v, want one getContent", calls)
	}
}

// TestIssueDescriptionEmptyIsNoCall pins the short-circuit: an issue with no
// description reference is not fetched from the collaborator.
func TestIssueDescriptionEmptyIsNoCall(t *testing.T) {
	f := newFakeHuly(t)
	_, live := connectFake(t, f, map[string]string{"HULY_TOKEN": "t"})
	client, _ := live.Get()

	out, err := client.issueDescription(context.Background(), map[string]any{"issue": "NAVI-408"})
	if err != nil {
		t.Fatalf("issueDescription: %v", err)
	}
	if !strings.Contains(out, `"empty":true`) {
		t.Errorf("result = %s, want empty:true", out)
	}
	if calls := collabCalls(f); len(calls) != 0 {
		t.Errorf("collaborator calls = %v, want none", calls)
	}
}

// TestProseMirrorText pins the text extraction across the node shapes Huly
// stores, including nested lists and formatting marks.
func TestProseMirrorText(t *testing.T) {
	markup := `{"type":"doc","content":[` +
		`{"type":"paragraph","content":[{"type":"text","text":"plain "},{"type":"text","text":"bold","marks":[{"type":"bold"}]}]},` +
		`{"type":"bulletList","content":[` +
		`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"one"}]}]},` +
		`{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"two"}]}]}]}]}`
	got := proseMirrorText(markup)
	if got != "plain bold\none\ntwo" {
		t.Errorf("proseMirrorText = %q, want the text with block newlines", got)
	}
	if got := proseMirrorText("not json"); got != "not json" {
		t.Errorf("proseMirrorText(non-JSON) = %q, want the raw value", got)
	}
	if got := proseMirrorText(""); got != "" {
		t.Errorf("proseMirrorText(\"\") = %q, want empty", got)
	}
}

// TestMarkupParagraphs pins the write shape: one paragraph per non-empty line.
func TestMarkupParagraphs(t *testing.T) {
	var doc struct {
		Type    string `json:"type"`
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(markupParagraphs("one\n\n  \ntwo\nthree")), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc.Type != "doc" || len(doc.Content) != 3 {
		t.Fatalf("doc = %+v, want three paragraphs", doc)
	}
	for i, want := range []string{"one", "two", "three"} {
		if doc.Content[i].Type != "paragraph" || len(doc.Content[i].Content) != 1 || doc.Content[i].Content[0].Text != want {
			t.Errorf("paragraph %d = %+v, want %q", i, doc.Content[i], want)
		}
	}
}
