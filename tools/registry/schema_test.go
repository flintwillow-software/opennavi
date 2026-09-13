package registry

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustParse(t *testing.T, raw string) Schema {
	t.Helper()
	s, err := ParseSchema(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseSchema(%s): %v", raw, err)
	}
	return s
}

func mustValidate(t *testing.T, s Schema, args string) error {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(json.RawMessage(args), &m); err != nil {
		t.Fatalf("test args not an object: %v", err)
	}
	c := newSchemaChecker()
	return c.Validate(s, m)
}

func TestSchemaAcceptsValidObject(t *testing.T) {
	s := mustParse(t, `{
		"type": "object",
		"required": ["path", "depth"],
		"properties": {
			"path":  {"type": "string", "minLength": 1},
			"depth": {"type": "integer", "minimum": 1, "maximum": 10},
			"tags":  {"type": "array", "items": {"type": "string"}, "minItems": 1}
		}
	}`)
	if err := mustValidate(t, s, `{"path":"a.go","depth":3,"tags":["x"]}`); err != nil {
		t.Errorf("valid arguments rejected: %v", err)
	}
}

func TestSchemaRequiredMissing(t *testing.T) {
	s := mustParse(t, `{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`)
	err := mustValidate(t, s, `{}`)
	if err == nil || !strings.Contains(err.Error(), `missing required property "path"`) {
		t.Errorf("err = %v, want missing required property", err)
	}
}

func TestSchemaTypeMismatch(t *testing.T) {
	s := mustParse(t, `{"type":"object","properties":{"depth":{"type":"integer"}}}`)
	err := mustValidate(t, s, `{"depth":"deep"}`)
	if err == nil || !strings.Contains(err.Error(), "expected type integer") {
		t.Errorf("err = %v, want type mismatch", err)
	}
}

func TestSchemaIntegerIsSubsetOfNumber(t *testing.T) {
	s := mustParse(t, `{"type":"object","properties":{"n":{"type":"number"}}}`)
	if err := mustValidate(t, s, `{"n":3}`); err != nil {
		t.Errorf("integer value rejected for number type: %v", err)
	}
}

func TestSchemaAdditionalPropertiesFalse(t *testing.T) {
	s := mustParse(t, `{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"additionalProperties": false
	}`)
	err := mustValidate(t, s, `{"path":"a.go","surprise":1}`)
	if err == nil || !strings.Contains(err.Error(), `unexpected property "surprise"`) {
		t.Errorf("err = %v, want unexpected property", err)
	}
}

func TestSchemaAdditionalPropertiesSchema(t *testing.T) {
	s := mustParse(t, `{
		"type": "object",
		"properties": {"path": {"type": "string"}},
		"additionalProperties": {"type": "boolean"}
	}`)
	if err := mustValidate(t, s, `{"path":"a.go","flag":true}`); err != nil {
		t.Errorf("valid additional property rejected: %v", err)
	}
	if err := mustValidate(t, s, `{"path":"a.go","flag":"yes"}`); err == nil {
		t.Error("wrong-typed additional property accepted")
	}
}

func TestSchemaEnum(t *testing.T) {
	s := mustParse(t, `{"type":"object","properties":{"format":{"enum":["text","json"]}}}`)
	if err := mustValidate(t, s, `{"format":"json"}`); err != nil {
		t.Errorf("allowed enum value rejected: %v", err)
	}
	if err := mustValidate(t, s, `{"format":"yaml"}`); err == nil {
		t.Error("disallowed enum value accepted")
	}
}

func TestSchemaBounds(t *testing.T) {
	s := mustParse(t, `{"type":"object","properties":{
		"n": {"type":"integer","minimum":1,"maximum":5},
		"s": {"type":"string","minLength":2,"maxLength":4},
		"a": {"type":"array","minItems":1,"maxItems":2}
	}}`)
	if err := mustValidate(t, s, `{"n":0}`); err == nil {
		t.Error("below-minimum accepted")
	}
	if err := mustValidate(t, s, `{"n":6}`); err == nil {
		t.Error("above-maximum accepted")
	}
	if err := mustValidate(t, s, `{"s":"a"}`); err == nil {
		t.Error("below-minLength accepted")
	}
	if err := mustValidate(t, s, `{"s":"abcde"}`); err == nil {
		t.Error("above-maxLength accepted")
	}
	if err := mustValidate(t, s, `{"a":[]}`); err == nil {
		t.Error("below-minItems accepted")
	}
	if err := mustValidate(t, s, `{"a":[1,2,3]}`); err == nil {
		t.Error("above-maxItems accepted")
	}
	if err := mustValidate(t, s, `{"n":3,"s":"ab","a":[1]}`); err != nil {
		t.Errorf("in-bounds values rejected: %v", err)
	}
}

func TestSchemaEmptyObjectAcceptsAnything(t *testing.T) {
	s := mustParse(t, `{}`)
	if err := mustValidate(t, s, `{"anything":["goes",true,1]}`); err != nil {
		t.Errorf("empty schema rejected valid object: %v", err)
	}
}

func TestSchemaNestedObjects(t *testing.T) {
	s := mustParse(t, `{
		"type": "object",
		"properties": {
			"options": {
				"type": "object",
				"required": ["mode"],
				"properties": {"mode": {"type": "string"}}
			}
		}
	}`)
	if err := mustValidate(t, s, `{"options":{"mode":"fast"}}`); err != nil {
		t.Errorf("valid nested object rejected: %v", err)
	}
	err := mustValidate(t, s, `{"options":{}}`)
	if err == nil || !strings.Contains(err.Error(), "options.mode") {
		t.Errorf("err = %v, want nested-path error", err)
	}
}

func TestParseSchemaRejectsNonObject(t *testing.T) {
	if _, err := ParseSchema(json.RawMessage(`[]`)); err == nil {
		t.Error("array schema accepted")
	}
	if _, err := ParseSchema(json.RawMessage(`not json`)); err == nil {
		t.Error("non-JSON schema accepted")
	}
	if _, err := ParseSchema(nil); err == nil {
		t.Error("empty schema accepted")
	}
}
