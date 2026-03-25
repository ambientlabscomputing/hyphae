// Command hyphae runs the hyphae gateway service.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/admin_server"
	"github.com/ambientlabscomputing/hyphae/internal/bootstrap"
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
	reaperInterval := 5 * time.Second
	if d, err := time.ParseDuration(settings.Channels.ReapInterval); err == nil && d > 0 {
		reaperInterval = d
	}
	channelRepo := repository.NewChannelRepository(ctx, reaperInterval)

	// Load channel grant verify key if the feature is enabled.
	var channelGrantVerifyKey *ecdsa.PublicKey
	if settings.Channels.Enabled {
		keyPath := settings.Channels.GrantVerifyKeyPath
		if keyPath == "" {
			// Use the same certs directory as the CA cert (typically /etc/underleaf/certs/
			// inside the container). Fall back to a relative path if ca_cert is unset.
			certsDir := "certs"
			if settings.Tunnel.CACert != "" {
				certsDir = filepath.Dir(settings.Tunnel.CACert)
			}
			keyPath = filepath.Join(certsDir, "channel_grant_verify.pem")
		}

		// Auto-fetch the token-signing public key from server_api (same retry
		// pattern as the CA cert bootstrap above).
		const maxKeyAttempts = 5
		keyBackoff := 2 * time.Second
		for attempt := 1; attempt <= maxKeyAttempts; attempt++ {
			if err := bootstrap.EnsureGrantVerifyKey(ctx, settings, keyPath); err == nil {
				break
			} else {
				logger.Error("grant verify key fetch failed", "attempt", attempt, "max", maxKeyAttempts, "backoff", keyBackoff, "error", err)
				if attempt == maxKeyAttempts {
					logger.Error("grant verify key bootstrap failed after all attempts")
					os.Exit(1)
				}
				select {
				case <-ctx.Done():
					logger.Error("context cancelled during grant verify key bootstrap")
					os.Exit(1)
				case <-time.After(keyBackoff):
				}
				keyBackoff *= 2
			}
		}
		logger.Info("Grant verify key fetched from server_api", "path", keyPath)

		key, err := loadChannelGrantVerifyKey(keyPath)
		if err != nil {
			logger.Error("Failed to load channel grant verify key", "error", err)
			os.Exit(1)
		}
		channelGrantVerifyKey = key
		logger.Info("Channel grant verify key loaded", "path", keyPath)
	}

	svc, err := service.NewService(ctx, repo, channelRepo, service.ServiceConfig{
		MaxLeasesPerOrg:       settings.Leases.MaxPerOrg,
		MaxTTLSeconds:         settings.Leases.MaxTTLSeconds,
		ChannelsEnabled:       settings.Channels.Enabled,
		ChannelGrantVerifyKey: channelGrantVerifyKey,
		ChannelMaxIdleSeconds: settings.Channels.MaxIdleSeconds,
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

	// Bootstrap TLS cert if configured.
	var certMgr *bootstrap.CertManager
	if settings.Bootstrap.CertCN != "" {
		logger.Info("cert bootstrap enabled", "cert_cn", settings.Bootstrap.CertCN)
		certMgr = runBootstrap(ctx, settings, logger)
	}

	tunnelSrv := tunnel.NewTunnelServer(tunnel.Config{
		Port:                  settings.Tunnel.Port,
		CACert:                settings.Tunnel.CACert,
		TLSCert:               settings.Tunnel.TLSCert,
		TLSKey:                settings.Tunnel.TLSKey,
		GetCertificate:        certMgrGetCertificate(certMgr),
		HandshakeTimeout:      settings.TunnelHandshakeTimeout(),
		MaxConnectionsPerNode: settings.Tunnel.MaxConnectionsPerNode,
		MaxTotalConnections:   settings.Tunnel.MaxTotalConnections,
		ChannelsEnabled:       settings.Channels.Enabled,
		ChannelIdleSeconds:    settings.Channels.MaxIdleSeconds,
	}, svc)

	// Start certificate renewal loop (no-op if bootstrap was not configured).
	if certMgr != nil {
		go bootstrap.RunRenewalLoop(ctx, settings, certMgr, logger)
	}

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

// runBootstrap fetches the CA cert and ensures the hyphae TLS cert is valid,
// retrying with exponential backoff (5 attempts, 2s initial). Exits the process
// if all attempts fail — the cert is required for the tunnel server to start.
// Returns a *bootstrap.CertManager holding the live cert for hot-swap renewal.
func runBootstrap(ctx context.Context, settings *utils.Settings, logger *slog.Logger) *bootstrap.CertManager {
	const maxAttempts = 5

	// Step 1: Fetch CA cert from server_api (public endpoint, no auth).
	backoff := 2 * time.Second
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := bootstrap.EnsureCACert(ctx, settings); err == nil {
			break
		} else {
			logger.Error("CA cert fetch failed", "attempt", attempt, "max", maxAttempts, "backoff", backoff, "error", err)
			if attempt == maxAttempts {
				logger.Error("CA cert bootstrap failed after all attempts")
				os.Exit(1)
			}
			select {
			case <-ctx.Done():
				logger.Error("context cancelled during CA cert bootstrap")
				os.Exit(1)
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	logger.Info("CA cert fetched from server_api")

	// Step 2: Ensure our own TLS cert (CSR flow if absent or expiring).
	backoff = 2 * time.Second
	var cert tls.Certificate
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var err error
		cert, err = bootstrap.EnsureCert(ctx, settings)
		if err == nil {
			logger.Info("TLS cert bootstrapped", "cert_cn", settings.Bootstrap.CertCN)
			return bootstrap.NewCertManager(cert)
		}
		logger.Error("hyphae cert bootstrap failed", "attempt", attempt, "max", maxAttempts, "backoff", backoff, "error", err)
		if attempt == maxAttempts {
			logger.Error("hyphae cert bootstrap failed after all attempts")
			os.Exit(1)
		}
		select {
		case <-ctx.Done():
			logger.Error("context cancelled during cert bootstrap")
			os.Exit(1)
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	// Unreachable — loop always returns or calls os.Exit, but compiler needs it.
	return bootstrap.NewCertManager(cert)
}

// certMgrGetCertificate returns the CertManager's GetCertificate callback when
// the manager is non-nil, or nil when bootstrap was not configured.
func certMgrGetCertificate(mgr *bootstrap.CertManager) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if mgr == nil {
		return nil
	}
	return mgr.GetCertificate
}

// loadChannelGrantVerifyKey reads an ECDSA P-256 public key from a PKIX PEM
// file and returns it for verifying ES256 channel grant JWTs signed by server_api.
func loadChannelGrantVerifyKey(path string) (*ecdsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read grant verify key %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKIX public key in %s: %w", path, err)
	}
	ecKey, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key in %s is not ECDSA (got %T)", path, key)
	}
	return ecKey, nil
}
