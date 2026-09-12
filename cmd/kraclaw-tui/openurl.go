package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

type runFn func(name string, args ...string) error

// OpenURL launches the system browser to the given URL. Bes...
func OpenURL(url string) error {
	return openURLFor(runtime.GOOS, url, func(name string, args ...string) error {
		return exec.Command(name, args...).Start()
	})
}

func openURLFor(goos, url string, run runFn) error {
	var (
		name string
		args []string
		hint string
	)

	switch goos {
	case "linux":
		name, args = "xdg-open", []string{url}
		hint = "install xdg-open or copy the URL manually"
	case "darwin":
		name, args = "open", []string{url}
		hint = "copy the URL manually"
	case "windows":
		name, args = "cmd", []string{"/c", "start", "", url}
		hint = "copy the URL manually"
	default:
		return fmt.Errorf("openurl: unsupported OS %q", goos)
	}

	if err := run(name, args...); err != nil {
		return fmt.Errorf("openurl on %s: %w (%s)", goos, err, hint)
	}

	return nil
}
