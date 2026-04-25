package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

type runFn func(name string, args ...string) error

// OpenURL launches the system browser. It is a best-effort helper: callers
// must always display the URL alongside any UI affordance, so failures here
// are non-fatal — the device-flow user_code is what actually matters.
func OpenURL(url string) error {
	return openURLFor(runtime.GOOS, url, func(name string, args ...string) error {
		return exec.Command(name, args...).Start()
	})
}

func openURLFor(goos, url string, run runFn) error {
	switch goos {
	case "linux":
		return run("xdg-open", url)
	case "darwin":
		return run("open", url)
	case "windows":
		// "" arg before url is the title for the cmd start subshell.
		return run("cmd", "/c", "start", "", url)
	default:
		return fmt.Errorf("openurl: unsupported OS %q", goos)
	}
}
