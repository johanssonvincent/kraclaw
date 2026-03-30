package sandbox

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

func newTestController() *Controller {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)
	ctrlClient := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()

	ctrl, err := New(fake.NewSimpleClientset(), ctrlClient, nil, "test-ns", "agent:latest")
	if err != nil {
		panic("newTestController: " + err.Error())
	}
	return ctrl
}

func TestCreateSandbox(t *testing.T) {
	ctrl := newTestController()
	ctx := context.Background()

	status, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder:   "test-group",
		GroupJID:      "123@g.us",
		IsMain:        true,
		Timeout:       5 * time.Minute,
		AssistantName: "Kraclaw",
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}

	if !strings.HasPrefix(status.Name, "kraclaw-agent-test-group-") {
		t.Fatalf("unexpected sandbox name: %s", status.Name)
	}
	if status.Group != "test-group" {
		t.Fatalf("expected group 'test-group', got %q", status.Group)
	}

	// Verify the Sandbox was created in k8s via ctrlClient.
	sandbox := &agentsandboxv1alpha1.Sandbox{}
	err = ctrl.ctrlClient.Get(ctx, client.ObjectKey{Namespace: "test-ns", Name: status.Name}, sandbox)
	if err != nil {
		t.Fatalf("sandbox not found: %v", err)
	}

	// Check labels.
	if sandbox.Labels[labelManagedBy] != managedByValue {
		t.Fatalf("missing managed-by label")
	}
	if sandbox.Labels[labelRole] != roleAgent {
		t.Fatalf("missing role label")
	}
	if sandbox.Labels[labelGroup] != "test-group" {
		t.Fatalf("missing group label")
	}

	// GROUP_FOLDER env var must be injected into the agent container.
	containers := sandbox.Spec.PodTemplate.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("no containers in pod spec")
	}
	var foundGroupFolder bool
	for _, env := range containers[0].Env {
		if env.Name == "GROUP_FOLDER" && env.Value == "test-group" {
			foundGroupFolder = true
			break
		}
	}
	if !foundGroupFolder {
		t.Fatalf("GROUP_FOLDER env var not set on agent container")
	}

	// GROUP_FOLDER env var must also be injected into the init container.
	initContainers := sandbox.Spec.PodTemplate.Spec.InitContainers
	if len(initContainers) == 0 {
		t.Fatal("no init containers in pod spec")
	}
	var foundInitGroupFolder bool
	for _, env := range initContainers[0].Env {
		if env.Name == "GROUP_FOLDER" && env.Value == "test-group" {
			foundInitGroupFolder = true
			break
		}
	}
	if !foundInitGroupFolder {
		t.Fatalf("GROUP_FOLDER env var not set on init container")
	}
}

// failNTimesCreateInterceptor returns an interceptor.Funcs that fails Create
// calls failCount times with the given error, then delegates to the real client.
func failNTimesCreateInterceptor(failCount int, err error) interceptor.Funcs {
	var attempts atomic.Int32
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			n := int(attempts.Add(1))
			if n <= failCount {
				return err
			}
			return c.Create(ctx, obj, opts...)
		},
	}
}

func newTestControllerWithCreateInterceptor(funcs interceptor.Funcs) *Controller {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	ctrlClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(funcs).
		Build()

	ctrl, err := New(fake.NewSimpleClientset(), ctrlClient, nil, "test-ns", "agent:latest")
	if err != nil {
		panic("newTestControllerWithCreateInterceptor: " + err.Error())
	}
	return ctrl
}

func TestCreateSandbox_RetryTransientSuccess(t *testing.T) {
	// First attempt fails with transient error, second succeeds.
	ctrl := newTestControllerWithCreateInterceptor(
		failNTimesCreateInterceptor(1, fmt.Errorf("connection refused")),
	)
	ctx := context.Background()

	status, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "retry-ok",
		GroupJID:    "retry-ok@g.us",
	})
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if status.Group != "retry-ok" {
		t.Fatalf("expected group 'retry-ok', got %q", status.Group)
	}
}

func TestCreateSandbox_RetryExhausted(t *testing.T) {
	// All 3 attempts fail with transient error.
	ctrl := newTestControllerWithCreateInterceptor(
		failNTimesCreateInterceptor(3, fmt.Errorf("connection refused")),
	)
	ctx := context.Background()

	_, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "retry-fail",
		GroupJID:    "retry-fail@g.us",
	})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if !strings.Contains(err.Error(), "failed after") {
		t.Fatalf("expected 'failed after' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected wrapped original error, got: %v", err)
	}
}

