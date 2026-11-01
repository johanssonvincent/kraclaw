package store

import (
	"strings"
	"testing"
)

// TestContainerConfig_RejectsUnsafeMountPaths pins the first line of defence
// for additional hostPath mounts: whatever an API client sends must be an
// absolute, already-clean path that is not the filesystem root, because a
// hostPath volume is a node-level capability handed to a sandboxed agent.
func TestContainerConfig_RejectsUnsafeMountPaths(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "relative host path",
			json:    `{"additionalMounts":[{"hostPath":"var/lib/keys"}]}`,
			wantErr: "must be an absolute path",
		},
		{
			name:    "filesystem root",
			json:    `{"additionalMounts":[{"hostPath":"/"}]}`,
			wantErr: "must not be the filesystem root",
		},
		{
			name:    "unclean host path",
			json:    `{"additionalMounts":[{"hostPath":"/var/lib//keys"}]}`,
			wantErr: "must be a clean path",
		},
		{
			name:    "unclean container path",
			json:    `{"additionalMounts":[{"hostPath":"/data/keys","containerPath":"/app/../secrets"}]}`,
			wantErr: "must be a clean path",
		},
		{
			name:    "empty host path",
			json:    `{"additionalMounts":[{"hostPath":""}]}`,
			wantErr: "hostPath is required",
		},
		{
			name:    "negative timeout",
			json:    `{"timeout":-1}`,
			wantErr: "timeout must not be negative",
		},
		{
			name:    "duplicate container path",
			json:    `{"additionalMounts":[{"hostPath":"/data/a","containerPath":"/app/x"},{"hostPath":"/data/b","containerPath":"/app/x"}]}`,
			wantErr: "duplicate container path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseContainerConfig([]byte(tt.json))
			if err == nil {
				t.Fatalf("ParseContainerConfig(%s) = nil error, want %q", tt.json, tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseContainerConfig(%s) error = %q, want it to contain %q", tt.json, err, tt.wantErr)
			}
		})
	}
}

func TestContainerConfig_AcceptsCleanAbsoluteMounts(t *testing.T) {
	cc, err := ParseContainerConfig([]byte(`{"additionalMounts":[{"hostPath":"/data/keys","containerPath":"/app/keys","readonly":true}]}`))
	if err != nil {
		t.Fatalf("ParseContainerConfig() error = %v", err)
	}

	if len(cc.AdditionalMounts) != 1 || cc.AdditionalMounts[0].HostPath != "/data/keys" {
		t.Fatalf("unexpected mounts: %+v", cc.AdditionalMounts)
	}
}

// TestAdditionalMount_ValidateAllowedRoot is the policy half of the check: an
// absolute path is not automatically an acceptable one.
func TestAdditionalMount_ValidateAllowedRoot(t *testing.T) {
	allowed := []string{"/data/group-assets", "/srv/shared"}

	tests := []struct {
		name     string
		hostPath string
		roots    []string
		wantErr  string
	}{
		{name: "inside an allowed root", hostPath: "/data/group-assets/keys", roots: allowed},
		{name: "equal to an allowed root", hostPath: "/data/group-assets", roots: allowed},
		{name: "inside the second allowed root", hostPath: "/srv/shared/notes", roots: allowed},
		{name: "outside every allowed root", hostPath: "/var/lib/kubelet", roots: allowed, wantErr: "outside the allowed mount roots"},
		{name: "sibling with the same prefix is outside", hostPath: "/data/group-assets-other/x", roots: allowed, wantErr: "outside the allowed mount roots"},
		{name: "unrelated system path is outside", hostPath: "/etc/passwd", roots: allowed, wantErr: "outside the allowed mount roots"},
		{name: "no policy configured denies", hostPath: "/data/group-assets/keys", roots: nil, wantErr: "no allowed mount roots configured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AdditionalMount{HostPath: tt.hostPath}.ValidateAllowedRoot(tt.roots)

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("ValidateAllowedRoot(%q) error = %v, want nil", tt.hostPath, err)
				}

				return
			}

			if err == nil {
				t.Fatalf("ValidateAllowedRoot(%q) = nil, want %q", tt.hostPath, tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ValidateAllowedRoot(%q) error = %q, want it to contain %q", tt.hostPath, err, tt.wantErr)
			}
		})
	}
}
