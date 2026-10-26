package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// ProviderAdapter defines the interface that provider-specific LLM adapters must fulfill.
// Each adapter implements the provider-independent LLMClient interface, declares its stable
// provider identifier, and reports its supported model catalog.
type ProviderAdapter interface {
	LLMClient
	ProviderID() string
	SupportedModels() []string
	IsModelSupported(model string) bool
	GenerateWithCredential(ctx context.Context, credential string, req LLMRequest) (LLMResponse, error)
}

type wrappedAdapter struct {
	LLMClient
	providerID      string
	supportedModels []string
}

func (w *wrappedAdapter) ProviderID() string {
	return w.providerID
}

func (w *wrappedAdapter) SupportedModels() []string {
	return append([]string(nil), w.supportedModels...)
}

func (w *wrappedAdapter) IsModelSupported(model string) bool {
	if len(w.supportedModels) == 0 {
		return true // accepts any model if no restriction specified
	}
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return true
	}
	for _, m := range w.supportedModels {
		if strings.EqualFold(m, trimmed) {
			return true
		}
	}
	return false
}

func (w *wrappedAdapter) GenerateWithCredential(ctx context.Context, credential string, req LLMRequest) (LLMResponse, error) {
	if credAdapter, ok := w.LLMClient.(interface {
		GenerateWithCredential(ctx context.Context, credential string, req LLMRequest) (LLMResponse, error)
	}); ok {
		return credAdapter.GenerateWithCredential(ctx, credential, req)
	}
	if credential != "" {
		ctx = WithAPIKey(ctx, credential)
	}
	return w.LLMClient.Generate(ctx, req)
}

// WrapClientAsAdapter adapts any LLMClient implementation into a ProviderAdapter.
func WrapClientAsAdapter(providerID string, client LLMClient, supportedModels ...string) (ProviderAdapter, error) {
	if client == nil {
		return nil, ErrNilLLMClient
	}
	cleanID := strings.ToLower(strings.TrimSpace(providerID))
	if cleanID == "" {
		return nil, errors.New("provider adapter: provider ID cannot be empty")
	}
	return &wrappedAdapter{
		LLMClient:       client,
		providerID:      cleanID,
		supportedModels: supportedModels,
	}, nil
}

// ProviderRegistry manages registration and thread-safe resolution of ProviderAdapters.
type ProviderRegistry struct {
	mu       sync.RWMutex
	adapters map[string]ProviderAdapter
}

// NormalizeProviderID maps common aliases to canonical provider identifiers.
func NormalizeProviderID(provider string) string {
	lower := strings.ToLower(strings.TrimSpace(provider))
	switch lower {
	case "claude":
		return "anthropic"
	case "gemini":
		return "google"
	case "gpt":
		return "openai"
	case "xai":
		return "grok"
	case "groqcloud":
		return "groq"
	default:
		return lower
	}
}

// NewProviderRegistry creates a new, empty ProviderRegistry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		adapters: make(map[string]ProviderAdapter),
	}
}

// Register registers an adapter under its normalized ProviderID.
func (r *ProviderRegistry) Register(adapter ProviderAdapter) error {
	if adapter == nil {
		return errors.New("provider registry: adapter cannot be nil")
	}
	cleanID := strings.ToLower(strings.TrimSpace(adapter.ProviderID()))
	if cleanID == "" {
		return errors.New("provider registry: adapter provider ID cannot be empty")
	}
	if strings.ContainsAny(cleanID, "/\\#?&% :") {
		return fmt.Errorf("provider registry: provider ID %q contains invalid characters", cleanID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[cleanID] = adapter
	return nil
}

// RegisterWithAliases registers an adapter under its ProviderID and one or more alias identifiers.
func (r *ProviderRegistry) RegisterWithAliases(adapter ProviderAdapter, aliases ...string) error {
	if err := r.Register(adapter); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, alias := range aliases {
		cleanAlias := strings.ToLower(strings.TrimSpace(alias))
		if cleanAlias != "" && !strings.ContainsAny(cleanAlias, "/\\#?&% :") {
			r.adapters[cleanAlias] = adapter
		}
	}
	return nil
}

// GetAdapter retrieves the adapter registered for the specified provider ID or alias.
func (r *ProviderRegistry) GetAdapter(provider string) (ProviderAdapter, error) {
	cleanID := strings.ToLower(strings.TrimSpace(provider))
	r.mu.RLock()
	defer r.mu.RUnlock()

	adapter, ok := r.adapters[cleanID]
	if !ok {
		// Fallback to canonical provider identifier if aliased
		norm := NormalizeProviderID(cleanID)
		adapter, ok = r.adapters[norm]
	}
	if !ok {
		return nil, fmt.Errorf("%w: provider %q is not registered", ErrUnsupportedProvider, cleanID)
	}
	return adapter, nil
}

// HasProvider checks whether an adapter is registered for the specified provider ID or alias.
func (r *ProviderRegistry) HasProvider(provider string) bool {
	cleanID := strings.ToLower(strings.TrimSpace(provider))
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.adapters[cleanID]; ok {
		return true
	}
	_, ok := r.adapters[NormalizeProviderID(cleanID)]
	return ok
}

// SupportedProviders returns a sorted list of registered provider identifiers.
func (r *ProviderRegistry) SupportedProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]string, 0, len(r.adapters))
	for p := range r.adapters {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	return providers
}

