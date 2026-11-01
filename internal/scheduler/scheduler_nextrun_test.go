package scheduler

import (
	"testing"
	"time"

	"github.com/johanssonvincent/kraclaw/internal/store"
)

func TestComputeNextRunInterval(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name          string
		scheduleValue string
		lastRun       *time.Time
		nextRun       *time.Time
		wantOffset    time.Duration // relative to time.Now() inside the call
	}{
		{
			name:          "first run is immediate",
			scheduleValue: "1h",
			wantOffset:    0,
		},
		{
			name:          "already scheduled in the future is kept",
			scheduleValue: "1h",
			lastRun:       timePtr(now.Add(-30 * time.Minute)),
			nextRun:       timePtr(now.Add(30 * time.Minute)),
			wantOffset:    30 * time.Minute,
		},
		{
			name:          "catch-up skips whole intervals without drifting",
			scheduleValue: "1h",
			lastRun:       timePtr(now.Add(-30 * time.Minute)),
			nextRun:       timePtr(now.Add(-150 * time.Minute)),
			wantOffset:    30 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &store.ScheduledTask{
				ScheduleType:  store.ScheduleInterval,
				ScheduleValue: tt.scheduleValue,
				LastRun:       tt.lastRun,
				NextRun:       tt.nextRun,
			}

			got, err := (&Scheduler{}).computeNextRun(task)
			if err != nil {
				t.Fatalf("computeNextRun() error = %v", err)
			}

			if got == nil {
				t.Fatal("computeNextRun() = nil, want a time")
			}

			if delta := time.Until(*got) - tt.wantOffset; delta > 2*time.Second || delta < -2*time.Second {
				t.Errorf("computeNextRun() = %v (in %v), want in ~%v", *got, time.Until(*got), tt.wantOffset)
			}

			if tt.lastRun != nil && tt.nextRun != nil {
				// Anchored catch-up must stay on the original grid.
				if rem := got.Sub(*tt.nextRun) % time.Hour; rem != 0 {
					t.Errorf("computeNextRun() = %v is off the %v grid (remainder %v)", *got, tt.scheduleValue, rem)
				}
			}
		})
	}
}

// TestComputeNextRunIntervalSmallStepTerminates pins a tight feedback loop on
// the catch-up scan: it stepped forward one interval per iteration, so a
// sub-millisecond interval combined with any real history never finished
// (1ns x 1h = 3.6e12 iterations) and the scheduler goroutine never returned.
func TestComputeNextRunIntervalSmallStepTerminates(t *testing.T) {
	done := make(chan error, 1)

	go func() {
		lastRun := time.Now().Add(-time.Hour)
		nextRun := lastRun

		_, err := (&Scheduler{}).computeNextRun(&store.ScheduledTask{
			ScheduleType:  store.ScheduleInterval,
			ScheduleValue: "1ns",
			LastRun:       &lastRun,
			NextRun:       &nextRun,
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("computeNextRun() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("computeNextRun() did not terminate: catch-up loop steps one interval at a time")
	}
}

func timePtr(t time.Time) *time.Time { return &t }
