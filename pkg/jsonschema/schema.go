package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

const (
	// MaxSchemaBytes bounds the schema size to 64KB to prevent resource exhaustion.
	MaxSchemaBytes = 64 * 1024
	// MaxSchemaDepth bounds the schema nesting depth to 10 levels.
	MaxSchemaDepth = 10
	// MaxRegexLength bounds regex pattern strings to 250 characters.
	MaxRegexLength = 250
	// MaxOutputBytes bounds model output to 1MB before parsing.
	MaxOutputBytes = 1024 * 1024
	// DraftVersion is the supported JSON Schema draft.
	DraftVersion = "http://json-schema.org/draft-07/schema#"
)

var (
	ErrSchemaTooLarge     = errors.New("schema exceeds maximum allowed size of 64KB")
	ErrSchemaTooDeep      = errors.New("schema exceeds maximum allowed depth of 10")
	ErrRemoteRefForbidden = errors.New("remote schema references ($ref) are forbidden")
	ErrInvalidRegex       = errors.New("regex pattern is invalid or exceeds maximum length")
	ErrOutputTooLarge     = errors.New("model output exceeds maximum allowed size of 1MB")
	ErrInvalidJSON        = errors.New("output is not valid JSON")
	ErrSchemaViolation    = errors.New("output does not satisfy JSON schema")
)

// ValidationError provides structured error information for a schema violation.
type ValidationError struct {
	Path    string
	Message string
	Rule    string
}

func (e *ValidationError) Error() string {
	if e.Path == "" || e.Path == "#" {
		return fmt.Sprintf("%v: %s (rule: %s)", ErrSchemaViolation, e.Message, e.Rule)
	}
	return fmt.Sprintf("%v at %s: %s (rule: %s)", ErrSchemaViolation, e.Path, e.Message, e.Rule)
}

func (e *ValidationError) Unwrap() error {
	return ErrSchemaViolation
}

// Schema represents a compiled JSON Schema (Draft-07 subset).
type Schema struct {
	// Boolean schema
	isBool  bool
	boolVal bool

	id    string
	title string
	desc  string

	// Types
	types []string

	// Combinators
	allOf []*Schema
	anyOf []*Schema
	oneOf []*Schema
	not   *Schema

	// Reference
	ref    string
	refSub *Schema

	// Enums and Constants
	hasEnum  bool
	enum     []any
	hasConst bool
	constVal any

	// Numeric constraints
	multipleOf       *float64
	maximum          *float64
	exclusiveMaximum *float64
	minimum          *float64
	exclusiveMinimum *float64

	// String constraints
	maxLength *int
	minLength *int
	pattern   *regexp.Regexp
	format    string

	// Array constraints
	items           *Schema
	itemsTuple      []*Schema
	additionalItems *Schema
	allowAddItems   *bool
	maxItems        *int
	minItems        *int
	uniqueItems     bool

	// Object constraints
	maxProperties        *int
	minProperties        *int
	required             []string
	properties           map[string]*Schema
	patternProperties    map[*regexp.Regexp]*Schema
	additionalProperties *Schema
	allowAddProps        *bool
	propertyNames        *Schema

	// Root context for local definitions
	definitions map[string]*Schema
}

// CompileBytes compiles a JSON Schema from raw UTF-8 bytes.
func CompileBytes(data []byte) (*Schema, error) {
	if len(data) > MaxSchemaBytes {
		return nil, ErrSchemaTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("empty schema")
	}

	var raw any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON in schema: %w", err)
	}
	if dec.More() {
		return nil, errors.New("unexpected trailing content after schema JSON")
	}

	return Compile(raw)
}

// Compile compiles a JSON Schema from a parsed Go representation (bool or map[string]any).
func Compile(raw any) (*Schema, error) {
	// Verify serialization size <= 64KB
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize schema: %w", err)
	}
	if len(b) > MaxSchemaBytes {
		return nil, ErrSchemaTooLarge
	}

	root := &Schema{definitions: make(map[string]*Schema)}
	if err := compileSchema(raw, root, root, 1, "#"); err != nil {
		return nil, err
	}
	// Resolve references
	if err := resolveRefs(root, root); err != nil {
		return nil, err
	}
	return root, nil
}

