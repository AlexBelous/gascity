package testenv

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// passwdHomeForCurrentUser returns the real OS passwd home for the current
// uid, skipping the test on platforms or environments where that cannot be
// resolved. HOME normalization is scoped to darwin/linux only, matching
// platformSupervisorHomeOverrideError (cmd/gc/cmd_supervisor_lifecycle.go).
func passwdHomeForCurrentUser(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "linux":
	default:
		t.Skip("HOME normalization only applies on darwin/linux")
	}
	lu, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil || strings.TrimSpace(lu.HomeDir) == "" {
		t.Skip("cannot resolve passwd home for current uid")
	}
	return lu.HomeDir
}

func TestNormalizeHomeToPasswdHomeSetsRealHome(t *testing.T) {
	passwdHome := passwdHomeForCurrentUser(t)
	t.Setenv("HOME", t.TempDir())

	normalizeHomeToPasswdHome()

	if got := os.Getenv("HOME"); filepath.Clean(got) != filepath.Clean(passwdHome) {
		t.Fatalf("HOME after normalizeHomeToPasswdHome() = %q, want %q", got, passwdHome)
	}
}

func TestNormalizeHomeToPasswdHomeIdempotentWhenAlreadyCorrect(t *testing.T) {
	passwdHome := passwdHomeForCurrentUser(t)
	t.Setenv("HOME", passwdHome)

	normalizeHomeToPasswdHome()
	if got := os.Getenv("HOME"); got != passwdHome {
		t.Fatalf("HOME changed by normalizeHomeToPasswdHome() when already correct: got %q, want unchanged %q", got, passwdHome)
	}

	// Calling again must remain a no-op.
	normalizeHomeToPasswdHome()
	if got := os.Getenv("HOME"); got != passwdHome {
		t.Fatalf("HOME after second normalizeHomeToPasswdHome() call = %q, want unchanged %q", got, passwdHome)
	}
}
