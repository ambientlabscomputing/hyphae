// Command hyphae runs the hyphae gateway service.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/admin_server"
	"github.com/ambientlabscomputing/hyphae/internal/proxy"
	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/internal/router"
	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/internal/tunnel"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	settings, err := utils.LoadSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hyphae: failed to load settings: %v\n", err)
		os.Exit(1)
	}

	logger, ctx := utils.InitLoggerWithContext(ctx, settings)
	logger.Info("Starting hyphae", "version", version)

	repo := repository.NewRepository(ctx)

	svc, err := service.NewService(ctx, repo, service.ServiceConfig{
		MaxLeasesPerOrg: settings.Leases.MaxPerOrg,
		MaxTTLSeconds:   settings.Leases.MaxTTLSeconds,
	})
	if err != nil {
		logger.Error("Failed to create service", "error", err)
		os.Exit(1)
	}

	if err := router.StartMiddleware(ctx, settings); err != nil {
		logger.Error("Failed to initialise JWT middleware", "error", err)
		os.Exit(1)
	}
	appRouter := router.NewAppRouter(svc, settings, ctx)

	tunnelSrv := tunnel.NewTunnelServer(tunnel.Config{
		Port:                  settings.Tunnel.Port,
		CACert:                settings.Tunnel.CACert,
		TLSCert:               settings.Tunnel.TLSCert,
		TLSKey:                settings.Tunnel.TLSKey,
		HandshakeTimeout:      settings.TunnelHandshakeTimeout(),
		MaxConnectionsPerNode: settings.Tunnel.MaxConnectionsPerNode,
		MaxTotalConnections:   settings.Tunnel.MaxTotalConnections,
	}, svc)

	proxySrv := proxy.NewProxyServer(proxy.Config{
		Port:           settings.PublicGateway.Port,
		TLSCert:        settings.PublicGateway.TLSCert,
		TLSKey:         settings.PublicGateway.TLSKey,
		MaxConnections: settings.PublicGateway.MaxConnections,
		ReadTimeout:    settings.PublicGatewayReadTimeout(),
		WriteTimeout:   settings.PublicGatewayWriteTimeout(),
		IdleTimeout:    settings.PublicGatewayIdleTimeout(),
	}, repo)

	var adminSrv *admin_server.AdminServer
	if settings.AdminSocket.Enabled {
		adminSrv = admin_server.NewAdminServer(svc, settings.AdminSocket.SocketPath, version)
		if err := adminSrv.Start(ctx); err != nil {
			logger.Error("Failed to start admin server", "error", err)
			os.Exit(1)
		}
	}

	go func() {
		if err := tunnelSrv.Listen(ctx); err != nil {
			logger.Error("Tunnel server error", "error", err)
		}
	}()
	go func() {
		if err := proxySrv.Listen(ctx); err != nil {
			logger.Error("Proxy server error", "error", err)
		}
	}()

	addr := appRouter.Addr()
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           appRouter.Handler(),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("Management API listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Management API error", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("Shutting down hyphae — draining connections (15s)")

	// Orderly shutdown: management API first (stops new requests),
	// then admin socket, then proxy/tunnel (context cancel closes their listeners).
	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := httpSrv.Shutdown(shutCtx); err != nil {
		logger.Warn("Management API shutdown error", "error", err)
	}
	if adminSrv != nil {
		if err := adminSrv.Stop(shutCtx); err != nil {
			logger.Warn("Admin server shutdown error", "error", err)
		}
	}
	// Proxy and tunnel servers are stopped by context cancellation above.
	// Give them a moment to drain active connections.
	select {
	case <-shutCtx.Done():
		logger.Warn("Shutdown timed out — forcing exit")
	case <-time.After(5 * time.Second):
		logger.Info("Hyphae stopped cleanly")
	}
}