func TestCreateSandbox_NonTransientFailsFast(t *testing.T) {
	// Non-transient error should not retry.
	var attempts atomic.Int32
	funcs := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			attempts.Add(1)
			return fmt.Errorf("already exists")
		},
	}
	ctrl := newTestControllerWithCreateInterceptor(funcs)
	ctx := context.Background()

	_, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "no-retry",
		GroupJID:    "no-retry@g.us",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected 1 attempt for non-transient error, got %d", attempts.Load())
	}
}

func TestCreateSandbox_ContextCancelledDuringBackoff(t *testing.T) {
	// First attempt fails with transient error; context is cancelled during backoff.
	ctrl := newTestControllerWithCreateInterceptor(
		failNTimesCreateInterceptor(3, fmt.Errorf("connection refused")),
	)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay (less than backoff of 100ms).
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "ctx-cancel",
		GroupJID:    "ctx-cancel@g.us",
	})
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestCreateSandbox_FirstAttemptSucceeds(t *testing.T) {
	// No failures — first attempt succeeds.
	var attempts atomic.Int32
	funcs := interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			attempts.Add(1)
			return c.Create(ctx, obj, opts...)
		},
	}
	ctrl := newTestControllerWithCreateInterceptor(funcs)
	ctx := context.Background()

	status, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "first-ok",
		GroupJID:    "first-ok@g.us",
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", attempts.Load())
	}
}

func TestGetSandbox(t *testing.T) {
	ctrl := newTestController()
	ctx := context.Background()

	created, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "get-test",
		GroupJID:    "get-test@g.us",
		Timeout:     time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	status, err := ctrl.GetSandbox(ctx, created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if status.Name != created.Name {
		t.Fatalf("expected %s, got %s", created.Name, status.Name)
	}
}

func TestListSandboxes(t *testing.T) {
	ctrl := newTestController()
	ctx := context.Background()

	for _, folder := range []string{"group-a", "group-b"} {
		_, err := ctrl.CreateSandbox(ctx, SandboxConfig{
			GroupFolder: folder,
			GroupJID:    folder + "@g.us",
			Timeout:     time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	statuses, err := ctrl.ListSandboxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 sandboxes, got %d", len(statuses))
	}
}

func TestStopSandbox(t *testing.T) {
	ctrl := newTestController()
	ctx := context.Background()

	created, err := ctrl.CreateSandbox(ctx, SandboxConfig{
		GroupFolder: "stop-test",
		GroupJID:    "stop-test@g.us",
		Timeout:     time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := ctrl.StopSandbox(ctx, created.Name); err != nil {
		t.Fatal(err)
	}

	// Sandbox should be gone.
	_, err = ctrl.GetSandbox(ctx, created.Name)
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestSandboxToStatus_States(t *testing.T) {
	tests := []struct {
		name     string
		sandbox  agentsandboxv1alpha1.Sandbox
		expected SandboxState
	}{
		{
			name: "pending",
			sandbox: agentsandboxv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelGroup: "g"}},
				Status:     agentsandboxv1alpha1.SandboxStatus{Conditions: []metav1.Condition{}},
			},
			expected: StatePending,
		},
		{
			name: "ready",
			sandbox: agentsandboxv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelGroup: "g"}},
				Status: agentsandboxv1alpha1.SandboxStatus{
					Conditions: []metav1.Condition{
						{
							Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
							Status: metav1.ConditionTrue,
						},
					},
				},
			},
			expected: StateRunning,
		},
		{
			name: "succeeded",
			sandbox: agentsandboxv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelGroup: "g"}},
				Status: agentsandboxv1alpha1.SandboxStatus{
					Conditions: []metav1.Condition{
						{
							Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
							Status: metav1.ConditionFalse,
							Reason: "Succeeded",
						},
					},
				},
			},
			expected: StateCompleted,
		},
		{
			name: "failed",
			sandbox: agentsandboxv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelGroup: "g"}},
				Status: agentsandboxv1alpha1.SandboxStatus{
					Conditions: []metav1.Condition{
						{
							Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
							Status: metav1.ConditionFalse,
							Reason: "Failed",
						},
					},
				},
			},
			expected: StateFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := sandboxToStatus(&tt.sandbox)
			if s.State != tt.expected {
				t.Fatalf("expected state %q, got %q", tt.expected, s.State)
			}
		})
	}
}

