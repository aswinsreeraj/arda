package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/aswinsreeraj/arda/services/gateway/auth"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// Config defines settings for the process-local token-bucket rate limiter.
type Config struct {
	Enabled bool
	RPS     float64 // Requests per second
	Burst   int     // Burst capacity
}

// DefaultConfig provides sensible defaults for local development.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		RPS:     20.0, // 20 requests per second
		Burst:   40,   // up to 40 burst
	}
}

// clientEntry tracks a token-bucket limiter and its last access time.
type clientEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Limiter provides process-local, in-memory rate limiting scoped by tenant ID or client IP.
//
// NOTE: This limiter is process-local and is not globally consistent across multiple gateway replicas.
type Limiter struct {
	config  Config
	mu      sync.Mutex
	clients map[string]*clientEntry
}

// NewLimiter creates an in-memory rate limiter.
func NewLimiter(cfg Config) *Limiter {
	l := &Limiter{
		config:  cfg,
		clients: make(map[string]*clientEntry),
	}
	return l
}

// Allow reports whether an event with the given key is allowed to happen.
func (l *Limiter) Allow(key string) bool {
	if !l.config.Enabled {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	entry, exists := l.clients[key]
	if !exists {
		entry = &clientEntry{
			limiter:  rate.NewLimiter(rate.Limit(l.config.RPS), l.config.Burst),
			lastSeen: now,
		}
		l.clients[key] = entry
	}
	entry.lastSeen = now

	// Clean up stale entries if map grows large (older than 10 minutes)
	if len(l.clients) > 1000 {
		for k, v := range l.clients {
			if now.Sub(v.lastSeen) > 10*time.Minute {
				delete(l.clients, k)
			}
		}
	}

	return entry.limiter.Allow()
}

// KeyForRequest derives a rate-limiting key based on validated tenant identity or remote IP.
func KeyForRequest(r *http.Request) string {
	if identity, ok := auth.IdentityFromContext(r.Context()); ok {
		return "tenant:" + identity.TenantID.String()
	}
	// Fall back to client IP
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return "ip:" + host
}

// Middleware creates a Gin middleware that enforces rate limiting.
func Middleware(limiter *Limiter, writeErr func(w http.ResponseWriter, status int, code, message string)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter == nil {
			c.Next()
			return
		}
		key := KeyForRequest(c.Request)
		if !limiter.Allow(key) {
			c.Header("Retry-After", "1")
			c.Abort()
			writeErr(c.Writer, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "rate limit exceeded, please retry later")
			return
		}
		c.Next()
	}
}
