//go:build linux

package proctable

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictLinuxDoesNotHidePermissionFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stat := []byte("42 (agent) S 1 42 13 0 -1 0 0 0 0 0 0 0 0 0 0 0 1 0 98765 0")
	for name, data := range map[string][]byte{"stat": stat, "comm": []byte("agent"), "environ": []byte("GC_SESSION_ID=sid\x00GC_TEMPLATE=worker\x00GC_CITY_PATH=/city\x00GC_RUNTIME_EPOCH=2\x00GC_INSTANCE_TOKEN=secret\x00")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := observeLinuxRoot(root, os.ReadFile)
	if err != nil || len(got) != 1 {
		t.Fatalf("exact fixture: %+v %v", got, err)
	}
	reader := func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "/environ") {
			return nil, fs.ErrPermission
		}
		return os.ReadFile(path)
	}
	if got, err := observeLinuxRoot(root, reader); err == nil || len(got) != 0 {
		t.Fatalf("unreadable process treated as absent: %+v %v", got, err)
	}
}

func TestStrictLinuxFencesPIDReuse(t *testing.T) {
	calls := 0
	reader := func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "/stat") {
			calls++
			start := "98765"
			if calls > 1 {
				start = "98766"
			}
			return []byte("42 (agent) S 1 42 13 0 -1 0 0 0 0 0 0 0 0 0 0 0 1 0 " + start + " 0"), nil
		}
		if strings.HasSuffix(path, "/comm") {
			return []byte("agent"), nil
		}
		return []byte("GC_SESSION_ID=sid\x00"), nil
	}
	if _, err := observeLinuxProcess("/fixture", 42, reader); err == nil {
		t.Fatal("PID reuse spliced identity")
	}
}
