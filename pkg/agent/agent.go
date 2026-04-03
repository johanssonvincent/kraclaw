package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	nats "github.com/nats-io/nats.go"
)

// Config holds common agent configuration from environment.
type Config struct {
	NATSURL  string
	GroupJID string
	AgentID  string
	ProxyURL string
	Provider string
	Group    string // group folder (GROUP_FOLDER)
}

// LoadConfig reads agent config from environment variables.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		NATSURL:  os.Getenv("NATS_URL"),
		GroupJID: os.Getenv("KRACLAW_GROUP"),
		AgentID:  os.Getenv("KRACLAW_AGENT_ID"),
		ProxyURL: os.Getenv("KRACLAW_PROXY_URL"),
		Provider: os.Getenv("KRACLAW_PROVIDER"),
		Group:    os.Getenv("GROUP_FOLDER"),
	}
	if cfg.NATSURL == "" {
		cfg.NATSURL = "nats://localhost:4222"
	}
	if cfg.AgentID == "" {
		cfg.AgentID = "main"
	}
	if cfg.GroupJID == "" {
		return nil, fmt.Errorf("KRACLAW_GROUP is required")
	}
	if cfg.Group == "" {
		return nil, fmt.Errorf("GROUP_FOLDER is required")
	}
	return cfg, nil
}

// ConnectNATS creates a NATS client from a URL.
func ConnectNATS(url string) (*nats.Conn, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	return nc, nil
}

// Run is the main agent lifecycle: connect, process, shutdown.
func Run(handler func(ctx context.Context, ipc *IPCClient, log *slog.Logger) error) error {
	log := slog.Default()

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log.Info("agent starting", "group", cfg.Group, "agent_id", cfg.AgentID, "provider", cfg.Provider)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	nc, err := ConnectNATS(cfg.NATSURL)
	if err != nil {
		return fmt.Errorf("connect nats: %w", err)
	}
	defer nc.Close()

	ipcClient, err := NewIPCClient(nc, cfg.GroupJID, cfg.AgentID, log)
	if err != nil {
		return fmt.Errorf("create ipc client: %w", err)
	}

	if err := handler(ctx, ipcClient, log); err != nil {
		return fmt.Errorf("agent handler: %w", err)
	}

	log.Info("agent stopped")
	return nil
}
