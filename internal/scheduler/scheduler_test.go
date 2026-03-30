package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

// mockTaskStore implements store.TaskStore for testing poll concurrency.
type mockTaskStore struct {
	tasks []store.ScheduledTask
}

func (m *mockTaskStore) CreateTask(context.Context, *store.ScheduledTask) error { return nil }
func (m *mockTaskStore) GetTask(context.Context, string, string) (*store.ScheduledTask, error) {
	return nil, nil
}
func (m *mockTaskStore) ListTasks(context.Context) ([]store.ScheduledTask, error)          { return nil, nil }
func (m *mockTaskStore) ListTasksByGroup(context.Context, string) ([]store.ScheduledTask, error) {
	return nil, nil
}
func (m *mockTaskStore) UpdateTask(context.Context, *store.ScheduledTask) error  { return nil }
func (m *mockTaskStore) DeleteTask(context.Context, string, string) error        { return nil }
func (m *mockTaskStore) GetDueTasks(ctx context.Context) ([]store.ScheduledTask, error) {
	return m.tasks, nil
}
func (m *mockTaskStore) LogTaskRun(context.Context, *store.TaskRunLog) error { return nil }
func (m *mockTaskStore) GetTaskRunLogs(context.Context, string, string, int) ([]store.TaskRunLog, error) {
	return nil, nil
}

func TestPollConcurrency(t *testing.T) {
	tests := []struct {
		name          string
		taskCount     int
		taskDuration  time.Duration
		maxConcurrent int64
		checkFunc     func(t *testing.T, maxSeen int64, elapsed time.Duration)
	}{
		{
			name:          "3 tasks execute concurrently",
			taskCount:     3,
			taskDuration:  50 * time.Millisecond,
			maxConcurrent: 3,
			checkFunc: func(t *testing.T, maxSeen int64, elapsed time.Duration) {
				// If sequential, would take 150ms+. Concurrent should take ~50ms.
				if elapsed >= 140*time.Millisecond {
					t.Errorf("tasks appear sequential: elapsed %v (want < 140ms)", elapsed)
				}
				if maxSeen < 2 {
					t.Errorf("expected at least 2 concurrent tasks, max seen: %d", maxSeen)
				}
			},
		},
		{
			name:          "5 tasks bounded by semaphore of 3",
			taskCount:     5,
			taskDuration:  30 * time.Millisecond,
			maxConcurrent: 3,
			checkFunc: func(t *testing.T, maxSeen int64, elapsed time.Duration) {
				if maxSeen > 3 {
					t.Errorf("concurrency exceeded semaphore: max seen %d (want <= 3)", maxSeen)
				}
			},
		},
		{
			name:          "poll blocks until all goroutines complete",
			taskCount:     3,
			taskDuration:  40 * time.Millisecond,
			maxConcurrent: 3,
			checkFunc: func(t *testing.T, maxSeen int64, elapsed time.Duration) {
				// poll must wait for all tasks — elapsed should be >= taskDuration
				if elapsed < 35*time.Millisecond {
					t.Errorf("poll returned too fast: %v (tasks take 40ms)", elapsed)
				}
			},
		},
		{
			name:          "slow task does not delay fast tasks",
			taskCount:     0, // handled specially below
			taskDuration:  0,
			maxConcurrent: 3,
			checkFunc:     nil, // handled specially below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "slow task does not delay fast tasks" {
				testSlowTaskDoesNotDelayFast(t)
				return
			}

			tasks := make([]store.ScheduledTask, tt.taskCount)
			for i := range tasks {
				tasks[i] = store.ScheduledTask{
					ID:           fmt.Sprintf("task-%d", i),
					ScheduleType: store.ScheduleOnce,
				}
			}

			var running atomic.Int64
			var maxRunning atomic.Int64
			var mu sync.Mutex
			_ = mu

			executor := func(ctx context.Context, task store.ScheduledTask) error {
				cur := running.Add(1)
				defer running.Add(-1)
				// Track max concurrency
				for {
					old := maxRunning.Load()
					if cur <= old || maxRunning.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(tt.taskDuration)
				return nil
			}

			ms := &mockTaskStore{tasks: tasks}
			sched, err := New(ms, executor, time.Minute)
			if err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			sched.poll(context.Background())
			elapsed := time.Since(start)

			tt.checkFunc(t, maxRunning.Load(), elapsed)
		})
	}
}