func compileSchema(raw any, s *Schema, root *Schema, depth int, path string) error {
	if depth > MaxSchemaDepth {
		return ErrSchemaTooDeep
	}

	switch v := raw.(type) {
	case bool:
		s.isBool = true
		s.boolVal = v
		return nil
	case map[string]any:
		return compileObjectSchema(v, s, root, depth, path)
	default:
		return fmt.Errorf("schema at %s must be a boolean or object, got %T", path, raw)
	}
}

func compileObjectSchema(m map[string]any, s *Schema, root *Schema, depth int, path string) error {
	// First collect definitions/defs if at root
	if depth == 1 {
		for _, defKey := range []string{"definitions", "$defs"} {
			if defs, ok := m[defKey].(map[string]any); ok {
				for name, defRaw := range defs {
					defSchema := &Schema{definitions: root.definitions}
					defPath := fmt.Sprintf("#/%s/%s", defKey, name)
					if err := compileSchema(defRaw, defSchema, root, depth+1, defPath); err != nil {
						return err
					}
					root.definitions[defPath] = defSchema
					root.definitions["#/definitions/"+name] = defSchema
					root.definitions["#/$defs/"+name] = defSchema
				}
			}
		}
	}

	// $ref
	if refVal, ok := m["$ref"].(string); ok {
		// Strict check for remote or forbidden references
		low := strings.ToLower(refVal)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") ||
			strings.HasPrefix(low, "ftp://") || strings.HasPrefix(low, "//") ||
			!strings.HasPrefix(refVal, "#") {
			return fmt.Errorf("%w: %q", ErrRemoteRefForbidden, refVal)
		}
		s.ref = refVal
		return nil
	}

	// type
	if tVal, ok := m["type"]; ok {
		switch tv := tVal.(type) {
		case string:
			if !isValidType(tv) {
				return fmt.Errorf("unknown schema type %q at %s", tv, path)
			}
			s.types = []string{tv}
		case []any:
			for _, item := range tv {
				str, ok := item.(string)
				if !ok || !isValidType(str) {
					return fmt.Errorf("unknown schema type %v at %s", item, path)
				}
				s.types = append(s.types, str)
			}
		default:
			return fmt.Errorf("'type' at %s must be string or array of strings", path)
		}
	}

	// enum
	if enumVal, ok := m["enum"].([]any); ok {
		s.hasEnum = true
		s.enum = enumVal
	}

	// const
	if constVal, ok := m["const"]; ok {
		s.hasConst = true
		s.constVal = constVal
	}

	// Numerics
	if val, ok := getFloat(m["multipleOf"]); ok {
		if val <= 0 {
			return fmt.Errorf("'multipleOf' at %s must be strictly positive", path)
		}
		s.multipleOf = &val
	}
	if val, ok := getFloat(m["maximum"]); ok {
		s.maximum = &val
	}
	if val, ok := getFloat(m["exclusiveMaximum"]); ok {
		s.exclusiveMaximum = &val
	}
	if val, ok := getFloat(m["minimum"]); ok {
		s.minimum = &val
	}
	if val, ok := getFloat(m["exclusiveMinimum"]); ok {
		s.exclusiveMinimum = &val
	}

	// Strings
	if val, ok := getInt(m["maxLength"]); ok {
		if val < 0 {
			return fmt.Errorf("'maxLength' at %s cannot be negative", path)
		}
		s.maxLength = &val
	}
	if val, ok := getInt(m["minLength"]); ok {
		if val < 0 {
			return fmt.Errorf("'minLength' at %s cannot be negative", path)
		}
		s.minLength = &val
	}
	if patVal, ok := m["pattern"].(string); ok {
		if len(patVal) > MaxRegexLength {
			return fmt.Errorf("%w: pattern at %s exceeds %d chars", ErrInvalidRegex, path, MaxRegexLength)
		}
		re, err := regexp.Compile(patVal)
		if err != nil {
			return fmt.Errorf("%w at %s: %v", ErrInvalidRegex, path, err)
		}
		s.pattern = re
	}
	if fmtVal, ok := m["format"].(string); ok {
		s.format = fmtVal
	}

	// Arrays
	if itemsVal, ok := m["items"]; ok {
		switch it := itemsVal.(type) {
		case map[string]any, bool:
			sub := &Schema{definitions: root.definitions}
			if err := compileSchema(it, sub, root, depth+1, path+"/items"); err != nil {
				return err
			}
			s.items = sub
		case []any:
			for i, elem := range it {
				sub := &Schema{definitions: root.definitions}
				subPath := fmt.Sprintf("%s/items/%d", path, i)
				if err := compileSchema(elem, sub, root, depth+1, subPath); err != nil {
					return err
				}
				s.itemsTuple = append(s.itemsTuple, sub)
			}
		default:
			return fmt.Errorf("'items' at %s must be a schema or array of schemas", path)
		}
	}

	if addItems, ok := m["additionalItems"]; ok {
		switch ai := addItems.(type) {
		case bool:
			s.allowAddItems = &ai
		case map[string]any:
			sub := &Schema{definitions: root.definitions}
			if err := compileSchema(ai, sub, root, depth+1, path+"/additionalItems"); err != nil {
				return err
			}
			s.additionalItems = sub
		}
	}

	if val, ok := getInt(m["maxItems"]); ok {
		if val < 0 {
			return fmt.Errorf("'maxItems' at %s cannot be negative", path)
		}
		s.maxItems = &val
	}
	if val, ok := getInt(m["minItems"]); ok {
		if val < 0 {
			return fmt.Errorf("'minItems' at %s cannot be negative", path)
		}
		s.minItems = &val
	}
	if val, ok := m["uniqueItems"].(bool); ok {
		s.uniqueItems = val
	}

	// Objects
	if propsVal, ok := m["properties"].(map[string]any); ok {
		s.properties = make(map[string]*Schema)
		for k, pRaw := range propsVal {
			sub := &Schema{definitions: root.definitions}
			if err := compileSchema(pRaw, sub, root, depth+1, path+"/properties/"+k); err != nil {
				return err
			}
			s.properties[k] = sub
		}
	}

	if patPropsVal, ok := m["patternProperties"].(map[string]any); ok {
		s.patternProperties = make(map[*regexp.Regexp]*Schema)
		for patStr, pRaw := range patPropsVal {
			if len(patStr) > MaxRegexLength {
				return fmt.Errorf("%w: patternProperties key at %s exceeds %d chars", ErrInvalidRegex, path, MaxRegexLength)
			}
			re, err := regexp.Compile(patStr)
			if err != nil {
				return fmt.Errorf("%w for patternProperties at %s: %v", ErrInvalidRegex, path, err)
			}
			sub := &Schema{definitions: root.definitions}
			if err := compileSchema(pRaw, sub, root, depth+1, path+"/patternProperties/"+patStr); err != nil {
				return err
			}
			s.patternProperties[re] = sub
		}
	}

	if addProps, ok := m["additionalProperties"]; ok {
		switch ap := addProps.(type) {
		case bool:
			s.allowAddProps = &ap
		case map[string]any:
			sub := &Schema{definitions: root.definitions}
			if err := compileSchema(ap, sub, root, depth+1, path+"/additionalProperties"); err != nil {
				return err
			}
			s.additionalProperties = sub
		default:
			return fmt.Errorf("'additionalProperties' at %s must be boolean or schema", path)
		}
	}

	if reqVal, ok := m["required"].([]any); ok {
		for _, r := range reqVal {
			if rStr, ok := r.(string); ok {
				s.required = append(s.required, rStr)
			}
		}
	}

	if val, ok := getInt(m["maxProperties"]); ok {
		if val < 0 {
			return fmt.Errorf("'maxProperties' at %s cannot be negative", path)
		}
		s.maxProperties = &val
	}
	if val, ok := getInt(m["minProperties"]); ok {
		if val < 0 {
			return fmt.Errorf("'minProperties' at %s cannot be negative", path)
		}
		s.minProperties = &val
	}

	// Combinators
	for key, target := range map[string]*[]*Schema{"allOf": &s.allOf, "anyOf": &s.anyOf, "oneOf": &s.oneOf} {
		if arr, ok := m[key].([]any); ok {
			for i, elem := range arr {
				sub := &Schema{definitions: root.definitions}
				subPath := fmt.Sprintf("%s/%s/%d", path, key, i)
				if err := compileSchema(elem, sub, root, depth+1, subPath); err != nil {
					return err
				}
				*target = append(*target, sub)
			}
		}
	}

	if notVal, ok := m["not"]; ok {
		sub := &Schema{definitions: root.definitions}
		if err := compileSchema(notVal, sub, root, depth+1, path+"/not"); err != nil {
			return err
		}
		s.not = sub
	}

	return nil
}

