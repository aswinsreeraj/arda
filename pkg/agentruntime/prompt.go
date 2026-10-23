package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/aswinsreeraj/arda/services/registry/domain"
)

var (
	// ErrMalformedPlaceholder indicates that a prompt template placeholder has invalid, unclosed, or nested syntax.
	ErrMalformedPlaceholder = errors.New("agentruntime: malformed template placeholder")

	// ErrMissingPlaceholderValue indicates that a referenced placeholder key path was not found in the payload.
	ErrMissingPlaceholderValue = errors.New("agentruntime: missing placeholder value in payload")

	// ErrUnsupportedValueType indicates that a payload value is not a supported scalar type for template rendering.
	ErrUnsupportedValueType = errors.New("agentruntime: unsupported payload value type for template rendering")
)

// PromptBuilder provides deterministic prompt construction from templates and execution input payloads.
//
// Template Syntax:
//   - Canonical Syntax: {{input.<path>}} (e.g. {{input.customer_id}}, {{input.user.address.city}}).
//     This is the documented standard format for all ARDA prompt templates.
//   - Compatibility Syntax: Bare paths without the 'input.' prefix (e.g. {{customer_id}}) are supported
//     for backwards compatibility and resolved unambiguously against the execution input payload.
//   - Nested keys are addressed with dot notation: {{input.customer.profile.tier}}.
//   - Placeholders are resolved strictly against the execution input payload; arbitrary expressions or code are not executed.
//
// Supported Scalar Types:
//   - string: rendered as string literal
//   - bool: "true" or "false"
//   - integers (int, int8..int64, uint, uint8..uint64): base-10 decimal representation
//   - floating-point (float32, float64, json.Number): deterministic base-10 decimal representation
//
// Complex types (maps, slices, structs, nil, functions) return ErrUnsupportedValueType to prevent ambiguous stringification.
type PromptBuilder struct{}

// NewPromptBuilder returns a new PromptBuilder instance.
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

// Build constructs a prompt from a raw template string and payload.
func (b *PromptBuilder) Build(template string, payload map[string]any) (string, error) {
	return BuildPrompt(template, payload)
}

// BuildFromConfig constructs a prompt using the PromptTemplate from domain.Configuration.
func (b *PromptBuilder) BuildFromConfig(cfg domain.Configuration, payload map[string]any) (string, error) {
	return BuildPromptFromConfig(cfg, payload)
}

// BuildPrompt constructs a prompt by substituting placeholders in template with corresponding payload values.
func BuildPrompt(template string, payload map[string]any) (string, error) {
	var builder strings.Builder
	idx := 0
	n := len(template)

	for idx < n {
		open := strings.Index(template[idx:], "{{")
		closeBeforeOpen := strings.Index(template[idx:], "}}")

		// If there is a "}}" before the next "{{" (or no "{{" remains), it is an unmatched closing delimiter
		if closeBeforeOpen != -1 && (open == -1 || closeBeforeOpen < open) {
			return "", fmt.Errorf("%w: unmatched closing '}}' at offset %d", ErrMalformedPlaceholder, idx+closeBeforeOpen)
		}

		// No more placeholders
		if open == -1 {
			builder.WriteString(template[idx:])
			break
		}

		// Append literal text preceding "{{"
		builder.WriteString(template[idx : idx+open])
		idx += open + 2 // move past "{{"

		// Find matching "}}"
		closeIdx := strings.Index(template[idx:], "}}")
		if closeIdx == -1 {
			return "", fmt.Errorf("%w: unclosed '{{' starting at offset %d", ErrMalformedPlaceholder, idx-2)
		}

		rawTag := template[idx : idx+closeIdx]
		// Reject nested "{{"
		if strings.Contains(rawTag, "{{") {
			return "", fmt.Errorf("%w: nested '{{' inside placeholder at offset %d", ErrMalformedPlaceholder, idx)
		}

		trimmedTag := strings.TrimSpace(rawTag)
		if trimmedTag == "" {
			return "", fmt.Errorf("%w: empty placeholder at offset %d", ErrMalformedPlaceholder, idx-2)
		}

		// Parse key path into segments
		segments, err := parsePathSegments(trimmedTag)
		if err != nil {
			return "", err
		}

		// Resolve key path from payload
		val, err := resolvePayloadPath(payload, segments)
		if err != nil {
			return "", fmt.Errorf("%w for %q: %v", ErrMissingPlaceholderValue, trimmedTag, err)
		}

		// Format scalar value safely
		formatted, err := formatScalar(val)
		if err != nil {
			return "", fmt.Errorf("%w for %q: %v", ErrUnsupportedValueType, trimmedTag, err)
		}

		builder.WriteString(formatted)
		idx += closeIdx + 2 // move past "}}"
	}

	return builder.String(), nil
}

// BuildPromptFromConfig extracts cfg.PromptTemplate and constructs a prompt using the payload.
func BuildPromptFromConfig(cfg domain.Configuration, payload map[string]any) (string, error) {
	return BuildPrompt(cfg.PromptTemplate, payload)
}

func parsePathSegments(tag string) ([]string, error) {
	path := tag
	if strings.HasPrefix(path, "input.") {
		path = strings.TrimPrefix(path, "input.")
	} else if path == "input" {
		return nil, fmt.Errorf("%w: placeholder 'input' refers to entire payload object", ErrUnsupportedValueType)
	}

	if path == "" {
		return nil, fmt.Errorf("%w: empty path in placeholder %q", ErrMalformedPlaceholder, tag)
	}

	if strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return nil, fmt.Errorf("%w: placeholder %q has leading or trailing dot", ErrMalformedPlaceholder, tag)
	}

	parts := strings.Split(path, ".")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("%w: consecutive dots in placeholder %q", ErrMalformedPlaceholder, tag)
		}
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
				return nil, fmt.Errorf("%w: invalid character %q in placeholder %q", ErrMalformedPlaceholder, r, tag)
			}
		}
	}

	return parts, nil
}

func resolvePayloadPath(payload map[string]any, segments []string) (any, error) {
	if payload == nil {
		return nil, errors.New("payload is nil")
	}

	var current any = payload
	for _, seg := range segments {
		currMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot traverse key %q on non-map object", seg)
		}
		val, exists := currMap[seg]
		if !exists {
			return nil, fmt.Errorf("key %q not found", seg)
		}
		current = val
	}

	return current, nil
}

func formatScalar(val any) (string, error) {
	if val == nil {
		return "", fmt.Errorf("%w: nil value cannot be formatted", ErrUnsupportedValueType)
	}

	switch v := val.(type) {
	case string:
		return v, nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "", fmt.Errorf("%w: non-finite float value", ErrUnsupportedValueType)
		}
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case float32:
		f64 := float64(v)
		if math.IsNaN(f64) || math.IsInf(f64, 0) {
			return "", fmt.Errorf("%w: non-finite float value", ErrUnsupportedValueType)
		}
		return strconv.FormatFloat(f64, 'f', -1, 32), nil
	case json.Number:
		return v.String(), nil
	default:
		return "", fmt.Errorf("%w: unsupported type %T", ErrUnsupportedValueType, val)
	}
}
