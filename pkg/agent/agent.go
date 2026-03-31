package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
)

// Config holds common agent configuration from environment.
type Config struct {
	RedisURL string
	Group    string
	GroupJID string
	ProxyURL string
	Provider string
}

// LoadConfig reads agent config from environment variables.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		RedisURL: os.Getenv("REDIS_URL"),
		Group:    os.Getenv("KRACLAW_GROUP_FOLDER"),
		GroupJID: os.Getenv("KRACLAW_GROUP"),
		ProxyURL: os.Getenv("KRACLAW_PROXY_URL"),
		Provider: os.Getenv("KRACLAW_PROVIDER"),
	}
	if cfg.RedisURL == "" {
		cfg.RedisURL = "redis://localhost:6379"
	}
	if cfg.Group == "" {
		cfg.Group = os.Getenv("GROUP_FOLDER")
	}
	if cfg.Group == "" {
		return nil, fmt.Errorf("KRACLAW_GROUP_FOLDER or GROUP_FOLDER is required")
	}
	return cfg, nil
}

// ConnectRedis creates a Redis client from a URL.
func ConnectRedis(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rdb, nil
}

// Run is the main agent lifecycle: connect, process, shutdown.
func Run(handler func(ctx context.Context, ipc *IPCClient, log *slog.Logger) error) error {
	log := slog.Default()

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log.Info("agent starting", "group", cfg.Group, "provider", cfg.Provider)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	rdb, err := ConnectRedis(ctx, cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer rdb.Close()

	ipcClient, err := NewIPCClient(rdb, cfg.Group)
	if err != nil {
		return fmt.Errorf("create ipc client: %w", err)
	}

	if err := handler(ctx, ipcClient, log); err != nil {
		return fmt.Errorf("agent handler: %w", err)
	}

	log.Info("agent stopped")
	return nil
}
