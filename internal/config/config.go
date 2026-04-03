package config

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config holds all application configuration.
type Config struct {
	Server    ServerConfig
	MySQL     MySQLConfig
	Redis     RedisConfig
	NATS      NATSConfig
	K8s       K8sConfig
	Proxy     ProxyConfig
	Queue     QueueConfig
	Scheduler SchedulerConfig
	Metrics   MetricsConfig
	Logging   LoggingConfig
	Channels  ChannelsConfig
}

type ServerConfig struct {
	GRPCAddr              string `envconfig:"GRPC_ADDR" default:":50051"`
	RESTAddr              string `envconfig:"REST_ADDR" default:":8080"`
	GRPCInsecure          bool   `envconfig:"GRPC_INSECURE" default:"false"`
	GRPCTLSCertFile       string `envconfig:"GRPC_TLS_CERT_FILE" default:"/var/run/kraclaw/grpc-tls/tls.crt"`
	GRPCTLSKeyFile        string `envconfig:"GRPC_TLS_KEY_FILE" default:"/var/run/kraclaw/grpc-tls/tls.key"`
	GRPCTLSClientCAFile   string `envconfig:"GRPC_TLS_CLIENT_CA_FILE" default:"/var/run/kraclaw/grpc-tls/ca.crt"`
	GRPCAllowedCIDRs      string `envconfig:"GRPC_ALLOWED_CIDRS" default:"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fd00::/8,127.0.0.0/8,::1/128"`
	GRPCReflectionEnabled bool   `envconfig:"GRPC_REFLECTION_ENABLED" default:"false"`
}

type MySQLConfig struct {
	DSN             string        `envconfig:"MYSQL_DSN" required:"true"`
	MaxOpenConns    int           `envconfig:"MYSQL_MAX_OPEN_CONNS" default:"10"`
	MaxIdleConns    int           `envconfig:"MYSQL_MAX_IDLE_CONNS" default:"5"`
	ConnMaxLifetime time.Duration `envconfig:"MYSQL_CONN_MAX_LIFETIME" default:"5m"`
}

type RedisConfig struct {
	URL string `envconfig:"REDIS_URL" default:"redis://localhost:6379"`
}

type NATSConfig struct {
	URL string `envconfig:"NATS_URL" default:"nats://localhost:4222"`
}

type K8sConfig struct {
	Namespace              string        `envconfig:"K8S_NAMESPACE" default:"kraclaw"`
	AgentImage             string        `envconfig:"AGENT_IMAGE"`
	AgentImageAnthropic    string        `envconfig:"AGENT_IMAGE_ANTHROPIC"`
	AgentImageOpenAI       string        `envconfig:"AGENT_IMAGE_OPENAI"`
	InCluster              bool          `envconfig:"K8S_IN_CLUSTER" default:"true"`
	SessionsPVC            string        `envconfig:"K8S_SESSIONS_PVC" default:"kraclaw-sessions"`
	GroupsPVC              string        `envconfig:"K8S_GROUPS_PVC" default:"kraclaw-groups"`
	DataPVC                string        `envconfig:"K8S_DATA_PVC" default:"kraclaw-data"`
	SandboxStartupTimeout  time.Duration `envconfig:"SANDBOX_STARTUP_TIMEOUT" default:"5m"`
	SandboxProxyURL        string        `envconfig:"SANDBOX_PROXY_URL" default:"http://kraclaw-credproxy:3001"`
}

type ProxyConfig struct {
	Addr string `envconfig:"PROXY_ADDR" default:":3001"`

	// Anthropic (platform-level fallback)
	AnthropicUpstreamURL string `envconfig:"ANTHROPIC_UPSTREAM_URL" default:"https://api.anthropic.com"`
	AnthropicAPIKey      string `envconfig:"ANTHROPIC_API_KEY"`
	AnthropicAPIVersion  string `envconfig:"ANTHROPIC_VERSION" default:"2023-06-01"`

	// OpenAI (platform-level fallback)
	OpenAIUpstreamURL string `envconfig:"OPENAI_UPSTREAM_URL" default:"https://api.openai.com"`
	OpenAIAPIKey      string `envconfig:"OPENAI_API_KEY"`

	// Encryption key for credential store (hex-encoded 32 bytes)
	CredentialEncryptionKey string `envconfig:"CREDENTIAL_ENCRYPTION_KEY"`
}

