package main

import (
	"errors"
	"runtime"
	"testing"
)

func TestOpenURL_DispatchesPerOS(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		goos     string
		wantArg0 string
	}{
		"linux uses xdg-open": {goos: "linux", wantArg0: "xdg-open"},
		"darwin uses open":    {goos: "darwin", wantArg0: "open"},
		"windows uses cmd":    {goos: "windows", wantArg0: "cmd"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got string
			fakeRun := func(name string, _ ...string) error {
				got = name
				return nil
			}
			if err := openURLFor(tt.goos, "https://example.com", fakeRun); err != nil {
				t.Fatalf("openURLFor: %v", err)
			}
			if got != tt.wantArg0 {
				t.Errorf("openURLFor goos=%q invoked %q, want %q", tt.goos, got, tt.wantArg0)
			}
		})
	}
}

func TestOpenURL_PropagatesError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	got := openURLFor(runtime.GOOS, "https://example.com", func(string, ...string) error { return want })
	if !errors.Is(got, want) {
		t.Errorf("openURLFor error = %v, want %v", got, want)
	}
}
