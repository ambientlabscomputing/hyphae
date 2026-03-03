// Command hyphae runs the hyphae gateway service.
package main

import (
	"context"
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
		panic("failed to load settings: " + err.Error())
	}

	logger, ctx := utils.InitLoggerWithContext(ctx, settings)
	logger.Info("Starting hyphae", "version", version)

	repo := repository.NewRepository(ctx)

	svc, err := service.NewService(ctx, repo)
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
		Port:   settings.Tunnel.Port,
		CACert: settings.Tunnel.CACert,
	}, svc)

	proxySrv := proxy.NewProxyServer(proxy.Config{
		Port:    settings.PublicGateway.Port,
		TLSCert: settings.PublicGateway.TLSCert,
		TLSKey:  settings.PublicGateway.TLSKey,
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

	if err := appRouter.Run(ctx); err != nil {
		logger.Error("Management API error", "error", err)
	}

	<-ctx.Done()
	logger.Info("Shutting down hyphae")
	if adminSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = adminSrv.Stop(shutCtx)
	}
}
