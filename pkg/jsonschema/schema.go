package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	// MaxInstanceDepth bounds the runtime instance traversal depth.
	MaxInstanceDepth = 32
	// MaxValidationSteps bounds the total evaluation steps to prevent DoS.
	MaxValidationSteps = 100_000
	// MaxInstanceItems bounds array length for quadratic operations.
	MaxInstanceItems = 100_000
	// MaxInstanceProps bounds object property count.
	MaxInstanceProps = 10_000
	// DraftVersion is the supported JSON Schema draft.
	DraftVersion = "http://json-schema.org/draft-07/schema#"
)

var (
	ErrSchemaTooLarge        = errors.New("schema exceeds maximum allowed size of 64KB")
	ErrSchemaTooDeep         = errors.New("schema exceeds maximum allowed depth of 10")
	ErrRemoteRefForbidden    = errors.New("remote schema references ($ref) are forbidden")
	ErrInvalidRegex          = errors.New("regex pattern is invalid or exceeds maximum length")
	ErrOutputTooLarge        = errors.New("model output exceeds maximum allowed size of 1MB")
	ErrInvalidJSON           = errors.New("output is not valid JSON")
	ErrSchemaViolation       = errors.New("output does not satisfy JSON schema")
	ErrInvalidSchema         = errors.New("invalid JSON schema definition")
	ErrResourceLimitExceeded = errors.New("validation resource limit exceeded")
	ErrReferenceNotFound     = errors.New("schema reference not found")
	ErrCircularReference     = errors.New("unresolvable circular schema reference")
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

type dependencyConstraint struct {
	requiredProps []string
	schema        *Schema
}

// Schema represents a compiled JSON Schema (Draft-07).
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
	contains        *Schema
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
	dependencies         map[string]*dependencyConstraint

	// Root context for local definitions and pointers
	definitions map[string]*Schema
	rootRaw     any
	rootSchema  *Schema
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

	root := &Schema{
		definitions: make(map[string]*Schema),
		rootRaw:     raw,
	}
	root.rootSchema = root

	if err := compileSchema(raw, root, root, 1, "#"); err != nil {
		return nil, err
	}
	// Resolve local references
	var refSchemas []*Schema
	if err := resolveRefs(root, root, make(map[*Schema]bool), &refSchemas); err != nil {
		return nil, err
	}
	// Check for direct circular reference loops (alias chains without validation guards)
	for _, refSchema := range refSchemas {
		curr := refSchema
		visitedChain := make(map[*Schema]bool)
		for curr != nil && curr.ref != "" {
			if visitedChain[curr] {
				return nil, fmt.Errorf("%w: unguardable reference cycle detected at %s", ErrCircularReference, refSchema.ref)
			}
			visitedChain[curr] = true
			curr = curr.refSub
		}
	}
	return root, nil
}

func compileSchema(raw any, s *Schema, root *Schema, depth int, path string) error {
	if depth > MaxSchemaDepth {
		return ErrSchemaTooDeep
	}

	s.rootRaw = root.rootRaw
	s.rootSchema = root

	switch v := raw.(type) {
	case bool:
		s.isBool = true
		s.boolVal = v
		return nil
	case map[string]any:
		return compileObjectSchema(v, s, root, depth, path)
	default:
		return fmt.Errorf("%w: schema at %s must be boolean or object, got %T", ErrInvalidSchema, path, raw)
	}
}

