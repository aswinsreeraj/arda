package vault

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// MemoryStore is an in-memory, thread-safe implementation of SecretStore
// designed for clean dependency injection in unit tests and local sandboxes.
type MemoryStore struct {
	mu      sync.RWMutex
	secrets map[string]map[string]string // keyed by "tenants/<tenant_id>/<key>"
}

// NewMemoryStore initializes a new in-memory SecretStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		secrets: make(map[string]map[string]string),
	}
}

func (m *MemoryStore) storageKey(tenantID uuid.UUID, key string) (string, error) {
	if tenantID == uuid.Nil {
		return "", ErrTenantRequired
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("tenants/%s/%s", tenantID.String(), key), nil
}

// WriteSecret stores a secret in memory under the tenant-scoped namespace.
func (m *MemoryStore) WriteSecret(ctx context.Context, tenantID uuid.UUID, key string, data map[string]string) error {
	sKey, err := m.storageKey(tenantID, key)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	copyData := make(map[string]string, len(data))
	for k, v := range data {
		copyData[k] = v
	}
	m.secrets[sKey] = copyData
	return nil
}

// WriteSecretString stores a single string value under key "value".
func (m *MemoryStore) WriteSecretString(ctx context.Context, tenantID uuid.UUID, key string, val string) error {
	return m.WriteSecret(ctx, tenantID, key, map[string]string{"value": val})
}

// ReadSecret retrieves a secret from memory under the tenant-scoped namespace.
func (m *MemoryStore) ReadSecret(ctx context.Context, tenantID uuid.UUID, ref string) (map[string]string, error) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return nil, err
	}

	sKey, err := m.storageKey(tenantID, parsed.Key)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	data, ok := m.secrets[sKey]
	if !ok {
		return nil, &SanitizedError{Op: "read", Key: parsed.Key, Err: ErrSecretNotFound}
	}

	copyData := make(map[string]string, len(data))
	for k, v := range data {
		copyData[k] = v
	}
	return copyData, nil
}

// ReadSecretValue retrieves a single secret value from memory.
func (m *MemoryStore) ReadSecretValue(ctx context.Context, tenantID uuid.UUID, ref string) (string, error) {
	parsed, err := ParseReference(ref)
	if err != nil {
		return "", err
	}

	data, err := m.ReadSecret(ctx, tenantID, parsed.Key)
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

// DeleteSecret removes a secret from memory.
func (m *MemoryStore) DeleteSecret(ctx context.Context, tenantID uuid.UUID, key string) error {
	sKey, err := m.storageKey(tenantID, key)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.secrets, sKey)
	return nil
}
