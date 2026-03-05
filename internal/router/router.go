package router

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/gin-gonic/gin"
)

// AppRouter wraps the Gin engine and service.
type AppRouter struct {
	engine   *gin.Engine
	service  service.Service
	settings *utils.Settings
	appCtx   context.Context
}

// NewAppRouter constructs and wires the Gin router.
func NewAppRouter(svc service.Service, settings *utils.Settings, appCtx context.Context) *AppRouter {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(SlogRecoveryMiddleware(appCtx))
	engine.Use(SlogLoggerMiddleware(appCtx))
	engine.Use(SecurityHeadersMiddleware())

	r := &AppRouter{
		engine:   engine,
		service:  svc,
		settings: settings,
		appCtx:   appCtx,
	}

	// Public health endpoint — no auth
	engine.GET("/health", r.HealthHandler)

	// Management API — requires Auth0 JWT
	api := engine.Group(settings.BasePath)
	api.Use(TraceIDMiddleware(appCtx))
	if settings.RateLimiting.Enabled {
		api.Use(RateLimitMiddleware(settings.RateLimiting.RequestsPerMinute, settings.RateLimiting.Burst))
	}
	api.Use(JWTAuthMiddleware(appCtx))
	// Limit request bodies to 1 MiB on mutating endpoints.
	api.Use(MaxBodySizeMiddleware(1 << 20))

	api.POST("/leases", r.IssueLeaseHandler)
	api.GET("/leases", r.ListLeasesHandler)
	api.GET("/leases/:id", r.GetLeaseHandler)
	api.DELETE("/leases/:id", r.RevokeLeaseHandler)
	api.GET("/connections", r.ListConnectionsHandler)

	return r
}

// Run starts the management API HTTP server.
func (r *AppRouter) Run(ctx context.Context) error {
	addr := r.settings.Address + ":" + r.settings.Port
	utils.GetLogger(ctx).Info("Management API listening", "addr", addr)
	return r.engine.Run(addr)
}

// Addr returns the listen address for the management API.
func (r *AppRouter) Addr() string {
	return r.settings.Address + ":" + r.settings.Port
}

// Handler returns the underlying http.Handler for use with http.Server.
func (r *AppRouter) Handler() http.Handler {
	return r.engine
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// HealthHandler godoc
// @Summary Health check
// @Produce json
// @Success 200 {object} sdk.HealthResponse
// @Router /health [get]
func (r *AppRouter) HealthHandler(c *gin.Context) {
	result, err := r.service.Health(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// IssueLeaseHandler godoc
// @Summary Issue a new lease
// @Accept json
// @Produce json
// @Success 201 {object} sdk.IssueLeaseResponse
// @Router /api/v1/leases [post]
func (r *AppRouter) IssueLeaseHandler(c *gin.Context) {
	logger := utils.GetLogger(c.Request.Context())
	var req sdk.IssueLeaseRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		logger.Error("failed to decode issue lease request", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	lease, err := r.service.IssueLease(c.Request.Context(), req)
	if err != nil {
		logger.Error("failed to issue lease", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, sdk.IssueLeaseResponse{Lease: lease})
}

// ListLeasesHandler godoc
// @Summary List all active leases
// @Produce json
// @Success 200 {object} sdk.ListLeasesResponse
// @Router /api/v1/leases [get]
func (r *AppRouter) ListLeasesHandler(c *gin.Context) {
	leases, err := r.service.ListLeases(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sdk.ListLeasesResponse{Leases: leases, Count: len(leases)})
}

// GetLeaseHandler godoc
// @Summary Get a lease by ID
// @Produce json
// @Param id path string true "Lease ID"
// @Success 200 {object} sdk.Lease
// @Router /api/v1/leases/{id} [get]
func (r *AppRouter) GetLeaseHandler(c *gin.Context) {
	lease, err := r.service.GetLease(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, lease)
}

// RevokeLeaseHandler godoc
// @Summary Revoke a lease and close its tunnel
// @Param id path string true "Lease ID"
// @Success 204
// @Router /api/v1/leases/{id} [delete]
func (r *AppRouter) RevokeLeaseHandler(c *gin.Context) {
	if err := r.service.RevokeLease(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// ListConnectionsHandler godoc
// @Summary List all active tunnel connections
// @Produce json
// @Success 200 {object} sdk.ListConnectionsResponse
// @Router /api/v1/connections [get]
func (r *AppRouter) ListConnectionsHandler(c *gin.Context) {
	conns, err := r.service.ListConnections(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sdk.ListConnectionsResponse{Connections: conns, Count: len(conns)})
}