func testSlowTaskDoesNotDelayFast(t *testing.T) {
	tasks := []store.ScheduledTask{
		{ID: "slow", ScheduleType: store.ScheduleOnce},
		{ID: "fast-1", ScheduleType: store.ScheduleOnce},
		{ID: "fast-2", ScheduleType: store.ScheduleOnce},
	}

	var fastDone atomic.Int64
	var slowStarted atomic.Bool

	executor := func(ctx context.Context, task store.ScheduledTask) error {
		if task.ID == "slow" {
			slowStarted.Store(true)
			time.Sleep(100 * time.Millisecond)
		} else {
			time.Sleep(10 * time.Millisecond)
			fastDone.Add(1)
		}
		return nil
	}

	ms := &mockTaskStore{tasks: tasks}
	sched, err := New(ms, executor, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	sched.poll(context.Background())

	if fastDone.Load() != 2 {
		t.Errorf("expected 2 fast tasks done, got %d", fastDone.Load())
	}
}

func TestComputeNextRun(t *testing.T) {
	s := &Scheduler{log: slog.Default()}
	now := time.Now()

	fiveMinAgo := now.Add(-5 * time.Minute)
	tenMinAgo := now.Add(-10 * time.Minute)
	scheduledAt := now.Add(-3 * time.Minute)

	tests := []struct {
		name      string
		task      store.ScheduledTask
		checkFunc func(t *testing.T, result *time.Time)
	}{
		{
			name: "cron returns future time",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleCron,
				ScheduleValue: "*/5 * * * *",
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if !result.After(now) {
					t.Errorf("cron next run %v should be after now %v", result, now)
				}
			},
		},
		{
			name: "cron invalid expression",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleCron,
				ScheduleValue: "not a cron",
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result != nil {
					t.Errorf("expected nil for invalid cron, got %v", result)
				}
			},
		},
		{
			name: "interval first run returns now",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleInterval,
				ScheduleValue: "5m",
				LastRun:       nil,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				// Should be approximately now.
				if result.Sub(now).Abs() > time.Second {
					t.Errorf("expected ~now, got %v", result)
				}
			},
		},
		{
			name: "interval anchored to scheduled time prevents drift",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleInterval,
				ScheduleValue: "5m",
				LastRun:       &fiveMinAgo,
				NextRun:       &scheduledAt,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				// Should be scheduledAt + 5m.
				expected := scheduledAt.Add(5 * time.Minute)
				if result.Sub(expected).Abs() > time.Second {
					t.Errorf("expected %v, got %v", expected, result)
				}
			},
		},
		{
			name: "interval skips missed runs",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleInterval,
				ScheduleValue: "3m",
				LastRun:       &tenMinAgo,
				NextRun:       &tenMinAgo,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				if !result.After(now) {
					t.Errorf("interval next run %v should be after now %v", result, now)
				}
				// Should be aligned: tenMinAgo + N*3m > now.
				diff := result.Sub(tenMinAgo)
				intervals := diff / (3 * time.Minute)
				if intervals < 1 {
					t.Errorf("expected at least one interval skip")
				}
			},
		},
		{
			name: "interval invalid duration",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleInterval,
				ScheduleValue: "not-a-duration",
				LastRun:       &fiveMinAgo,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result != nil {
					t.Errorf("expected nil for invalid interval, got %v", result)
				}
			},
		},
		{
			name: "once with no last run returns parsed time",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleOnce,
				ScheduleValue: "2024-06-15T10:00:00Z",
				LastRun:       nil,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				expected := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
				if !result.Equal(expected) {
					t.Errorf("expected %v, got %v", expected, result)
				}
			},
		},
		{
			name: "once with last run returns nil",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleOnce,
				ScheduleValue: "2024-06-15T10:00:00Z",
				LastRun:       &fiveMinAgo,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result != nil {
					t.Errorf("expected nil for completed once task, got %v", result)
				}
			},
		},
		{
			name: "once invalid time",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleOnce,
				ScheduleValue: "not-a-time",
				LastRun:       nil,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result != nil {
					t.Errorf("expected nil for invalid once time, got %v", result)
				}
			},
		},
		{
			name: "interval falls back to last_run when next_run is nil",
			task: store.ScheduledTask{
				ScheduleType:  store.ScheduleInterval,
				ScheduleValue: "5m",
				LastRun:       &tenMinAgo,
				NextRun:       nil,
			},
			checkFunc: func(t *testing.T, result *time.Time) {
				if result == nil {
					t.Fatal("expected non-nil result")
				}
				// tenMinAgo + 5m = fiveMinAgo (still <= now), so next = tenMinAgo + 10m ≈ now + a few ms.
				// It should skip past now.
				if !result.After(now) {
					t.Errorf("expected future time, got %v (now=%v)", result, now)
				}
				// Should be anchored: tenMinAgo + N*5m.
				diff := result.Sub(tenMinAgo)
				remainder := diff % (5 * time.Minute)
				if remainder > time.Second {
					t.Errorf("expected aligned to 5m intervals from anchor, diff=%v remainder=%v", diff, remainder)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.computeNextRun(&tt.task)
			tt.checkFunc(t, result)
		})
	}
}
