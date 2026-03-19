package bootstrap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/utils"
)

const DefaultRenewBeforeDays = 7

// CertManager holds the active TLS certificate for the tunnel server and
// supports atomic hot-swap via the GetCertificate callback. Wiring this into
// tls.Config.GetCertificate allows RunRenewalLoop to rotate the cert without
// restarting the tunnel listener.
type CertManager struct {
	mu   sync.RWMutex
	cert *tls.Certificate
}

// NewCertManager wraps an initial certificate for use with the tunnel server.
func NewCertManager(cert tls.Certificate) *CertManager {
	return &CertManager{cert: &cert}
}

// GetCertificate satisfies the tls.Config.GetCertificate signature. The tunnel
// server calls this for every new TLS handshake so cert renewals from
// RunRenewalLoop take effect without a listener restart.
func (m *CertManager) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cert, nil
}

// Update atomically replaces the live certificate.
func (m *CertManager) Update(cert tls.Certificate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cert = &cert
}

// EnsureCACert fetches the CA certificate from server_api's public endpoint and
// writes it to the path specified by settings.Tunnel.CACert.
func EnsureCACert(ctx context.Context, settings *utils.Settings) error {
	caPath := settings.Tunnel.CACert
	if caPath == "" {
		caPath = "certs/ca.crt"
	}

	baseURL := strings.TrimRight(settings.ServerAPI.BaseURL, "/")
	caCertURL := baseURL + "/ca/certificate"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, caCertURL, nil)
	if err != nil {
		return fmt.Errorf("build CA cert request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch CA cert from %s: %w", caCertURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("CA cert fetch returned %d: %s", resp.StatusCode, string(body))
	}

	caCertPEM, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read CA cert response: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(caPath), 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}
	if err := os.WriteFile(caPath, caCertPEM, 0o644); err != nil {
		return fmt.Errorf("write CA cert to %s: %w", caPath, err)
	}

	return nil
}

// EnsureGrantVerifyKey fetches the token-signing public key from server_api's
// public /tokens/public-key endpoint and writes it as PEM to keyPath. This is
// the ES256 public key used to verify channel grant JWTs. The endpoint requires
// no authentication (public keys are safe to expose).
func EnsureGrantVerifyKey(ctx context.Context, settings *utils.Settings, keyPath string) error {
	baseURL := strings.TrimRight(settings.ServerAPI.BaseURL, "/")
	keyURL := baseURL + "/tokens/public-key?format=pem"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keyURL, nil)
	if err != nil {
		return fmt.Errorf("build grant verify key request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch grant verify key from %s: %w", keyURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("grant verify key fetch returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode grant verify key response: %w", err)
	}
	if result.PublicKey == "" {
		return fmt.Errorf("grant verify key response has empty public_key field")
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return fmt.Errorf("create dir for grant verify key: %w", err)
	}
	if err := os.WriteFile(keyPath, []byte(result.PublicKey), 0o644); err != nil {
		return fmt.Errorf("write grant verify key to %s: %w", keyPath, err)
	}

	return nil
}

// EnsureCert returns a valid TLS certificate for Hyphae. If the cert on disk is
// absent or will expire within RenewBeforeDays, it fetches a fresh cert from
// server_api via the service CSR endpoint and writes it to disk.
func EnsureCert(ctx context.Context, settings *utils.Settings) (tls.Certificate, error) {
	certPath := settings.Tunnel.TLSCert
	keyPath := settings.Tunnel.TLSKey

	renewBefore := time.Duration(settings.Bootstrap.RenewBeforeDays) * 24 * time.Hour
	if renewBefore == 0 {
		renewBefore = DefaultRenewBeforeDays * 24 * time.Hour
	}

	// Check if the on-disk cert is present, signed by the current CA, and
	// not expiring soon.
	if _, err := os.Stat(certPath); err == nil {
		if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
			if x509Cert, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
				if time.Until(x509Cert.NotAfter) > renewBefore && verifyCertAgainstCA(x509Cert, settings.Tunnel.CACert) {
					return cert, nil
				}
			}
		}
	}

	// Cert is absent or expiring — fetch a new one via M2M CSR flow.
	certCN := settings.Bootstrap.CertCN

	token, err := fetchM2MToken(ctx, settings)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("fetch M2M token: %w", err)
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate ECDSA key: %w", err)
	}

	// Build DNS SANs from config; always include certCN.
	dnsNames := []string{certCN}
	for _, n := range settings.Bootstrap.CertDNSNames {
		if n != certCN {
			dnsNames = append(dnsNames, n)
		}
	}

	csrPEM, err := generateCSR(privKey, certCN, dnsNames)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate CSR: %w", err)
	}

	certPEM, err := signServiceCSR(ctx, settings, token, "hyphae", csrPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sign CSR with server_api: %w", err)
	}

	keyBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create certs dir: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("write cert to %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write key to %s: %w", keyPath, err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse newly issued cert: %w", err)
	}
	return cert, nil
}

type m2mTokenResponse struct {
	AccessToken string `json:"access_token"`
}

func fetchM2MToken(ctx context.Context, settings *utils.Settings) (string, error) {
	m2m := settings.Bootstrap.M2M
	form := url.Values{
		"client_id": {m2m.ClientID},
		"audience":  {m2m.Audience},
	}
	if m2m.Username != "" {
		form.Set("grant_type", "password")
		form.Set("username", m2m.Username)
		form.Set("password", string(m2m.Password))
	} else {
		form.Set("grant_type", "client_credentials")
		form.Set("client_secret", string(m2m.ClientSecret))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m2m.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request to %s: %w", m2m.TokenURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp m2mTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	return tokenResp.AccessToken, nil
}

func generateCSR(key *ecdsa.PrivateKey, cn string, dnsNames []string) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"Underleaf"},
			CommonName:   cn,
		},
		DNSNames: dnsNames,
	}
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes}), nil
}

type serviceCSRRequestBody struct {
	CSR     string `json:"csr"`
	Service string `json:"service"`
}

type serviceCSRResponse struct {
	CertificatePEM string `json:"certificate_pem"`
}

func signServiceCSR(ctx context.Context, settings *utils.Settings, token, service string, csrPEM []byte) ([]byte, error) {
	reqBody, err := json.Marshal(serviceCSRRequestBody{
		CSR:     string(csrPEM),
		Service: service,
	})
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimRight(settings.ServerAPI.BaseURL, "/")
	csrURL := baseURL + "/services/csr"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, csrURL,
		strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("build CSR request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CSR request to %s: %w", csrURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("CSR signing returned %d: %s", resp.StatusCode, string(body))
	}

	var csrResp serviceCSRResponse
	if err := json.NewDecoder(resp.Body).Decode(&csrResp); err != nil {
		return nil, fmt.Errorf("decode CSR response: %w", err)
	}
	return []byte(csrResp.CertificatePEM), nil
}

// verifyCertAgainstCA checks that cert was signed by the CA at caPath.
func verifyCertAgainstCA(cert *x509.Certificate, caPath string) bool {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return false
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return false
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err == nil
}
