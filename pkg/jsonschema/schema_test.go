package jsonschema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 1. Missing vs Explicitly Null Properties
func TestSemantic_MissingVsNullProperties(t *testing.T) {
	schemaJSON := `{
		"type": "object",
		"required": ["mandatory"],
		"properties": {
			"mandatory": {"type": "string"},
			"optional_str": {"type": "string"},
			"nullable_str": {"type": ["string", "null"]}
		},
		"additionalProperties": false
	}`
	schema, err := CompileBytes([]byte(schemaJSON))
	if err != nil {
		t.Fatalf("failed to compile schema: %v", err)
	}

	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "missing optional property is valid",
			json:    `{"mandatory": "ok"}`,
			wantErr: false,
		},
		{
			name:    "missing required property is invalid",
			json:    `{"optional_str": "val"}`,
			wantErr: true,
		},
		{
			name:    "explicit null for non-nullable string is invalid",
			json:    `{"mandatory": "ok", "optional_str": null}`,
			wantErr: true,
		},
		{
			name:    "explicit null for nullable union type is valid",
			json:    `{"mandatory": "ok", "nullable_str": null}`,
			wantErr: false,
		},
		{
			name:    "string value for nullable union type is valid",
			json:    `{"mandatory": "ok", "nullable_str": "hello"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := schema.ValidateJSON([]byte(tt.json))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// 2. Integer vs Number Validation
func TestSemantic_IntegerVsNumberValidation(t *testing.T) {
	intSchema, err := Compile(map[string]any{"type": "integer"})
	if err != nil {
		t.Fatalf("failed to compile int schema: %v", err)
	}
	numSchema, err := Compile(map[string]any{"type": "number"})
	if err != nil {
		t.Fatalf("failed to compile num schema: %v", err)
	}

	tests := []struct {
		name     string
		val      any
		validInt bool
		validNum bool
	}{
		{name: "integer 42", val: 42, validInt: true, validNum: true},
		{name: "float integer 42.0", val: 42.0, validInt: true, validNum: true},
		{name: "float decimal 42.5", val: 42.5, validInt: false, validNum: true},
		{name: "negative int -10", val: -10, validInt: true, validNum: true},
		{name: "json.Number integer 100", val: json.Number("100"), validInt: true, validNum: true},
		{name: "json.Number decimal 100.5", val: json.Number("100.5"), validInt: false, validNum: true},
		{name: "string 42", val: "42", validInt: false, validNum: false},
		{name: "boolean true", val: true, validInt: false, validNum: false},
		{name: "null", val: nil, validInt: false, validNum: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errInt := intSchema.Validate(tt.val)
			if (errInt == nil) != tt.validInt {
				t.Errorf("intSchema.Validate(%v) = %v, want valid: %v", tt.val, errInt, tt.validInt)
			}
			errNum := numSchema.Validate(tt.val)
			if (errNum == nil) != tt.validNum {
				t.Errorf("numSchema.Validate(%v) = %v, want valid: %v", tt.val, errNum, tt.validNum)
			}
		})
	}
}

// 3. Inclusive and Exclusive Numeric Bounds
func TestSemantic_InclusiveAndExclusiveNumericBounds(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type":             "number",
		"minimum":          10.0,
		"maximum":          20.0,
		"exclusiveMinimum": 10.0,
		"exclusiveMaximum": 20.0,
	})
	if err != nil {
		t.Fatalf("failed to compile schema: %v", err)
	}

	tests := []struct {
		val     float64
		wantErr bool
	}{
		{val: 10.0, wantErr: true}, // equal to exclusiveMinimum -> fail
		{val: 10.01, wantErr: false},
		{val: 15.0, wantErr: false},
		{val: 19.99, wantErr: false},
		{val: 20.0, wantErr: true}, // equal to exclusiveMaximum -> fail
		{val: 25.0, wantErr: true}, // exceeds maximum -> fail
		{val: 5.0, wantErr: true},  // less than minimum -> fail
	}

	for _, tt := range tests {
		err := schema.Validate(tt.val)
		if (err != nil) != tt.wantErr {
			t.Errorf("Validate(%v) error = %v, wantErr = %v", tt.val, err, tt.wantErr)
		}
	}

	// Non-numeric types should ignore numeric bounds per Draft-07
	anySchema, err := Compile(map[string]any{
		"minimum": 10.0,
	})
	if err != nil {
		t.Fatalf("failed to compile: %v", err)
	}
	if err := anySchema.Validate("a string"); err != nil {
		t.Errorf("non-numeric instance should ignore minimum constraint, got: %v", err)
	}
}

// 4. multipleOf with Decimal Values
func TestSemantic_MultipleOfDecimals(t *testing.T) {
	tests := []struct {
		name       string
		multipleOf float64
		val        float64
		wantErr    bool
	}{
		{name: "integer multiple 10 of 2", multipleOf: 2.0, val: 10.0, wantErr: false},
		{name: "integer non-multiple 10 of 3", multipleOf: 3.0, val: 10.0, wantErr: true},
		{name: "decimal 0.3 of 0.1", multipleOf: 0.1, val: 0.3, wantErr: false},
		{name: "decimal 19.99 of 0.01", multipleOf: 0.01, val: 19.99, wantErr: false},
		{name: "decimal 0.0075 of 0.0001", multipleOf: 0.0001, val: 0.0075, wantErr: false},
		{name: "decimal 0.35 of 0.1 non-multiple", multipleOf: 0.1, val: 0.35, wantErr: true},
		{name: "zero is multiple of 5", multipleOf: 5.0, val: 0.0, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Compile(map[string]any{
				"type":       "number",
				"multipleOf": tt.multipleOf,
			})
			if err != nil {
				t.Fatalf("compile failed: %v", err)
			}
			err = s.Validate(tt.val)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%v) error = %v, wantErr = %v", tt.val, err, tt.wantErr)
			}
		})
	}

	// multipleOf <= 0 rejected at compile time
	for _, bad := range []float64{0.0, -1.0, -0.5} {
		_, err := Compile(map[string]any{"multipleOf": bad})
		if err == nil {
			t.Errorf("expected error for multipleOf %v, got nil", bad)
		}
	}
}

// 5. oneOf Matching Exactly One Branch
func TestSemantic_OneOf(t *testing.T) {
	schema, err := Compile(map[string]any{
		"oneOf": []any{
			map[string]any{"type": "number", "multipleOf": 3.0},
			map[string]any{"type": "number", "multipleOf": 5.0},
		},
	})
	if err != nil {
		t.Fatalf("compile oneOf failed: %v", err)
	}

	// 9 matches only multipleOf 3 -> pass
	if err := schema.Validate(9.0); err != nil {
		t.Errorf("9 should match exactly oneOf branch: %v", err)
	}
	// 10 matches only multipleOf 5 -> pass
	if err := schema.Validate(10.0); err != nil {
		t.Errorf("10 should match exactly oneOf branch: %v", err)
	}
	// 15 matches both -> fail
	if err := schema.Validate(15.0); err == nil {
		t.Error("15 matches both branches, expected failure")
	}
	// 7 matches neither -> fail
	if err := schema.Validate(7.0); err == nil {
		t.Error("7 matches no branch, expected failure")
	}

	// empty oneOf rejected at compile time
	_, err = Compile(map[string]any{"oneOf": []any{}})
	if err == nil {
		t.Error("expected compile error for empty oneOf array")
	}
}

// 6. anyOf, allOf, and not Combinations
func TestSemantic_Combinators(t *testing.T) {
	schema, err := Compile(map[string]any{
		"allOf": []any{
			map[string]any{"type": "object"},
			map[string]any{"required": []any{"kind"}},
		},
		"anyOf": []any{
			map[string]any{
				"properties": map[string]any{
					"kind": map[string]any{"const": "A"},
				},
			},
			map[string]any{
				"properties": map[string]any{
					"kind": map[string]any{"const": "B"},
				},
			},
		},
		"not": map[string]any{
			"required": []any{"forbidden"},
		},
	})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{name: "kind A valid", json: `{"kind": "A"}`, wantErr: false},
		{name: "kind B valid", json: `{"kind": "B"}`, wantErr: false},
		{name: "kind C invalid for anyOf", json: `{"kind": "C"}`, wantErr: true},
		{name: "forbidden present violates not", json: `{"kind": "A", "forbidden": true}`, wantErr: true},
		{name: "not an object violates allOf", json: `"A"`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := schema.ValidateJSON([]byte(tt.json))
			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// 7. additionalProperties with Boolean and Schema
func TestSemantic_AdditionalProperties(t *testing.T) {
	// 1. additionalProperties: false
	schemaFalse, err := Compile(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"known": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	})
	if err != nil {
		t.Fatalf("compile false failed: %v", err)
	}
	if err := schemaFalse.Validate(map[string]any{"known": "val"}); err != nil {
		t.Errorf("known property should pass: %v", err)
	}
	if err := schemaFalse.Validate(map[string]any{"known": "val", "unknown": 123}); err == nil {
		t.Error("additional property should fail with false schema")
	}

	// 2. additionalProperties: schema
	schemaType, err := Compile(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"known": map[string]any{"type": "string"},
		},
		"additionalProperties": map[string]any{
			"type": "number",
		},
	})
	if err != nil {
		t.Fatalf("compile type failed: %v", err)
	}
	if err := schemaType.Validate(map[string]any{"known": "val", "num": 42.0}); err != nil {
		t.Errorf("additional number property should pass: %v", err)
	}
	if err := schemaType.Validate(map[string]any{"known": "val", "str": "bad"}); err == nil {
		t.Error("additional string property should fail against number schema")
	}
}

// 8. patternProperties Interaction with Declared Properties
func TestSemantic_PatternProperties(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"S_fixed": map[string]any{"type": "string", "minLength": 5},
		},
		"patternProperties": map[string]any{
			"^S_": map[string]any{"type": "string"},
			"^I_": map[string]any{"type": "integer"},
		},
		"additionalProperties": false,
	})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// S_fixed matches both 'properties' and '^S_' -> must satisfy both!
	if err := schema.Validate(map[string]any{"S_fixed": "12345"}); err != nil {
		t.Errorf("S_fixed with len 5 should pass: %v", err)
	}
	if err := schema.Validate(map[string]any{"S_fixed": "123"}); err == nil {
		t.Error("S_fixed with len 3 should fail minLength in properties")
	}

	// I_count matches patternProperties '^I_' -> must be integer
	if err := schema.Validate(map[string]any{"I_count": 42}); err != nil {
		t.Errorf("I_count int should pass: %v", err)
	}
	if err := schema.Validate(map[string]any{"I_count": "string"}); err == nil {
		t.Error("I_count string should fail '^I_' integer schema")
	}

	// other property does not match properties or patternProperties -> fails additionalProperties: false
	if err := schema.Validate(map[string]any{"other": "extra"}); err == nil {
		t.Error("other property should fail additionalProperties: false")
	}
}

// 9. Tuple-Style items and additionalItems
func TestSemantic_TupleItemsAndAdditionalItems(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type": "array",
		"items": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
		},
		"additionalItems": false,
	})
	if err != nil {
		t.Fatalf("compile tuple failed: %v", err)
	}

	// 2 items matching tuple types
	if err := schema.Validate([]any{"id-1", 100.0}); err != nil {
		t.Errorf("valid tuple should pass: %v", err)
	}
	// 2 items with wrong type
	if err := schema.Validate([]any{"id-1", "not-number"}); err == nil {
		t.Error("tuple item 1 with string should fail number type")
	}
	// 3 items with additionalItems: false
	if err := schema.Validate([]any{"id-1", 100.0, "extra"}); err == nil {
		t.Error("3rd item should fail additionalItems: false")
	}

	// additionalItems with schema
	schemaAdd, err := Compile(map[string]any{
		"type": "array",
		"items": []any{
			map[string]any{"type": "string"},
		},
		"additionalItems": map[string]any{
			"type": "boolean",
		},
	})
	if err != nil {
		t.Fatalf("compile tuple add schema failed: %v", err)
	}
	if err := schemaAdd.Validate([]any{"header", true, false}); err != nil {
		t.Errorf("additional booleans should pass: %v", err)
	}
	if err := schemaAdd.Validate([]any{"header", "not-bool"}); err == nil {
		t.Error("additional string should fail boolean schema")
	}
}

// 10. contains Keyword
func TestSemantic_Contains(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type": "array",
		"contains": map[string]any{
			"type":    "number",
			"minimum": 50.0,
		},
	})
	if err != nil {
		t.Fatalf("compile contains failed: %v", err)
	}

	if err := schema.Validate([]any{"a", "b", 75.0}); err != nil {
		t.Errorf("array with 75.0 should pass contains: %v", err)
	}
	if err := schema.Validate([]any{"a", "b", 25.0}); err == nil {
		t.Error("array with only 25.0 should fail contains minimum 50")
	}
	if err := schema.Validate([]any{}); err == nil {
		t.Error("empty array should fail contains")
	}
}

// 11. dependencies Keyword
func TestSemantic_Dependencies(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type": "object",
		"dependencies": map[string]any{
			"credit_card": []any{"billing_address"},
			"premium": map[string]any{
				"required": []any{"subscription_tier"},
			},
		},
	})
	if err != nil {
		t.Fatalf("compile dependencies failed: %v", err)
	}

	// Property dependency satisfied
	if err := schema.Validate(map[string]any{
		"credit_card":     "4111...",
		"billing_address": "123 Main St",
	}); err != nil {
		t.Errorf("satisfied property dependency should pass: %v", err)
	}
	// Property dependency missing
	if err := schema.Validate(map[string]any{
		"credit_card": "4111...",
	}); err == nil {
		t.Error("missing billing_address should fail dependencies")
	}

	// Schema dependency satisfied
	if err := schema.Validate(map[string]any{
		"premium":           true,
		"subscription_tier": "gold",
	}); err != nil {
		t.Errorf("satisfied schema dependency should pass: %v", err)
	}
	// Schema dependency missing
	if err := schema.Validate(map[string]any{
		"premium": true,
	}); err == nil {
		t.Error("premium without subscription_tier should fail schema dependency")
	}
}

// 12. propertyNames Keyword
func TestSemantic_PropertyNames(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type": "object",
		"propertyNames": map[string]any{
			"pattern":   "^[a-z_]+$",
			"maxLength": 10,
		},
	})
	if err != nil {
		t.Fatalf("compile propertyNames failed: %v", err)
	}

	if err := schema.Validate(map[string]any{"valid_key": 1, "test": 2}); err != nil {
		t.Errorf("valid lowercase keys should pass: %v", err)
	}
	if err := schema.Validate(map[string]any{"InvalidKey": 1}); err == nil {
		t.Error("uppercase key should fail pattern in propertyNames")
	}
	if err := schema.Validate(map[string]any{"extremely_long_key_name": 1}); err == nil {
		t.Error("key exceeding maxLength 10 should fail propertyNames")
	}
}

// 13. uniqueItems for Nested Arrays and Objects
func TestSemantic_UniqueItems(t *testing.T) {
	schema, err := Compile(map[string]any{
		"type":        "array",
		"uniqueItems": true,
	})
	if err != nil {
		t.Fatalf("compile uniqueItems failed: %v", err)
	}

	// Primitives
	if err := schema.Validate([]any{1, 2, 3}); err != nil {
		t.Errorf("unique numbers should pass: %v", err)
	}
	if err := schema.Validate([]any{1, 2, 1}); err == nil {
		t.Error("duplicate numbers should fail uniqueItems")
	}

	// Nested arrays
	if err := schema.Validate([]any{[]any{1, 2}, []any{1, 3}}); err != nil {
		t.Errorf("different nested arrays should pass: %v", err)
	}
	if err := schema.Validate([]any{[]any{1, 2}, []any{1, 2}}); err == nil {
		t.Error("identical nested arrays should fail uniqueItems")
	}

	// Nested objects (key order agnostic)
	obj1 := map[string]any{"a": 1, "b": 2}
	obj2 := map[string]any{"b": 2, "a": 1}
	if err := schema.Validate([]any{obj1, obj2}); err == nil {
		t.Error("equivalent objects with different key order should fail uniqueItems")
	}
}

// 14. Local References and Circular Protections
func TestSemantic_LocalReferences(t *testing.T) {
	// Recursive tree schema (guarded recursion)
	treeSchemaJSON := `{
		"$defs": {
			"node": {
				"type": "object",
				"required": ["val"],
				"properties": {
					"val": {"type": "integer"},
					"children": {
						"type": "array",
						"items": {"$ref": "#/$defs/node"}
					}
				},
				"additionalProperties": false
			}
		},
		"$ref": "#/$defs/node"
	}`
	treeSchema, err := CompileBytes([]byte(treeSchemaJSON))
	if err != nil {
		t.Fatalf("compile tree schema failed: %v", err)
	}

	// Finite tree instance passes
	validTree := `{"val": 1, "children": [{"val": 2, "children": []}, {"val": 3}]}`
	if _, err := treeSchema.ValidateJSON([]byte(validTree)); err != nil {
		t.Errorf("valid recursive tree should pass: %v", err)
	}

	// Self-reference to root (#)
	rootRefJSON := `{
		"type": "object",
		"properties": {
			"child": {"$ref": "#"}
		}
	}`
	rootRefSchema, err := CompileBytes([]byte(rootRefJSON))
	if err != nil {
		t.Fatalf("compile root ref failed: %v", err)
	}
	if err := rootRefSchema.Validate(map[string]any{"child": map[string]any{}}); err != nil {
		t.Errorf("root ref should pass: %v", err)
	}

	// Unguardable direct cycle (A -> A) rejected at compile time
	cyclicJSON := `{
		"$defs": {
			"loop": {"$ref": "#/$defs/loop"}
		},
		"$ref": "#/$defs/loop"
	}`
	_, err = CompileBytes([]byte(cyclicJSON))
	if err == nil {
		t.Error("expected compile error for direct unguardable circular reference")
	}

	// Missing reference rejected at compile time
	missingRefJSON := `{
		"$ref": "#/$defs/does_not_exist"
	}`
	_, err = CompileBytes([]byte(missingRefJSON))
	if err == nil {
		t.Error("expected compile error for missing reference")
	}
}

// 15. String Formats
func TestSemantic_StringFormats(t *testing.T) {
	tests := []struct {
		format  string
		valid   string
		invalid string
	}{
		{format: "date-time", valid: "2026-10-24T23:59:59Z", invalid: "not-a-datetime"},
		{format: "date", valid: "2026-10-24", invalid: "2026/10/24"},
		{format: "time", valid: "23:59:59Z", invalid: "25:00:00"},
		{format: "email", valid: "user@example.com", invalid: "user@@example"},
		{format: "ipv4", valid: "192.168.1.1", invalid: "999.999.999.999"},
		{format: "ipv6", valid: "2001:db8::1", invalid: "192.168.1.1"},
		{format: "uri", valid: "https://example.com/api", invalid: "relative/path"},
		{format: "uuid", valid: "550e8400-e29b-41d4-a716-446655440000", invalid: "not-a-uuid"},
		{format: "regex", valid: "^[a-z]+$", invalid: "[a-z"},
		{format: "json-pointer", valid: "/path/to/item", invalid: "path/without/slash"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			s, err := Compile(map[string]any{
				"type":   "string",
				"format": tt.format,
			})
			if err != nil {
				t.Fatalf("compile format %s failed: %v", tt.format, err)
			}
			if err := s.Validate(tt.valid); err != nil {
				t.Errorf("format %s valid value %q rejected: %v", tt.format, tt.valid, err)
			}
			if err := s.Validate(tt.invalid); err == nil {
				t.Errorf("format %s invalid value %q accepted", tt.format, tt.invalid)
			}
		})
	}
}

// 16. Malformed Schema Keywords Rejected at Compilation
func TestSemantic_MalformedSchemaKeywords(t *testing.T) {
	malformed := []struct {
		name   string
		schema map[string]any
	}{
		{name: "type as number", schema: map[string]any{"type": 123}},
		{name: "unknown type", schema: map[string]any{"type": "float"}},
		{name: "required as string", schema: map[string]any{"required": "id"}},
		{name: "required with duplicate keys", schema: map[string]any{"required": []any{"id", "id"}}},
		{name: "properties as array", schema: map[string]any{"properties": []any{}}},
		{name: "maximum as string", schema: map[string]any{"maximum": "100"}},
		{name: "minimum as string", schema: map[string]any{"minimum": "0"}},
		{name: "exclusiveMinimum as bool in Draft-07", schema: map[string]any{"exclusiveMinimum": true}},
		{name: "minLength negative", schema: map[string]any{"minLength": -5}},
		{name: "maxLength negative", schema: map[string]any{"maxLength": -1}},
		{name: "maxItems negative", schema: map[string]any{"maxItems": -1}},
		{name: "minItems negative", schema: map[string]any{"minItems": -1}},
		{name: "uniqueItems not bool", schema: map[string]any{"uniqueItems": "true"}},
		{name: "maxProperties negative", schema: map[string]any{"maxProperties": -1}},
		{name: "allOf empty array", schema: map[string]any{"allOf": []any{}}},
		{name: "anyOf empty array", schema: map[string]any{"anyOf": []any{}}},
		{name: "oneOf empty array", schema: map[string]any{"oneOf": []any{}}},
		{name: "enum not array", schema: map[string]any{"enum": "red"}},
		{name: "pattern invalid regex", schema: map[string]any{"pattern": "[a-z"}},
		{name: "remote $ref", schema: map[string]any{"$ref": "https://remote.com/schema.json"}},
	}

	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(tt.schema)
			if err == nil {
				t.Fatalf("expected compile error for %s, got nil", tt.name)
			}
		})
	}
}

// 17. Resource Limits Enforced in Compilation and Validation
func TestResourceLimits_CompilationAndValidation(t *testing.T) {
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

	// Regex length bounds (>250 chars)
	longPattern := strings.Repeat("a", 251)
	_, err = Compile(map[string]any{
		"type":    "string",
		"pattern": longPattern,
	})
	if !errors.Is(err, ErrInvalidRegex) {
		t.Fatalf("expected ErrInvalidRegex for pattern > 250 chars, got: %v", err)
	}

	// Model output size limit (>1MB before JSON parsing)
	schema, err := Compile(map[string]any{"type": "string"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	hugeOutput := make([]byte, MaxOutputBytes+10)
	for i := range hugeOutput {
		hugeOutput[i] = 'a'
	}
	_, err = schema.ValidateJSON(hugeOutput)
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("expected ErrOutputTooLarge, got: %v", err)
	}

	// Deeply nested instance (> 32 levels) fails closed
	var deepInstance any = "leaf"
	for i := 0; i < 40; i++ {
		deepInstance = map[string]any{"sub": deepInstance}
	}
	// Permissive schema
	permSchema, _ := Compile(true)
	err = permSchema.Validate(deepInstance)
	if err == nil {
		t.Fatal("expected failure on instance exceeding depth 32, got nil")
	}
	if !strings.Contains(err.Error(), "maximum allowed depth") {
		t.Errorf("expected max depth message, got: %v", err)
	}
}
