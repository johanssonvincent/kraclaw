package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(*testing.T, *Config)
		wantErr bool
	}{
		{
			name:    "required fields missing",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "minimal valid config",
			env: map[string]string{
				"MYSQL_DSN":         "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":       "registry.local/agent:latest",
				"ANTHROPIC_API_KEY": "sk-test",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.MySQL.DSN != "user:pass@tcp(localhost:3306)/kraclaw" {
					t.Errorf("unexpected MySQL DSN: %s", cfg.MySQL.DSN)
				}
				if cfg.K8s.AgentImage != "registry.local/agent:latest" {
					t.Errorf("unexpected agent image: %s", cfg.K8s.AgentImage)
				}
			},
		},
		{
			name: "defaults applied",
			env: map[string]string{
				"MYSQL_DSN":         "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":       "registry.local/agent:latest",
				"ANTHROPIC_API_KEY": "sk-test",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.GRPCAddr != ":50051" {
					t.Errorf("unexpected gRPC addr: %s", cfg.Server.GRPCAddr)
				}
				if cfg.Server.RESTAddr != ":8080" {
					t.Errorf("unexpected REST addr: %s", cfg.Server.RESTAddr)
				}
				if cfg.Server.GRPCTLSCertFile != "/var/run/kraclaw/grpc-tls/tls.crt" {
					t.Errorf("unexpected gRPC TLS cert file: %s", cfg.Server.GRPCTLSCertFile)
				}
				if cfg.Server.GRPCAllowedCIDRs != "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fd00::/8,127.0.0.0/8,::1/128" {
					t.Errorf("unexpected gRPC allowed CIDRs: %s", cfg.Server.GRPCAllowedCIDRs)
				}
				if cfg.Queue.MaxConcurrent != 5 {
					t.Errorf("unexpected max concurrent: %d", cfg.Queue.MaxConcurrent)
				}
				if cfg.Queue.IdleTimeout != 30*time.Minute {
					t.Errorf("unexpected idle timeout: %v", cfg.Queue.IdleTimeout)
				}
				if cfg.MySQL.MaxOpenConns != 10 {
					t.Errorf("unexpected max open conns: %d", cfg.MySQL.MaxOpenConns)
				}
				if cfg.Logging.Format != "json" {
					t.Errorf("unexpected log format: %s", cfg.Logging.Format)
				}
				if cfg.Channels.AssistantName != "Kraclaw" {
					t.Errorf("unexpected assistant name: %s", cfg.Channels.AssistantName)
				}
			},
		},
		{
			name: "custom values",
			env: map[string]string{
				"MYSQL_DSN":          "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":        "registry.local/agent:latest",
				"ANTHROPIC_API_KEY":  "sk-test",
				"GRPC_ADDR":          ":9090",
				"GRPC_ALLOWED_CIDRS": "10.42.0.0/16,10.43.0.0/16",
				"MAX_CONCURRENT":     "10",
				"LOG_LEVEL":          "debug",
				"ASSISTANT_NAME":     "Andy",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.GRPCAddr != ":9090" {
					t.Errorf("unexpected gRPC addr: %s", cfg.Server.GRPCAddr)
				}
				if cfg.Server.GRPCAllowedCIDRs != "10.42.0.0/16,10.43.0.0/16" {
					t.Errorf("unexpected gRPC allowed CIDRs: %s", cfg.Server.GRPCAllowedCIDRs)
				}
				if cfg.Queue.MaxConcurrent != 10 {
					t.Errorf("unexpected max concurrent: %d", cfg.Queue.MaxConcurrent)
				}
				if cfg.Logging.Level != "debug" {
					t.Errorf("unexpected log level: %s", cfg.Logging.Level)
				}
				if cfg.Channels.AssistantName != "Andy" {
					t.Errorf("unexpected assistant name: %s", cfg.Channels.AssistantName)
				}
			},
		},
		{
			name: "missing API key and OAuth token",
			env: map[string]string{
				"MYSQL_DSN":   "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE": "registry.local/agent:latest",
			},
			wantErr: true,
		},
		{
			name: "invalid timezone",
			env: map[string]string{
				"MYSQL_DSN":         "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":       "registry.local/agent:latest",
				"ANTHROPIC_API_KEY": "sk-test",
				"TZ":                "Not/A/Timezone",
			},
			wantErr: true,
		},
		{
			name: "zero max concurrent",
			env: map[string]string{
				"MYSQL_DSN":         "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":       "registry.local/agent:latest",
				"ANTHROPIC_API_KEY": "sk-test",
				"MAX_CONCURRENT":    "0",
			},
			wantErr: true,
		},
		{
			name: "empty gRPC allowlist",
			env: map[string]string{
				"MYSQL_DSN":          "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":        "registry.local/agent:latest",
				"ANTHROPIC_API_KEY":  "sk-test",
				"GRPC_ALLOWED_CIDRS": "",
			},
			wantErr: true,
		},
		{
			name: "OAuth token instead of API key",
			env: map[string]string{
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"ANTHROPIC_OAUTH_TOKEN": "oauth-test",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.Proxy.OAuthToken != "oauth-test" {
					t.Errorf("unexpected OAuth token: %s", cfg.Proxy.OAuthToken)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all relevant env vars
			envKeys := []string{
				"MYSQL_DSN", "AGENT_IMAGE", "GRPC_ADDR", "REST_ADDR",
				"GRPC_TLS_CERT_FILE", "GRPC_TLS_KEY_FILE", "GRPC_TLS_CLIENT_CA_FILE",
				"GRPC_ALLOWED_CIDRS", "GRPC_REFLECTION_ENABLED",
				"REDIS_URL", "K8S_NAMESPACE", "K8S_IN_CLUSTER",
				"PROXY_ADDR", "PROXY_UPSTREAM_URL", "ANTHROPIC_API_KEY",
				"MAX_CONCURRENT", "IDLE_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT",
				"ASSISTANT_NAME", "TZ", "DISCORD_TOKEN", "TELEGRAM_TOKEN",
				"METRICS_ENABLED", "METRICS_PATH",
				"MYSQL_MAX_OPEN_CONNS", "MYSQL_MAX_IDLE_CONNS", "MYSQL_CONN_MAX_LIFETIME",
				"TASK_CLOSE_DELAY", "MAX_RETRIES", "RETRY_BASE_DELAY",
				"SCHEDULER_POLL_INTERVAL", "ANTHROPIC_OAUTH_TOKEN",
			}
			for _, k := range envKeys {
				_ = os.Unsetenv(k)
			}

			for k, v := range tt.env {
				_ = os.Setenv(k, v)
			}

			cfg, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil && cfg != nil {
				tt.check(t, cfg)
			}
		})
	}
}
