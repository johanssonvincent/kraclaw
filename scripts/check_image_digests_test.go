package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// scriptPath resolves check-image-digests.sh relative to this test file so the
// test is independent of the caller's working directory.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "check-image-digests.sh")
}

const goodDigest = "@sha256:93c2978f705f1a26633fb6576cc23a46b79d16b75763a4b9781e9296bea8fe96"

func TestCheckImageDigests(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		values   string
		wantPass bool
	}{
		"all three keys digest-pinned": {
			values: "k8s:\n" +
				"  agentImageAnthropic: \"ghcr.io/x/a" + goodDigest + "\"\n" +
				"  agentImageOpenAI: \"ghcr.io/x/o" + goodDigest + "\"\n" +
				"sandbox:\n" +
				"  agentImage: \"ghcr.io/x/a" + goodDigest + "\"\n",
			wantPass: true,
		},
		"sandbox.agentImage on :latest is rejected": {
			values: "k8s:\n" +
				"  agentImageAnthropic: \"ghcr.io/x/a" + goodDigest + "\"\n" +
				"  agentImageOpenAI: \"ghcr.io/x/o" + goodDigest + "\"\n" +
				"sandbox:\n" +
				"  agentImage: \"ghcr.io/x/a:latest\"\n",
			wantPass: false,
		},
		"anthropic placeholder all-zero digest is rejected": {
			values: "k8s:\n" +
				"  agentImageAnthropic: \"ghcr.io/x/a@sha256:0000000000000000000000000000000000000000000000000000000000000000\"\n" +
				"  agentImageOpenAI: \"ghcr.io/x/o" + goodDigest + "\"\n",
			wantPass: false,
		},
		"openai tagged reference is rejected": {
			values: "k8s:\n" +
				"  agentImageAnthropic: \"ghcr.io/x/a" + goodDigest + "\"\n" +
				"  agentImageOpenAI: \"ghcr.io/x/o:v1.2.3\"\n",
			wantPass: false,
		},
	}

	script := scriptPath(t)
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := filepath.Join(t.TempDir(), "values.yaml")
			if err := os.WriteFile(f, []byte(tt.values), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			err := exec.Command("bash", script, f).Run()
			gotPass := err == nil
			if gotPass != tt.wantPass {
				t.Errorf("script on %q: pass = %v (err=%v), want pass = %v", tt.values, gotPass, err, tt.wantPass)
			}
		})
	}
}
