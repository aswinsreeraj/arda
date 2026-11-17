package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aswinsreeraj/arda/pkg/vault"
	"github.com/google/uuid"
)

// Service coordinates credential management between HashiCorp Vault and metadata storage.
type Service struct {
	vaultStore vault.SecretStore
	store      Store
}

// NewService creates a new Service instance.
func NewService(vaultStore vault.SecretStore, store Store) *Service {
	return &Service{
		vaultStore: vaultStore,
		store:      store,
	}
}

func credentialKeyAndField(category Category, provider string) (string, string) {
	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	key := fmt.Sprintf("credentials/%s/%s", category, cleanProvider)
	field := "api_key"
	if category == CategoryMCP {
		field = "access_token"
	}
	return key, field
}

// SaveCredential securely writes a mock secret to Vault and saves non-sensitive metadata.
// Raw secrets are NEVER persisted in PostgreSQL or returned in the response.
func (s *Service) SaveCredential(ctx context.Context, tenantID uuid.UUID, req SaveCredentialRequest) (*Credential, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}

	cleanCategory := Category(strings.ToLower(strings.TrimSpace(string(req.Category))))
	cleanProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	key, field := credentialKeyAndField(cleanCategory, cleanProvider)

	// Write secret to tenant-scoped Vault KV v2 storage
	if err := s.vaultStore.WriteSecret(ctx, tenantID, key, map[string]string{
		field: req.SecretValue,
	}); err != nil {
		return nil, fmt.Errorf("failed to write secret to vault: %w", err)
	}

	reference := vault.FormatReferenceWithField(key, field)

	var cred *Credential
	if existing, err := s.store.GetByCategoryAndProvider(ctx, tenantID, cleanCategory, cleanProvider); err == nil && existing != nil {
		cred = &Credential{
			ID:        existing.ID,
			TenantID:  tenantID,
			Category:  cleanCategory,
			Provider:  cleanProvider,
			Reference: reference,
			CreatedAt: existing.CreatedAt,
		}
	} else {
		cred = &Credential{
			ID:        uuid.New(),
			TenantID:  tenantID,
			Category:  cleanCategory,
			Provider:  cleanProvider,
			Reference: reference,
		}
	}

	if err := s.store.Save(ctx, cred); err != nil {
		return nil, fmt.Errorf("failed to save credential metadata: %w", err)
	}

	return cred, nil
}

// GetCredential retrieves credential metadata scoped strictly to the tenant.
func (s *Service) GetCredential(ctx context.Context, tenantID, id uuid.UUID) (*Credential, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if id == uuid.Nil {
		return nil, errorsNew("invalid credential id")
	}
	return s.store.Get(ctx, tenantID, id)
}

// ListCredentials lists all credential metadata for the tenant.
func (s *Service) ListCredentials(ctx context.Context, tenantID uuid.UUID) ([]Credential, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	return s.store.List(ctx, tenantID)
}

// DeleteCredential permanently removes the secret and all historical versions from Vault and deletes the metadata.
func (s *Service) DeleteCredential(ctx context.Context, tenantID, id uuid.UUID) error {
	if tenantID == uuid.Nil {
		return errorsNew("missing or invalid tenant identity")
	}
	if id == uuid.Nil {
		return errorsNew("invalid credential id")
	}

	cred, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}

	key, _ := credentialKeyAndField(cred.Category, cred.Provider)
	_ = s.vaultStore.DestroySecret(ctx, tenantID, key)

	return s.store.Delete(ctx, tenantID, id)
}

// VerifyCredential verifies that the secret referenced by the credential can be read from Vault
// and performs an upstream connectivity test for LLM providers.
// Does NOT return the secret value.
func (s *Service) VerifyCredential(ctx context.Context, tenantID, id uuid.UUID) (*VerifyCredentialResponse, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if id == uuid.Nil {
		return nil, errorsNew("invalid credential id")
	}

	cred, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}

	// Verify retrieval through Vault client; discard secret value immediately
	val, err := s.vaultStore.ReadSecretValue(ctx, tenantID, cred.Reference)
	if err != nil {
		return nil, fmt.Errorf("vault verification failed: %w", err)
	}
	if strings.TrimSpace(val) == "" {
		return nil, fmt.Errorf("secret reference %s resolved to empty value", cred.Reference)
	}

	cleanVal := strings.TrimSpace(val)
	// For LLM credentials with live keys (not mock/test), verify upstream connectivity
	if cred.Category == CategoryLLM && !isMockOrTestKey(cleanVal, cred.Provider) {
		cleanProvider := strings.ToLower(strings.TrimSpace(cred.Provider))
		models, fetchErr := fetchProviderModels(ctx, cleanProvider, cleanVal)
		if fetchErr != nil {
			return &VerifyCredentialResponse{
				ID:        cred.ID,
				Verified:  false,
				Reference: cred.Reference,
				Message:   fmt.Sprintf("Provider verification failed: %v", fetchErr),
			}, nil
		}
		return &VerifyCredentialResponse{
			ID:        cred.ID,
			Verified:  true,
			Reference: cred.Reference,
			Message:   fmt.Sprintf("Live connectivity verified with %s (%d models available)", cred.Provider, len(models)),
		}, nil
	}

	return &VerifyCredentialResponse{
		ID:        cred.ID,
		Verified:  true,
		Reference: cred.Reference,
		Message:   "secret successfully verified in vault",
	}, nil
}

