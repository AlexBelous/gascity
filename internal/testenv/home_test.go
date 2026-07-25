package testenv

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	homeHelperEnvVar       = "GC_TESTENV_HOME_HELPER"
	homeHelperResultPrefix = "GC_TESTENV_HOME_HELPER_RESULT="
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

// TestHelperProcessPrintHome is not a real test. TestInitNormalizesHomeAcrossGoTestSubprocess
// re-invokes this test binary with this test selected and homeHelperEnvVar
// set, so the child process prints its own HOME (as observed after package
// init() has run) for the parent to assert against.
func TestHelperProcessPrintHome(t *testing.T) {
	if os.Getenv(homeHelperEnvVar) != "1" {
		t.Skip("not invoked as the HOME-normalization helper subprocess")
	}
	fmt.Println(homeHelperResultPrefix + os.Getenv("HOME"))
}

func TestInitNormalizesHomeAcrossGoTestSubprocess(t *testing.T) {
	passwdHome := passwdHomeForCurrentUser(t)
	wrongHome := t.TempDir()
	if filepath.Clean(wrongHome) == filepath.Clean(passwdHome) {
		t.Fatalf("test setup: t.TempDir() %q collided with real passwd home %q", wrongHome, passwdHome)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessPrintHome$", "-test.v=true")
	cmd.Env = append(os.Environ(), homeHelperEnvVar+"=1", "HOME="+wrongHome)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("helper subprocess failed: %v\noutput:\n%s", err, out.String())
	}

	var gotHome string
	found := false
	for _, line := range strings.Split(out.String(), "\n") {
		if after, ok := strings.CutPrefix(line, homeHelperResultPrefix); ok {
			gotHome = after
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("helper subprocess output missing %q line:\n%s", homeHelperResultPrefix, out.String())
	}
	if filepath.Clean(gotHome) != filepath.Clean(passwdHome) {
		t.Fatalf("subprocess HOME after init() = %q, want real passwd home %q (deliberately-wrong ambient HOME was %q)", gotHome, passwdHome, wrongHome)
	}
}
