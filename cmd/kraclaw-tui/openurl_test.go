package main

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestOpenURL_DispatchesPerOS(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		goos     string
		url      string
		wantName string
		wantArgs []string
	}{
		"linux uses xdg-open":           {goos: "linux", url: "https://example.com", wantName: "xdg-open", wantArgs: []string{"https://example.com"}},
		"darwin uses open":              {goos: "darwin", url: "https://example.com", wantName: "open", wantArgs: []string{"https://example.com"}},
		"windows uses cmd /c start \"\"": {goos: "windows", url: "https://example.com", wantName: "cmd", wantArgs: []string{"/c", "start", "", "https://example.com"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var gotName string
			var gotArgs []string
			fakeRun := func(name string, args ...string) error {
				gotName = name
				gotArgs = append([]string{}, args...)
				return nil
			}
			if err := openURLFor(tt.goos, tt.url, fakeRun); err != nil {
				t.Fatalf("openURLFor: %v", err)
			}
			if gotName != tt.wantName {
				t.Errorf("openURLFor goos=%q name = %q, want %q", tt.goos, gotName, tt.wantName)
			}
			if !slices.Equal(gotArgs, tt.wantArgs) {
				t.Errorf("openURLFor goos=%q args = %v, want %v", tt.goos, gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestOpenURL_UnsupportedOS(t *testing.T) {
	t.Parallel()
	err := openURLFor("freebsd", "https://example.com", func(string, ...string) error {
		t.Fatalf("run should not be called for unsupported OS")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported OS") {
		t.Errorf("openURLFor(freebsd) err = %v, want unsupported OS error", err)
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
