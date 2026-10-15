package observationattempt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// MaxEncodedAttemptBytes bounds the disk artifact, not Encoder's memory buffer.
const MaxEncodedAttemptBytes int64 = 33 * 1024 * 1024

// MaxCandidateRecordBytes bounds the small, immutable candidate link.
const MaxCandidateRecordBytes int64 = 16 * 1024

// CandidateRecord is audit evidence, never a final selection, admission or
// delivery certificate. Its envelope must still pass the owning service's final
// guards. Correlation requires the actual COMPLETE public reply with this hash.
type CandidateRecord struct {
	Schema                       string   `json:"schema"`
	CandidateAttempt             int      `json:"candidate_attempt"`
	FailedAttempt                Receipt  `json:"failed_attempt"`
	FirstIdentity                Identity `json:"first_identity"`
	CandidateIdentity            Identity `json:"candidate_identity"`
	CandidateTypedEnvelopeSHA256 string   `json:"candidate_typed_envelope_sha256"`
}

var errRetention = errors.New("typed observation retention unavailable")

// Store retains at most eight immutable files; it never deletes or rotates.
// The caller must supply a trusted parent directory within its own runtime.
type Store struct {
	dir   string
	limit int64
}

// NewStore verifies an existing private directory or creates its final component.
func NewStore(dir string, limit int64) (*Store, error) {
	if !filepath.IsAbs(dir) || limit < 1 || limit > MaxEncodedAttemptBytes {
		return nil, errRetention
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errRetention
	}
	i, err := os.Lstat(dir)
	if err != nil || !i.IsDir() || i.Mode().Perm() != 0o700 {
		return nil, errRetention
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Getuid()) {
		return nil, errRetention
	}
	return &Store{dir: dir, limit: limit}, nil
}

type limitedHashWriter struct {
	w     io.Writer
	h     hash.Hash
	n     int64
	limit int64
}

func (w *limitedHashWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.n
	if int64(len(p)) > remaining {
		if remaining > 0 {
			n, err := w.Write(p[:remaining])
			if err != nil {
				return n, err
			}
			return n, errRetention
		}
		return 0, errRetention
	}
	n, err := w.w.Write(p)
	_, _ = w.h.Write(p[:n])
	w.n += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// Save durably preserves collected typed JSON at0600. Eight O_EXCL slots bound
// concurrent callers across Store instances. A partial/failed file consumes its
// slot and is retained. Encode may buffer the whole value before the capped write.
func (s *Store) Save(ctx context.Context, value any) (Receipt, error) {
	var receipt Receipt
	if ctx.Err() != nil {
		return receipt, errRetention
	}
	// Recheck metadata on every save; a preexisting symlink is never accepted.
	if _, err := NewStore(s.dir, s.limit); err != nil {
		return receipt, errRetention
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return receipt, errRetention
	}
	defer root.Close() //nolint:errcheck // file and directory durability checked below
	for slot := 0; slot < 8; slot++ {
		name := fmt.Sprintf("failed-attempt-%d.json", slot)
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return receipt, errRetention
		}
		w := limitedHashWriter{w: f, h: sha256.New(), limit: s.limit}
		encodeErr := json.NewEncoder(&w).Encode(value)
		syncErr := f.Sync()
		closeErr := f.Close()
		d, openErr := root.Open(".")
		var dirErr error
		if openErr == nil {
			dirErr = errors.Join(d.Sync(), d.Close())
		}
		receipt = Receipt{Path: filepath.Join(s.dir, name), SHA256: hex.EncodeToString(w.h.Sum(nil)), Bytes: w.n}
		if encodeErr != nil || syncErr != nil || closeErr != nil || openErr != nil || dirErr != nil || ctx.Err() != nil {
			return receipt, errRetention
		}
		receipt.Complete = true
		return receipt, nil
	}
	return receipt, errRetention
}

// SaveCandidate creates exactly one sibling candidate record for a retained slot.
// It re-reads the immutable failed body before linking it. An incomplete record
// is retained but can never authorize returning a successful second attempt.
func (s *Store) SaveCandidate(ctx context.Context, failed Receipt, first, selected Identity, envelopeSHA string) (Receipt, error) {
	var receipt Receipt
	if ctx.Err() != nil || !failed.Complete || !digest(failed.SHA256) || !digest(envelopeSHA) || failed.Bytes < 1 || failed.Bytes > s.limit || filepath.Dir(failed.Path) != s.dir {
		return receipt, errRetention
	}
	name, owned := filepath.Base(failed.Path), false
	for slot := 0; slot < 8; slot++ {
		owned = owned || name == fmt.Sprintf("failed-attempt-%d.json", slot)
	}
	if !owned {
		return receipt, errRetention
	}
	if _, err := NewStore(s.dir, s.limit); err != nil {
		return receipt, errRetention
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return receipt, errRetention
	}
	defer root.Close() //nolint:errcheck // file and directory durability checked below
	i, err := root.Lstat(name)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0o600 || i.Size() != failed.Bytes {
		return receipt, errRetention
	}
	st, ok := i.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return receipt, errRetention
	}
	f, err := root.Open(name)
	if err != nil {
		return receipt, errRetention
	}
	h := sha256.New()
	n, readErr := io.Copy(h, io.LimitReader(f, s.limit+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || n != failed.Bytes || hex.EncodeToString(h.Sum(nil)) != failed.SHA256 || ctx.Err() != nil {
		return receipt, errRetention
	}
	data, err := json.Marshal(CandidateRecord{Schema: "whole-observation-candidate/v1", CandidateAttempt: 2, FailedAttempt: failed, FirstIdentity: first, CandidateIdentity: selected, CandidateTypedEnvelopeSHA256: envelopeSHA})
	if err != nil || int64(len(data)+1) > MaxCandidateRecordBytes {
		return receipt, errRetention
	}
	data = append(data, '\n')
	name += ".candidate.json"
	f, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return receipt, errRetention
	}
	nWritten, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr = f.Close()
	d, openErr := root.Open(".")
	var dirErr error
	if openErr == nil {
		dirErr = errors.Join(d.Sync(), d.Close())
	}
	sum := sha256.Sum256(data[:nWritten])
	receipt = Receipt{Path: filepath.Join(s.dir, name), SHA256: hex.EncodeToString(sum[:]), Bytes: int64(nWritten)}
	if writeErr != nil || nWritten != len(data) || syncErr != nil || closeErr != nil || openErr != nil || dirErr != nil || ctx.Err() != nil {
		return receipt, errRetention
	}
	receipt.Complete = true
	return receipt, nil
}
