package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
	"github.com/robfig/cron/v3"
	"golang.org/x/sync/semaphore"
)

// maxConcurrentTasks bounds how many tasks can execute simultaneously within a poll window.
const maxConcurrentTasks = int64(3)

// TaskExecutor executes a single scheduled task.
type TaskExecutor func(ctx context.Context, task store.ScheduledTask) error

// Scheduler polls for due tasks and executes them.
type Scheduler struct {
	store        store.TaskStore
	executor     TaskExecutor
	pollInterval time.Duration
	log          *slog.Logger
	semaphore    *semaphore.Weighted // bounds concurrent task execution
}

// New creates a new Scheduler.
func New(s store.TaskStore, executor TaskExecutor, pollInterval time.Duration) (*Scheduler, error) {
	if s == nil {
		return nil, fmt.Errorf("scheduler: task store is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("scheduler: executor is required")
	}
	return &Scheduler{
		store:        s,
		executor:     executor,
		pollInterval: pollInterval,
		log:          slog.Default(),
		semaphore:    semaphore.NewWeighted(maxConcurrentTasks),
	}, nil
}

// Start runs the scheduler loop, blocking until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	s.log.Info("scheduler started", "poll_interval", s.pollInterval)

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		s.poll(ctx)

		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) poll(ctx context.Context) {
	tasks, err := s.store.GetDueTasks(ctx)
	if err != nil {
		s.log.Error("failed to get due tasks", "error", err)
		return
	}

	if len(tasks) > 0 {
		s.log.Info("found due tasks", "count", len(tasks))
	}

	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(t store.ScheduledTask) {
			defer wg.Done()
			// If ctx is cancelled while waiting for a slot, Acquire returns an
			// error and runTask is skipped. The task row is left untouched
			// (LastRun/NextRun unchanged), so GetDueTasks will re-surface it on
			// the next poll tick provided NextRun is still in the past.
			if err := s.semaphore.Acquire(ctx, 1); err != nil {
				s.log.Error("semaphore acquire cancelled", "task_id", t.ID, "error", err)
				return
			}
			defer s.semaphore.Release(1)
			s.runTask(ctx, t)
		}(task)
	}
	wg.Wait()
}

func (s *Scheduler) runTask(ctx context.Context, task store.ScheduledTask) {
	start := time.Now()
	s.log.Info("running task", "task_id", task.ID, "group", task.GroupFolder)

	err := s.executor(ctx, task)

	duration := time.Since(start)
	status := store.RunSuccess
	var errStr *string
	if err != nil {
		status = store.RunError
		e := err.Error()
		errStr = &e
		s.log.Error("task failed", "task_id", task.ID, "error", err, "duration", duration)
	} else {
		s.log.Info("task completed", "task_id", task.ID, "duration", duration)
	}

	// Log the task run regardless of outcome so the run history is always complete.
	logErr := s.store.LogTaskRun(ctx, &store.TaskRunLog{
		TaskID:      task.ID,
		GroupFolder: task.GroupFolder,
		RunAt:       start,
		DurationMs:  int(duration.Milliseconds()),
		Status:      status,
		Error:       errStr,
	})
	if logErr != nil {
		s.log.Error("failed to log task run", "task_id", task.ID, "error", logErr)
	}

	if err != nil {
		// Leave LastRun/NextRun/Status untouched so the task row is re-surfaced
		// by GetDueTasks on the next poll tick. The TaskRunLog row above captures
		// the failure history. UpdateTask is intentionally skipped here.
		return
	}

	// Advance the schedule only on success.
	now := time.Now()
	task.LastRun = &now
	nextRun := s.computeNextRun(&task)
	task.NextRun = nextRun
	if nextRun == nil && task.ScheduleType == store.ScheduleOnce {
		task.Status = store.TaskCompleted
	}

	if updateErr := s.store.UpdateTask(ctx, &task); updateErr != nil {
		if task.ScheduleType == store.ScheduleOnce {
			s.log.Error("failed to mark once-task completed; task will re-fire on next poll",
				"task_id", task.ID, "error", updateErr)
		} else {
			s.log.Error("failed to update task", "task_id", task.ID, "error", updateErr)
		}
	}
}

// computeNextRun calculates the next run time for a task.
func (s *Scheduler) computeNextRun(task *store.ScheduledTask) *time.Time {
	now := time.Now()

	switch task.ScheduleType {
	case store.ScheduleCron:
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(task.ScheduleValue)
		if err != nil {
			s.log.Error("invalid cron expression", "task_id", task.ID, "expr", task.ScheduleValue, "error", err)
			return nil
		}
		next := sched.Next(now)
		return &next

	case store.ScheduleInterval:
		d, err := time.ParseDuration(task.ScheduleValue)
		if err != nil || d <= 0 {
			s.log.Error("invalid interval", "task_id", task.ID, "value", task.ScheduleValue, "error", err)
			return nil
		}

		if task.LastRun == nil {
			return &now
		}

		// Anchor to last scheduled time and skip forward to prevent drift.
		var lastScheduled time.Time
		if task.NextRun != nil {
			lastScheduled = *task.NextRun
		} else {
			lastScheduled = *task.LastRun
		}

		next := lastScheduled
		for !next.After(now) {
			next = next.Add(d)
		}
		return &next

	case store.ScheduleOnce:
		if task.LastRun != nil {
			return nil
		}
		t, err := time.Parse(time.RFC3339, task.ScheduleValue)
		if err != nil {
			s.log.Error("invalid once schedule", "task_id", task.ID, "value", task.ScheduleValue, "error", err)
			return nil
		}
		return &t

	default:
		return nil
	}
}
