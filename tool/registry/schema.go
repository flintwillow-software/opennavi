package registry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Schema is a JSON Schema (a pragmatic subset) that tool-call arguments
// are validated against before dispatch. Construct with
// ParseSchema; the zero value is rejected at registration — a schema of
// `{}` accepts any object.
//
// Supported keywords: type (incl. lists), properties, required,
// additionalProperties (bool or schema), enum, items, minItems,
// maxItems, minLength, maxLength, minimum, maximum. Unsupported
// keywords are ignored, not errored: the public schema stays strict by
// authoring convention, and the runtime never relaxes validation.
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

// ValidateArgs validates decoded arguments against a JSON Schema (the
// pragmatic subset documented on Schema). It is the entry point for a
// surface outside the tool registry — the typed-object surface enforces its
// declared action schemas through it (ADR 0017 decision 4) — so an action's
// arguments are checked by the same machinery a tool call is.
//
// Arguments are normalized through JSON first, so an in-process caller's Go
// values (int, struct) check exactly like the decoded request a remote
// caller sends.
func ValidateArgs(raw json.RawMessage, args map[string]any) error {
	s, err := ParseSchema(raw)
	if err != nil {
		return err
	}
	normalized, err := normalizeArgs(args)
	if err != nil {
		return err
	}
	return newSchemaChecker().Validate(s, normalized)
}

// normalizeArgs round-trips arguments through JSON so validation sees the
// same shapes a wire caller produces.
func normalizeArgs(args map[string]any) (map[string]any, error) {
	if args == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("arguments are not JSON-encodable: %w", err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	return out, nil
}

// schemaChecker validates decoded arguments against parsed schemas.
// Parsing happens lazily and is memoized per Schema value.
type schemaChecker struct {
	cache map[string]parsedSchema
}

// parsedSchema is the compiled form of a schema document.
type parsedSchema struct {
	types                []string
	properties           map[string]parsedSchema
	required             []string
	additionalProperties *parsedSchema
	allowAdditional      bool
	hasAdditional        bool
	enum                 []any
	items                *parsedSchema
	minItems             *int
	maxItems             *int
	minLength            *int
	maxLength            *int
	minimum              *float64
	maximum              *float64
}

func newSchemaChecker() *schemaChecker {
	return &schemaChecker{cache: make(map[string]parsedSchema)}
}

// Validate checks args against the schema, compiling the schema on
// first use. A `{}` schema accepts any object.
func (c *schemaChecker) Validate(schema Schema, args map[string]any) error {
	key := string(schema.raw)
	parsed, ok := c.cache[key]
	if !ok {
		var doc map[string]any
		if err := json.Unmarshal(schema.raw, &doc); err != nil {
			return fmt.Errorf("schema is not a JSON object: %w", err)
		}
		p, err := compileSchema(doc)
		if err != nil {
			return err
		}
		parsed = p
		c.cache[key] = parsed
	}
	return validateAgainst(parsed, args, "")
}

// compileSchema builds the compiled form of one schema document.
func compileSchema(doc map[string]any) (parsedSchema, error) {
	var p parsedSchema

	switch t := doc["type"].(type) {
	case string:
		p.types = []string{t}
	case []any:
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return p, fmt.Errorf("schema type list must contain only strings")
			}
			p.types = append(p.types, s)
		}
	}

	if e, ok := doc["enum"].([]any); ok {
		p.enum = e
	}

	if props, ok := doc["properties"].(map[string]any); ok {
		p.properties = make(map[string]parsedSchema, len(props))
		for name, sub := range props {
			subDoc, ok := sub.(map[string]any)
			if !ok {
				return p, fmt.Errorf("schema property %q must be an object", name)
			}
			subP, err := compileSchema(subDoc)
			if err != nil {
				return p, fmt.Errorf("property %q: %w", name, err)
			}
			p.properties[name] = subP
		}
	}

	if req, ok := doc["required"].([]any); ok {
		for _, r := range req {
			s, ok := r.(string)
			if !ok {
				return p, fmt.Errorf("schema required list must contain only strings")
			}
			p.required = append(p.required, s)
		}
	}

	switch ap := doc["additionalProperties"].(type) {
	case bool:
		p.hasAdditional = true
		p.allowAdditional = ap
	case map[string]any:
		subP, err := compileSchema(ap)
		if err != nil {
			return p, fmt.Errorf("additionalProperties: %w", err)
		}
		p.hasAdditional = true
		p.allowAdditional = true
		p.additionalProperties = &subP
	}

	if items, ok := doc["items"].(map[string]any); ok {
		subP, err := compileSchema(items)
		if err != nil {
			return p, fmt.Errorf("items: %w", err)
		}
		p.items = &subP
	}

	p.minItems = intKeyword(doc, "minItems")
	p.maxItems = intKeyword(doc, "maxItems")
	p.minLength = intKeyword(doc, "minLength")
	p.maxLength = intKeyword(doc, "maxLength")
	p.minimum = floatKeyword(doc, "minimum")
	p.maximum = floatKeyword(doc, "maximum")

	return p, nil
}