func TestCleanupOrphans(t *testing.T) {
	ctrl := newTestController()
	ctx := context.Background()

	// Create a completed sandbox.
	completedSandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kraclaw-agent-done-abc123",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelRole:      roleAgent,
				labelGroup:     "done",
			},
		},
		Status: agentsandboxv1alpha1.SandboxStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
					Status: metav1.ConditionFalse,
					Reason: "Succeeded",
				},
			},
		},
	}

	// Create a running sandbox.
	runningSandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kraclaw-agent-active-def456",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelRole:      roleAgent,
				labelGroup:     "active",
			},
		},
		Status: agentsandboxv1alpha1.SandboxStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	if err := ctrl.ctrlClient.Create(ctx, completedSandbox); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.ctrlClient.Create(ctx, runningSandbox); err != nil {
		t.Fatal(err)
	}

	if err := ctrl.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}

	// Completed sandbox should be deleted.
	err := ctrl.ctrlClient.Get(ctx, client.ObjectKey{Namespace: "test-ns", Name: "kraclaw-agent-done-abc123"}, &agentsandboxv1alpha1.Sandbox{})
	if err == nil {
		t.Fatal("expected completed sandbox to be deleted")
	}

	// Running sandbox should still exist.
	err = ctrl.ctrlClient.Get(ctx, client.ObjectKey{Namespace: "test-ns", Name: "kraclaw-agent-active-def456"}, &agentsandboxv1alpha1.Sandbox{})
	if err != nil {
		t.Fatal("expected running sandbox to still exist")
	}
}

// deleteFailClient wraps a controller-runtime client and makes Delete return
// NotFound for any resource whose name matches failName. This simulates the
// race where a Sandbox is deleted between ListSandboxes and StopSandbox.
type deleteFailClient struct {
	client.WithWatch
	failName string
}

func (d *deleteFailClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if obj.GetName() == d.failName {
		return apierrors.NewNotFound(
			agentsandboxv1alpha1.Resource("sandboxes"),
			obj.GetName(),
		)
	}
	return d.WithWatch.Delete(ctx, obj, opts...)
}

func TestCleanupOrphans_IsNotFoundSkipped(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = agentsandboxv1alpha1.AddToScheme(scheme)

	// Pre-populate the fake client with a completed sandbox.
	completedSandbox := &agentsandboxv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kraclaw-agent-gone-aaa111",
			Namespace: "test-ns",
			Labels: map[string]string{
				labelManagedBy: managedByValue,
				labelRole:      roleAgent,
				labelGroup:     "gone",
			},
		},
		Status: agentsandboxv1alpha1.SandboxStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
					Status: metav1.ConditionFalse,
					Reason: "Succeeded",
				},
			},
		},
	}

	baseClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(completedSandbox).
		Build()

	// Wrap with a client that returns NotFound on Delete for our sandbox.
	wrappedClient := &deleteFailClient{
		WithWatch: baseClient.(client.WithWatch),
		failName:  "kraclaw-agent-gone-aaa111",
	}

	ctrl, err := New(fake.NewSimpleClientset(), wrappedClient, nil, "test-ns", "agent:latest")
	if err != nil {
		t.Fatal(err)
	}

	// CleanupOrphans should handle NotFound gracefully — no error returned.
	if err := ctrl.CleanupOrphans(context.Background()); err != nil {
		t.Fatalf("CleanupOrphans returned error: %v", err)
	}
}

func TestCleanupOrphans_CleansTerminal(t *testing.T) {
	ctrl := newTestController()
	ctx := context.Background()

	// Create two terminal sandboxes: one completed, one failed.
	for _, sb := range []*agentsandboxv1alpha1.Sandbox{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kraclaw-agent-completed-bbb222",
				Namespace: "test-ns",
				Labels: map[string]string{
					labelManagedBy: managedByValue,
					labelRole:      roleAgent,
					labelGroup:     "completed-group",
				},
			},
			Status: agentsandboxv1alpha1.SandboxStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
						Status: metav1.ConditionFalse,
						Reason: "Succeeded",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kraclaw-agent-failed-ccc333",
				Namespace: "test-ns",
				Labels: map[string]string{
					labelManagedBy: managedByValue,
					labelRole:      roleAgent,
					labelGroup:     "failed-group",
				},
			},
			Status: agentsandboxv1alpha1.SandboxStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(agentsandboxv1alpha1.SandboxConditionReady),
						Status: metav1.ConditionFalse,
						Reason: "Failed",
					},
				},
			},
		},
	} {
		if err := ctrl.ctrlClient.Create(ctx, sb); err != nil {
			t.Fatal(err)
		}
	}

	if err := ctrl.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}

	// Both terminal sandboxes should be deleted.
	for _, name := range []string{"kraclaw-agent-completed-bbb222", "kraclaw-agent-failed-ccc333"} {
		err := ctrl.ctrlClient.Get(ctx, client.ObjectKey{Namespace: "test-ns", Name: name}, &agentsandboxv1alpha1.Sandbox{})
		if err == nil {
			t.Fatalf("expected sandbox %s to be deleted", name)
		}
	}
}

