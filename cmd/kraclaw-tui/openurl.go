package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

type runFn func(name string, args ...string) error

// OpenURL launches the system browser to the given URL. Best-effort:
// failure is non-fatal because the device-flow user_code is always shown.
//
// Platform requirements:
//   - linux: xdg-open must be in PATH (missing in headless containers).
//   - darwin: open(1) is provided by the OS.
//   - windows: cmd.exe; the empty-string title arg before the URL is
//     load-bearing — without it cmd's start parses the URL as the title.
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
		return run("cmd", "/c", "start", "", url)
	default:
		return fmt.Errorf("openurl: unsupported OS %q", goos)
	}
}