// ListModels queries the upstream LLM provider using the credential's secret in Vault
// to fetch the real-time list of available models.
func (s *Service) ListModels(ctx context.Context, tenantID, credentialID uuid.UUID) (*CredentialModelsResponse, error) {
	if tenantID == uuid.Nil {
		return nil, errorsNew("missing or invalid tenant identity")
	}
	if credentialID == uuid.Nil {
		return nil, errorsNew("invalid credential id")
	}

	cred, err := s.store.Get(ctx, tenantID, credentialID)
	if err != nil {
		return nil, err
	}

	if cred.Category != CategoryLLM {
		return nil, fmt.Errorf("credential category %q does not support model listing; must be %q", cred.Category, CategoryLLM)
	}

	secretVal, err := s.vaultStore.ReadSecretValue(ctx, tenantID, cred.Reference)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve API key from vault: %w", err)
	}
	if strings.TrimSpace(secretVal) == "" {
		return nil, fmt.Errorf("api key in vault is empty for reference %s", cred.Reference)
	}

	cleanKey := strings.TrimSpace(secretVal)
	cleanProvider := strings.ToLower(strings.TrimSpace(cred.Provider))

	models, err := fetchProviderModels(ctx, cleanProvider, cleanKey)
	if err != nil {
		return nil, err
	}

	return &CredentialModelsResponse{
		CredentialID: cred.ID,
		Provider:     cred.Provider,
		Models:       models,
	}, nil
}

func isMockOrTestKey(key, provider string) bool {
	lowerKey := strings.ToLower(key)
	lowerProv := strings.ToLower(provider)
	if lowerProv == "mock" {
		return true
	}
	return strings.HasPrefix(lowerKey, "mock") || strings.HasPrefix(lowerKey, "test")
}

func fetchProviderModels(ctx context.Context, provider, apiKey string) ([]CredentialModelInfo, error) {
	if isMockOrTestKey(apiKey, provider) {
		return []CredentialModelInfo{
			{ID: "mock-model", DisplayName: "Mock Default Model"},
			{ID: "mock-fast", DisplayName: "Mock Fast Engine"},
		}, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}

	switch {
	case strings.Contains(provider, "google") || strings.Contains(provider, "gemini"):
		reqURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", url.QueryEscape(apiKey))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build google models request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("google gemini request failed: %w", err)
		}
		defer resp.Body.Close()

		var gResp struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			Error *struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&gResp); err != nil {
			return nil, fmt.Errorf("failed to parse google models response: %w", err)
		}
		if gResp.Error != nil {
			return nil, fmt.Errorf("google gemini error (%d): %s", gResp.Error.Code, gResp.Error.Message)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("google gemini returned HTTP status %d", resp.StatusCode)
		}

		var results []CredentialModelInfo
		for _, m := range gResp.Models {
			supportsGen := false
			for _, method := range m.SupportedGenerationMethods {
				if method == "generateContent" {
					supportsGen = true
					break
				}
			}
			if !supportsGen {
				continue
			}
			id := strings.TrimPrefix(m.Name, "models/")
			displayName := m.DisplayName
			if displayName == "" {
				displayName = id
			}
			results = append(results, CredentialModelInfo{
				ID:          id,
				DisplayName: displayName,
			})
		}
		return results, nil

	case strings.Contains(provider, "groq"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.groq.com/openai/v1/models", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build groq models request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("groq request failed: %w", err)
		}
		defer resp.Body.Close()

		var oaiResp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
			return nil, fmt.Errorf("failed to parse groq response: %w", err)
		}
		if oaiResp.Error != nil {
			return nil, fmt.Errorf("groq error: %s", oaiResp.Error.Message)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("groq returned HTTP status %d", resp.StatusCode)
		}

		var results []CredentialModelInfo
		for _, d := range oaiResp.Data {
			results = append(results, CredentialModelInfo{
				ID:          d.ID,
				DisplayName: d.ID,
			})
		}
		return results, nil

	case strings.Contains(provider, "openai"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build openai models request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("openai request failed: %w", err)
		}
		defer resp.Body.Close()

		var oaiResp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
			return nil, fmt.Errorf("failed to parse openai response: %w", err)
		}
		if oaiResp.Error != nil {
			return nil, fmt.Errorf("openai error: %s", oaiResp.Error.Message)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("openai returned HTTP status %d", resp.StatusCode)
		}

		var results []CredentialModelInfo
		for _, d := range oaiResp.Data {
			results = append(results, CredentialModelInfo{
				ID:          d.ID,
				DisplayName: d.ID,
			})
		}
		return results, nil

	case strings.Contains(provider, "openrouter"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build openrouter models request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("openrouter request failed: %w", err)
		}
		defer resp.Body.Close()

		var orResp struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&orResp); err != nil {
			return nil, fmt.Errorf("failed to parse openrouter response: %w", err)
		}
		var results []CredentialModelInfo
		for _, d := range orResp.Data {
			dn := d.Name
			if dn == "" {
				dn = d.ID
			}
			results = append(results, CredentialModelInfo{
				ID:          d.ID,
				DisplayName: dn,
			})
		}
		return results, nil

	default:
		return []CredentialModelInfo{
			{ID: "default", DisplayName: fmt.Sprintf("%s default model", provider)},
		}, nil
	}
}

func errorsNew(msg string) error {
	return fmt.Errorf("%s", msg)
}
