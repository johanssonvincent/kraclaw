package store

import (
	"testing"
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
