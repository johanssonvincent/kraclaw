package sandbox

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// CleanupOrphans lists all kraclaw-managed Sandbox resources and deletes completed/failed ones.
// Running sandboxes are logged as warnings but not force-deleted.
func (c *Controller) CleanupOrphans(ctx context.Context) error {
	sandboxes, err := c.ListSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("sandbox: list orphans: %w", err)
	}

	var cleaned int
	for _, s := range sandboxes {
		switch s.State {
		case StateCompleted, StateFailed:
			if err := c.StopSandbox(ctx, s.Name); err != nil {
				if apierrors.IsNotFound(err) {
					c.log.Info("orphan already cleaned up", "name", s.Name)
					cleaned++
					continue
				}
				c.log.Error("failed to clean up orphan", "name", s.Name, "error", err)
				continue
			}
			cleaned++
		case StateRunning:
			c.log.Warn("orphan sandbox still running, skipping", "name", s.Name, "group", s.Group)
		}
	}

	if cleaned > 0 {
		c.log.Info("cleaned up orphan sandboxes", "count", cleaned)
	}
	return nil
}