// ResolvingLLMClientOption configures optional behavior on a ResolvingLLMClient.
type ResolvingLLMClientOption func(*ResolvingLLMClient)

// WithDefaultProvider specifies a fallback provider when request configuration does not specify one.
func WithDefaultProvider(provider string) ResolvingLLMClientOption {
	return func(c *ResolvingLLMClient) {
		c.defaultProvider = strings.ToLower(strings.TrimSpace(provider))
	}
}

// WithResolvingDefaultModel specifies a fallback model when request configuration does not specify one.
func WithResolvingDefaultModel(model string) ResolvingLLMClientOption {
	return func(c *ResolvingLLMClient) {
		c.defaultModel = strings.TrimSpace(model)
	}
}

// ResolvingLLMClient dynamically resolves the appropriate provider adapter and tenant-scoped credentials
// at execution time, fulfilling the provider-independent LLMClient interface.
type ResolvingLLMClient struct {
	registry           *ProviderRegistry
	credentialResolver CredentialResolver
	defaultProvider    string
	defaultModel       string
}

// NewResolvingLLMClient initializes a ResolvingLLMClient.
func NewResolvingLLMClient(
	registry *ProviderRegistry,
	credentialResolver CredentialResolver,
	opts ...ResolvingLLMClientOption,
) (*ResolvingLLMClient, error) {
	if registry == nil {
		return nil, errors.New("resolving llm client: provider registry cannot be nil")
	}

	c := &ResolvingLLMClient{
		registry:           registry,
		credentialResolver: credentialResolver,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// Generate routes an LLMRequest to the appropriate provider adapter.
// It resolves credentials from Vault when necessary and verifies model compatibility.
func (c *ResolvingLLMClient) Generate(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	if err := ctx.Err(); err != nil {
		return LLMResponse{}, err
	}
	if err := req.Validate(); err != nil {
		return LLMResponse{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = c.defaultProvider
	}
	if provider == "" {
		return LLMResponse{}, fmt.Errorf("%w: provider is not specified and no default provider is configured", ErrMissingProvider)
	}

	adapter, err := c.registry.GetAdapter(provider)
	if err != nil {
		return LLMResponse{}, err
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.defaultModel
	}
	if model != "" && !adapter.IsModelSupported(model) {
		return LLMResponse{}, fmt.Errorf("%w: model %q is not supported by provider %q", ErrUnsupportedModel, model, provider)
	}
	req.Model = model

	var apiKey string
	if provider != "mock" {
		if c.credentialResolver == nil {
			return LLMResponse{}, fmt.Errorf("%w: credential resolver is not configured for provider %q", ErrMissingCredential, provider)
		}
		if req.TenantID == uuid.Nil {
			return LLMResponse{}, fmt.Errorf("%w: tenant ID is required to resolve credentials for provider %q", ErrInvalidTenant, provider)
		}

		var err error
		apiKey, err = c.credentialResolver.ResolveCredential(ctx, req.TenantID, provider, req.CredentialRef)
		if err != nil && NormalizeProviderID(provider) != provider {
			// Fall back to canonical provider name if alias lookup missed
			apiKey, err = c.credentialResolver.ResolveCredential(ctx, req.TenantID, NormalizeProviderID(provider), req.CredentialRef)
		}
		if err != nil {
			return LLMResponse{}, err
		}
	}

	resp, err := adapter.GenerateWithCredential(ctx, apiKey, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return LLMResponse{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return LLMResponse{}, context.DeadlineExceeded
		}
		return LLMResponse{}, err
	}

	return resp, nil
}