func compileObjectSchema(m map[string]any, s *Schema, root *Schema, depth int, path string) error {
	// Register self in root definitions for direct pointer access if applicable
	if path != "" {
		root.definitions[path] = s
	}

	// 1. Collect definitions/$defs at root or current level
	for _, defKey := range []string{"definitions", "$defs"} {
		if defsVal, ok := m[defKey]; ok {
			defs, ok := defsVal.(map[string]any)
			if !ok {
				return fmt.Errorf("%w: '%s' at %s must be an object", ErrInvalidSchema, defKey, path)
			}
			for name, defRaw := range defs {
				defSchema := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
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

	// 2. $ref
	if refVal, ok := m["$ref"]; ok {
		refStr, ok := refVal.(string)
		if !ok {
			return fmt.Errorf("%w: '$ref' at %s must be a string", ErrInvalidSchema, path)
		}
		low := strings.ToLower(refStr)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") ||
			strings.HasPrefix(low, "ftp://") || strings.HasPrefix(low, "//") ||
			!strings.HasPrefix(refStr, "#") {
			return fmt.Errorf("%w: %q", ErrRemoteRefForbidden, refStr)
		}
		s.ref = refStr
		// In Draft-07, when $ref is present on an object, other keywords are typically ignored
		return nil
	}

	// 3. type
	if tVal, ok := m["type"]; ok {
		switch tv := tVal.(type) {
		case string:
			if !isValidType(tv) {
				return fmt.Errorf("%w: unknown schema type %q at %s", ErrInvalidSchema, tv, path)
			}
			s.types = []string{tv}
		case []any:
			if len(tv) == 0 {
				return fmt.Errorf("%w: 'type' array at %s cannot be empty", ErrInvalidSchema, path)
			}
			seen := make(map[string]bool)
			for _, item := range tv {
				str, ok := item.(string)
				if !ok || !isValidType(str) {
					return fmt.Errorf("%w: unknown schema type %v at %s", ErrInvalidSchema, item, path)
				}
				if seen[str] {
					return fmt.Errorf("%w: duplicate type %q in 'type' array at %s", ErrInvalidSchema, str, path)
				}
				seen[str] = true
				s.types = append(s.types, str)
			}
		default:
			return fmt.Errorf("%w: 'type' at %s must be string or array of strings", ErrInvalidSchema, path)
		}
	}

	// 4. enum
	if enumVal, ok := m["enum"]; ok {
		arr, ok := enumVal.([]any)
		if !ok {
			return fmt.Errorf("%w: 'enum' at %s must be an array", ErrInvalidSchema, path)
		}
		s.hasEnum = true
		s.enum = arr
	}

	// 5. const
	if constVal, ok := m["const"]; ok {
		s.hasConst = true
		s.constVal = constVal
	}

	// 6. Numerics
	if val, exists := m["multipleOf"]; exists {
		f, ok := getFloatStrict(val)
		if !ok {
			return fmt.Errorf("%w: 'multipleOf' at %s must be a number", ErrInvalidSchema, path)
		}
		if f <= 0 {
			return fmt.Errorf("%w: 'multipleOf' at %s must be strictly greater than 0", ErrInvalidSchema, path)
		}
		s.multipleOf = &f
	}
	if val, exists := m["maximum"]; exists {
		f, ok := getFloatStrict(val)
		if !ok {
			return fmt.Errorf("%w: 'maximum' at %s must be a number", ErrInvalidSchema, path)
		}
		s.maximum = &f
	}
	if val, exists := m["exclusiveMaximum"]; exists {
		f, ok := getFloatStrict(val)
		if !ok {
			return fmt.Errorf("%w: 'exclusiveMaximum' at %s must be a number (Draft-07)", ErrInvalidSchema, path)
		}
		s.exclusiveMaximum = &f
	}
	if val, exists := m["minimum"]; exists {
		f, ok := getFloatStrict(val)
		if !ok {
			return fmt.Errorf("%w: 'minimum' at %s must be a number", ErrInvalidSchema, path)
		}
		s.minimum = &f
	}
	if val, exists := m["exclusiveMinimum"]; exists {
		f, ok := getFloatStrict(val)
		if !ok {
			return fmt.Errorf("%w: 'exclusiveMinimum' at %s must be a number (Draft-07)", ErrInvalidSchema, path)
		}
		s.exclusiveMinimum = &f
	}

	// 7. Strings
	if val, exists := m["maxLength"]; exists {
		i, ok := getIntStrict(val)
		if !ok || i < 0 {
			return fmt.Errorf("%w: 'maxLength' at %s must be a non-negative integer", ErrInvalidSchema, path)
		}
		s.maxLength = &i
	}
	if val, exists := m["minLength"]; exists {
		i, ok := getIntStrict(val)
		if !ok || i < 0 {
			return fmt.Errorf("%w: 'minLength' at %s must be a non-negative integer", ErrInvalidSchema, path)
		}
		s.minLength = &i
	}
	if patVal, exists := m["pattern"]; exists {
		patStr, ok := patVal.(string)
		if !ok {
			return fmt.Errorf("%w: 'pattern' at %s must be a string", ErrInvalidSchema, path)
		}
		if len(patStr) > MaxRegexLength {
			return fmt.Errorf("%w: pattern at %s exceeds %d chars", ErrInvalidRegex, path, MaxRegexLength)
		}
		re, err := regexp.Compile(patStr)
		if err != nil {
			return fmt.Errorf("%w at %s: %v", ErrInvalidRegex, path, err)
		}
		s.pattern = re
	}
	if fmtVal, exists := m["format"]; exists {
		fmtStr, ok := fmtVal.(string)
		if !ok {
			return fmt.Errorf("%w: 'format' at %s must be a string", ErrInvalidSchema, path)
		}
		s.format = fmtStr
	}

	// 8. Arrays
	if itemsVal, exists := m["items"]; exists {
		switch it := itemsVal.(type) {
		case map[string]any, bool:
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(it, sub, root, depth+1, path+"/items"); err != nil {
				return err
			}
			s.items = sub
		case []any:
			for i, elem := range it {
				sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
				subPath := fmt.Sprintf("%s/items/%d", path, i)
				if err := compileSchema(elem, sub, root, depth+1, subPath); err != nil {
					return err
				}
				s.itemsTuple = append(s.itemsTuple, sub)
			}
		default:
			return fmt.Errorf("%w: 'items' at %s must be a schema or array of schemas", ErrInvalidSchema, path)
		}
	}

	if addItems, exists := m["additionalItems"]; exists {
		switch ai := addItems.(type) {
		case bool:
			s.allowAddItems = &ai
		case map[string]any:
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(ai, sub, root, depth+1, path+"/additionalItems"); err != nil {
				return err
			}
			s.additionalItems = sub
		default:
			return fmt.Errorf("%w: 'additionalItems' at %s must be a boolean or schema", ErrInvalidSchema, path)
		}
	}

	if containsVal, exists := m["contains"]; exists {
		switch cv := containsVal.(type) {
		case map[string]any, bool:
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(cv, sub, root, depth+1, path+"/contains"); err != nil {
				return err
			}
			s.contains = sub
		default:
			return fmt.Errorf("%w: 'contains' at %s must be a boolean or schema", ErrInvalidSchema, path)
		}
	}

	if val, exists := m["maxItems"]; exists {
		i, ok := getIntStrict(val)
		if !ok || i < 0 {
			return fmt.Errorf("%w: 'maxItems' at %s must be a non-negative integer", ErrInvalidSchema, path)
		}
		s.maxItems = &i
	}
	if val, exists := m["minItems"]; exists {
		i, ok := getIntStrict(val)
		if !ok || i < 0 {
			return fmt.Errorf("%w: 'minItems' at %s must be a non-negative integer", ErrInvalidSchema, path)
		}
		s.minItems = &i
	}
	if val, exists := m["uniqueItems"]; exists {
		b, ok := val.(bool)
		if !ok {
			return fmt.Errorf("%w: 'uniqueItems' at %s must be a boolean", ErrInvalidSchema, path)
		}
		s.uniqueItems = b
	}

	// 9. Objects
	if propsVal, exists := m["properties"]; exists {
		propsMap, ok := propsVal.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: 'properties' at %s must be an object", ErrInvalidSchema, path)
		}
		s.properties = make(map[string]*Schema)
		for k, pRaw := range propsMap {
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(pRaw, sub, root, depth+1, path+"/properties/"+k); err != nil {
				return err
			}
			s.properties[k] = sub
		}
	}

	if patPropsVal, exists := m["patternProperties"]; exists {
		patMap, ok := patPropsVal.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: 'patternProperties' at %s must be an object", ErrInvalidSchema, path)
		}
		s.patternProperties = make(map[*regexp.Regexp]*Schema)
		for patStr, pRaw := range patMap {
			if len(patStr) > MaxRegexLength {
				return fmt.Errorf("%w: patternProperties key at %s exceeds %d chars", ErrInvalidRegex, path, MaxRegexLength)
			}
			re, err := regexp.Compile(patStr)
			if err != nil {
				return fmt.Errorf("%w for patternProperties at %s: %v", ErrInvalidRegex, path, err)
			}
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(pRaw, sub, root, depth+1, path+"/patternProperties/"+patStr); err != nil {
				return err
			}
			s.patternProperties[re] = sub
		}
	}

	if addProps, exists := m["additionalProperties"]; exists {
		switch ap := addProps.(type) {
		case bool:
			s.allowAddProps = &ap
		case map[string]any:
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(ap, sub, root, depth+1, path+"/additionalProperties"); err != nil {
				return err
			}
			s.additionalProperties = sub
		default:
			return fmt.Errorf("%w: 'additionalProperties' at %s must be boolean or schema", ErrInvalidSchema, path)
		}
	}

	if pnVal, exists := m["propertyNames"]; exists {
		switch pn := pnVal.(type) {
		case map[string]any, bool:
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(pn, sub, root, depth+1, path+"/propertyNames"); err != nil {
				return err
			}
			s.propertyNames = sub
		default:
			return fmt.Errorf("%w: 'propertyNames' at %s must be boolean or schema", ErrInvalidSchema, path)
		}
	}

	if reqVal, exists := m["required"]; exists {
		reqArr, ok := reqVal.([]any)
		if !ok {
			return fmt.Errorf("%w: 'required' at %s must be an array of strings", ErrInvalidSchema, path)
		}
		seen := make(map[string]bool)
		for i, r := range reqArr {
			rStr, ok := r.(string)
			if !ok {
				return fmt.Errorf("%w: 'required[%d]' at %s must be a string", ErrInvalidSchema, i, path)
			}
			if seen[rStr] {
				return fmt.Errorf("%w: duplicate required property %q at %s", ErrInvalidSchema, rStr, path)
			}
			seen[rStr] = true
			s.required = append(s.required, rStr)
		}
	}

	if val, exists := m["maxProperties"]; exists {
		i, ok := getIntStrict(val)
		if !ok || i < 0 {
			return fmt.Errorf("%w: 'maxProperties' at %s must be a non-negative integer", ErrInvalidSchema, path)
		}
		s.maxProperties = &i
	}
	if val, exists := m["minProperties"]; exists {
		i, ok := getIntStrict(val)
		if !ok || i < 0 {
			return fmt.Errorf("%w: 'minProperties' at %s must be a non-negative integer", ErrInvalidSchema, path)
		}
		s.minProperties = &i
	}

	// 10. Dependencies
	if depsVal, exists := m["dependencies"]; exists {
		depsMap, ok := depsVal.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: 'dependencies' at %s must be an object", ErrInvalidSchema, path)
		}
		s.dependencies = make(map[string]*dependencyConstraint)
		for propName, depRaw := range depsMap {
			switch dv := depRaw.(type) {
			case []any:
				var reqProps []string
				seen := make(map[string]bool)
				for i, p := range dv {
					pStr, ok := p.(string)
					if !ok {
						return fmt.Errorf("%w: 'dependencies.%s[%d]' at %s must be a string", ErrInvalidSchema, propName, i, path)
					}
					if seen[pStr] {
						return fmt.Errorf("%w: duplicate dependency property %q at %s", ErrInvalidSchema, pStr, path)
					}
					seen[pStr] = true
					reqProps = append(reqProps, pStr)
				}
				s.dependencies[propName] = &dependencyConstraint{requiredProps: reqProps}
			case map[string]any, bool:
				sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
				if err := compileSchema(dv, sub, root, depth+1, path+"/dependencies/"+propName); err != nil {
					return err
				}
				s.dependencies[propName] = &dependencyConstraint{schema: sub}
			default:
				return fmt.Errorf("%w: 'dependencies.%s' at %s must be an array of strings or schema", ErrInvalidSchema, propName, path)
			}
		}
	}

	// 11. Combinators
	for key, target := range map[string]*[]*Schema{"allOf": &s.allOf, "anyOf": &s.anyOf, "oneOf": &s.oneOf} {
		if val, exists := m[key]; exists {
			arr, ok := val.([]any)
			if !ok {
				return fmt.Errorf("%w: '%s' at %s must be an array", ErrInvalidSchema, key, path)
			}
			if len(arr) == 0 {
				return fmt.Errorf("%w: '%s' array at %s cannot be empty (Draft-07)", ErrInvalidSchema, key, path)
			}
			for i, elem := range arr {
				sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
				subPath := fmt.Sprintf("%s/%s/%d", path, key, i)
				if err := compileSchema(elem, sub, root, depth+1, subPath); err != nil {
					return err
				}
				*target = append(*target, sub)
			}
		}
	}

	if notVal, exists := m["not"]; exists {
		switch nv := notVal.(type) {
		case map[string]any, bool:
			sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
			if err := compileSchema(nv, sub, root, depth+1, path+"/not"); err != nil {
				return err
			}
			s.not = sub
		default:
			return fmt.Errorf("%w: 'not' at %s must be a boolean or schema", ErrInvalidSchema, path)
		}
	}

	return nil
}

