package store

import (
	"testing"
	"time"
)

func TestScheduledTaskValidateCron(t *testing.T) {
	tests := []struct {
		name  string
		expr  string
		valid bool
	}{
		{name: "@daily accepted", expr: "@daily", valid: true},
		{name: "@hourly accepted", expr: "@hourly", valid: true},
		{name: "@every 1h30m accepted", expr: "@every 1h30m", valid: true},
		{name: "5-field cron accepted", expr: "*/5 * * * *", valid: true},
		{name: "6-field cron rejected", expr: "0 25 12 * * *", valid: false},
		{name: "garbage rejected", expr: "not a cron", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &ScheduledTask{
				ID:            "test",
				ScheduleType:  ScheduleCron,
				ScheduleValue: tt.expr,
			}

			err := st.Validate()
			if tt.valid {
				if err != nil {
					t.Fatalf("Validate(%q): want valid, got error: %v", tt.expr, err)
				}

				return
			}

			if err == nil {
				t.Errorf("Validate(%q): want error, got nil", tt.expr)
			}
		})
	}
}

func TestScheduledTaskInitialNextRun(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	t.Run("cron returns the next matching instant", func(t *testing.T) {
		task := &ScheduledTask{ScheduleType: ScheduleCron, ScheduleValue: "0 * * * *"}

		got, err := task.InitialNextRun(now)
		if err != nil {
			t.Fatalf("InitialNextRun() error = %v", err)
		}
		if got == nil {
			t.Fatal("InitialNextRun() = nil, want a time")
		}

		want := time.Date(2026, 11, 8, 13, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("InitialNextRun() = %v, want %v", got, want)
		}
	})

	t.Run("interval runs one interval after creation", func(t *testing.T) {
		task := &ScheduledTask{ScheduleType: ScheduleInterval, ScheduleValue: "90m"}

		got, err := task.InitialNextRun(now)
		if err != nil {
			t.Fatalf("InitialNextRun() error = %v", err)
		}

		want := now.Add(90 * time.Minute)
		if got == nil || !got.Equal(want) {
			t.Errorf("InitialNextRun() = %v, want %v", got, want)
		}
	})

	t.Run("once returns the scheduled instant", func(t *testing.T) {
		at := now.Add(2 * time.Hour)
		task := &ScheduledTask{ScheduleType: ScheduleOnce, ScheduleValue: at.Format(time.RFC3339)}

		got, err := task.InitialNextRun(now)
		if err != nil {
			t.Fatalf("InitialNextRun() error = %v", err)
		}
		if got == nil || !got.Equal(at) {
			t.Errorf("InitialNextRun() = %v, want %v", got, at)
		}
	})

	t.Run("invalid schedules are rejected", func(t *testing.T) {
		tests := []struct {
			name string
			task ScheduledTask
		}{
			{name: "bad cron", task: ScheduledTask{ScheduleType: ScheduleCron, ScheduleValue: "not a cron"}},
			{name: "bad interval", task: ScheduledTask{ScheduleType: ScheduleInterval, ScheduleValue: "soon"}},
			{name: "negative interval", task: ScheduledTask{ScheduleType: ScheduleInterval, ScheduleValue: "-5m"}},
			{name: "bad once", task: ScheduledTask{ScheduleType: ScheduleOnce, ScheduleValue: "tomorrow"}},
			{name: "unknown type", task: ScheduledTask{ScheduleType: "monthly", ScheduleValue: "1"}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := tt.task.InitialNextRun(now); err == nil {
					t.Error("InitialNextRun() error = nil, want an error")
				}
			})
		}
	})
}

func TestScheduledTaskEnsureNextRun(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	t.Run("fills in a missing next_run", func(t *testing.T) {
		task := &ScheduledTask{ScheduleType: ScheduleInterval, ScheduleValue: "1h"}

		if err := task.EnsureNextRun(now); err != nil {
			t.Fatalf("EnsureNextRun() error = %v", err)
		}
		if task.NextRun == nil {
			t.Fatal("NextRun = nil after EnsureNextRun, want a time")
		}
	})

	t.Run("leaves an existing next_run untouched", func(t *testing.T) {
		existing := now.Add(15 * time.Minute)
		task := &ScheduledTask{ScheduleType: ScheduleInterval, ScheduleValue: "1h", NextRun: &existing}

		if err := task.EnsureNextRun(now); err != nil {
			t.Fatalf("EnsureNextRun() error = %v", err)
		}
		if task.NextRun == nil || !task.NextRun.Equal(existing) {
			t.Errorf("NextRun = %v, want the existing %v", task.NextRun, existing)
		}
	})
}
