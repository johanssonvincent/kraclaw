package sandbox

import (
	"context"
	"testing"
	"time"

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

	ctrl, err := New(fake.NewSimpleClientset(), ctrlClient, nil, "test-ns", "agent:latest", "redis://localhost:6379", "http://localhost:3001")
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

	ctrl, err := New(fake.NewSimpleClientset(), ctrlClient, nil, "test-ns", "agent:latest", "redis://localhost:6379", "http://localhost:3001")
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

	ctrl, err := New(fake.NewSimpleClientset(), ctrlClient, nil, "test-ns", "agent:latest", "redis://localhost:6379", "http://localhost:3001")
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

	ctrl, err := New(fake.NewSimpleClientset(), ctrlClient, nil, "test-ns", "agent:latest", "redis://localhost:6379", "http://localhost:3001")
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
