//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type (
	remoteWorkJournal       struct{ dir string }
	remoteWorkJournalRecord struct {
		RequestID string              `json:"request_id"`
		Op        string              `json:"op"`
		BeadID    string              `json:"bead_id,omitempty"`
		Digest    string              `json:"digest"`
		Response  *remoteWorkResponse `json:"response,omitempty"`
	}
)

func newRemoteWorkJournal(dir string) *remoteWorkJournal { return &remoteWorkJournal{dir: dir} }
func remoteWorkScopeKey(scope string) string {
	h := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(h[:])
}

func (j *remoteWorkJournal) key(scope string, r remoteWorkRequest) string {
	h := sha256.Sum256([]byte(r.RequestID))
	return remoteWorkScopeKey(scope) + "-" + hex.EncodeToString(h[:])
}

func (j *remoteWorkJournal) canStart(scope string) error {
	entries, e := os.ReadDir(j.dir)
	if e != nil || len(entries) > 4096 {
		return errRemoteWorkDenied
	}
	prefix := remoteWorkScopeKey(scope) + "-"
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".intent") {
			continue
		}
		intent, e := remoteWorkReadJournalRecord(filepath.Join(j.dir, name))
		if e != nil || !remoteWorkHex(intent.Digest, 64) || intent.Response != nil || !remoteWorkID.MatchString(intent.RequestID) {
			return errRemoteWorkDenied
		}
		done, e := remoteWorkReadJournalRecord(filepath.Join(j.dir, strings.TrimSuffix(name, ".intent")+".receipt"))
		old := remoteWorkRequest{RequestID: intent.RequestID, Op: intent.Op, BeadID: intent.BeadID}
		if e != nil || done.Response == nil || done.Digest != intent.Digest || done.RequestID != intent.RequestID || done.Op != intent.Op || done.BeadID != intent.BeadID || validateRemoteWorkResponse(old, *done.Response) != nil {
			return errRemoteWorkDenied
		}
		if j.key(scope, old) != strings.TrimSuffix(name, ".intent") {
			return errRemoteWorkDenied
		}
	}
	return nil
}

func remoteWorkReadJournalRecord(path string) (remoteWorkJournalRecord, error) {
	var r remoteWorkJournalRecord
	f, e := remoteWorkOpenFile(path, syscall.O_RDONLY)
	if e != nil {
		return r, e
	}
	defer func() { _ = f.Close() }()
	raw, e := io.ReadAll(io.LimitReader(f, remoteWorkFrameLimit+1))
	if e != nil || len(raw) > remoteWorkFrameLimit || remoteWorkDecodeStrict(raw, &r) != nil {
		return r, errRemoteWorkDenied
	}
	return r, nil
}

func (j *remoteWorkJournal) secureDir() error {
	st, e := os.Lstat(j.dir)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		return errRemoteWorkDenied
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(raw.Uid) != os.Geteuid() {
		return errRemoteWorkDenied
	}
	return nil
}

func remoteWorkOpenFile(path string, flags int) (*os.File, error) {
	fd, e := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		_ = f.Close()
		return nil, errRemoteWorkDenied
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || raw.Nlink != 1 || int(raw.Uid) != os.Geteuid() {
		_ = f.Close()
		return nil, errRemoteWorkDenied
	}
	return f, nil
}

func (j *remoteWorkJournal) Lock(scope string, _ remoteWorkRequest) (func(), error) {
	if e := j.secureDir(); e != nil {
		return nil, e
	}
	h := sha256.Sum256([]byte(scope))
	p := filepath.Join(j.dir, hex.EncodeToString(h[:])+".lock")
	f, e := remoteWorkOpenFile(p, syscall.O_RDWR|syscall.O_CREAT)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, e
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func (j *remoteWorkJournal) Load(scope string, r remoteWorkRequest) (remoteWorkJournalRecord, bool, error) {
	f, e := remoteWorkOpenFile(filepath.Join(j.dir, j.key(scope, r)+".intent"), syscall.O_RDONLY)
	if errors.Is(e, os.ErrNotExist) {
		if j.canStart(scope) != nil {
			return remoteWorkJournalRecord{}, false, errRemoteWorkDenied
		}
		return remoteWorkJournalRecord{}, false, nil
	}
	if e != nil {
		return remoteWorkJournalRecord{}, false, e
	}
	defer func() { _ = f.Close() }()
	raw, e := io.ReadAll(io.LimitReader(f, remoteWorkFrameLimit+1))
	var rec remoteWorkJournalRecord
	if e != nil || len(raw) > remoteWorkFrameLimit || remoteWorkDecodeStrict(raw, &rec) != nil || rec.Digest != remoteWorkRequestDigest(r) || rec.Response != nil || rec.RequestID != r.RequestID || rec.Op != r.Op || rec.BeadID != r.BeadID {
		return rec, false, errRemoteWorkDenied
	}
	receipt, e := remoteWorkOpenFile(filepath.Join(j.dir, j.key(scope, r)+".receipt"), syscall.O_RDONLY)
	if errors.Is(e, os.ErrNotExist) {
		return rec, true, nil
	}
	if e != nil {
		return rec, false, e
	}
	defer func() { _ = receipt.Close() }()
	raw, e = io.ReadAll(io.LimitReader(receipt, remoteWorkFrameLimit+1))
	var done remoteWorkJournalRecord
	if e != nil || len(raw) > remoteWorkFrameLimit || remoteWorkDecodeStrict(raw, &done) != nil || done.Digest != rec.Digest || done.RequestID != rec.RequestID || done.Op != rec.Op || done.BeadID != rec.BeadID || done.Response == nil || validateRemoteWorkResponse(r, *done.Response) != nil {
		return rec, false, errRemoteWorkDenied
	}
	return done, true, nil
}

func (j *remoteWorkJournal) write(path string, r remoteWorkJournalRecord) error {
	f, e := remoteWorkOpenFile(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(r)
	if len(raw) > remoteWorkFrameLimit {
		e = errRemoteWorkDenied
	}
	if e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	d, e := os.Open(j.dir)
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func (j *remoteWorkJournal) Intent(scope string, r remoteWorkRequest) error {
	if j.canStart(scope) != nil {
		return errRemoteWorkDenied
	}
	return j.write(filepath.Join(j.dir, j.key(scope, r)+".intent"), remoteWorkJournalRecord{Digest: remoteWorkRequestDigest(r), RequestID: r.RequestID, Op: r.Op, BeadID: r.BeadID})
}

func (j *remoteWorkJournal) Finish(scope string, r remoteWorkRequest, out remoteWorkResponse) error {
	if validateRemoteWorkResponse(r, out) != nil {
		return errRemoteWorkDenied
	}
	return j.write(filepath.Join(j.dir, j.key(scope, r)+".receipt"), remoteWorkJournalRecord{Digest: remoteWorkRequestDigest(r), RequestID: r.RequestID, Op: r.Op, BeadID: r.BeadID, Response: &out})
}
