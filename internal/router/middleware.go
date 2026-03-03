package router

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/MicahParks/keyfunc"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// ── JWKS ─────────────────────────────────────────────────────────────────────

var jwks *keyfunc.JWKS

// StartMiddleware initialises the JWKS key cache from Auth0.
func StartMiddleware(ctx context.Context, settings *utils.Settings) error {
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
func VerifyToken(tokenString string) (*jwt.Token, error) {
	if jwks == nil {
		return nil, fmt.Errorf("JWKS not initialised")
	}
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, jwks.Keyfunc,
		jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("token is invalid")
	}
	return token, nil
}

// ── Auth middleware ───────────────────────────────────────────────────────────

// JWTAuthMiddleware validates a Bearer JWT (Auth0 RS256).
// server_api uses its M2M client_credentials grant to talk to this endpoint,
// so the token will be a standard Auth0 JWT — no custom "uf_" prefix handling needed here.
func JWTAuthMiddleware(appCtx context.Context) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := utils.GetLogger(appCtx)

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing Authorization header"})
			c.Abort()
			return
		}

		var tokenString string
		fmt.Sscanf(authHeader, "Bearer %s", &tokenString)

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