func TestGenerateSandboxName_LongFolder(t *testing.T) {
	ctrl := newTestController()
	name, err := ctrl.generateSandboxName("this-is-a-very-long-folder-name-that-exceeds-the-limit-for-kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	if len(name) > 63 {
		t.Fatalf("sandbox name too long: %d chars (%s)", len(name), name)
	}
}

func TestBuildSandbox_AdditionalMounts(t *testing.T) {
	ctrl := newTestController()
	cfg := SandboxConfig{
		GroupFolder: "test-group",
		GroupJID:    "test@jid",
		ContainerConfig: &store.ContainerConfig{
			AdditionalMounts: []store.AdditionalMount{
				{HostPath: "/data/models", ContainerPath: "/models", ReadOnly: true},
				{HostPath: "/data/cache", ContainerPath: "/cache", ReadOnly: false},
			},
		},
	}

	sb := ctrl.buildSandbox("test-sandbox", cfg)
	podSpec := sb.Spec.PodTemplate.Spec

	// Base volumes: sessions, groups, data (3) + 2 extra = 5
	if len(podSpec.Volumes) != 5 {
		t.Fatalf("expected 5 volumes, got %d", len(podSpec.Volumes))
	}
	// Base mounts on agent container: 3 + 2 extra = 5
	agentMounts := podSpec.Containers[0].VolumeMounts
	if len(agentMounts) != 5 {
		t.Fatalf("expected 5 volume mounts, got %d", len(agentMounts))
	}

	// Verify extra volume names and HostPath sources.
	extra0 := podSpec.Volumes[3]
	if extra0.Name != "extra-0" {
		t.Fatalf("extra volume 0 name = %q, want %q", extra0.Name, "extra-0")
	}
	if extra0.HostPath == nil || extra0.HostPath.Path != "/data/models" {
		t.Fatalf("extra volume 0 HostPath = %v, want /data/models", extra0.HostPath)
	}
	if *extra0.HostPath.Type != corev1.HostPathDirectory {
		t.Fatalf("extra volume 0 HostPath type = %v, want Directory", *extra0.HostPath.Type)
	}

	extra1 := podSpec.Volumes[4]
	if extra1.Name != "extra-1" {
		t.Fatalf("extra volume 1 name = %q, want %q", extra1.Name, "extra-1")
	}
	if extra1.HostPath == nil || extra1.HostPath.Path != "/data/cache" {
		t.Fatalf("extra volume 1 HostPath = %v, want /data/cache", extra1.HostPath)
	}

	// Verify extra mount paths and ReadOnly.
	mount0 := agentMounts[3]
	if mount0.Name != "extra-0" || mount0.MountPath != "/models" || !mount0.ReadOnly {
		t.Fatalf("extra mount 0 = %+v, want name=extra-0 mountPath=/models readOnly=true", mount0)
	}
	mount1 := agentMounts[4]
	if mount1.Name != "extra-1" || mount1.MountPath != "/cache" || mount1.ReadOnly {
		t.Fatalf("extra mount 1 = %+v, want name=extra-1 mountPath=/cache readOnly=false", mount1)
	}
}

func TestBuildSandbox_NoAdditionalMounts(t *testing.T) {
	ctrl := newTestController()
	cfg := SandboxConfig{
		GroupFolder: "test-group",
		GroupJID:    "test@jid",
	}

	sb := ctrl.buildSandbox("test-sandbox", cfg)
	podSpec := sb.Spec.PodTemplate.Spec

	// Base only: 3 volumes, 3 mounts.
	if len(podSpec.Volumes) != 3 {
		t.Fatalf("expected 3 volumes, got %d", len(podSpec.Volumes))
	}
	if len(podSpec.Containers[0].VolumeMounts) != 3 {
		t.Fatalf("expected 3 volume mounts, got %d", len(podSpec.Containers[0].VolumeMounts))
	}
}

func TestBuildSandbox_AdditionalMountFallback(t *testing.T) {
	ctrl := newTestController()
	cfg := SandboxConfig{
		GroupFolder: "test-group",
		GroupJID:    "test@jid",
		ContainerConfig: &store.ContainerConfig{
			AdditionalMounts: []store.AdditionalMount{
				{HostPath: "/data/shared", ContainerPath: "", ReadOnly: false},
			},
		},
	}

	sb := ctrl.buildSandbox("test-sandbox", cfg)
	agentMounts := sb.Spec.PodTemplate.Spec.Containers[0].VolumeMounts

	// When ContainerPath is empty, MountPath should fall back to HostPath.
	lastMount := agentMounts[len(agentMounts)-1]
	if lastMount.MountPath != "/data/shared" {
		t.Fatalf("MountPath = %q, want %q (fallback to HostPath)", lastMount.MountPath, "/data/shared")
	}
}
