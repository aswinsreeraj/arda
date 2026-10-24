package jsonschema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCompile_ValidBasic(t *testing.T) {
	schemaJSON := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"type": "object",
		"required": ["id", "name", "score"],
		"properties": {
			"id": {"type": "string"},
			"name": {"type": "string", "minLength": 2, "maxLength": 50},
			"score": {"type": "number", "minimum": 0, "maximum": 100},
			"active": {"type": "boolean"},
			"tags": {
				"type": "array",
				"items": {"type": "string"},
				"uniqueItems": true
			}
		},
		"additionalProperties": false
	}`

	schema, err := CompileBytes([]byte(schemaJSON))
	if err != nil {
		t.Fatalf("unexpected error compiling schema: %v", err)
	}

	// Valid payload
	validJSON := `{
		"id": "agent-123",
		"name": "ArdaAgent",
		"score": 95.5,
		"active": true,
		"tags": ["go", "security"]
	}`
	val, err := schema.ValidateJSON([]byte(validJSON))
	if err != nil {
		t.Fatalf("expected valid JSON to pass validation, got: %v", err)
	}
	m, ok := val.(map[string]any)
	if !ok || m["id"] != "agent-123" {
		t.Fatalf("expected parsed map with id 'agent-123', got: %v", val)
	}

	// Missing required field "score"
	missingReq := `{"id": "agent-123", "name": "ArdaAgent"}`
	_, err = schema.ValidateJSON([]byte(missingReq))
	if err == nil {
		t.Fatal("expected error for missing required field, got nil")
	}
	if !errors.Is(err, ErrSchemaViolation) {
		t.Fatalf("expected ErrSchemaViolation, got: %v", err)
	}

	// Incorrect field type (score as string)
	wrongType := `{"id": "agent-123", "name": "ArdaAgent", "score": "high"}`
	_, err = schema.ValidateJSON([]byte(wrongType))
	if err == nil {
		t.Fatal("expected error for wrong type, got nil")
	}

	// Additional property when prohibited
	extraProp := `{"id": "agent-123", "name": "ArdaAgent", "score": 90, "unexpected": "boom"}`
	_, err = schema.ValidateJSON([]byte(extraProp))
	if err == nil {
		t.Fatal("expected error for additional property, got nil")
	}

	// Duplicate items when uniqueItems is true
	dupTags := `{"id": "agent-123", "name": "ArdaAgent", "score": 90, "tags": ["a", "a"]}`
	_, err = schema.ValidateJSON([]byte(dupTags))
	if err == nil {
		t.Fatal("expected error for duplicate items, got nil")
	}
}

func TestValidateJSON_MalformedAndEmpty(t *testing.T) {
	schema, err := Compile(map[string]any{"type": "object"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// Empty
	_, err = schema.ValidateJSON([]byte(""))
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("expected ErrInvalidJSON on empty input, got: %v", err)
	}

	// Malformed JSON (truncated or invalid syntax)
	_, err = schema.ValidateJSON([]byte(`{"key": "value"`))
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("expected ErrInvalidJSON on malformed JSON, got: %v", err)
	}

	// Trailing junk
	_, err = schema.ValidateJSON([]byte(`{"key": "value"} trailing`))
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("expected ErrInvalidJSON on trailing characters, got: %v", err)
	}

	// Code fence (must NOT be silently stripped)
	_, err = schema.ValidateJSON([]byte("```json\n{\"key\": \"value\"}\n```"))
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("expected code fenced response to fail JSON parsing directly without silent stripping, got: %v", err)
	}
}

