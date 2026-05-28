package sandbox

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/johanssonvincent/kraclaw/internal/metrics"
)

// SandboxEvent represents a lifecycle event for a sandbox.
type SandboxEvent struct {
	Type   string // "added", "updated", "deleted"
	Status SandboxStatus
}

// WatchSandboxes returns a channel of sandbox lifecycle events.
// It performs a List first to capture the current resourceVersion, then starts a Watch
// from that point to avoid replaying historical events on reconnect.
// The returned channel closes when the watch stream ends (ctx cancel or API disconnect).
// Callers should wrap this in a retry loop (see orchestrator.sandboxWatcher).
func (c *Controller) WatchSandboxes(ctx context.Context) (<-chan SandboxEvent, error) {
	ch := make(chan SandboxEvent, 64)

	labelSel := labelManagedBy + "=" + managedByValue

	// List current sandboxes to obtain a resourceVersion anchor.
	list := &agentsandboxv1alpha1.SandboxList{}
	if err := c.ctrlClient.List(ctx, list, &client.ListOptions{
		Namespace: c.namespace,
		Raw:       &metav1.ListOptions{LabelSelector: labelSel},
	}); err != nil {
		return nil, fmt.Errorf("sandbox: list sandboxes for watch: %w", err)
	}

	// Watch from the resourceVersion of the List result.
	w, err := c.ctrlClient.Watch(ctx, &agentsandboxv1alpha1.SandboxList{}, &client.ListOptions{
		Namespace: c.namespace,
		Raw: &metav1.ListOptions{
			LabelSelector:   labelSel,
			ResourceVersion: list.ResourceVersion,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox: watch sandboxes: %w", err)
	}

	go func() {
		defer w.Stop()
		defer close(ch)

		// seen tracks per-sandbox-name which phases we have already recorded
		// so duplicate Modified events don't double-observe the histogram.
		seen := map[string]map[string]bool{}

		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-w.ResultChan():
				if !ok {
					// Watch stream ended — caller's retry loop handles reconnect.
					return
				}

				sandbox, ok := event.Object.(*agentsandboxv1alpha1.Sandbox)
				if !ok {
					continue
				}

				var evType string
				switch event.Type {
				case watch.Added:
					evType = "added"
				case watch.Modified:
					evType = "updated"
				case watch.Deleted:
					evType = "deleted"
					delete(seen, sandbox.Name)
				default:
					continue
				}

				if event.Type != watch.Deleted {
					recordPhaseTransitions(sandbox, seen)
				}

				select {
				case ch <- SandboxEvent{Type: evType, Status: *sandboxToStatus(sandbox)}:
				default:
					c.log.Warn("sandbox event channel full, dropping event",
						"type", evType, "sandbox", sandbox.Name)
				}
			}
		}
	}()

	return ch, nil
}

// recordPhaseTransitions observes cold-start phase histograms for the
// PodScheduled and Ready conditions on a Sandbox. seen is keyed by sandbox
// name → phase name so duplicate Modified events do not double-record.
// Durations are measured from the Sandbox's CreationTimestamp to the
// condition's LastTransitionTime.
func recordPhaseTransitions(sb *agentsandboxv1alpha1.Sandbox, seen map[string]map[string]bool) {
	if seen[sb.Name] == nil {
		seen[sb.Name] = map[string]bool{}
	}
	created := sb.CreationTimestamp.Time
	for _, cond := range sb.Status.Conditions {
		if cond.Status != metav1.ConditionTrue {
			continue
		}
		var phase string
		switch cond.Type {
		case "PodScheduled":
			phase = "pod_scheduled"
		case string(agentsandboxv1alpha1.SandboxConditionReady):
			phase = "pod_ready"
		default:
			continue
		}
		if seen[sb.Name][phase] {
			continue
		}
		seen[sb.Name][phase] = true
		metrics.SandboxSpawnDuration.WithLabelValues(phase).Observe(cond.LastTransitionTime.Time.Sub(created).Seconds())
	}
}
