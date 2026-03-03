package utils

import (
	"fmt"
	"log/slog"
	"os"
	"reflect"

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
		Port    string `yaml:"port"`     // default 443
		TLSCert string `yaml:"tls_cert"` // path to fullchain PEM
		TLSKey  string `yaml:"tls_key"`  // path to private key PEM
	} `yaml:"public_gateway"`

	// Tunnel listener — mTLS, accepts connections from MMA nodes
	Tunnel struct {
		Port   string `yaml:"port"`    // default 9090
		CACert string `yaml:"ca_cert"` // platform CA cert used to verify node client certs
	} `yaml:"tunnel"`

	// Auth0 JWT validation (used by server_api M2M and org_jwt mode)
	Auth struct {
		AuthDomain   string `yaml:"auth_domain"`
		AuthAudience string `yaml:"auth_audience"`
	} `yaml:"auth"`

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
// applies defaults for unset fields.
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

	if err := settings.Validate(); err != nil {
		return nil, err
	}
	return &settings, nil
}

func (s *Settings) Validate() error {
	if !s.LogToStderr && !s.LogToFile {
		return fmt.Errorf("at least one of log_to_stderr or log_to_file must be true")
	}
	return nil
}
