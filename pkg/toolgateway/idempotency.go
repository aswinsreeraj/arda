package toolgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// IdempotencyLockManager coordinates in-flight invocation locks to prevent race conditions on concurrent identical calls.
type IdempotencyLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewIdempotencyLockManager constructs a key-based mutex manager.
func NewIdempotencyLockManager() *IdempotencyLockManager {
	return &IdempotencyLockManager{
		locks: make(map[string]*sync.Mutex),
	}
}

// Acquire gets or creates a mutex for the given invocation ID and locks it.
func (m *IdempotencyLockManager) Acquire(invocationID string) func() {
	m.mu.Lock()
	lock, ok := m.locks[invocationID]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[invocationID] = lock
	}
	m.mu.Unlock()

	lock.Lock()
	return func() {
		lock.Unlock()
	}
}

// HashArguments calculates a deterministic SHA-256 hash of tool arguments.
func HashArguments(args map[string]any) (string, error) {
	if args == nil {
		args = make(map[string]any)
	}
	// json.Marshal on map[string]any sorts keys deterministically in Go
	data, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("failed to marshal arguments for hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// DeriveInvocationID derives a stable invocation identifier from execution ID, tool name, and call metadata.
func DeriveInvocationID(executionID string, toolName string, args map[string]any) (string, error) {
	// If caller explicitly supplied an idempotency key in arguments, prioritize it
	if key, ok := args["_idempotency_key"].(string); ok && key != "" {
		return fmt.Sprintf("inv_%s_%s", executionID, key), nil
	}

	hash, err := HashArguments(args)
	if err != nil {
		return "", err
	}
	// Deterministic invocation ID based on execution scope, tool, and argument digest
	return fmt.Sprintf("inv_%s_%s_%s", executionID, toolName, hash[:16]), nil
}
