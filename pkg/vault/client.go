package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SecretStore defines the minimal internal interface for storing and resolving tenant-scoped secrets.
type SecretStore interface {
	// WriteSecret writes a key-value secret under the tenant-scoped namespace.
	WriteSecret(ctx context.Context, tenantID uuid.UUID, key string, data map[string]string) error

	// ReadSecret resolves a secret reference and returns the full key-value map.
	ReadSecret(ctx context.Context, tenantID uuid.UUID, ref string) (map[string]string, error)

	// ReadSecretValue resolves a secret reference and returns a single secret string value.
	ReadSecretValue(ctx context.Context, tenantID uuid.UUID, ref string) (string, error)

	// DeleteSecret removes a secret at the tenant-scoped path.
	DeleteSecret(ctx context.Context, tenantID uuid.UUID, key string) error
}

// TokenProvider defines an interface for obtaining a Vault authentication token.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// StaticTokenProvider provides a static Vault token.
type StaticTokenProvider string

func (s StaticTokenProvider) Token(ctx context.Context) (string, error) {
	tok := strings.TrimSpace(string(s))
	if tok == "" {
		return "", fmt.Errorf("%w: vault token is empty", ErrVaultAuthentication)
	}
	return tok, nil
}

// HTTPDoer is an abstraction for making HTTP requests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config holds runtime configuration for the Vault client.
type Config struct {
	// Address is the base URL of the Vault server (e.g. "http://127.0.0.1:8200").
	Address string

	// Token is the static Vault authentication token.
	Token string

	// MountPath is the KV v2 mount path (defaults to "secret").
	MountPath string

	// Timeout is the default bounded timeout for requests (defaults to 5s).
	Timeout time.Duration

	// TokenProvider optionally supplies dynamic or renewable tokens.
	TokenProvider TokenProvider
}

// ConfigFromEnv loads Vault configuration from environment variables with sensible defaults.
func ConfigFromEnv() Config {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}
	token := os.Getenv("VAULT_TOKEN")
	mount := os.Getenv("VAULT_MOUNT")
	if mount == "" {
		mount = "secret"
	}
	timeout := 5 * time.Second
	if tStr := os.Getenv("VAULT_TIMEOUT"); tStr != "" {
		if d, err := time.ParseDuration(tStr); err == nil && d > 0 {
			timeout = d
		}
	}
	return Config{
		Address:   addr,
		Token:     token,
		MountPath: mount,
		Timeout:   timeout,
	}
}

// Client is a tenant-isolated HashiCorp Vault KV v2 client.
type Client struct {
	address       string
	mountPath     string
	timeout       time.Duration
	tokenProvider TokenProvider
	httpClient    HTTPDoer
}

// Option configures a Client instance.
type Option func(*Client)

// WithHTTPClient sets a custom HTTPDoer (e.g. for testing or custom TLS transports).
func WithHTTPClient(doer HTTPDoer) Option {
	return func(c *Client) {
		if doer != nil {
			c.httpClient = doer
		}
	}
}

// WithTimeout sets a custom default bounded timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// NewClient initializes a new Vault KV v2 client.
// Fails closed if the Vault address or authentication credentials are missing.
func NewClient(cfg Config, opts ...Option) (*Client, error) {
	addr := strings.TrimRight(strings.TrimSpace(cfg.Address), "/")
	if addr == "" {
		return nil, fmt.Errorf("%w: vault address is required and cannot be empty", ErrVaultAuthentication)
	}

	var tp TokenProvider
	if cfg.TokenProvider != nil {
		tp = cfg.TokenProvider
	} else if strings.TrimSpace(cfg.Token) != "" {
		tp = StaticTokenProvider(cfg.Token)
	} else {
		return nil, fmt.Errorf("%w: vault token or token provider is required and cannot be empty", ErrVaultAuthentication)
	}

	mount := strings.Trim(strings.TrimSpace(cfg.MountPath), "/")
	if mount == "" {
		mount = "secret"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	client := &Client{
		address:       addr,
		mountPath:     mount,
		timeout:       timeout,
		tokenProvider: tp,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        50,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		},
	}

	for _, opt := range opts {
		opt(client)
	}

	return client, nil
}

// buildTenantPath constructs the tenant-scoped KV v2 path: "tenants/<tenant_id>/<key>".
func (c *Client) buildTenantPath(tenantID uuid.UUID, key string) (string, error) {
	if tenantID == uuid.Nil {
		return "", ErrTenantRequired
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/%s", tenantID.String(), key), nil
}

// WriteSecret writes a secret key-value map under the tenant-scoped namespace.
// The payload is strictly sent to Vault and never logged or included in error messages.
func (c *Client) WriteSecret(ctx context.Context, tenantID uuid.UUID, key string, data map[string]string) error {
	tenantPath, err := c.buildTenantPath(tenantID, key)
	if err != nil {
		return err
	}

	if data == nil {
		data = make(map[string]string)
	}

	endpoint := fmt.Sprintf("%s/v1/%s/data/%s", c.address, c.mountPath, tenantPath)
	payload := map[string]any{
		"data": data,
	}

	return c.executeRequest(ctx, http.MethodPost, endpoint, key, payload, nil)
}

// WriteSecretString is a convenience helper for writing a single secret string under key "value".
func (c *Client) WriteSecretString(ctx context.Context, tenantID uuid.UUID, key string, secretValue string) error {
	return c.WriteSecret(ctx, tenantID, key, map[string]string{"value": secretValue})
}

// ReadSecret resolves a secret reference and returns the key-value map from Vault KV v2.
func (c *Client) ReadSecret(ctx context.Context, tenantID uuid.UUID, ref string) (map[string]string, error) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return nil, err
	}

	tenantPath, err := c.buildTenantPath(tenantID, parsed.Key)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/v1/%s/data/%s", c.address, c.mountPath, tenantPath)

	var respBody struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}

	if err := c.executeRequest(ctx, http.MethodGet, endpoint, parsed.Key, nil, &respBody); err != nil {
		return nil, err
	}

	if respBody.Data.Data == nil {
		return nil, &SanitizedError{Op: "read", Key: parsed.Key, Err: ErrSecretNotFound}
	}

	result := make(map[string]string, len(respBody.Data.Data))
	for k, v := range respBody.Data.Data {
		switch val := v.(type) {
		case string:
			result[k] = val
		default:
			result[k] = fmt.Sprintf("%v", val)
		}
	}

	return result, nil
}

