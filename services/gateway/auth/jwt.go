package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// JWTConfig holds configuration for JWT verification.
type JWTConfig struct {
	Secret   []byte
	Issuer   string
	Audience string
}

// Validate checks that the JWT configuration is valid and safe to use.
func (c JWTConfig) Validate() error {
	if len(c.Secret) == 0 {
		return errors.New("jwt secret key is required and cannot be empty (fails closed)")
	}
	if len(c.Secret) < 16 {
		return errors.New("jwt secret key must be at least 16 bytes for security")
	}
	return nil
}

// Claims represents the standard and custom claims required by ARDA.
type Claims struct {
	TenantID string `json:"tenant_id"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// TokenValidator verifies and parses JWT bearer tokens.
type TokenValidator struct {
	config JWTConfig
}

// NewTokenValidator creates a new TokenValidator, verifying configuration safety.
func NewTokenValidator(cfg JWTConfig) (*TokenValidator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid jwt configuration: %w", err)
	}
	return &TokenValidator{config: cfg}, nil
}

// ValidateToken parses, validates signature and claims, and returns the verified Identity.
func (v *TokenValidator) ValidateToken(tokenString string) (Identity, error) {
	if tokenString == "" {
		return Identity{}, errors.New("token string is empty")
	}

	var claims Claims

	// Enforce strict HMAC-SHA256 verification and explicitly reject 'none' or other algorithms
	token, err := jwt.ParseWithClaims(
		tokenString,
		&claims,
		func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, fmt.Errorf("unexpected signing algorithm: %s, only HS256 is supported", token.Method.Alg())
			}
			return v.config.Secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)

	if err != nil {
		return Identity{}, fmt.Errorf("token validation failed: %w", err)
	}

	if !token.Valid {
		return Identity{}, errors.New("token is invalid")
	}

	// Validate issuer if configured
	if v.config.Issuer != "" {
		iss, err := claims.GetIssuer()
		if err != nil || iss != v.config.Issuer {
			return Identity{}, fmt.Errorf("token issuer %q does not match expected issuer %q", iss, v.config.Issuer)
		}
	}

	// Validate audience if configured
	if v.config.Audience != "" {
		auds, err := claims.GetAudience()
		if err != nil {
			return Identity{}, fmt.Errorf("failed to retrieve audience: %w", err)
		}
		found := false
		for _, aud := range auds {
			if aud == v.config.Audience {
				found = true
				break
			}
		}
		if !found {
			return Identity{}, fmt.Errorf("token audience does not match expected audience %q", v.config.Audience)
		}
	}

	// Validate subject (user ID)
	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return Identity{}, errors.New("missing or empty 'sub' claim in token")
	}

	// Validate tenant_id
	if claims.TenantID == "" {
		return Identity{}, errors.New("missing 'tenant_id' claim in token")
	}
	tenantUUID, err := uuid.Parse(claims.TenantID)
	if err != nil || tenantUUID == uuid.Nil {
		return Identity{}, fmt.Errorf("invalid 'tenant_id' claim: %w", err)
	}

	// Validate role
	if claims.Role == "" {
		return Identity{}, errors.New("missing 'role' claim in token")
	}
	role, err := ParseRole(claims.Role)
	if err != nil {
		return Identity{}, fmt.Errorf("invalid 'role' claim: %w", err)
	}

	return Identity{
		UserID:   sub,
		TenantID: tenantUUID,
		Role:     role,
	}, nil
}

// IssueToken generates a signed access JWT for the verified identity.
func (v *TokenValidator) IssueToken(id Identity, ttl time.Duration) (string, error) {
	return GenerateTestToken(id, v.config, ttl)
}

// Config returns the active JWTConfig.
func (v *TokenValidator) Config() JWTConfig {
	return v.config
}

// GenerateTestToken creates a signed token for testing or local authorization purposes.
func GenerateTestToken(id Identity, cfg JWTConfig, ttl time.Duration) (string, error) {
	now := time.Now().UTC()
	claims := Claims{
		TenantID: id.TenantID.String(),
		Role:     string(id.Role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   id.UserID,
			Issuer:    cfg.Issuer,
			Audience:  jwt.ClaimStrings{cfg.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(cfg.Secret)
}
