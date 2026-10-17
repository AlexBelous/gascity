package observationattempt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRetentionConcurrentCapAndMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	if _, err := NewStore(dir, 1024); err != nil {
		t.Fatal(err)
	}
	type preserved struct {
		body []byte
		info os.FileInfo
	}
	old := make(map[string]preserved)
	for slot := 0; slot < 8; slot++ {
		names := []string{fmt.Sprintf("failed-attempt-%d.json", slot)}
		if slot == 0 || slot == 3 || slot == 7 {
			names = append(names, names[0]+".candidate.json")
		}
		for _, name := range names {
			path := filepath.Join(dir, name)
			body := []byte(fmt.Sprintf("legacy evidence %s\n", name))
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			old[path] = preserved{body, info}
		}
	}
	var n atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var paths sync.Map
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := NewStore(dir, 1024)
			if err != nil {
				t.Error(err)
				return
			}
			r, err := s.Save(context.Background(), map[string]string{"schema": "typed-test"})
			if err == nil {
				n.Add(1)
				if _, occupied := paths.LoadOrStore(r.Path, true); occupied {
					t.Error("two Store instances reused a slot")
				}
				if _, occupied := old[r.Path]; occupied {
					t.Error("old slot reused")
				}
				b, e := os.ReadFile(r.Path)
				if e != nil {
					t.Error(e)
					return
				}
				sum := sha256.Sum256(b)
				if !r.Complete || r.SHA256 != hex.EncodeToString(sum[:]) || int64(len(b)) != r.Bytes {
					t.Error("receipt mismatch")
				}
				info, e := os.Stat(r.Path)
				if e != nil || info.Mode().Perm() != 0o600 {
					t.Error("file mode")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 8 || len(entries) != 19 {
		t.Fatalf("cap success=%d files=%d", n.Load(), len(entries))
	}
	store, err := NewStore(dir, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := store.Save(context.Background(), "seventeenth"); err == nil || receipt.Complete || receipt.Path != "" {
		t.Fatal("seventeenth slot accepted")
	}
	for path, want := range old {
		// Stat before reading: the test's read must not alter the atime whose
		// preservation this exact metadata comparison checks.
		got, err := os.Lstat(path)
		if err != nil || !reflect.DeepEqual(want.info.Sys(), got.Sys()) || want.info.Mode() != got.Mode() || want.info.Size() != got.Size() || want.info.ModTime() != got.ModTime() {
			t.Fatalf("legacy metadata changed: %s", path)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != string(want.body) {
			t.Fatalf("legacy bytes changed: %s", path)
		}
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("directory mode")
	}
}

func TestCandidateReadbackLinksImmutableFailedAttempt(t *testing.T) {
	for _, mode := range []string{"valid", "body-changed", "wrong-hash", "wrong-path", "already-recorded", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			store, err := NewStore(filepath.Join(t.TempDir(), "evidence"), 4096)
			if err != nil {
				t.Fatal(err)
			}
			failed, err := store.Save(context.Background(), struct{ Schema string }{"fixture"})
			if err != nil {
				t.Fatal(err)
			}
			h, _ := fixtureHooks()
			first, selected := h.Collect(context.Background()).ID, h.Collect(context.Background()).ID
			envelope := strings.Repeat("b", 64)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "body-changed":
				if err = os.WriteFile(failed.Path, []byte("different body"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "wrong-hash":
				failed.SHA256 = strings.Repeat("0", 64)
			case "wrong-path":
				failed.Path = filepath.Join(t.TempDir(), "foreign.json")
			case "already-recorded":
				if _, err = store.SaveCandidate(ctx, failed, first, selected, envelope); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			receipt, err := store.SaveCandidate(ctx, failed, first, selected, envelope)
			if (err == nil) != (mode == "valid") || receipt.Complete != (mode == "valid") {
				t.Fatal("selection readback result", mode, err)
			}
			if mode != "valid" {
				return
			}
			b, err := os.ReadFile(receipt.Path)
			if err != nil {
				t.Fatal(err)
			}
			var record CandidateRecord
			if err = json.Unmarshal(b, &record); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(b)
			info, err := os.Lstat(receipt.Path)
			firstJSON, _ := json.Marshal(first)
			selectedJSON, _ := json.Marshal(selected)
			gotFirstJSON, _ := json.Marshal(record.FirstIdentity)
			gotSelectedJSON, _ := json.Marshal(record.CandidateIdentity)
			if err != nil || info.Mode().Perm() != 0o600 || record.CandidateAttempt != 2 || record.FailedAttempt != failed || string(firstJSON) != string(gotFirstJSON) || string(selectedJSON) != string(gotSelectedJSON) || record.CandidateTypedEnvelopeSHA256 != envelope || receipt.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("selection link or private readback was lost")
			}
		})
	}
}

func TestRetentionBoundPreservesPartialAndExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "receipts")
	s, err := NewStore(dir, 8)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Save(context.Background(), map[string]string{"payload": "longer than8bytes"})
	if err == nil || r.Complete {
		t.Fatal("oversized artifact accepted")
	}
	b, e := os.ReadFile(r.Path)
	if e != nil || len(b) != 8 {
		t.Fatal("bounded partial not retained")
	}
	r2, err := s.Save(context.Background(), 0)
	if err != nil || r2.Path == r.Path {
		t.Fatal("partial overwritten")
	}
	for slot := 2; slot < 16; slot++ {
		if _, err := s.Save(context.Background(), 0); err != nil {
			t.Fatalf("slot %d unavailable before fixed bound: %v", slot, err)
		}
	}
	if receipt, err := s.Save(context.Background(), 0); err == nil || receipt.Path != "" || receipt.Complete {
		t.Fatal("partial occupied slot was reused beyond fixed bound")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 16 {
		t.Fatal("partial/full fixed bound changed")
	}
	again, e := os.ReadFile(r.Path)
	if e != nil || string(again) != string(b) {
		t.Fatal("history changed")
	}
}

func TestCandidateExpandedFixedSlotBoundary(t *testing.T) {
	for slot := 8; slot <= 16; slot++ {
		t.Run(fmt.Sprint(slot), func(t *testing.T) {
			store, err := NewStore(filepath.Join(t.TempDir(), "evidence"), 4096)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.dir, fmt.Sprintf("failed-attempt-%d.json", slot))
			body := []byte("{\"schema\":\"fixed-slot-fixture\"}\n")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			failed := Receipt{Path: path, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Complete: true}
			h, _ := fixtureHooks()
			first, selected := h.Collect(context.Background()).ID, h.Collect(context.Background()).ID
			receipt, err := store.SaveCandidate(context.Background(), failed, first, selected, strings.Repeat("b", 64))
			if slot == 16 {
				if err == nil || receipt.Complete || receipt.Path != "" {
					t.Fatal("candidate index16 accepted")
				}
				if _, err := os.Lstat(path + ".candidate.json"); !os.IsNotExist(err) {
					t.Fatal("out-of-range candidate artifact created")
				}
				return
			}
			if err != nil || !receipt.Complete || receipt.Path != path+".candidate.json" {
				t.Fatalf("fixed slot %d candidate unavailable: %v", slot, err)
			}
			if _, err := store.SaveCandidate(context.Background(), failed, first, selected, strings.Repeat("b", 64)); err == nil {
				t.Fatal("candidate audit overwritten")
			}
		})
	}
}

func TestRetentionRejectsSymlinkAndPublicDirectory(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(link, 1024); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(target, 1024); err == nil {
		t.Fatal("public dir accepted")
	}
}
