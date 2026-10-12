package vault

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// segmentRegex validates a single path segment in a secret key.
	// Must start with an alphanumeric character and contain only alphanumeric, underscores, hyphens, or dots.
	segmentRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.]*$`)

	// fieldRegex validates an optional field name after '#' in a secret reference.
	fieldRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

	// reservedRootNamespaces are segment names that cannot be used as the root segment of a secret key
	// to avoid namespace confusion with Vault system paths or ARDA tenant hierarchies.
	reservedRootNamespaces = map[string]struct{}{
		"tenants": {},
		"sys":     {},
		"auth":    {},
		"v1":      {},
		"v2":      {},
		"secret":  {},
		"admin":   {},
	}
)

// SecretReference represents a validated reference to a secret stored in Vault.
type SecretReference struct {
	// Raw is the original unparsed reference string.
	Raw string

	// Key is the validated relative key path within the tenant's namespace (e.g. "llm/openai" or "vault-openai-key").
	Key string

	// Field is the optional specific field within the secret KV map (e.g. "api_key" from "secret:llm/openai#api_key").
	Field string
}

// ParseReference parses and validates a secret reference string.
// Supported formats include:
//   - "secret:<key>"
//   - "secret:<key>#<field>"
//   - "ref:<key>"
//   - "ref:<key>#<field>"
//   - "<key>"
//
// Rejects path traversal, forbidden characters, reserved namespaces, and malformed references.
func ParseReference(ref string) (*SecretReference, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: reference cannot be empty", ErrInvalidReference)
	}

	target := trimmed
	if strings.HasPrefix(target, "secret:") {
		target = strings.TrimPrefix(target, "secret:")
	} else if strings.HasPrefix(target, "ref:") {
		target = strings.TrimPrefix(target, "ref:")
	} else if strings.HasPrefix(target, "env:") {
		return nil, fmt.Errorf("%w: environment reference %q is not a vault secret reference", ErrInvalidReference, ref)
	}

	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("%w: secret key after prefix cannot be empty", ErrInvalidReference)
	}

	var key, field string
	if hashIdx := strings.Index(target, "#"); hashIdx != -1 {
		key = target[:hashIdx]
		field = target[hashIdx+1:]
		if field == "" {
			return nil, fmt.Errorf("%w: field name after '#' cannot be empty", ErrInvalidReference)
		}
		if !fieldRegex.MatchString(field) {
			return nil, fmt.Errorf("%w: field name %q contains invalid characters (allowed: alphanumeric, _, -)", ErrInvalidReference, field)
		}
	} else {
		key = target
	}

	if err := ValidateKey(key); err != nil {
		return nil, err
	}

	return &SecretReference{
		Raw:   trimmed,
		Key:   key,
		Field: field,
	}, nil
}

// ValidateKey validates that a secret key is safe, properly formatted, and contained within the tenant namespace.
// Specifically prevents directory traversal (..), null bytes, leading/trailing slashes, and reserved root segments.
func ValidateKey(key string) error {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return fmt.Errorf("%w: secret key cannot be empty", ErrInvalidReference)
	}
	if len(trimmed) > 255 {
		return fmt.Errorf("%w: secret key exceeds maximum length of 255 characters", ErrInvalidReference)
	}
	if strings.Contains(trimmed, "\\") {
		return fmt.Errorf("%w: backslashes are not permitted in secret keys", ErrInvalidReference)
	}
	if strings.HasPrefix(trimmed, "/") {
		return fmt.Errorf("%w: leading slashes are not permitted in secret keys", ErrInvalidReference)
	}
	if strings.HasSuffix(trimmed, "/") {
		return fmt.Errorf("%w: trailing slashes are not permitted in secret keys", ErrInvalidReference)
	}
	if strings.Contains(trimmed, "//") {
		return fmt.Errorf("%w: consecutive slashes are not permitted in secret keys", ErrInvalidReference)
	}

	segments := strings.Split(trimmed, "/")
	for i, seg := range segments {
		if seg == "" {
			return fmt.Errorf("%w: empty path segment in secret key", ErrInvalidReference)
		}
		if seg == "." || seg == ".." {
			return fmt.Errorf("%w: path traversal segment %q is strictly forbidden", ErrInvalidReference, seg)
		}
		if strings.HasSuffix(seg, ".") {
			return fmt.Errorf("%w: path segment cannot end with a dot: %q", ErrInvalidReference, seg)
		}
		if !segmentRegex.MatchString(seg) {
			return fmt.Errorf("%w: path segment %q contains invalid characters; must start with alphanumeric and contain only [a-zA-Z0-9_.-]", ErrInvalidReference, seg)
		}
		if i == 0 {
			lowerRoot := strings.ToLower(seg)
			if _, reserved := reservedRootNamespaces[lowerRoot]; reserved {
				return fmt.Errorf("%w: root segment %q is reserved and cannot be used in tenant secret keys", ErrInvalidReference, seg)
			}
		}
	}

	return nil
}

// FormatReference formats a secret key into the canonical ARDA secret reference string: "secret:<key>".
func FormatReference(key string) string {
	return "secret:" + strings.TrimSpace(key)
}

// FormatReferenceWithField formats a secret key and field into "secret:<key>#<field>".
func FormatReferenceWithField(key, field string) string {
	return fmt.Sprintf("secret:%s#%s", strings.TrimSpace(key), strings.TrimSpace(field))
}
