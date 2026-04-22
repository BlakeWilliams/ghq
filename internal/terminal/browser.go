package terminal

import (
	"os/exec"
	"runtime"
)

// OpenURL opens the given URL in the user's default browser.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
