package utils

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"time"

	"gopkg.in/yaml.v3"
)

// SecretString masks itself when printed/logged.
type SecretString string

func (s SecretString) String() string { return "****" }

// Settings holds all configuration for hyphae.
type Settings struct {
	// HTTP management API
	BasePath string `yaml:"base_path"` // management API path prefix, default /api/v1
	Port     string `yaml:"port"`      // management API port, default 8084
	Address  string `yaml:"address"`   // bind address, default 0.0.0.0

	// Logging
	LogLevel    string `yaml:"log_level"`
	LogFormat   string `yaml:"log_format"`
	LogToStderr bool   `yaml:"log_to_stderr"`
	LogToFile   bool   `yaml:"log_to_file"`
	LogFilePath string `yaml:"log_file_path"`

	// Public TLS gateway (wildcard cert for *.underleafapp.com)
	PublicGateway struct {
		Port           string `yaml:"port"`            // default 443
		TLSCert        string `yaml:"tls_cert"`        // path to fullchain PEM
		TLSKey         string `yaml:"tls_key"`         // path to private key PEM
		MaxConnections int    `yaml:"max_connections"` // 0 = 10000 (default)
		ReadTimeout    string `yaml:"read_timeout"`    // duration string, default "30s"
		WriteTimeout   string `yaml:"write_timeout"`   // duration string, default "60s"
		IdleTimeout    string `yaml:"idle_timeout"`    // duration string, default "60s"
	} `yaml:"public_gateway"`

	// Tunnel listener — mTLS, accepts connections from MMA nodes
	Tunnel struct {
		Port                  string `yaml:"port"`                     // default 9090
		CACert                string `yaml:"ca_cert"`                  // platform CA cert
		TLSCert               string `yaml:"tls_cert"`                 // tunnel server certificate
		TLSKey                string `yaml:"tls_key"`                  // tunnel server private key
		HandshakeTimeout      string `yaml:"handshake_timeout"`        // default "10s"
		MaxConnectionsPerNode int    `yaml:"max_connections_per_node"` // 0 = 10 (default)
		MaxTotalConnections   int    `yaml:"max_total_connections"`    // 0 = 1000 (default)
	} `yaml:"tunnel"`

	// Auth0 JWT validation (used by server_api M2M and org_jwt mode)
	Auth struct {
		AuthDomain   string `yaml:"auth_domain"`
		AuthAudience string `yaml:"auth_audience"`
	} `yaml:"auth"`

	// Rate limiting for management API
	RateLimiting struct {
		Enabled           bool    `yaml:"enabled"`             // default true
		RequestsPerMinute float64 `yaml:"requests_per_minute"` // default 60
		Burst             int     `yaml:"burst"`               // default 20
	} `yaml:"rate_limiting"`

	// Lease policy
	Leases struct {
		MaxPerOrg     int   `yaml:"max_per_org"`     // 0 = 100 (default)
		MaxTTLSeconds int64 `yaml:"max_ttl_seconds"` // 0 = 86400 (default)
	} `yaml:"leases"`

	// Admin Unix socket (hyphctl)
	AdminSocket struct {
		Enabled    bool   `yaml:"enabled"`
		SocketPath string `yaml:"socket_path"`
	} `yaml:"admin_socket"`
}

var defaults = map[string]interface{}{
	"BasePath":    "/api/v1",
	"Port":        "8084",
	"Address":     "0.0.0.0",
	"LogLevel":    "info",
	"LogFormat":   "text",
	"LogToStderr": true,
	"LogToFile":   false,
	"LogFilePath": "./logs/hyphae.log",
}

// LoadSettings reads config from $CONFIG_PATH (default ./config.yaml) and
// applies defaults for unset fields. Secrets can be overridden via env vars:
//
//	HYPHAE_AUTH_DOMAIN    — overrides auth.auth_domain
//	HYPHAE_AUTH_AUDIENCE  — overrides auth.auth_audience
func LoadSettings() (*Settings, error) {
	slog.Info("Loading settings")
	settings := Settings{}
	val := reflect.ValueOf(&settings).Elem()
	for key, value := range defaults {
		field := val.FieldByName(key)
		if field.IsValid() && field.CanSet() {
			field.Set(reflect.ValueOf(value))
		}
	}

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "./config.yaml"
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		dir, _ := os.Getwd()
		slog.Error("failed to read config file", "error", err, "path", configPath, "dir", dir)
		return nil, fmt.Errorf("read config %s: %w", configPath, err)
	}

	var fileSettings Settings
	if err = yaml.Unmarshal(data, &fileSettings); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	fileVal := reflect.ValueOf(&fileSettings).Elem()
	for i := 0; i < fileVal.NumField(); i++ {
		field := fileVal.Type().Field(i)
		fv := fileVal.Field(i)
		if fv.IsValid() && !fv.IsZero() {
			sf := val.FieldByName(field.Name)
			if sf.IsValid() && sf.CanSet() {
				sf.Set(fv)
			}
		}
	}

	// Env var overrides for secrets (never commit these to config.yaml)
	if v := os.Getenv("HYPHAE_AUTH_DOMAIN"); v != "" {
		settings.Auth.AuthDomain = v
	}
	if v := os.Getenv("HYPHAE_AUTH_AUDIENCE"); v != "" {
		settings.Auth.AuthAudience = v
	}

	if err := settings.Validate(); err != nil {
		return nil, err
	}
	return &settings, nil
}