func resolveRefs(s *Schema, root *Schema) error {
	if s.ref != "" {
		target, ok := root.definitions[s.ref]
		if !ok {
			return fmt.Errorf("reference not found: %q", s.ref)
		}
		s.refSub = target
	}

	if s.items != nil {
		if err := resolveRefs(s.items, root); err != nil {
			return err
		}
	}
	for _, it := range s.itemsTuple {
		if err := resolveRefs(it, root); err != nil {
			return err
		}
	}
	if s.additionalItems != nil {
		if err := resolveRefs(s.additionalItems, root); err != nil {
			return err
		}
	}
	for _, prop := range s.properties {
		if err := resolveRefs(prop, root); err != nil {
			return err
		}
	}
	for _, prop := range s.patternProperties {
		if err := resolveRefs(prop, root); err != nil {
			return err
		}
	}
	if s.additionalProperties != nil {
		if err := resolveRefs(s.additionalProperties, root); err != nil {
			return err
		}
	}
	for _, sub := range s.allOf {
		if err := resolveRefs(sub, root); err != nil {
			return err
		}
	}
	for _, sub := range s.anyOf {
		if err := resolveRefs(sub, root); err != nil {
			return err
		}
	}
	for _, sub := range s.oneOf {
		if err := resolveRefs(sub, root); err != nil {
			return err
		}
	}
	if s.not != nil {
		if err := resolveRefs(s.not, root); err != nil {
			return err
		}
	}
	return nil
}

