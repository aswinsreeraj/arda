package gateway

import (
	"fmt"
	"net/http"

	"gorm.io/gorm"

	"github.com/aswinsreeraj/arda/pkg/temporal"
	"github.com/aswinsreeraj/arda/services/gateway/auth"
	gatewayhttp "github.com/aswinsreeraj/arda/services/gateway/http"
	"github.com/aswinsreeraj/arda/services/gateway/proxy"
	"github.com/aswinsreeraj/arda/services/gateway/ratelimit"
	"github.com/aswinsreeraj/arda/services/registry"
	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
)

// Config holds runtime configuration for API Gateway v1.
type Config struct {
	Port                   string
	JWTSecret              []byte
	JWTIssuer              string
	JWTAudience            string
	RateLimitRPS           float64
	RateLimitBurst         int
	RateLimitEnabled       bool
	ReliabilityServiceAddr string
	TemporalAddr           string
	TemporalNamespace      string
	TemporalTaskQueue      string
}

// Server encapsulates the API Gateway HTTP server and dependencies.
type Server struct {
	Handler            http.Handler
	JWTValidator       *auth.TokenValidator
	RateLimiter        *ratelimit.Limiter
	ReliabilityService proxy.ReliabilityService
	TemporalClient     *temporal.Client
}

// NewServer builds and validates the API Gateway. It fails closed if auth configuration is missing.
func NewServer(cfg Config, db *gorm.DB) (*Server, error) {
	// Fails closed if JWT configuration is missing or invalid
	jwtValidator, err := auth.NewTokenValidator(auth.JWTConfig{
		Secret:   cfg.JWTSecret,
		Issuer:   cfg.JWTIssuer,
		Audience: cfg.JWTAudience,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize jwt authentication (failing closed): %w", err)
	}

	rateCfg := ratelimit.DefaultConfig()
	if cfg.RateLimitRPS > 0 {
		rateCfg.RPS = cfg.RateLimitRPS
	}
	if cfg.RateLimitBurst > 0 {
		rateCfg.Burst = cfg.RateLimitBurst
	}
	rateCfg.Enabled = cfg.RateLimitEnabled
	limiter := ratelimit.NewLimiter(rateCfg)

	// Optionally initialize gRPC reliability client if upstream address is set
	var relSvc proxy.ReliabilityService
	if cfg.ReliabilityServiceAddr != "" {
		relClient, err := proxy.NewReliabilityClient(cfg.ReliabilityServiceAddr)
		if err != nil {
			// Upstream is offline or unreachable; document gap
			return nil, fmt.Errorf("failed to connect to reliability upstream: %w", err)
		}
		relSvc = relClient
	}

	// Optionally initialize Temporal client if upstream address is set
	var tempClient *temporal.Client
	if cfg.TemporalAddr != "" {
		tc, err := temporal.NewClient(temporal.Config{
			Address:   cfg.TemporalAddr,
			Namespace: cfg.TemporalNamespace,
			TaskQueue: cfg.TemporalTaskQueue,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize temporal client: %w", err)
		}
		tempClient = tc
	}

	// Mount Registry HTTP handlers if database connection is supplied.
	// Uses ContextTenantResolver so tenant identity is derived exclusively
	// from the cryptographically verified JWT context and never from headers.
	var regHandler http.Handler
	var regSvc gatewayhttp.RegistryService
	if db != nil {
		regServer := registry.NewServer(db, registryhttp.ContextTenantResolver{})
		regHandler = regServer.Handler
		regSvc = regServer.Service
	}

	gwHandler := gatewayhttp.NewGateway(gatewayhttp.GatewayConfig{
		JWTValidator:       jwtValidator,
		RateLimiter:        limiter,
		RegistryHandler:    regHandler,
		RegistryService:    regSvc,
		ReliabilityService: relSvc,
		TemporalClient:     tempClient,
	})

	return &Server{
		Handler:            gwHandler,
		JWTValidator:       jwtValidator,
		RateLimiter:        limiter,
		ReliabilityService: relSvc,
		TemporalClient:     tempClient,
	}, nil
}
