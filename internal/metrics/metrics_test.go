package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TestSpawnPhaseWireValues guards against accidental relabeling of the cold-start
// phase constants. The wire strings are load-bearing — a change here orphans the
// corresponding dashboard/alert series.
func TestSpawnPhaseWireValues(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		phase SpawnPhase
		want  string
	}{
		"ensure_stream": {PhaseEnsureStream, "ensure_stream"},
		"crd_create":    {PhaseCRDCreate, "crd_create"},
		"pod_scheduled": {PhasePodScheduled, "pod_scheduled"},
		"pod_ready":     {PhasePodReady, "pod_ready"},
		"first_output":  {PhaseFirstOutput, "first_output"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := string(tt.phase); got != tt.want {
				t.Errorf("SpawnPhase wire value = %q, want %q", got, tt.want)
			}
		})
	}
}

// phaseSampleCount reads the observation count for a phase label on the global
// SandboxSpawnDuration histogram. Process-global; callers must not run parallel.
func phaseSampleCount(t *testing.T, phase string) uint64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "kraclaw_sandbox_spawn_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "phase" && lp.GetValue() == phase {
					return m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

// TestObserveSpawnPhase verifies the helper increments the histogram sample
// count for the labeled phase. Not parallel: it asserts a global-histogram delta.
func TestObserveSpawnPhase(t *testing.T) {
	const phase = PhaseFirstOutput
	before := phaseSampleCount(t, string(phase))
	ObserveSpawnPhase(phase, 250*time.Millisecond)
	if got := phaseSampleCount(t, string(phase)) - before; got != 1 {
		t.Errorf("ObserveSpawnPhase sample-count delta = %d, want 1", got)
	}
}
