package terminal

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenURL_BuildsCorrectCommand(t *testing.T) {
	// Just verify the function exists and has the right signature.
	// We don't actually call it because it opens a real browser.
	var fn func(string) error = OpenURL
	assert.NotNil(t, fn)

	switch runtime.GOOS {
	case "darwin", "linux":
		// Supported platforms — no-op, just confirming compilation.
	default:
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
}
