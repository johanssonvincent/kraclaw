package scheduler

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	gosql "github.com/go-sql-driver/mysql"
	"github.com/johanssonvincent/kraclaw/internal/store"
	"golang.org/x/sync/semaphore"
)

// maxConcurrentTasks bounds how many tasks can execute simultaneously within a poll window.
const maxConcurrentTasks = int64(3)

func isTransientAdvanceError(err error) bool {
	if errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}

	if mysqlErr, ok := errors.AsType[*gosql.MySQLError](err); ok {
		return mysqlErr.Number == 1205 || mysqlErr.Number == 1213
	}

	return false
}

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

	nextRun, err := s.computeNextRun(&task)
	if err != nil {
		s.log.Error("invalid schedule; pausing task", "task_id", task.ID, "schedule", task.ScheduleValue, "error", err)

		task.Status = store.TaskPaused
		if pauseErr := s.store.UpdateTask(ctx, &task); pauseErr != nil {
			s.log.Error("failed to pause task", "task_id", task.ID, "error", pauseErr)
		}

		return
	}

	task.LastRun = &start
	if task.ScheduleType == store.ScheduleOnce {
		task.NextRun = nil
		task.Status = store.TaskCompleted
	} else {
		task.NextRun = nextRun
	}

	if err := s.store.UpdateTask(ctx, &task); err != nil {
		if isTransientAdvanceError(err) {
			s.log.Error("task advance deferred to next poll", "task_id", task.ID, "error", err)

			return
		}

		s.log.Error("task advance failed permanently; pausing task", "task_id", task.ID, "error", err)

		task.Status = store.TaskPaused
		if pauseErr := s.store.UpdateTask(ctx, &task); pauseErr != nil {
			s.log.Error("failed to pause task", "task_id", task.ID, "error", pauseErr)
		}
		return
	}

	task.LastRun = &start
	if task.ScheduleType == store.ScheduleOnce {
		task.NextRun = nil
		task.Status = store.TaskCompleted
	} else {
		task.NextRun = nextRun
	}

	if err := s.store.UpdateTask(ctx, &task); err != nil {
		s.log.Error("failed to persist task advance", "task_id", task.ID, "error", err)

		return
	}

	err = s.executor(ctx, task)

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

	outcome := "enqueued"
	if err != nil {
		outcome = err.Error()
	}

	logErr := s.store.LogTaskRun(ctx, &store.TaskRunLog{
		TaskID:      task.ID,
		GroupFolder: task.GroupFolder,
		RunAt:       start,
		DurationMs:  int(duration.Milliseconds()),
		Status:      status,
		Error:       errStr,
		Result:      &outcome,
	})
	if logErr != nil {
		s.log.Error("failed to log task run", "task_id", task.ID, "error", logErr)
	}

	task.LastResult = &outcome
	if updateErr := s.store.UpdateTask(ctx, &task); updateErr != nil {
		s.log.Error("failed to record task outcome", "task_id", task.ID, "error", updateErr)
	}
}

// computeNextRun calculates the next run time for a task.
func (s *Scheduler) computeNextRun(task *store.ScheduledTask) (*time.Time, error) {
	now := time.Now()

	switch task.ScheduleType {
	case store.ScheduleCron:
		sched, err := store.CronParser.Parse(task.ScheduleValue)
		if err != nil {
			return nil, fmt.Errorf("parse cron expression: %w", err)
		}
		next := sched.Next(now)

		return &next, nil

	case store.ScheduleInterval:
		d, err := time.ParseDuration(task.ScheduleValue)
		if err != nil {
			return nil, fmt.Errorf("parse interval: %w", err)
		}

		if d <= 0 {
			return nil, fmt.Errorf("interval %q must be positive", task.ScheduleValue)
		}

		if task.LastRun == nil {
			return &now, nil
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

		return &next, nil

	case store.ScheduleOnce:
		if task.LastRun != nil {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339, task.ScheduleValue)
		if err != nil {
			return nil, fmt.Errorf("parse once schedule: %w", err)
		}

		return &t, nil

	default:
		return nil, fmt.Errorf("unknown schedule type %q", task.ScheduleType)
	}
}
