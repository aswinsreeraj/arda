package credentials

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Category defines the category of credential (LLM provider vs MCP server).
type Category string

const (
	CategoryLLM Category = "llm"
	CategoryMCP Category = "mcp"
)

// ValidateCategory checks whether the category is supported.
func (c Category) Validate() error {
	switch c {
	case CategoryLLM, CategoryMCP:
		return nil
	default:
		return fmt.Errorf("unsupported credential category %q; must be 'llm' or 'mcp'", c)
	}
}

// Credential represents tenant-scoped credential metadata.
// The actual secret value is stored exclusively in HashiCorp Vault.
type Credential struct {
	ID        uuid.UUID `json:"id"`
	TenantID  uuid.UUID `json:"tenant_id"`
	Category  Category  `json:"category"`
	Provider  string    `json:"provider"`
	Reference string    `json:"reference"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SaveCredentialRequest is the payload to store a mock credential.
type SaveCredentialRequest struct {
	Category    Category `json:"category"`
	Provider    string   `json:"provider"`
	SecretValue string   `json:"secret_value"`
}

// Validate checks request completeness and safety.
func (r SaveCredentialRequest) Validate() error {
	if err := r.Category.Validate(); err != nil {
		return err
	}
	trimmedProvider := strings.TrimSpace(r.Provider)
	if trimmedProvider == "" {
		return errors.New("provider identifier is required and cannot be empty")
	}
	if len(trimmedProvider) > 100 {
		return errors.New("provider identifier exceeds maximum length of 100 characters")
	}
	if strings.ContainsAny(trimmedProvider, "/\\#?&% ") {
		return errors.New("provider identifier contains invalid characters; must be alphanumeric, underscores, or hyphens")
	}
	if strings.TrimSpace(r.SecretValue) == "" {
		return errors.New("secret value is required and cannot be empty")
	}
	return nil
}

// VerifyCredentialResponse contains administrative verification status without disclosing secrets.
type VerifyCredentialResponse struct {
	ID        uuid.UUID `json:"id"`
	Verified  bool      `json:"verified"`
	Reference string    `json:"reference"`
	Message   string    `json:"message"`
}
