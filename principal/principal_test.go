package principal

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	ref, err := New(KindNavi, "navi-code")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ref.Kind() != KindNavi || ref.Name() != "navi-code" {
		t.Errorf("New = %q/%q, want navi/navi-code", ref.Kind(), ref.Name())
	}
	if got, want := ref.String(), "navi:navi-code"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	if ref.IsZero() {
		t.Error("a constructed ref must not be zero")
	}
	if err := ref.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// Kinds are open: an unlisted kind is still a valid principal kind.
	if _, err := New(Kind("agent"), "scout"); err != nil {
		t.Errorf("New with an open kind: %v", err)
	}

	bad := []struct {
		name string
		kind Kind
		ref  string
	}{
		{"empty kind", "", "x"},
		{"empty name", KindUser, ""},
		{"blank name", KindUser, "   "},
		{"separator in name", KindUser, "a:b"},
		{"separator in kind", Kind("a:b"), "c"},
	}
	for _, tc := range bad {
		if _, err := New(tc.kind, tc.ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("New(%q, %q) = %v, want ErrInvalidRef", tc.kind, tc.ref, err)
		}
	}
}

func TestParse(t *testing.T) {
	ref, err := Parse("user:alice@example.com")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ref.Kind() != KindUser || ref.Name() != "alice@example.com" {
		t.Errorf("Parse = %q/%q", ref.Kind(), ref.Name())
	}
	if got := ParseMust(t, " group:eng "); got.Kind() != KindGroup || got.Name() != "eng" {
		t.Errorf("Parse trimmed = %q/%q, want group/eng", got.Kind(), got.Name())
	}

	if _, err := Parse(""); !errors.Is(err, ErrEmptyRef) {
		t.Errorf("Parse(%q) = %v, want ErrEmptyRef", "", err)
	}
	for _, s := range []string{"navi-code", ":name", "kind:", "a:b:c"} {
		if _, err := Parse(s); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidRef", s, err)
		}
	}
}

func TestParseLegacy(t *testing.T) {
	// The pre-principal form: a bare navi name reads as navi:<name>.
	ref, err := ParseLegacy("navi-code")
	if err != nil {
		t.Fatalf("ParseLegacy: %v", err)
	}
	if ref.Kind() != KindNavi || ref.Name() != "navi-code" {
		t.Errorf("ParseLegacy = %q/%q, want navi/navi-code", ref.Kind(), ref.Name())
	}

	// A qualified value is parsed, not rewritten.
	ref, err = ParseLegacy("connector:github")
	if err != nil {
		t.Fatalf("ParseLegacy qualified: %v", err)
	}
	if ref.Kind() != KindConnector || ref.Name() != "github" {
		t.Errorf("ParseLegacy qualified = %q/%q", ref.Kind(), ref.Name())
	}

	if _, err := ParseLegacy("   "); !errors.Is(err, ErrEmptyRef) {
		t.Errorf("ParseLegacy blank = %v, want ErrEmptyRef", err)
	}
}

func TestMustParse(t *testing.T) {
	if got := MustParse("navi:navi-code"); got.String() != "navi:navi-code" {
		t.Errorf("MustParse = %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustParse on a malformed literal did not panic")
		}
	}()
	MustParse("not-qualified")
}

func TestZeroRef(t *testing.T) {
	var ref Ref
	if !ref.IsZero() {
		t.Error("the zero Ref must report zero")
	}
	if got := ref.String(); got != "" {
		t.Errorf("zero Ref String = %q, want empty", got)
	}
	if err := ref.Validate(); !errors.Is(err, ErrEmptyRef) {
		t.Errorf("zero Ref Validate = %v, want ErrEmptyRef", err)
	}
}

func TestRefIsAMapKey(t *testing.T) {
	a := MustParse("navi:one")
	b := MustParse("navi:one")
	c := MustParse("navi:two")
	seen := map[Ref]int{a: 1}
	if seen[b] != 1 {
		t.Error("equal refs must hash equal")
	}
	if _, ok := seen[c]; ok {
		t.Error("distinct refs must not collide")
	}
}

func TestRefTextRoundTrip(t *testing.T) {
	ref := MustParse("service:relay")
	data, err := ref.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	if string(data) != "service:relay" {
		t.Errorf("MarshalText = %q", data)
	}
	var back Ref
	if err := back.UnmarshalText(data); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != ref {
		t.Errorf("round trip = %v, want %v", back, ref)
	}
	// Strict: an unqualified value is rejected rather than migrated.
	if err := back.UnmarshalText([]byte("navi-code")); !errors.Is(err, ErrInvalidRef) {
		t.Errorf("UnmarshalText legacy = %v, want ErrInvalidRef", err)
	}
}

func TestRefJSONRoundTrip(t *testing.T) {
	ref := MustParse("group:eng")
	data, err := json.Marshal(map[string]any{"subject": ref})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"subject":"group:eng"}` {
		t.Errorf("Marshal = %s, want the string form", data)
	}

	var got struct {
		Subject Ref `json:"subject"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Subject != ref {
		t.Errorf("Unmarshal = %v, want %v", got.Subject, ref)
	}

	// "" decodes to the zero Ref, and a malformed value is refused.
	if err := json.Unmarshal([]byte(`{"subject":""}`), &got); err != nil {
		t.Fatalf("Unmarshal empty: %v", err)
	}
	if !got.Subject.IsZero() {
		t.Errorf("empty JSON = %v, want the zero Ref", got.Subject)
	}
	if err := json.Unmarshal([]byte(`{"subject":"bare"}`), &got); !errors.Is(err, ErrInvalidRef) {
		t.Errorf("Unmarshal malformed = %v, want ErrInvalidRef", err)
	}
}

// ParseMust is a test helper for the common valid case.
func ParseMust(t *testing.T, s string) Ref {
	t.Helper()
	ref, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return ref
}