type QueueConfig struct {
	MaxConcurrent         int           `envconfig:"MAX_CONCURRENT" default:"5"`
	IdleTimeout           time.Duration `envconfig:"IDLE_TIMEOUT" default:"30m"`
	TaskCloseDelay        time.Duration `envconfig:"TASK_CLOSE_DELAY" default:"10s"`
	MaxRetries            int           `envconfig:"MAX_RETRIES" default:"5"`
	RetryBaseDelay        time.Duration `envconfig:"RETRY_BASE_DELAY" default:"5s"`
	RateLimitTokensPerSec int           `envconfig:"RATE_LIMIT_TOKENS_PER_SEC" default:"10"`
	MaxMessageSizeBytes   int           `envconfig:"MAX_MESSAGE_SIZE_BYTES" default:"32768"`
	MessageLimit          int           `envconfig:"MESSAGE_LIMIT" default:"500"`
}

type SchedulerConfig struct {
	PollInterval time.Duration `envconfig:"SCHEDULER_POLL_INTERVAL" default:"60s"`
}

type MetricsConfig struct {
	Enabled bool   `envconfig:"METRICS_ENABLED" default:"true"`
	Path    string `envconfig:"METRICS_PATH" default:"/metrics"`
}

type LoggingConfig struct {
	Level  string `envconfig:"LOG_LEVEL" default:"info"`
	Format string `envconfig:"LOG_FORMAT" default:"json"`
}

type ChannelsConfig struct {
	AssistantName string `envconfig:"ASSISTANT_NAME" default:"Kraclaw"`
	Timezone      string `envconfig:"TZ" default:"UTC"`

	// Discord
	DiscordToken string `envconfig:"DISCORD_TOKEN"`

	// Telegram
	TelegramToken string `envconfig:"TELEGRAM_TOKEN"`
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	if c.Proxy.AnthropicAPIKey == "" && c.Proxy.OpenAIAPIKey == "" {
		return fmt.Errorf("at least one provider credential must be set (ANTHROPIC_API_KEY or OPENAI_API_KEY)")
	}
	// OpenAI-only setups require the multi-provider proxy path (with encryption key)
	// because the legacy proxy only supports Anthropic.
	hasAnthropic := c.Proxy.AnthropicAPIKey != ""
	if !hasAnthropic && c.Proxy.OpenAIAPIKey != "" && c.Proxy.CredentialEncryptionKey == "" {
		return fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY is required when only OpenAI credentials are configured (legacy proxy only supports Anthropic)")
	}
	if !c.Server.GRPCInsecure && (c.Server.GRPCTLSCertFile == "" || c.Server.GRPCTLSKeyFile == "" || c.Server.GRPCTLSClientCAFile == "") {
		return fmt.Errorf("GRPC_TLS_CERT_FILE, GRPC_TLS_KEY_FILE, and GRPC_TLS_CLIENT_CA_FILE must all be set (or set GRPC_INSECURE=true)")
	}
	if c.Server.GRPCAllowedCIDRs == "" {
		return fmt.Errorf("GRPC_ALLOWED_CIDRS must not be empty")
	}
	if _, err := time.LoadLocation(c.Channels.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Channels.Timezone, err)
	}
	if c.Queue.MaxConcurrent <= 0 {
		return fmt.Errorf("MAX_CONCURRENT must be positive, got %d", c.Queue.MaxConcurrent)
	}
	if c.K8s.AgentImage == "" && c.K8s.AgentImageAnthropic == "" && c.K8s.AgentImageOpenAI == "" {
		return fmt.Errorf("at least one agent image must be set (AGENT_IMAGE, AGENT_IMAGE_ANTHROPIC, or AGENT_IMAGE_OPENAI)")
	}
	if c.Proxy.CredentialEncryptionKey != "" {
		if len(c.Proxy.CredentialEncryptionKey) != 64 {
			return fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY must be 64 hex characters (32 bytes), got %d characters", len(c.Proxy.CredentialEncryptionKey))
		}
		if _, err := hex.DecodeString(c.Proxy.CredentialEncryptionKey); err != nil {
			return fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY must be valid hex: %w", err)
		}
	}
	return nil
}