func resolveRefs(s *Schema, root *Schema, visited map[*Schema]bool, refSchemas *[]*Schema) error {
	if s == nil || visited[s] {
		return nil
	}
	visited[s] = true

	if s.ref != "" {
		*refSchemas = append(*refSchemas, s)
		if s.ref == "#" {
			// Direct self-reference to root
			s.refSub = root
		} else if target, ok := root.definitions[s.ref]; ok {
			s.refSub = target
		} else {
			// Attempt to resolve JSON Pointer into root schema
			targetSchema, err := resolveJSONPointer(root, s.ref)
			if err != nil {
				return fmt.Errorf("%w: %q: %v", ErrReferenceNotFound, s.ref, err)
			}
			s.refSub = targetSchema
		}
		if s.refSub != nil {
			if err := resolveRefs(s.refSub, root, visited, refSchemas); err != nil {
				return err
			}
		}
	}

	if s == root {
		for _, def := range root.definitions {
			if err := resolveRefs(def, root, visited, refSchemas); err != nil {
				return err
			}
		}
	}

	if s.items != nil {
		if err := resolveRefs(s.items, root, visited, refSchemas); err != nil {
			return err
		}
	}
	for _, it := range s.itemsTuple {
		if err := resolveRefs(it, root, visited, refSchemas); err != nil {
			return err
		}
	}
	if s.additionalItems != nil {
		if err := resolveRefs(s.additionalItems, root, visited, refSchemas); err != nil {
			return err
		}
	}
	if s.contains != nil {
		if err := resolveRefs(s.contains, root, visited, refSchemas); err != nil {
			return err
		}
	}
	for _, prop := range s.properties {
		if err := resolveRefs(prop, root, visited, refSchemas); err != nil {
			return err
		}
	}
	for _, prop := range s.patternProperties {
		if err := resolveRefs(prop, root, visited, refSchemas); err != nil {
			return err
		}
	}
	if s.additionalProperties != nil {
		if err := resolveRefs(s.additionalProperties, root, visited, refSchemas); err != nil {
			return err
		}
	}
	if s.propertyNames != nil {
		if err := resolveRefs(s.propertyNames, root, visited, refSchemas); err != nil {
			return err
		}
	}
	for _, dep := range s.dependencies {
		if dep.schema != nil {
			if err := resolveRefs(dep.schema, root, visited, refSchemas); err != nil {
				return err
			}
		}
	}
	for _, sub := range s.allOf {
		if err := resolveRefs(sub, root, visited, refSchemas); err != nil {
			return err
		}
	}
	for _, sub := range s.anyOf {
		if err := resolveRefs(sub, root, visited, refSchemas); err != nil {
			return err
		}
	}
	for _, sub := range s.oneOf {
		if err := resolveRefs(sub, root, visited, refSchemas); err != nil {
			return err
		}
	}
	if s.not != nil {
		if err := resolveRefs(s.not, root, visited, refSchemas); err != nil {
			return err
		}
	}
	return nil
}