func TestValidateJSON_SizeLimit(t *testing.T) {
	schema, err := Compile(map[string]any{"type": "string"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// Output exceeding 1MB
	oversized := make([]byte, MaxOutputBytes+10)
	for i := range oversized {
		oversized[i] = 'a'
	}
	_, err = schema.ValidateJSON(oversized)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("expected ErrOutputTooLarge, got: %v", err)
	}
}

func TestCompile_SecurityBounds(t *testing.T) {
	// Schema size limit (>64KB)
	hugeSchema := make([]byte, MaxSchemaBytes+100)
	for i := range hugeSchema {
		hugeSchema[i] = ' '
	}
	copy(hugeSchema, []byte(`{"type": "object"}`))
	_, err := CompileBytes(hugeSchema)
	if !errors.Is(err, ErrSchemaTooLarge) {
		t.Fatalf("expected ErrSchemaTooLarge, got: %v", err)
	}

	// Schema depth limit (>10)
	var deep any = map[string]any{"type": "string"}
	for i := 0; i < 12; i++ {
		deep = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"nested": deep,
			},
		}
	}
	_, err = Compile(deep)
	if !errors.Is(err, ErrSchemaTooDeep) {
		t.Fatalf("expected ErrSchemaTooDeep, got: %v", err)
	}

	// Remote $ref rejected
	remoteRefs := []string{
		"http://evil.com/schema.json",
		"https://evil.com/schema.json",
		"ftp://evil.com/schema.json",
		"//evil.com/schema.json",
		"external.json",
	}
	for _, ref := range remoteRefs {
		s := map[string]any{
			"$ref": ref,
		}
		_, err := Compile(s)
		if !errors.Is(err, ErrRemoteRefForbidden) {
			t.Fatalf("expected ErrRemoteRefForbidden for ref %q, got: %v", ref, err)
		}
	}

	// Regex length bounds (>250 chars)
	longPattern := strings.Repeat("a", 251)
	_, err = Compile(map[string]any{
		"type":    "string",
		"pattern": longPattern,
	})
	if !errors.Is(err, ErrInvalidRegex) {
		t.Fatalf("expected ErrInvalidRegex for pattern > 250 chars, got: %v", err)
	}

	// Invalid regex pattern
	_, err = Compile(map[string]any{
		"type":    "string",
		"pattern": "[a-z",
	})
	if !errors.Is(err, ErrInvalidRegex) {
		t.Fatalf("expected ErrInvalidRegex for malformed regex, got: %v", err)
	}

	// Unknown type rejected
	_, err = Compile(map[string]any{
		"type": "unknown_type",
	})
	if err == nil {
		t.Fatal("expected error for unknown schema type, got nil")
	}
}

func TestLocalDefinitionsAndRef(t *testing.T) {
	schemaJSON := `{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"definitions": {
			"Coordinate": {
				"type": "object",
				"required": ["lat", "lng"],
				"properties": {
					"lat": {"type": "number"},
					"lng": {"type": "number"}
				},
				"additionalProperties": false
			}
		},
		"type": "object",
		"required": ["location"],
		"properties": {
			"location": {"$ref": "#/definitions/Coordinate"}
		}
	}`

	schema, err := CompileBytes([]byte(schemaJSON))
	if err != nil {
		t.Fatalf("failed to compile schema with definitions: %v", err)
	}

	valid := `{"location": {"lat": 37.7749, "lng": -122.4194}}`
	if _, err := schema.ValidateJSON([]byte(valid)); err != nil {
		t.Fatalf("expected valid definition ref to pass: %v", err)
	}

	invalid := `{"location": {"lat": 37.7749, "lng": "west"}}`
	if _, err := schema.ValidateJSON([]byte(invalid)); err == nil {
		t.Fatal("expected invalid definition ref to fail validation, got nil")
	}
}

