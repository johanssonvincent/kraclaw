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
		goos            string
		url             string
		wantName        string
		wantArgs        []string
		runErr          error
		wantErrContains string
	}{
		"linux uses xdg-open":           {goos: "linux", url: "https://example.com", wantName: "xdg-open", wantArgs: []string{"https://example.com"}},
		"darwin uses open":              {goos: "darwin", url: "https://example.com", wantName: "open", wantArgs: []string{"https://example.com"}},
		"windows uses cmd /c start \"\"": {goos: "windows", url: "https://example.com", wantName: "cmd", wantArgs: []string{"/c", "start", "", "https://example.com"}},
		"linux missing xdg-open includes hint": {
			goos:            "linux",
			url:             "https://example.com",
			wantName:        "xdg-open",
			wantArgs:        []string{"https://example.com"},
			runErr:          errors.New(`exec: "xdg-open": executable file not found in $PATH`),
			wantErrContains: "install xdg-open or copy the URL manually",
		},
		"darwin run failure includes hint": {
			goos:            "darwin",
			url:             "https://example.com",
			wantName:        "open",
			wantArgs:        []string{"https://example.com"},
			runErr:          errors.New("open: boom"),
			wantErrContains: "copy the URL manually",
		},
		"windows run failure includes hint": {
			goos:            "windows",
			url:             "https://example.com",
			wantName:        "cmd",
			wantArgs:        []string{"/c", "start", "", "https://example.com"},
			runErr:          errors.New("cmd: boom"),
			wantErrContains: "copy the URL manually",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var gotName string
			var gotArgs []string
			fakeRun := func(name string, args ...string) error {
				gotName = name
				gotArgs = append([]string{}, args...)
				return tt.runErr
			}
			err := openURLFor(tt.goos, tt.url, fakeRun)
			if tt.runErr == nil {
				if err != nil {
					t.Fatalf("openURLFor: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("openURLFor goos=%q runErr=%v: got nil err, want error", tt.goos, tt.runErr)
				}
				if !errors.Is(err, tt.runErr) {
					t.Errorf("openURLFor goos=%q err = %v, want errors.Is(%v)", tt.goos, err, tt.runErr)
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("openURLFor goos=%q err = %q, want substring %q", tt.goos, err.Error(), tt.wantErrContains)
				}
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