// ValidateJSON checks output size, strictly parses JSON (no code fence stripping, no repair),
// and validates the parsed value against the schema.
func (s *Schema) ValidateJSON(data []byte) (any, error) {
	if len(data) > MaxOutputBytes {
		return nil, ErrOutputTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty response", ErrInvalidJSON)
	}

	var val any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&val); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing characters after JSON", ErrInvalidJSON)
	}

	if err := s.Validate(val); err != nil {
		return nil, err
	}
	return val, nil
}

// Validate validates an in-memory JSON value against the schema.
func (s *Schema) Validate(val any) error {
	return s.validateWithPath(val, "#")
}

func (s *Schema) validateWithPath(val any, path string) error {
	if s.isBool {
		if !s.boolVal {
			return &ValidationError{Path: path, Message: "value rejected by false schema", Rule: "boolean_schema"}
		}
		return nil
	}

	if s.refSub != nil {
		return s.refSub.validateWithPath(val, path)
	}

	// 1. Type validation
	if len(s.types) > 0 {
		matched := false
		for _, t := range s.types {
			if matchType(val, t) {
				matched = true
				break
			}
		}
		if !matched {
			return &ValidationError{
				Path:    path,
				Message: fmt.Sprintf("expected type %v, got %s", s.types, jsonTypeOf(val)),
				Rule:    "type",
			}
		}
	}

	// 2. Enum
	if s.hasEnum {
		matched := false
		for _, e := range s.enum {
			if deepEqual(val, e) {
				matched = true
				break
			}
		}
		if !matched {
			return &ValidationError{Path: path, Message: "value does not match any allowed enum value", Rule: "enum"}
		}
	}

	// 3. Const
	if s.hasConst {
		if !deepEqual(val, s.constVal) {
			return &ValidationError{Path: path, Message: "value does not match expected const value", Rule: "const"}
		}
	}

	// 4. Numeric constraints
	if num, ok := toFloat(val); ok {
		if s.multipleOf != nil {
			div := num / *s.multipleOf
			if math.Abs(div-math.Round(div)) > 1e-9 {
				return &ValidationError{Path: path, Message: fmt.Sprintf("%v is not a multiple of %v", num, *s.multipleOf), Rule: "multipleOf"}
			}
		}
		if s.maximum != nil && num > *s.maximum {
			return &ValidationError{Path: path, Message: fmt.Sprintf("%v exceeds maximum %v", num, *s.maximum), Rule: "maximum"}
		}
		if s.exclusiveMaximum != nil && num >= *s.exclusiveMaximum {
			return &ValidationError{Path: path, Message: fmt.Sprintf("%v is greater than or equal to exclusiveMaximum %v", num, *s.exclusiveMaximum), Rule: "exclusiveMaximum"}
		}
		if s.minimum != nil && num < *s.minimum {
			return &ValidationError{Path: path, Message: fmt.Sprintf("%v is less than minimum %v", num, *s.minimum), Rule: "minimum"}
		}
		if s.exclusiveMinimum != nil && num <= *s.exclusiveMinimum {
			return &ValidationError{Path: path, Message: fmt.Sprintf("%v is less than or equal to exclusiveMinimum %v", num, *s.exclusiveMinimum), Rule: "exclusiveMinimum"}
		}
	}

	// 5. String constraints
	if str, ok := val.(string); ok {
		runes := []rune(str)
		length := len(runes)
		if s.maxLength != nil && length > *s.maxLength {
			return &ValidationError{Path: path, Message: fmt.Sprintf("string length %d exceeds maxLength %d", length, *s.maxLength), Rule: "maxLength"}
		}
		if s.minLength != nil && length < *s.minLength {
			return &ValidationError{Path: path, Message: fmt.Sprintf("string length %d is less than minLength %d", length, *s.minLength), Rule: "minLength"}
		}
		if s.pattern != nil && !s.pattern.MatchString(str) {
			return &ValidationError{Path: path, Message: fmt.Sprintf("string does not match pattern %q", s.pattern.String()), Rule: "pattern"}
		}
	}

	// 6. Array constraints
	if arr, ok := val.([]any); ok {
		if s.maxItems != nil && len(arr) > *s.maxItems {
			return &ValidationError{Path: path, Message: fmt.Sprintf("array length %d exceeds maxItems %d", len(arr), *s.maxItems), Rule: "maxItems"}
		}
		if s.minItems != nil && len(arr) < *s.minItems {
			return &ValidationError{Path: path, Message: fmt.Sprintf("array length %d is less than minItems %d", len(arr), *s.minItems), Rule: "minItems"}
		}
		if s.uniqueItems {
			for i := 0; i < len(arr); i++ {
				for j := i + 1; j < len(arr); j++ {
					if deepEqual(arr[i], arr[j]) {
						return &ValidationError{Path: path, Message: "array items must be unique", Rule: "uniqueItems"}
					}
				}
			}
		}

		if s.items != nil {
			for i, elem := range arr {
				elemPath := fmt.Sprintf("%s/%d", path, i)
				if err := s.items.validateWithPath(elem, elemPath); err != nil {
					return err
				}
			}
		} else if len(s.itemsTuple) > 0 {
			for i, elem := range arr {
				elemPath := fmt.Sprintf("%s/%d", path, i)
				if i < len(s.itemsTuple) {
					if err := s.itemsTuple[i].validateWithPath(elem, elemPath); err != nil {
						return err
					}
				} else {
					if s.allowAddItems != nil && !*s.allowAddItems {
						return &ValidationError{Path: elemPath, Message: "additional items not allowed in tuple", Rule: "additionalItems"}
					}
					if s.additionalItems != nil {
						if err := s.additionalItems.validateWithPath(elem, elemPath); err != nil {
							return err
						}
					}
				}
			}
		}
	}

	// 7. Object constraints
	if obj, ok := val.(map[string]any); ok {
		if s.maxProperties != nil && len(obj) > *s.maxProperties {
			return &ValidationError{Path: path, Message: fmt.Sprintf("object properties count %d exceeds maxProperties %d", len(obj), *s.maxProperties), Rule: "maxProperties"}
		}
		if s.minProperties != nil && len(obj) < *s.minProperties {
			return &ValidationError{Path: path, Message: fmt.Sprintf("object properties count %d is less than minProperties %d", len(obj), *s.minProperties), Rule: "minProperties"}
		}

		// Required properties
		for _, req := range s.required {
			if _, exists := obj[req]; !exists {
				return &ValidationError{
					Path:    path,
					Message: fmt.Sprintf("missing required property %q", req),
					Rule:    "required",
				}
			}
		}

		// Property validation
		for k, v := range obj {
			propPath := fmt.Sprintf("%s/%s", path, k)
			matchedSchema := false

			if s.properties != nil {
				if propSchema, ok := s.properties[k]; ok {
					matchedSchema = true
					if err := propSchema.validateWithPath(v, propPath); err != nil {
						return err
					}
				}
			}

			if s.patternProperties != nil {
				for pat, patSchema := range s.patternProperties {
					if pat.MatchString(k) {
						matchedSchema = true
						if err := patSchema.validateWithPath(v, propPath); err != nil {
							return err
						}
					}
				}
			}

			if !matchedSchema {
				if s.allowAddProps != nil && !*s.allowAddProps {
					return &ValidationError{
						Path:    propPath,
						Message: fmt.Sprintf("additional property %q is not allowed", k),
						Rule:    "additionalProperties",
					}
				}
				if s.additionalProperties != nil {
					if err := s.additionalProperties.validateWithPath(v, propPath); err != nil {
						return err
					}
				}
			}
		}
	}

	// 8. Combinators
	if len(s.allOf) > 0 {
		for _, sub := range s.allOf {
			if err := sub.validateWithPath(val, path); err != nil {
				return err
			}
		}
	}

	if len(s.anyOf) > 0 {
		anyMatched := false
		for _, sub := range s.anyOf {
			if err := sub.validateWithPath(val, path); err == nil {
				anyMatched = true
				break
			}
		}
		if !anyMatched {
			return &ValidationError{Path: path, Message: "value does not match any schema in anyOf", Rule: "anyOf"}
		}
	}

	if len(s.oneOf) > 0 {
		matchCount := 0
		for _, sub := range s.oneOf {
			if err := sub.validateWithPath(val, path); err == nil {
				matchCount++
			}
		}
		if matchCount != 1 {
			return &ValidationError{Path: path, Message: fmt.Sprintf("value matched %d schemas in oneOf, expected exactly 1", matchCount), Rule: "oneOf"}
		}
	}

	if s.not != nil {
		if err := s.not.validateWithPath(val, path); err == nil {
			return &ValidationError{Path: path, Message: "value must not match schema in 'not'", Rule: "not"}
		}
	}

	return nil
}