func TestCombinators(t *testing.T) {
	// allOf
	allOfSchema, err := Compile(map[string]any{
		"allOf": []any{
			map[string]any{"type": "number", "minimum": 10.0},
			map[string]any{"type": "number", "maximum": 20.0},
		},
	})
	if err != nil {
		t.Fatalf("compile allOf failed: %v", err)
	}
	if err := allOfSchema.Validate(15.0); err != nil {
		t.Fatalf("15 should pass allOf: %v", err)
	}
	if err := allOfSchema.Validate(5.0); err == nil {
		t.Fatal("5 should fail allOf")
	}

	// anyOf
	anyOfSchema, err := Compile(map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "integer"},
		},
	})
	if err != nil {
		t.Fatalf("compile anyOf failed: %v", err)
	}
	if err := anyOfSchema.Validate("hello"); err != nil {
		t.Fatalf("string should pass anyOf: %v", err)
	}
	if err := anyOfSchema.Validate(42); err != nil {
		t.Fatalf("integer should pass anyOf: %v", err)
	}
	if err := anyOfSchema.Validate(true); err == nil {
		t.Fatal("boolean should fail anyOf")
	}

	// oneOf
	oneOfSchema, err := Compile(map[string]any{
		"oneOf": []any{
			map[string]any{"type": "number", "multipleOf": 3.0},
			map[string]any{"type": "number", "multipleOf": 5.0},
		},
	})
	if err != nil {
		t.Fatalf("compile oneOf failed: %v", err)
	}
	if err := oneOfSchema.Validate(9.0); err != nil {
		t.Fatalf("9 matches only multipleOf 3: %v", err)
	}
	if err := oneOfSchema.Validate(10.0); err != nil {
		t.Fatalf("10 matches only multipleOf 5: %v", err)
	}
	// 15 matches both -> must fail oneOf
	if err := oneOfSchema.Validate(15.0); err == nil {
		t.Fatal("15 matches both, should fail oneOf")
	}

	// not
	notSchema, err := Compile(map[string]any{
		"not": map[string]any{"type": "string"},
	})
	if err != nil {
		t.Fatalf("compile not failed: %v", err)
	}
	if err := notSchema.Validate(123); err != nil {
		t.Fatalf("123 is not string, should pass: %v", err)
	}
	if err := notSchema.Validate("fail"); err == nil {
		t.Fatal("string should fail 'not'")
	}
}

func TestNumericsAndStrings(t *testing.T) {
	s, err := Compile(map[string]any{
		"type":             "number",
		"exclusiveMinimum": 0.0,
		"exclusiveMaximum": 10.0,
		"multipleOf":       2.5,
	})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	if err := s.Validate(5.0); err != nil {
		t.Fatalf("5.0 should pass: %v", err)
	}
	if err := s.Validate(0.0); err == nil {
		t.Fatal("0.0 should fail exclusiveMinimum")
	}
	if err := s.Validate(10.0); err == nil {
		t.Fatal("10.0 should fail exclusiveMaximum")
	}
	if err := s.Validate(7.0); err == nil {
		t.Fatal("7.0 should fail multipleOf 2.5")
	}

	strSchema, err := Compile(map[string]any{
		"type":    "string",
		"pattern": "^[A-Z]{3}-[0-9]{3}$",
	})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if err := strSchema.Validate("ABC-123"); err != nil {
		t.Fatalf("ABC-123 should match pattern: %v", err)
	}
	if err := strSchema.Validate("abc-123"); err == nil {
		t.Fatal("abc-123 should fail pattern")
	}
}

func TestBooleanSchemas(t *testing.T) {
	trueSchema, err := Compile(true)
	if err != nil {
		t.Fatalf("compile true failed: %v", err)
	}
	if err := trueSchema.Validate("anything"); err != nil {
		t.Fatalf("true schema should accept anything: %v", err)
	}

	falseSchema, err := Compile(false)
	if err != nil {
		t.Fatalf("compile false failed: %v", err)
	}
	if err := falseSchema.Validate("anything"); err == nil {
		t.Fatal("false schema should reject everything")
	}
}

func TestDeepEqual_JsonNumber(t *testing.T) {
	schema, err := Compile(map[string]any{
		"const": 42.0,
	})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if err := schema.Validate(json.Number("42")); err != nil {
		t.Fatalf("json.Number(42) should equal 42.0: %v", err)
	}
}