// Validate checks required fields and fills in defaults for optional ones.
func (s *Settings) Validate() error {
	if !s.LogToStderr && !s.LogToFile {
		return fmt.Errorf("at least one of log_to_stderr or log_to_file must be true")
	}
	if s.Auth.AuthDomain == "" {
		return fmt.Errorf("auth.auth_domain is required (or set HYPHAE_AUTH_DOMAIN)")
	}
	if s.Auth.AuthAudience == "" {
		return fmt.Errorf("auth.auth_audience is required (or set HYPHAE_AUTH_AUDIENCE)")
	}

	// Apply numeric defaults for new optional fields
	if s.PublicGateway.MaxConnections == 0 {
		s.PublicGateway.MaxConnections = 10000
	}
	if s.PublicGateway.ReadTimeout == "" {
		s.PublicGateway.ReadTimeout = "30s"
	}
	if s.PublicGateway.WriteTimeout == "" {
		s.PublicGateway.WriteTimeout = "60s"
	}
	if s.PublicGateway.IdleTimeout == "" {
		s.PublicGateway.IdleTimeout = "60s"
	}
	if s.Tunnel.HandshakeTimeout == "" {
		s.Tunnel.HandshakeTimeout = "10s"
	}
	if s.Tunnel.MaxConnectionsPerNode == 0 {
		s.Tunnel.MaxConnectionsPerNode = 10
	}
	if s.Tunnel.MaxTotalConnections == 0 {
		s.Tunnel.MaxTotalConnections = 1000
	}
	if s.RateLimiting.RequestsPerMinute == 0 {
		s.RateLimiting.Enabled = true
		s.RateLimiting.RequestsPerMinute = 60
		s.RateLimiting.Burst = 20
	}
	if s.Leases.MaxPerOrg == 0 {
		s.Leases.MaxPerOrg = 100
	}
	if s.Leases.MaxTTLSeconds == 0 {
		s.Leases.MaxTTLSeconds = 86400
	}

	// Validate all duration strings are parseable
	for _, pair := range []struct{ label, val string }{
		{"public_gateway.read_timeout", s.PublicGateway.ReadTimeout},
		{"public_gateway.write_timeout", s.PublicGateway.WriteTimeout},
		{"public_gateway.idle_timeout", s.PublicGateway.IdleTimeout},
		{"tunnel.handshake_timeout", s.Tunnel.HandshakeTimeout},
	} {
		if _, err := time.ParseDuration(pair.val); err != nil {
			return fmt.Errorf("invalid duration for %s %q: %w", pair.label, pair.val, err)
		}
	}
	return nil
}

// PublicGatewayReadTimeout returns the parsed ReadTimeout duration.
func (s *Settings) PublicGatewayReadTimeout() time.Duration {
	d, _ := time.ParseDuration(s.PublicGateway.ReadTimeout)
	return d
}

// PublicGatewayWriteTimeout returns the parsed WriteTimeout duration.
func (s *Settings) PublicGatewayWriteTimeout() time.Duration {
	d, _ := time.ParseDuration(s.PublicGateway.WriteTimeout)
	return d
}

// PublicGatewayIdleTimeout returns the parsed IdleTimeout duration.
func (s *Settings) PublicGatewayIdleTimeout() time.Duration {
	d, _ := time.ParseDuration(s.PublicGateway.IdleTimeout)
	return d
}

// TunnelHandshakeTimeout returns the parsed TunnelHandshakeTimeout duration.
func (s *Settings) TunnelHandshakeTimeout() time.Duration {
	d, _ := time.ParseDuration(s.Tunnel.HandshakeTimeout)
	return d
}
