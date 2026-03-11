package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/utils"
)

// RunRenewalLoop runs as a background goroutine and proactively renews the
// Hyphae tunnel TLS cert before it expires. The check interval is 24 hours;
// renewal is triggered when fewer than settings.Bootstrap.RenewBeforeDays days
// remain on the on-disk cert.
//
// After a successful renewal, the CertManager is updated in-place so new TLS
// handshakes use the fresh cert without restarting the tunnel listener.
func RunRenewalLoop(ctx context.Context, settings *utils.Settings, mgr *CertManager, logger *slog.Logger) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := checkAndRenew(ctx, settings, mgr, logger); err != nil {
				logger.Error("cert renewal failed — will retry in 24h", "error", err)
			}
		}
	}
}

func checkAndRenew(ctx context.Context, settings *utils.Settings, mgr *CertManager, logger *slog.Logger) error {
	renewBefore := time.Duration(settings.Bootstrap.RenewBeforeDays) * 24 * time.Hour
	if renewBefore == 0 {
		renewBefore = DefaultRenewBeforeDays * 24 * time.Hour
	}

	certPath := settings.Tunnel.TLSCert
	keyPath := settings.Tunnel.TLSKey

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		logger.Warn("could not load on-disk cert for renewal check, triggering renewal",
			"cert_path", certPath, "error", err)
	} else {
		x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse on-disk cert: %w", err)
		}
		if time.Until(x509Cert.NotAfter) > renewBefore {
			logger.Debug("tunnel cert still valid, skipping renewal",
				"expires_at", x509Cert.NotAfter,
				"renew_before_days", settings.Bootstrap.RenewBeforeDays)
			return nil
		}
		logger.Info("tunnel cert expiring soon, renewing",
			"expires_at", x509Cert.NotAfter,
			"renew_before_days", settings.Bootstrap.RenewBeforeDays)
	}

	// Also refresh the CA cert in case it was regenerated.
	if caErr := EnsureCACert(ctx, settings); caErr != nil {
		logger.Warn("CA cert refresh failed during renewal — proceeding with existing CA", "error", caErr)
	}

	newCert, err := EnsureCert(ctx, settings)
	if err != nil {
		return fmt.Errorf("fetch renewed cert: %w", err)
	}

	mgr.Update(newCert)
	logger.Info("tunnel TLS cert renewed and hot-swapped")
	return nil
}