func isValidType(t string) bool {
	switch t {
	case "null", "boolean", "object", "array", "number", "string", "integer":
		return true
	default:
		return false
	}
}

func jsonTypeOf(val any) string {
	if val == nil {
		return "null"
	}
	switch v := val.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if _, err := v.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case float64:
		if math.Floor(v) == v && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return "integer"
		}
		return "number"
	case int, int32, int64:
		return "integer"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", val)
	}
}

func matchType(val any, expected string) bool {
	if val == nil {
		return expected == "null"
	}
	switch expected {
	case "null":
		return val == nil
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "string":
		_, ok := val.(string)
		return ok
	case "number":
		_, ok := toFloat(val)
		return ok
	case "integer":
		if n, ok := toFloat(val); ok {
			return math.Floor(n) == n && !math.IsNaN(n) && !math.IsInf(n, 0)
		}
		return false
	case "array":
		_, ok := val.([]any)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	default:
		return false
	}
}

func toFloat(val any) (float64, bool) {
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func getFloat(val any) (float64, bool) {
	return toFloat(val)
}

func getInt(val any) (int, bool) {
	f, ok := toFloat(val)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func deepEqual(a, b any) bool {
	// Normalize json.Number / float64 comparisons
	fa, oka := toFloat(a)
	fb, okb := toFloat(b)
	if oka && okb {
		return fa == fb
	}

	if a == nil || b == nil {
		return a == b
	}

	switch va := a.(type) {
	case bool:
		vb, ok := b.(bool)
		return ok && va == vb
	case string:
		vb, ok := b.(string)
		return ok && va == vb
	case []any:
		vb, ok := b.([]any)
		if !ok || len(va) != len(vb) {
			return false
		}
		for i := range va {
			if !deepEqual(va[i], vb[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		vb, ok := b.(map[string]any)
		if !ok || len(va) != len(vb) {
			return false
		}
		for k, valA := range va {
			valB, exists := vb[k]
			if !exists || !deepEqual(valA, valB) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
