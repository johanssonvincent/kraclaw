package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natserver "github.com/nats-io/nats-server/v2/server"
)

func TestLoadConfig(t *testing.T) {
	configEnvKeys := []string{
		"NATS_URL",
		"NATS_USER",
		"NATS_PASSWORD",
		"KRACLAW_GROUP",
		"KRACLAW_AGENT_ID",
		"KRACLAW_PROXY_URL",
		"KRACLAW_PROVIDER",
		"GROUP_FOLDER",
	}

	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
		want    *Config
	}{
		{
			name:    "missing_group",
			env:     map[string]string{"GROUP_FOLDER": "some-folder"},
			wantErr: "KRACLAW_GROUP is required",
		},
		{
			name:    "missing_group_folder",
			env:     map[string]string{"KRACLAW_GROUP": "test@g.us"},
			wantErr: "GROUP_FOLDER is required",
		},
		{
			name: "defaults_applied",
			env: map[string]string{
				"KRACLAW_GROUP": "test@g.us",
				"GROUP_FOLDER":  "test-folder",
			},
			want: &Config{
				NATSURL:  "nats://localhost:4222",
				AgentID:  "main",
				GroupJID: "test@g.us",
				Group:    "test-folder",
			},
		},
		{
			name: "all_fields_set",
			env: map[string]string{
				"KRACLAW_GROUP":     "discord:123",
				"GROUP_FOLDER":      "mygroup",
				"NATS_URL":          "nats://custom:4222",
				"KRACLAW_AGENT_ID":  "worker-1",
				"KRACLAW_PROXY_URL": "http://proxy:3001",
				"KRACLAW_PROVIDER":  "openai",
			},
			want: &Config{
				NATSURL:  "nats://custom:4222",
				GroupJID: "discord:123",
				AgentID:  "worker-1",
				ProxyURL: "http://proxy:3001",
				Provider: "openai",
				Group:    "mygroup",
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			// Start every case from a clean slate so host environment leakage
			// cannot satisfy a required var or break the default assertions.
			for _, key := range configEnvKeys {
				t.Setenv(key, "")
			}

			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg, err := LoadConfig()

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("LoadConfig() err = nil, want %q", tt.wantErr)
				}

				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("LoadConfig() err = %v, want substring %q", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("LoadConfig() err = %v, want nil", err)
			}

			if cfg == nil {
				t.Fatal("LoadConfig() returned nil config without error")
			}

			fields := []struct {
				name, got, want string
			}{
				{"NATSURL", cfg.NATSURL, tt.want.NATSURL},
				{"NATSUser", cfg.NATSUser, tt.want.NATSUser},
				{"NATSPassword", cfg.NATSPassword, tt.want.NATSPassword},
				{"GroupJID", cfg.GroupJID, tt.want.GroupJID},
				{"AgentID", cfg.AgentID, tt.want.AgentID},
				{"ProxyURL", cfg.ProxyURL, tt.want.ProxyURL},
				{"Provider", cfg.Provider, tt.want.Provider},
				{"Group", cfg.Group, tt.want.Group},
			}
			for _, f := range fields {
				if f.got != f.want {
					t.Errorf("cfg.%s = %q, want %q", f.name, f.got, f.want)
				}
			}
		})
	}
}

func TestEnsureGroupDirs(t *testing.T) {
	cases := map[string]struct {
		homeEnv string
		setup   func(t *testing.T, home string)
		wantErr string
	}{
		"happy_path": {
			homeEnv: "",
			setup:   func(t *testing.T, home string) {},
		},
		"home_unset": {
			homeEnv: "__UNSET__",
			setup:   func(t *testing.T, home string) {},
			wantErr: "HOME unset",
		},
		"idempotent_existing": {
			homeEnv: "",
			setup: func(t *testing.T, home string) {
				if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
					t.Fatalf("seed: %v", err)
				}
			},
		},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			archives := t.TempDir()
			if tt.homeEnv == "__UNSET__" {
				t.Setenv("HOME", "")
			} else {
				t.Setenv("HOME", home)
			}
			t.Setenv("KRACLAW_AGENT_ARCHIVES_DIR", archives)
			tt.setup(t, home)
			err := ensureGroupDirs()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("ensureGroupDirs() err = %v, want nil", err)
					return
				}
				if _, statErr := os.Stat(filepath.Join(home, ".claude")); statErr != nil {
					t.Errorf(".claude not created: %v", statErr)
				}
				if _, statErr := os.Stat(archives); statErr != nil {
					t.Errorf("archives dir not created: %v", statErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ensureGroupDirs() err = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestRun_PrepullArg_ReturnsOnSignal(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"agent", "--prepull"}

	ctx, cancel := context.WithCancel(context.Background())
	oldWait := waitForPrepullSignal
	waitForPrepullSignal = func() {
		<-ctx.Done()
	}
	t.Cleanup(func() { waitForPrepullSignal = oldWait })

	done := make(chan error, 1)
	go func() {
		done <- Run(func(ctx context.Context, ipc *IPCClient, log *slog.Logger) error {
			return fmt.Errorf("handler must not run in prepull mode")
		})
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after prepull wait was released")
	}
}

func startTestNATSServerWithAuth(t *testing.T) string {
	t.Helper()
	opts := &natserver.Options{
		Host:     "127.0.0.1",
		Port:     -1,
		Username: "agentuser",
		Password: "agentpass",
	}
	s, err := natserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func TestConnectNATS(t *testing.T) {
	url := startTestNATSServerWithAuth(t)
	cases := []struct {
		name    string
		user    string
		pass    string
		wantErr bool
	}{
		{"correct credentials connect", "agentuser", "agentpass", false},
		{"wrong password fails", "agentuser", "nope", true},
		{"missing credentials fail", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nc, err := ConnectNATS(url, tc.user, tc.pass)
			if tc.wantErr {
				if err == nil {
					if nc != nil {
						nc.Close()
					}
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConnectNATS: %v", err)
			}
			if nc == nil {
				t.Fatal("expected non-nil connection")
			}
			nc.Close()
		})
	}
}
