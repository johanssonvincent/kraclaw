package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestSandboxConfig_ValidateRejectsMountOutsideRoots covers the policy gate that
// runs before any Kubernetes resource is built: a group's container config may
// only bind-mount host paths the deployment has allowlisted.
func TestSandboxConfig_ValidateRejectsMountOutsideRoots(t *testing.T) {
	base := func(mounts []store.AdditionalMount, roots []string) SandboxConfig {
		return SandboxConfig{
			GroupFolder:       "test-group",
			GroupJID:          "123@g.us",
			ContainerConfig:   &store.ContainerConfig{AdditionalMounts: mounts},
			AllowedMountRoots: roots,
		}
	}

	tests := []struct {
		name    string
		cfg     SandboxConfig
		wantErr string
	}{
		{
			name: "mount inside an allowed root",
			cfg:  base([]store.AdditionalMount{{HostPath: "/data/assets/keys"}}, []string{"/data/assets"}),
		},
		{
			name: "no container config",
			cfg:  SandboxConfig{GroupFolder: "g", GroupJID: "123@g.us"},
		},
		{
			name:    "node filesystem escape attempt",
			cfg:     base([]store.AdditionalMount{{HostPath: "/var/lib/kubelet"}}, []string{"/data/assets"}),
			wantErr: "outside the allowed mount roots",
		},
		{
			name:    "no roots configured denies any mount",
			cfg:     base([]store.AdditionalMount{{HostPath: "/data/assets/keys"}}, nil),
			wantErr: "no allowed mount roots configured",
		},
		{
			name:    "unsafe path shape is rejected even inside a root",
			cfg:     base([]store.AdditionalMount{{HostPath: "/"}}, []string{"/data/assets"}),
			wantErr: "must not be the filesystem root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestCreateSandbox_RejectedMountCreatesNothing proves the failure happens
// before the controller talks to the API server, so a rejected mount cannot
// leave a partially configured sandbox behind.
func TestCreateSandbox_RejectedMountCreatesNothing(t *testing.T) {
	ctrl := newTestController()

	list := &agentsandboxv1alpha1.SandboxList{}

	_, err := ctrl.CreateSandbox(context.Background(), SandboxConfig{
		GroupFolder:       "test-group",
		GroupJID:          "123@g.us",
		Timeout:           5 * time.Minute,
		ContainerConfig:   &store.ContainerConfig{AdditionalMounts: []store.AdditionalMount{{HostPath: "/var/lib/kubelet"}}},
		AllowedMountRoots: []string{"/data/assets"},
	})
	if err == nil {
		t.Fatal("CreateSandbox() = nil error, want the mount to be rejected")
	}

	if !strings.Contains(err.Error(), "outside the allowed mount roots") {
		t.Errorf("CreateSandbox() error = %q, want it to mention the allowed mount roots", err)
	}

	if err := ctrl.ctrlClient.List(context.Background(), list, client.InNamespace("test-ns")); err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}

	if len(list.Items) != 0 {
		t.Errorf("CreateSandbox() created %d sandbox(es) for a rejected mount, want 0", len(list.Items))
	}
}
