package router

import (
	"encoding/json"
	"net/http"

	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/gin-gonic/gin"
)

// RegisterChannelHandler godoc
// @Summary Register a channel forwarded from server_api
// @Accept json
// @Produce json
// @Success 201 {object} sdk.Channel
// @Router /api/v1/channels [post]
func (r *AppRouter) RegisterChannelHandler(c *gin.Context) {
	logger := utils.GetLogger(c.Request.Context())

	var ch sdk.Channel
	if err := json.NewDecoder(c.Request.Body).Decode(&ch); err != nil {
		logger.Error("failed to decode register channel request", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := r.service.RegisterChannel(c.Request.Context(), &ch); err != nil {
		logger.Error("failed to register channel", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, &ch)
}

// ListChannelsHandler godoc
// @Summary List all relay channels
// @Produce json
// @Success 200 {object} sdk.ListChannelsResponse
// @Router /api/v1/channels [get]
func (r *AppRouter) ListChannelsHandler(c *gin.Context) {
	channels, err := r.service.ListChannels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sdk.ListChannelsResponse{Channels: channels, Count: len(channels)})
}

// GetChannelHandler godoc
// @Summary Get a relay channel by ID
// @Produce json
// @Param id path string true "Channel ID"
// @Success 200 {object} sdk.Channel
// @Router /api/v1/channels/{id} [get]
func (r *AppRouter) GetChannelHandler(c *gin.Context) {
	ch, err := r.service.GetChannel(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ch)
}

// RevokeChannelHandler godoc
// @Summary Revoke a relay channel
// @Param id path string true "Channel ID"
// @Success 204
// @Router /api/v1/channels/{id} [delete]
func (r *AppRouter) RevokeChannelHandler(c *gin.Context) {
	if err := r.service.RevokeChannel(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// ListListenersHandler godoc
// @Summary List all connected channel listeners
// @Produce json
// @Success 200 {object} sdk.ListListenersResponse
// @Router /api/v1/listeners [get]
func (r *AppRouter) ListListenersHandler(c *gin.Context) {
	listeners, err := r.service.ListListeners(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sdk.ListListenersResponse{Listeners: listeners, Count: len(listeners)})
}
