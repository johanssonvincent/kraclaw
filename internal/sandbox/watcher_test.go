package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWatchSandboxes_ListBeforeWatch(t *testing.T) {
	// Verify that WatchSandboxes uses List+Watch pattern (List first to get resourceVersion,
	// then Watch from that point). The fake client supports this; we verify the function
	// succeeds and that created resources are observed (proving Watch is active from List's RV).
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	// Pre-populate a sandbox so List returns a non-empty resourceVersion.
	existing := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pre-existing",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelGroup:     "pre-group",
			},
		},
	}

	ctrlClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&agentsandboxv1alpha1.Sandbox{}).
		WithObjects(existing).
		Build()

	ctrl, err := New(fake.NewClientset(), ctrlClient, nil, "test-ns", nil, "nats://localhost:4222", "http://localhost:3001", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events, err := ctrl.WatchSandboxes(ctx)
	if err != nil {
		t.Fatalf("WatchSandboxes: %v", err)
	}

	// Create a new sandbox after watch started — must be observed.
	newSandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-sandbox",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelGroup:     "new-group",
			},
		},
	}
	if err := ctrlClient.Create(ctx, newSandbox); err != nil {
		t.Fatalf("Create: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Type != "added" {
			t.Errorf("expected added event, got %s", ev.Type)
		}
		if ev.Status.Name != "new-sandbox" {
			t.Errorf("expected new-sandbox, got %s", ev.Status.Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for added event after List+Watch")
	}
}

func TestWatchSandboxes_ChannelClosesOnCtxCancel(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	ctrlClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&agentsandboxv1alpha1.Sandbox{}).
		Build()

	ctrl, err := New(fake.NewClientset(), ctrlClient, nil, "test-ns", nil, "nats://localhost:4222", "http://localhost:3001", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events, err := ctrl.WatchSandboxes(ctx)
	if err != nil {
		t.Fatalf("WatchSandboxes: %v", err)
	}

	// Cancel context — channel should close.
	cancel()

	select {
	case _, ok := <-events:
		if ok {
			// Might get a stale event, drain until closed.
			for range events {
			}
		}
		// Channel closed — success.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for events channel to close after ctx cancel")
	}
}

func TestWatchSandboxes(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	ctrlClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&agentsandboxv1alpha1.Sandbox{}).
		Build()

	ctrl, err := New(fake.NewClientset(), ctrlClient, nil, "test-ns", nil, "nats://localhost:4222", "http://localhost:3001", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events, err := ctrl.WatchSandboxes(ctx)
	if err != nil {
		t.Fatalf("WatchSandboxes: %v", err)
	}

	// 1. Test 'added' event.
	sandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sandbox",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelGroup:     "test-group",
			},
		},
	}
	if err := ctrlClient.Create(ctx, sandbox); err != nil {
		t.Fatalf("Create: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Type != "added" {
			t.Errorf("expected added event, got %s", ev.Type)
		}
		if ev.Status.State != StatePending {
			t.Errorf("expected state %s, got %s", StatePending, ev.Status.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for added event")
	}

	// 2. Test 'updated' event (to Running).
	sandbox.Status.Conditions = []metav1.Condition{
		{
			Type:               string(agentsandboxv1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Ready",
			Message:            "sandbox is ready",
		},
	}
	if err := ctrlClient.Status().Update(ctx, sandbox); err != nil {
		t.Fatalf("Update Status: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Type != "updated" {
			t.Errorf("expected updated event, got %s", ev.Type)
		}
		if ev.Status.State != StateRunning {
			t.Errorf("expected state %s, got %s", StateRunning, ev.Status.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for updated event")
	}

	// 3. Test 'deleted' event.
	if err := ctrlClient.Delete(ctx, sandbox); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Type != "deleted" {
			t.Errorf("expected deleted event, got %s", ev.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deleted event")
	}
}

func TestWatchMapping_Lifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	ctrlClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&agentsandboxv1alpha1.Sandbox{}).
		Build()

	ctrl, err := New(fake.NewClientset(), ctrlClient, nil, "test-ns", nil, "nats://localhost:4222", "http://localhost:3001", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	events, err := ctrl.WatchSandboxes(ctx)
	if err != nil {
		t.Fatalf("WatchSandboxes: %v", err)
	}

	sandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lifecycle-sandbox",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelGroup:     "lifecycle-group",
			},
		},
	}
	if err := ctrlClient.Create(ctx, sandbox); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 1. Pending (Added).
	select {
	case ev := <-events:
		if ev.Status.State != StatePending {
			t.Errorf("expected state %s, got %s", StatePending, ev.Status.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for added event")
	}

	// 2. Running (Updated).
	sandbox.Status.Conditions = []metav1.Condition{
		{
			Type:               string(agentsandboxv1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Ready",
			Message:            "sandbox is ready",
		},
	}
	if err := ctrlClient.Status().Update(ctx, sandbox); err != nil {
		t.Fatalf("Update Status: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Status.State != StateRunning {
			t.Errorf("expected state %s, got %s", StateRunning, ev.Status.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for running event")
	}

	// 3. Completed (Updated).
	sandbox.Status.Conditions = []metav1.Condition{
		{
			Type:               string(agentsandboxv1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             "Succeeded",
			Message:            "sandbox succeeded",
			LastTransitionTime: metav1.Now(),
		},
	}
	if err := ctrlClient.Status().Update(ctx, sandbox); err != nil {
		t.Fatalf("Update Status: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Status.State != StateCompleted {
			t.Errorf("expected state %s, got %s", StateCompleted, ev.Status.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for completed event")
	}

	// 4. Failed (Updated).
	sandbox.Status.Conditions = []metav1.Condition{
		{
			Type:               string(agentsandboxv1alpha1.SandboxConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             "Failed",
			Message:            "sandbox failed",
			LastTransitionTime: metav1.Now(),
		},
	}
	if err := ctrlClient.Status().Update(ctx, sandbox); err != nil {
		t.Fatalf("Update Status: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Status.State != StateFailed {
			t.Errorf("expected state %s, got %s", StateFailed, ev.Status.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for failed event")
	}
}

// phaseSampleCount reads the current observation count for a given phase label
// of the global SandboxSpawnDuration histogram. Used for delta assertions; the
// metric is process-global so callers must not run in parallel with each other.
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

// TestRecordPhaseTransitions exercises the phase histogram recording and its
// data-quality guards. Cases are NOT run in parallel: they assert deltas on the
// process-global SandboxSpawnDuration histogram.
func TestRecordPhaseTransitions(t *testing.T) {
	cond := func(typ string, status metav1.ConditionStatus, ltt time.Time) metav1.Condition {
		c := metav1.Condition{Type: typ, Status: status}
		if !ltt.IsZero() {
			c.LastTransitionTime = metav1.NewTime(ltt)
		}
		return c
	}
	created := time.Date(2026, 5, 28, 0, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		conditions []metav1.Condition
		calls       int // recordPhaseTransitions invocations (default 1)
		zeroCreated bool
		wantSeen    map[string]bool
		wantDelta   map[string]uint64 // per-phase histogram sample-count increase
		wantWarn    bool
	}{
		"both phases recorded once": {
			conditions: []metav1.Condition{
				cond("PodScheduled", metav1.ConditionTrue, created.Add(500*time.Millisecond)),
				cond(string(agentsandboxv1alpha1.SandboxConditionReady), metav1.ConditionTrue, created.Add(2*time.Second)),
			},
			wantSeen:  map[string]bool{"pod_scheduled": true, "pod_ready": true},
			wantDelta: map[string]uint64{"pod_scheduled": 1, "pod_ready": 1},
		},
		"duplicate event does not double-record": {
			conditions: []metav1.Condition{
				cond("PodScheduled", metav1.ConditionTrue, created.Add(500*time.Millisecond)),
				cond(string(agentsandboxv1alpha1.SandboxConditionReady), metav1.ConditionTrue, created.Add(2*time.Second)),
			},
			calls:     2,
			wantSeen:  map[string]bool{"pod_scheduled": true, "pod_ready": true},
			wantDelta: map[string]uint64{"pod_scheduled": 1, "pod_ready": 1},
		},
		"condition false is skipped": {
			conditions: []metav1.Condition{
				cond("PodScheduled", metav1.ConditionFalse, created.Add(time.Second)),
			},
			wantSeen:  map[string]bool{"pod_scheduled": false},
			wantDelta: map[string]uint64{"pod_scheduled": 0},
		},
		"unknown condition type is skipped": {
			conditions: []metav1.Condition{
				cond("Unknown", metav1.ConditionTrue, created.Add(time.Second)),
			},
			wantSeen:  map[string]bool{"pod_scheduled": false},
			wantDelta: map[string]uint64{"pod_scheduled": 0},
		},
		"zero LastTransitionTime is skipped and warned": {
			conditions: []metav1.Condition{
				cond("PodScheduled", metav1.ConditionTrue, time.Time{}),
			},
			wantSeen:  map[string]bool{"pod_scheduled": false},
			wantDelta: map[string]uint64{"pod_scheduled": 0},
			wantWarn:  true,
		},
		"negative duration is skipped and warned": {
			conditions: []metav1.Condition{
				cond("PodScheduled", metav1.ConditionTrue, created.Add(-time.Second)),
			},
			wantSeen:  map[string]bool{"pod_scheduled": false},
			wantDelta: map[string]uint64{"pod_scheduled": 0},
			wantWarn:  true,
		},
		"zero CreationTimestamp skips whole object and warns": {
			// A valid LastTransitionTime against a zero created instant would
			// otherwise pass the d<0 guard as a huge positive sample.
			zeroCreated: true,
			conditions: []metav1.Condition{
				cond("PodScheduled", metav1.ConditionTrue, created.Add(time.Second)),
				cond(string(agentsandboxv1alpha1.SandboxConditionReady), metav1.ConditionTrue, created.Add(2*time.Second)),
			},
			wantSeen:  map[string]bool{"pod_scheduled": false, "pod_ready": false},
			wantDelta: map[string]uint64{"pod_scheduled": 0, "pod_ready": 0},
			wantWarn:  true,
		},
	}

	caseNum := 0
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			caseNum++
			sbName := fmt.Sprintf("phase-test-%d", caseNum)
			meta := metav1.ObjectMeta{Name: sbName, CreationTimestamp: metav1.NewTime(created)}
			if tt.zeroCreated {
				meta.CreationTimestamp = metav1.Time{}
			}
			sb := &agentsandboxv1alpha1.Sandbox{
				ObjectMeta: meta,
				Status:     agentsandboxv1alpha1.SandboxStatus{Conditions: tt.conditions},
			}

			before := map[string]uint64{}
			for phase := range tt.wantDelta {
				before[phase] = phaseSampleCount(t, phase)
			}

			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			seen := map[string]map[string]bool{}

			calls := tt.calls
			if calls == 0 {
				calls = 1
			}
			for range calls {
				recordPhaseTransitions(sb, seen, log)
			}

			for phase, want := range tt.wantSeen {
				if got := seen[sbName][phase]; got != want {
					t.Errorf("seen[%s][%s] = %v, want %v", sbName, phase, got, want)
				}
			}
			for phase, wantDelta := range tt.wantDelta {
				if got := phaseSampleCount(t, phase) - before[phase]; got != wantDelta {
					t.Errorf("phase %q sample-count delta = %d, want %d", phase, got, wantDelta)
				}
			}
			if gotWarn := strings.Contains(buf.String(), "skipping"); gotWarn != tt.wantWarn {
				t.Errorf("warn logged = %v, want %v (log: %q)", gotWarn, tt.wantWarn, buf.String())
			}
		})
	}
}