func resolveJSONPointer(root *Schema, ptr string) (*Schema, error) {
	if ptr == "#" || ptr == "" {
		return root, nil
	}
	if !strings.HasPrefix(ptr, "#/") {
		return nil, fmt.Errorf("pointer must start with '#/'")
	}

	tokens := strings.Split(ptr[2:], "/")
	var current any = root.rootRaw

	for _, token := range tokens {
		// Unescape JSON Pointer (~1 -> /, ~0 -> ~)
		unescaped := strings.ReplaceAll(token, "~1", "/")
		unescaped = strings.ReplaceAll(unescaped, "~0", "~")

		switch node := current.(type) {
		case map[string]any:
			val, exists := node[unescaped]
			if !exists {
				return nil, fmt.Errorf("token %q not found in object", unescaped)
			}
			current = val
		case []any:
			idx, err := strconv.Atoi(unescaped)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, fmt.Errorf("invalid array index %q", unescaped)
			}
			current = node[idx]
		default:
			return nil, fmt.Errorf("cannot navigate into %T with token %q", current, unescaped)
		}
	}

	// Compile the resolved subschema
	sub := &Schema{definitions: root.definitions, rootRaw: root.rootRaw, rootSchema: root}
	if err := compileSchema(current, sub, root, 1, ptr); err != nil {
		return nil, err
	}
	root.definitions[ptr] = sub
	return sub, nil
}

