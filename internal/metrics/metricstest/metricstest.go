// Package metricstest provides helpers for asserting on kraclaw metrics.
package metricstest

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// PhaseSampleCount returns the observation count for a phase label on the
// process-global sandbox spawn histogram; delta assertions on it must not run
// in parallel with each other.
func PhaseSampleCount(t testing.TB, phase string) uint64 {
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