func intKeyword(doc map[string]any, key string) *int {
	if v, ok := doc[key].(float64); ok {
		n := int(v)
		return &n
	}
	return nil
}

func floatKeyword(doc map[string]any, key string) *float64 {
	if v, ok := doc[key].(float64); ok {
		return &v
	}
	return nil
}

// validateAgainst checks one decoded value against a compiled schema.
// path is the human-readable location for error messages ("" = root).
func validateAgainst(p parsedSchema, value any, path string) error {
	if err := checkType(p.types, value, path); err != nil {
		return err
	}
	if err := checkEnum(p.enum, value, path); err != nil {
		return err
	}

	switch v := value.(type) {
	case map[string]any:
		return validateObject(p, v, path)
	case []any:
		return validateArray(p, v, path)
	case string:
		if p.minLength != nil && len(v) < *p.minLength {
			return fmt.Errorf("%s: length %d is below minLength %d", label(path), len(v), *p.minLength)
		}
		if p.maxLength != nil && len(v) > *p.maxLength {
			return fmt.Errorf("%s: length %d exceeds maxLength %d", label(path), len(v), *p.maxLength)
		}
	case float64:
		if p.minimum != nil && v < *p.minimum {
			return fmt.Errorf("%s: value %v is below minimum %v", label(path), v, *p.minimum)
		}
		if p.maximum != nil && v > *p.maximum {
			return fmt.Errorf("%s: value %v exceeds maximum %v", label(path), v, *p.maximum)
		}
	}
	return nil
}

func childPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func validateObject(p parsedSchema, obj map[string]any, path string) error {
	for _, req := range p.required {
		if _, ok := obj[req]; !ok {
			return fmt.Errorf("%s: missing required property %q", label(path), childPath(path, req))
		}
	}
	for name, sub := range p.properties {
		value, ok := obj[name]
		if !ok {
			continue
		}
		if err := validateAgainst(sub, value, childPath(path, name)); err != nil {
			return err
		}
	}
	if p.hasAdditional && !p.allowAdditional {
		for name := range obj {
			if _, declared := p.properties[name]; !declared {
				return fmt.Errorf("%s: unexpected property %q (additionalProperties is false)", label(path), name)
			}
		}
	}
	if p.additionalProperties != nil {
		for name, value := range obj {
			if _, declared := p.properties[name]; declared {
				continue
			}
			if err := validateAgainst(*p.additionalProperties, value, childPath(path, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateArray(p parsedSchema, arr []any, path string) error {
	if p.minItems != nil && len(arr) < *p.minItems {
		return fmt.Errorf("%s: %d items is below minItems %d", label(path), len(arr), *p.minItems)
	}
	if p.maxItems != nil && len(arr) > *p.maxItems {
		return fmt.Errorf("%s: %d items exceeds maxItems %d", label(path), len(arr), *p.maxItems)
	}
	if p.items != nil {
		for i, item := range arr {
			if err := validateAgainst(*p.items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkType(types []string, value any, path string) error {
	if len(types) == 0 {
		return nil // untyped schema node matches anything
	}
	actual := jsonTypeName(value)
	for _, t := range types {
		// JSON Schema treats integer as a subset of number.
		if t == actual || (t == "number" && actual == "integer") {
			return nil
		}
	}
	return fmt.Errorf("%s: expected type %s, got %s", label(path), strings.Join(types, "|"), actual)
}

func checkEnum(enum []any, value any, path string) error {
	if len(enum) == 0 {
		return nil
	}
	for _, allowed := range enum {
		if fmt.Sprint(allowed) == fmt.Sprint(value) {
			return nil
		}
	}
	return fmt.Errorf("%s: value %v is not one of the allowed values", label(path), value)
}

// jsonTypeName reports the JSON Schema type of a value. It accepts the
// decoded shapes encoding/json produces (float64, []any, map[string]any) and
// the Go shapes an in-process caller passes (int, int64, float32, ...), so a
// schema checks the same either way.
func jsonTypeName(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return "integer"
	case float64:
		if v == float64(int64(v)) {
			return "integer"
		}
		return "number"
	case float32:
		if v == float32(int64(v)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func label(path string) string {
	if path == "" {
		return "arguments"
	}
	return path
}
