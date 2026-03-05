package router

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// ── JWKS ─────────────────────────────────────────────────────────────────────

var (
	jwks        *keyfunc.JWKS
	jwtAudience string
	jwtIssuer   string
)

// StartMiddleware initialises the JWKS key cache from Auth0.
func StartMiddleware(ctx context.Context, settings *utils.Settings) error {
	jwtAudience = settings.Auth.AuthAudience
	// Auth0 issuer is always https://<domain>/
	jwtIssuer = "https://" + settings.Auth.AuthDomain + "/"
	return initJWKS(ctx, settings)
}

func initJWKS(ctx context.Context, settings *utils.Settings) error {
	logger := utils.GetLogger(ctx)
	jwksURL := "https://" + settings.Auth.AuthDomain + "/.well-known/jwks.json"
	var err error
	jwks, err = keyfunc.Get(jwksURL, keyfunc.Options{
		RefreshInterval:   time.Hour,
		RefreshTimeout:    10 * time.Second,
		RefreshUnknownKID: true,
		RefreshErrorHandler: func(e error) {
			logger.Error("JWKS refresh error", "error", e)
		},
		Client: &http.Client{Timeout: 10 * time.Second},
	})
	return err
}

// VerifyToken parses and validates a JWT using the cached JWKS.
// It enforces RS256, audience, and issuer claims.
func VerifyToken(tokenString string) (*jwt.Token, error) {
	if jwks == nil {
		return nil, fmt.Errorf("JWKS not initialized")
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, jwks.Keyfunc,
		jwt.WithValidMethods([]string{"RS256"}),
	)
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("token is invalid")
	}
	// Validate audience and issuer explicitly (jwt/v4 doesn't have option helpers for these).
	if !claims.VerifyAudience(jwtAudience, true) {
		return nil, fmt.Errorf("token audience mismatch")
	}
	if !claims.VerifyIssuer(jwtIssuer, true) {
		return nil, fmt.Errorf("token issuer mismatch")
	}
	return token, nil
}

// ── Auth middleware ───────────────────────────────────────────────────────────

// JWTAuthMiddleware validates a Bearer JWT (Auth0 RS256).
func JWTAuthMiddleware(appCtx context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := utils.GetLogger(appCtx)

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing Authorization header"})
			c.Abort()
			return
		}

		// Robust Bearer extraction — handles extra whitespace.
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid Authorization header format"})
			c.Abort()
			return
		}
		tokenString := strings.TrimSpace(authHeader[len(bearerPrefix):])
		if tokenString == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "empty bearer token"})
			c.Abort()
			return
		}

		token, err := VerifyToken(tokenString)
		if err != nil {
			logger.Warn("JWT verification failed", "error", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
			c.Abort()
			return
		}

		if sub, ok := claims["sub"].(string); ok {
			c.Set("requester_sub", sub)
		}

		c.Next()
	}
}

// ── Rate limiting ─────────────────────────────────────────────────────────────

type ipRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rateLimiterEntry
	r        rate.Limit
	b        int
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(r rate.Limit, b int) *ipRateLimiter {
	rl := &ipRateLimiter{
		limiters: make(map[string]*rateLimiterEntry),
		r:        r,
		b:        b,
	}
	// Periodically evict entries not seen in the last 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			rl.mu.Lock()
			for ip, entry := range rl.limiters {
				if time.Since(entry.lastSeen) > 5*time.Minute {
					delete(rl.limiters, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, exists := rl.limiters[ip]
	if !exists {
		lim := rate.NewLimiter(rl.r, rl.b)
		rl.limiters[ip] = &rateLimiterEntry{limiter: lim, lastSeen: time.Now()}
		return lim
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// RateLimitMiddleware enforces per-IP request rate limits on the management API.
func RateLimitMiddleware(requestsPerMinute float64, burst int) gin.HandlerFunc {
	limiter := newIPRateLimiter(rate.Limit(requestsPerMinute/60.0), burst)
	return func(c *gin.Context) {
		ip := c.ClientIP()
		if !limiter.getLimiter(ip).Allow() {
			c.Header("Retry-After", "60")
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// ── Security headers middleware ───────────────────────────────────────────────

// SecurityHeadersMiddleware adds HTTP security headers to all responses.
func SecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Cache-Control", "no-store")
		c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		c.Next()
	}
}

// ── Request body limit middleware ─────────────────────────────────────────────

// MaxBodySizeMiddleware rejects requests with a body larger than maxBytes.
func MaxBodySizeMiddleware(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// ── Trace ID middleware ───────────────────────────────────────────────────────

// TraceIDMiddleware injects a UUID trace ID into each request context and adds
// it to the response headers.
func TraceIDMiddleware(appCtx context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := c.GetHeader("X-Trace-ID")
		if traceID == "" {
			traceID = uuid.New().String()
		}
		ctx := utils.SetTraceID(c.Request.Context(), traceID)
		logger := utils.GetLogger(appCtx).With("trace_id", traceID)
		ctx = utils.WithLogger(ctx, logger)
		c.Request = c.Request.WithContext(ctx)
		c.Header("X-Trace-ID", traceID)
		c.Next()
	}
}

// ── Logging middleware ────────────────────────────────────────────────────────

// SlogLoggerMiddleware replaces Gin's stdout logger with slog.
func SlogLoggerMiddleware(appCtx context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		if raw := c.Request.URL.RawQuery; raw != "" {
			path += "?" + raw
		}
		c.Next()
		latency := time.Since(start)
		status := c.Writer.Status()
		logger := utils.GetLogger(appCtx)
		attrs := []slog.Attr{
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Duration("latency", latency),
			slog.String("client_ip", c.ClientIP()),
			slog.Int("bytes", c.Writer.Size()),
		}
		switch {
		case status >= 500:
			logger.LogAttrs(appCtx, slog.LevelError, "request", attrs...)
		case status >= 400:
			logger.LogAttrs(appCtx, slog.LevelWarn, "request", attrs...)
		default:
			logger.LogAttrs(appCtx, slog.LevelInfo, "request", attrs...)
		}
	}
}

// SlogRecoveryMiddleware captures panics via slog instead of stdout.
func SlogRecoveryMiddleware(appCtx context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				utils.GetLogger(appCtx).Error("panic recovered",
					"error", err,
					"stack", string(debug.Stack()),
					"path", c.Request.URL.Path,
				)
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}
