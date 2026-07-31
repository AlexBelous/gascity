package tmuxorphan

import (
	"errors"
	"os"
)

// SocketExists reports whether path is still present on disk. It is the
// literal "provably absent from disk" check the ga-026hrg exit contract
// calls for: an Lstat that neither follows nor requires a live listener on
// the far end, so a socket file left behind by a crashed server still
// counts as present (Scan will skip it, matching AGENTS.md's directive to
// treat anything short of a provable absence as out of bounds for reaping).
func SocketExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
