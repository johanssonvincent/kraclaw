package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Server: ServerConfig{
			GRPCInsecure:     true,
			GRPCAllowedCIDRs: "10.0.0.0/8",
		},
		Proxy: ProxyConfig{
			AnthropicAPIKey: "sk-test",
		},
		K8s: K8sConfig{
			AgentImage:          "ghcr.io/test/agent:latest",
			AgentImageAnthropic: "ghcr.io/test/anthropic:latest",
		},
		Queue: QueueConfig{
			MaxConcurrent: 5,
		},
		Channels: ChannelsConfig{
			Timezone: "UTC",
		},
	}
}

func TestValidate_RequiresAtLeastOneAgentImage(t *testing.T) {
	cfg := validConfig()
	cfg.K8s.AgentImage = ""
	cfg.K8s.AgentImageAnthropic = ""
	cfg.K8s.AgentImageOpenAI = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when no agent image configured")
	}
}

func TestValidate_RejectsLegacyAnthropicFallback(t *testing.T) {
	cfg := validConfig()
	cfg.K8s.AgentImage = "ghcr.io/test/agent:latest"
	cfg.K8s.AgentImageAnthropic = ""
	cfg.K8s.AgentImageOpenAI = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when ANTHROPIC_API_KEY is set without AGENT_IMAGE_ANTHROPIC")
	}
}

func TestValidate_AcceptsProviderSpecificImage(t *testing.T) {
	cfg := validConfig()
	cfg.K8s.AgentImage = ""
	cfg.K8s.AgentImageAnthropic = "ghcr.io/test/anthropic:latest"
	cfg.K8s.AgentImageOpenAI = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_InvalidEncryptionKeyLength(t *testing.T) {
	cfg := validConfig()
	cfg.Proxy.CredentialEncryptionKey = "tooshort"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for short encryption key")
	}
}

func TestValidate_InvalidEncryptionKeyHex(t *testing.T) {
	cfg := validConfig()
	cfg.Proxy.CredentialEncryptionKey = strings.Repeat("zz", 32)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestValidate_ValidEncryptionKey(t *testing.T) {
	cfg := validConfig()
	cfg.Proxy.CredentialEncryptionKey = strings.Repeat("ab", 32)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_OpenAIOnlyRequiresEncryptionKey(t *testing.T) {
	cfg := validConfig()
	cfg.Proxy.AnthropicAPIKey = ""
	cfg.Proxy.OpenAIAPIKey = "sk-openai-test"
	cfg.K8s.AgentImageAnthropic = ""
	cfg.K8s.AgentImageOpenAI = "ghcr.io/test/openai:latest"
	cfg.Proxy.CredentialEncryptionKey = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when only OpenAI credentials set without encryption key")
	}
}

func TestValidate_OpenAIOnlyWithEncryptionKeyPasses(t *testing.T) {
	cfg := validConfig()
	cfg.Proxy.AnthropicAPIKey = ""
	cfg.Proxy.OpenAIAPIKey = "sk-openai-test"
	cfg.K8s.AgentImageAnthropic = ""
	cfg.K8s.AgentImageOpenAI = "ghcr.io/test/openai:latest"
	cfg.Proxy.CredentialEncryptionKey = strings.Repeat("ab", 32)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_OpenAIProviderRequiresOpenAIImage(t *testing.T) {
	cfg := validConfig()
	cfg.Proxy.AnthropicAPIKey = ""
	cfg.Proxy.OpenAIAPIKey = "sk-openai-test"
	cfg.K8s.AgentImageAnthropic = ""
	cfg.K8s.AgentImageOpenAI = ""
	cfg.Proxy.CredentialEncryptionKey = strings.Repeat("ab", 32)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when OPENAI_API_KEY is set without AGENT_IMAGE_OPENAI")
	}
}

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
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
				"ANTHROPIC_API_KEY":     "sk-test",
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.MySQL.DSN != "user:pass@tcp(localhost:3306)/kraclaw" {
					t.Errorf("unexpected MySQL DSN: %s", cfg.MySQL.DSN)
				}
				if cfg.K8s.AgentImageAnthropic != "registry.local/anthropic:latest" {
					t.Errorf("unexpected anthropic agent image: %s", cfg.K8s.AgentImageAnthropic)
				}
			},
		},
		{
			name: "defaults applied",
			env: map[string]string{
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
				"ANTHROPIC_API_KEY":     "sk-test",
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
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
				"ANTHROPIC_API_KEY":     "sk-test",
				"GRPC_ADDR":             ":9090",
				"GRPC_ALLOWED_CIDRS":    "10.42.0.0/16,10.43.0.0/16",
				"MAX_CONCURRENT":        "10",
				"LOG_LEVEL":             "debug",
				"ASSISTANT_NAME":        "Andy",
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
			name: "missing API key",
			env: map[string]string{
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
			},
			wantErr: true,
		},
		{
			name: "invalid timezone",
			env: map[string]string{
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
				"ANTHROPIC_API_KEY":     "sk-test",
				"TZ":                    "Not/A/Timezone",
			},
			wantErr: true,
		},
		{
			name: "zero max concurrent",
			env: map[string]string{
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
				"ANTHROPIC_API_KEY":     "sk-test",
				"MAX_CONCURRENT":        "0",
			},
			wantErr: true,
		},
		{
			name: "empty gRPC allowlist",
			env: map[string]string{
				"MYSQL_DSN":             "user:pass@tcp(localhost:3306)/kraclaw",
				"AGENT_IMAGE":           "registry.local/agent:latest",
				"AGENT_IMAGE_ANTHROPIC": "registry.local/anthropic:latest",
				"ANTHROPIC_API_KEY":     "sk-test",
				"GRPC_ALLOWED_CIDRS":    "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all relevant env vars
			envKeys := []string{
				"MYSQL_DSN", "AGENT_IMAGE", "GRPC_ADDR", "REST_ADDR",
				"GRPC_TLS_CERT_FILE", "GRPC_TLS_KEY_FILE", "GRPC_TLS_CLIENT_CA_FILE",
				"GRPC_ALLOWED_CIDRS", "GRPC_REFLECTION_ENABLED",
				"K8S_NAMESPACE", "K8S_IN_CLUSTER",
				"PROXY_ADDR", "ANTHROPIC_UPSTREAM_URL", "ANTHROPIC_API_KEY",
				"OPENAI_UPSTREAM_URL", "OPENAI_API_KEY", "CREDENTIAL_ENCRYPTION_KEY",
				"AGENT_IMAGE_ANTHROPIC", "AGENT_IMAGE_OPENAI",
				"MAX_CONCURRENT", "IDLE_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT",
				"ASSISTANT_NAME", "TZ", "DISCORD_TOKEN", "TELEGRAM_TOKEN",
				"METRICS_ENABLED", "METRICS_PATH",
				"MYSQL_MAX_OPEN_CONNS", "MYSQL_MAX_IDLE_CONNS", "MYSQL_CONN_MAX_LIFETIME",
				"TASK_CLOSE_DELAY", "MAX_RETRIES", "RETRY_BASE_DELAY",
				"SCHEDULER_POLL_INTERVAL",
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