// ReadSecretValue resolves a secret reference and returns a single secret string value.
// If the reference contains a field selector (e.g. "secret:llm/openai#api_key"), that specific field is returned.
// Otherwise, it returns the "value" key, the single entry in the map, or the "api_key" key.
func (c *Client) ReadSecretValue(ctx context.Context, tenantID uuid.UUID, ref string) (string, error) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return "", err
	}

	data, err := c.ReadSecret(ctx, tenantID, parsed.Key)
	if err != nil {
		return "", err
	}

	if parsed.Field != "" {
		val, ok := data[parsed.Field]
		if !ok {
			return "", &SanitizedError{
				Op:  "read_value",
				Key: parsed.Raw,
				Err: fmt.Errorf("%w: field %q not found in secret", ErrSecretNotFound, parsed.Field),
			}
		}
		return val, nil
	}

	if val, ok := data["value"]; ok {
		return val, nil
	}

	if len(data) == 1 {
		for _, v := range data {
			return v, nil
		}
	}

	if val, ok := data["api_key"]; ok {
		return val, nil
	}

	return "", &SanitizedError{
		Op:  "read_value",
		Key: parsed.Raw,
		Err: fmt.Errorf("%w: secret contains multiple fields and no field selector was specified", ErrInvalidReference),
	}
}

// DeleteSecret soft-deletes the latest version of a secret under the tenant's namespace.
func (c *Client) DeleteSecret(ctx context.Context, tenantID uuid.UUID, key string) error {
	tenantPath, err := c.buildTenantPath(tenantID, key)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/v1/%s/data/%s", c.address, c.mountPath, tenantPath)
	return c.executeRequest(ctx, http.MethodDelete, endpoint, key, nil, nil)
}

// ReadSecretFromContext extracts the validated tenant ID from context and reads the secret.
func (c *Client) ReadSecretFromContext(ctx context.Context, ref string) (map[string]string, error) {
	tenantID, ok := TenantIDFromContext(ctx)
	if !ok {
		return nil, ErrTenantRequired
	}
	return c.ReadSecret(ctx, tenantID, ref)
}

// ReadSecretValueFromContext extracts the validated tenant ID from context and reads the single secret value.
func (c *Client) ReadSecretValueFromContext(ctx context.Context, ref string) (string, error) {
	tenantID, ok := TenantIDFromContext(ctx)
	if !ok {
		return "", ErrTenantRequired
	}
	return c.ReadSecretValue(ctx, tenantID, ref)
}

// executeRequest performs a bounded HTTP request to Vault with sanitized error handling.
func (c *Client) executeRequest(ctx context.Context, method, endpoint, safeKey string, body any, target any) error {
	// Apply bounded request timeout if context doesn't already have one
	reqCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	token, err := c.tokenProvider.Token(reqCtx)
	if err != nil {
		return &SanitizedError{Op: "auth", Key: safeKey, Err: ErrVaultAuthentication}
	}

	var bodyReader io.Reader
	if body != nil {
		payloadBytes, err := json.Marshal(body)
		if err != nil {
			return &SanitizedError{Op: "marshal", Key: safeKey, Err: ErrVaultOperation}
		}
		bodyReader = bytes.NewReader(payloadBytes)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, endpoint, bodyReader)
	if err != nil {
		return &SanitizedError{Op: "create_request", Key: safeKey, Err: ErrVaultOperation}
	}

	req.Header.Set("X-Vault-Token", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return &SanitizedError{Op: "timeout", Key: safeKey, Err: fmt.Errorf("%w: request deadline exceeded", ErrVaultUnavailable)}
		}
		return &SanitizedError{Op: "network", Key: safeKey, Err: fmt.Errorf("%w: connection failed", ErrVaultUnavailable)}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		if target != nil && resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
				return &SanitizedError{Op: "decode", Key: safeKey, Err: ErrVaultOperation}
			}
		}
		return nil

	case http.StatusNotFound:
		return &SanitizedError{Op: "read", Key: safeKey, Err: ErrSecretNotFound}

	case http.StatusUnauthorized, http.StatusForbidden:
		return &SanitizedError{Op: "auth", Key: safeKey, Err: ErrVaultAuthentication}

	default:
		if resp.StatusCode >= 500 {
			return &SanitizedError{Op: "server", Key: safeKey, Err: fmt.Errorf("%w: vault returned HTTP %d", ErrVaultUnavailable, resp.StatusCode)}
		}
		return &SanitizedError{Op: "operation", Key: safeKey, Err: fmt.Errorf("%w: vault returned HTTP %d", ErrVaultOperation, resp.StatusCode)}
	}
}