// ValidateJSON checks output size, strictly parses JSON (no code fence stripping, no repair),
// and validates the parsed value against the schema with resource limits.
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

type validationCtx struct {
	depth int
	steps int
}

// Validate validates an in-memory JSON value against the schema.
func (s *Schema) Validate(val any) error {
	if err := checkInstanceDepth(val, 0); err != nil {
		return &ValidationError{
			Path:    "#",
			Message: err.Error(),
			Rule:    "resource_limit",
		}
	}
	ctx := &validationCtx{depth: 0, steps: 0}
	return s.validateWithCtx(val, "#", ctx)
}

func checkInstanceDepth(val any, depth int) error {
	if depth > MaxInstanceDepth {
		return fmt.Errorf("instance nesting exceeds maximum allowed depth (%d)", MaxInstanceDepth)
	}
	switch v := val.(type) {
	case map[string]any:
		for _, child := range v {
			if err := checkInstanceDepth(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := checkInstanceDepth(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Schema) validateWithCtx(val any, path string, ctx *validationCtx) error {
	ctx.steps++
	if ctx.steps > MaxValidationSteps {
		return &ValidationError{
			Path:    path,
			Message: "evaluation exceeded maximum validation steps limit (100,000)",
			Rule:    "resource_limit",
		}
	}
	if ctx.depth > MaxInstanceDepth {
		return &ValidationError{
			Path:    path,
			Message: "instance nesting exceeds maximum allowed depth (32)",
			Rule:    "resource_limit",
		}
	}

	if s.isBool {
		if !s.boolVal {
			return &ValidationError{Path: path, Message: "value rejected by false schema", Rule: "boolean_schema"}
		}
		return nil
	}

	if s.refSub != nil {
		ctx.depth++
		err := s.refSub.validateWithCtx(val, path, ctx)
		ctx.depth--
		return err
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
			ctx.steps++
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

	// 4. Numeric constraints (only apply if value is a number)
	if num, ok := toFloat(val); ok {
		if s.multipleOf != nil {
			if !isMultipleOf(num, *s.multipleOf) {
				return &ValidationError{
					Path:    path,
					Message: fmt.Sprintf("%v is not a multiple of %v", num, *s.multipleOf),
					Rule:    "multipleOf",
				}
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

	// 5. String constraints (only apply if value is a string)
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
		if s.format != "" {
			if err := validateFormat(str, s.format); err != nil {
				return &ValidationError{
					Path:    path,
					Message: fmt.Sprintf("string %q violates format %q: %v", str, s.format, err),
					Rule:    "format",
				}
			}
		}
	}

	// 6. Array constraints (only apply if value is an array)
	if arr, ok := val.([]any); ok {
		if len(arr) > MaxInstanceItems {
			return &ValidationError{Path: path, Message: "array item count exceeds 100,000 limit", Rule: "resource_limit"}
		}
		if s.maxItems != nil && len(arr) > *s.maxItems {
			return &ValidationError{Path: path, Message: fmt.Sprintf("array length %d exceeds maxItems %d", len(arr), *s.maxItems), Rule: "maxItems"}
		}
		if s.minItems != nil && len(arr) < *s.minItems {
			return &ValidationError{Path: path, Message: fmt.Sprintf("array length %d is less than minItems %d", len(arr), *s.minItems), Rule: "minItems"}
		}
		if s.uniqueItems {
			for i := 0; i < len(arr); i++ {
				for j := i + 1; j < len(arr); j++ {
					ctx.steps++
					if ctx.steps > MaxValidationSteps {
						return &ValidationError{Path: path, Message: "evaluation exceeded maximum validation steps limit (100,000)", Rule: "resource_limit"}
					}
					if deepEqual(arr[i], arr[j]) {
						return &ValidationError{Path: path, Message: "array items must be unique", Rule: "uniqueItems"}
					}
				}
			}
		}

		if s.items != nil {
			ctx.depth++
			for i, elem := range arr {
				elemPath := fmt.Sprintf("%s/%d", path, i)
				if err := s.items.validateWithCtx(elem, elemPath, ctx); err != nil {
					ctx.depth--
					return err
				}
			}
			ctx.depth--
		} else if len(s.itemsTuple) > 0 {
			ctx.depth++
			for i, elem := range arr {
				elemPath := fmt.Sprintf("%s/%d", path, i)
				if i < len(s.itemsTuple) {
					if err := s.itemsTuple[i].validateWithCtx(elem, elemPath, ctx); err != nil {
						ctx.depth--
						return err
					}
				} else {
					if s.allowAddItems != nil && !*s.allowAddItems {
						ctx.depth--
						return &ValidationError{Path: elemPath, Message: "additional items not allowed in tuple", Rule: "additionalItems"}
					}
					if s.additionalItems != nil {
						if err := s.additionalItems.validateWithCtx(elem, elemPath, ctx); err != nil {
							ctx.depth--
							return err
						}
					}
				}
			}
			ctx.depth--
		}

		if s.contains != nil {
			containsMatched := false
			ctx.depth++
			for i, elem := range arr {
				elemPath := fmt.Sprintf("%s/%d", path, i)
				if err := s.contains.validateWithCtx(elem, elemPath, ctx); err == nil {
					containsMatched = true
					break
				}
			}
			ctx.depth--
			if !containsMatched {
				return &ValidationError{Path: path, Message: "array does not contain any item matching the 'contains' schema", Rule: "contains"}
			}
		}
	}

	// 7. Object constraints (only apply if value is an object)
	if obj, ok := val.(map[string]any); ok {
		if len(obj) > MaxInstanceProps {
			return &ValidationError{Path: path, Message: "object properties count exceeds 10,000 limit", Rule: "resource_limit"}
		}
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

		// Property names
		if s.propertyNames != nil {
			ctx.depth++
			for propKey := range obj {
				if err := s.propertyNames.validateWithCtx(propKey, fmt.Sprintf("%s/%s", path, propKey), ctx); err != nil {
					ctx.depth--
					return &ValidationError{
						Path:    path,
						Message: fmt.Sprintf("property name %q violates propertyNames schema: %v", propKey, err),
						Rule:    "propertyNames",
					}
				}
			}
			ctx.depth--
		}

		// Dependencies
		for propName, dep := range s.dependencies {
			if _, exists := obj[propName]; exists {
				for _, reqProp := range dep.requiredProps {
					if _, hasReq := obj[reqProp]; !hasReq {
						return &ValidationError{
							Path:    path,
							Message: fmt.Sprintf("property %q requires property %q to be present", propName, reqProp),
							Rule:    "dependencies",
						}
					}
				}
				if dep.schema != nil {
					ctx.depth++
					if err := dep.schema.validateWithCtx(val, path, ctx); err != nil {
						ctx.depth--
						return err
					}
					ctx.depth--
				}
			}
		}

		// Property validation
		ctx.depth++
		for k, v := range obj {
			propPath := fmt.Sprintf("%s/%s", path, k)
			matchedSchema := false

			if s.properties != nil {
				if propSchema, ok := s.properties[k]; ok {
					matchedSchema = true
					if err := propSchema.validateWithCtx(v, propPath, ctx); err != nil {
						ctx.depth--
						return err
					}
				}
			}

			if s.patternProperties != nil {
				for pat, patSchema := range s.patternProperties {
					if pat.MatchString(k) {
						matchedSchema = true
						if err := patSchema.validateWithCtx(v, propPath, ctx); err != nil {
							ctx.depth--
							return err
						}
					}
				}
			}

			if !matchedSchema {
				if s.allowAddProps != nil && !*s.allowAddProps {
					ctx.depth--
					return &ValidationError{
						Path:    propPath,
						Message: fmt.Sprintf("additional property %q is not allowed", k),
						Rule:    "additionalProperties",
					}
				}
				if s.additionalProperties != nil {
					if err := s.additionalProperties.validateWithCtx(v, propPath, ctx); err != nil {
						ctx.depth--
						return err
					}
				}
			}
		}
		ctx.depth--
	}

	// 8. Combinators
	if len(s.allOf) > 0 {
		for _, sub := range s.allOf {
			if err := sub.validateWithCtx(val, path, ctx); err != nil {
				return err
			}
		}
	}

	if len(s.anyOf) > 0 {
		anyMatched := false
		for _, sub := range s.anyOf {
			if err := sub.validateWithCtx(val, path, ctx); err == nil {
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
			if err := sub.validateWithCtx(val, path, ctx); err == nil {
				matchCount++
			}
		}
		if matchCount != 1 {
			return &ValidationError{Path: path, Message: fmt.Sprintf("value matched %d schemas in oneOf, expected exactly 1", matchCount), Rule: "oneOf"}
		}
	}

	if s.not != nil {
		if err := s.not.validateWithCtx(val, path, ctx); err == nil {
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
	if val == nil {
		return 0, false
	}
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

func getFloatStrict(val any) (float64, bool) {
	if val == nil {
		return 0, false
	}
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

func getIntStrict(val any) (int, bool) {
	f, ok := getFloatStrict(val)
	if !ok {
		return 0, false
	}
	if math.Floor(f) != f || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return int(f), true
}

func isMultipleOf(val, multiple float64) bool {
	if multiple <= 0 {
		return false
	}
	div := val / multiple
	rounded := math.Round(div)
	tol := 1e-9 * math.Max(1.0, math.Abs(div))
	return math.Abs(div-rounded) <= tol
}

func deepEqual(a, b any) bool {
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

var (
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	uuidRegex  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func validateFormat(str, format string) error {
	switch format {
	case "date-time":
		if _, err := time.Parse(time.RFC3339, str); err != nil {
			if _, err2 := time.Parse(time.RFC3339Nano, str); err2 != nil {
				return errors.New("must be a valid RFC 3339 date-time")
			}
		}
		return nil
	case "date":
		if _, err := time.Parse("2006-01-02", str); err != nil {
			return errors.New("must be a valid full-date (YYYY-MM-DD)")
		}
		return nil
	case "time":
		if _, err := time.Parse("15:04:05Z07:00", str); err != nil {
			if _, err2 := time.Parse("15:04:05", str); err2 != nil {
				return errors.New("must be a valid full-time (HH:MM:SS)")
			}
		}
		return nil
	case "email":
		if len(str) > 254 || !emailRegex.MatchString(str) {
			return errors.New("must be a valid email address")
		}
		return nil
	case "ipv4":
		ip := net.ParseIP(str)
		if ip == nil || ip.To4() == nil || !strings.Contains(str, ".") {
			return errors.New("must be a valid IPv4 address")
		}
		return nil
	case "ipv6":
		ip := net.ParseIP(str)
		if ip == nil || ip.To4() != nil || !strings.Contains(str, ":") {
			return errors.New("must be a valid IPv6 address")
		}
		return nil
	case "uri":
		u, err := url.Parse(str)
		if err != nil || u.Scheme == "" {
			return errors.New("must be a valid URI with scheme")
		}
		return nil
	case "uri-reference":
		if _, err := url.Parse(str); err != nil {
			return errors.New("must be a valid URI reference")
		}
		return nil
	case "uuid":
		if !uuidRegex.MatchString(str) {
			return errors.New("must be a valid UUID (RFC 4122)")
		}
		return nil
	case "regex":
		if _, err := regexp.Compile(str); err != nil {
			return fmt.Errorf("must be a valid regular expression: %w", err)
		}
		return nil
	case "json-pointer":
		if str != "" && !strings.HasPrefix(str, "/") {
			return errors.New("must be an RFC 6901 JSON pointer starting with '/'")
		}
		return nil
	default:
		// Per Draft-07, unknown formats are treated as informational annotations
		return nil
	}
}
