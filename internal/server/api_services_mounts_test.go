package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/johanssonvincent/kraclaw/internal/provider"
	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

// TestRegisterGroup_RejectsUnsafeMountPaths covers the request-level half of the
// hostPath hardening: container_config_json is client-supplied JSON that ends up
// as a HostPath volume on the agent pod, so a mount path that is not absolute
// and clean, or that is the filesystem root, must be refused at the API boundary
// instead of being persisted for the sandbox controller to apply.
func TestRegisterGroup_RejectsUnsafeMountPaths(t *testing.T) {
	tests := []struct {
		name          string
		configJSON    string
		wantForbidden bool
	}{
		{
			name:          "filesystem root",
			configJSON:    `{"additionalMounts":[{"hostPath":"/"}]}`,
			wantForbidden: true,
		},
		{
			name:          "relative path",
			configJSON:    `{"additionalMounts":[{"hostPath":"var/lib/kubelet"}]}`,
			wantForbidden: true,
		},
		{
			name:          "kubernetes control plane state",
			configJSON:    `{"additionalMounts":[{"hostPath":"/var/lib/kubelet/pods"}]}`,
			wantForbidden: false, // absolute and clean: shape passes, deployment policy decides
		},
		{
			name:          "clean absolute path",
			configJSON:    `{"additionalMounts":[{"hostPath":"/data/assets/keys","readonly":true}]}`,
			wantForbidden: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &groupService{store: &mockGroupStore{}, providers: provider.NewRegistry(), log: testLogger()}

			_, err := svc.RegisterGroup(context.Background(), &kraclawv1.RegisterGroupRequest{
				Jid:                 "test@jid",
				Folder:              "test",
				ContainerConfigJson: tt.configJSON,
			})

			if !tt.wantForbidden {
				if err != nil {
					t.Fatalf("RegisterGroup() error = %v, want the config to be accepted", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("RegisterGroup() accepted unsafe mount config %s", tt.configJSON)
			}

			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("RegisterGroup() code = %v, want InvalidArgument (%v)", status.Code(err), err)
			}
		})
	}
}
