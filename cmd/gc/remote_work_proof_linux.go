//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

func remoteWorkRootFile(path string, limit int) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errRemoteWorkDenied
	}
	for part := path; ; part = filepath.Dir(part) {
		st, e := os.Lstat(part)
		if e != nil || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o022 != 0 {
			return nil, errRemoteWorkDenied
		}
		raw, ok := st.Sys().(*syscall.Stat_t)
		if !ok || raw.Uid != 0 {
			return nil, errRemoteWorkDenied
		}
		if part == path {
			if !st.Mode().IsRegular() || raw.Nlink != 1 {
				return nil, errRemoteWorkDenied
			}
		} else if !st.IsDir() {
			return nil, errRemoteWorkDenied
		}
		if part == "/" {
			break
		}
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, errRemoteWorkDenied
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		return nil, errRemoteWorkDenied
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok || raw.Uid != 0 || raw.Nlink != 1 || st.Mode().Perm()&0o022 != 0 {
		return nil, errRemoteWorkDenied
	}
	data, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if e != nil || len(data) > limit {
		return nil, errRemoteWorkDenied
	}
	return data, nil
}

func loadRemoteWorkPolicy() (remoteWorkPolicy, error) {
	var p remoteWorkPolicy
	data, e := remoteWorkRootFile(remoteWorkPolicyPath, remoteWorkFrameLimit)
	if e != nil || remoteWorkDecodeStrict(data, &p) != nil {
		return p, errRemoteWorkDenied
	}
	if p.SchemaVersion != 1 || p.CityUID <= 0 || !remoteWorkHex(p.HelperSHA, 64) || !remoteWorkHex(p.LeaseSHA, 64) || !filepath.IsAbs(p.HelperPath) || !filepath.IsAbs(p.LeaseCommand) || !filepath.IsAbs(p.JournalDir) || len(p.Profiles) == 0 || len(p.Profiles) > 16 {
		return p, errRemoteWorkDenied
	}
	helperArgs := 0
	for _, arg := range p.ParentArgv {
		if arg == p.HelperPath {
			helperArgs++
		}
	}
	if helperArgs != 1 {
		return p, errRemoteWorkDenied
	}
	seen := map[string]bool{}
	for _, v := range p.Profiles {
		if !remoteWorkID.MatchString(v.ID) || seen[v.ID] || !filepath.IsAbs(v.CityPath) || v.Template == "" || v.ModelUID <= 0 || v.ModelUID == p.CityUID || v.RuntimeProvider == "" || v.RuntimeTarget == "" || v.ResourceDomain == "" || v.PolicyDigest == "" || !remoteWorkHex(v.SourceSHA, 40) || !remoteWorkHex(v.ManifestSHA, 64) {
			return p, errRemoteWorkDenied
		}
		seen[v.ID] = true
	}
	return p, nil
}

func remoteWorkProcParent(pid int) (remoteWorkParent, error) {
	var a remoteWorkParent
	a.PID = pid
	a.ChildUID = os.Geteuid()
	status, e := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if e != nil {
		return a, errRemoteWorkDenied
	}
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(status))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) > 0 && f[0] == "Uid:" {
			if len(f) != 5 {
				return a, errRemoteWorkDenied
			}
			for _, s := range f[1:] {
				if s != "0" {
					return a, errRemoteWorkDenied
				}
			}
			found = true
			a.UID = 0
		}
	}
	if !found || scanner.Err() != nil {
		return a, errRemoteWorkDenied
	}
	stat, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return a, errRemoteWorkDenied
	}
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return a, errRemoteWorkDenied
	}
	f := strings.Fields(string(stat[i+1:]))
	if len(f) < 20 {
		return a, errRemoteWorkDenied
	}
	a.Start = f[19]
	cmd, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if e != nil || len(cmd) == 0 || len(cmd) > 8192 || cmd[len(cmd)-1] != 0 {
		return a, errRemoteWorkDenied
	}
	a.Argv = strings.Split(string(cmd[:len(cmd)-1]), "\x00")
	return a, nil
}

func acquireRemoteWorkFrame(fd int, p remoteWorkPolicy) (remoteWorkFrame, error) {
	var zero remoteWorkFrame
	if fd != 3 || os.Getuid() != p.CityUID || os.Geteuid() != p.CityUID {
		return zero, errRemoteWorkDenied
	}
	pid := os.Getppid()
	a, e := remoteWorkProcParent(pid)
	if e != nil {
		return zero, e
	}
	helper, e := remoteWorkRootFile(p.HelperPath, 1<<20)
	if e != nil {
		return zero, e
	}
	h := sha256.Sum256(helper)
	a.HelperSHA = hex.EncodeToString(h[:])
	a.HelperSafe = true
	var st syscall.Stat_t
	if e = syscall.Fstat(fd, &st); e != nil {
		return zero, errRemoteWorkDenied
	}
	a.PipeRoot = st.Mode&syscall.S_IFMT == syscall.S_IFIFO && st.Uid == 0
	target, e := os.Readlink("/proc/self/fd/3")
	if e != nil {
		return zero, errRemoteWorkDenied
	}
	a.PipeAnonymous = strings.HasPrefix(target, "pipe:[") && strings.HasSuffix(target, "]")
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	if errno != 0 {
		return zero, errRemoteWorkDenied
	}
	a.PipeReadOnly = int(flags)&syscall.O_ACCMODE == syscall.O_RDONLY
	if verifyRemoteWorkParent(p, a) != nil {
		return zero, errRemoteWorkDenied
	}
	if e = syscall.SetNonblock(fd, true); e != nil {
		return zero, errRemoteWorkDenied
	}
	f := os.NewFile(uintptr(fd), "root proof")
	defer func() { _ = f.Close() }()
	if e = f.SetReadDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return zero, errRemoteWorkDenied
	}
	frame, e := decodeRemoteWorkFrame(f)
	if e != nil {
		return zero, e
	}
	after, e := remoteWorkProcParent(pid)
	if e != nil || os.Getppid() != pid || after.Start != a.Start || !reflect.DeepEqual(after.Argv, a.Argv) {
		return zero, errRemoteWorkDenied
	}
	return frame, nil
}

func remoteWorkSelectProfile(p remoteWorkPolicy, f remoteWorkFrame) (remoteWorkProfile, error) {
	var out remoteWorkProfile
	for _, v := range p.Profiles {
		if v.ID == f.ProfileID {
			out = v
			break
		}
	}
	id := f.Identity
	o := f.Observe
	if out.ID == "" || !remoteWorkID.MatchString(id.SID) || id.Template != out.Template || id.Handle == "" || !validBDChildNumber(id.Generation) || !remoteWorkHex(id.InstanceToken, 32) || !remoteWorkHex(id.Attempt, 32) || o.Phase != "entered" || o.ReceiptID == "" || !validBDChildNumber(o.Revision) || o.BootID == "" || o.Unit == "" || !strings.HasPrefix(o.Cgroup, "/") || o.InvocationID == "" || o.ModelUID != out.ModelUID || o.PID <= 1 || !validBDChildNumber(o.Start) || o.SourceSHA != out.SourceSHA || o.ManifestSHA != out.ManifestSHA {
		return out, errRemoteWorkDenied
	}
	age := time.Since(o.ObservedAt)
	if age < -2*time.Second || age > 20*time.Second {
		return out, errRemoteWorkDenied
	}
	return out, nil
}

func receiveRemoteWork(fd int, stdout io.Writer) error {
	p, e := loadRemoteWorkPolicy()
	if e != nil {
		return e
	}
	frame, e := acquireRemoteWorkFrame(fd, p)
	if e != nil {
		return e
	}
	profile, e := remoteWorkSelectProfile(p, frame)
	if e != nil {
		return e
	}
	ops, e := newRemoteWorkNative(p, profile, frame)
	if e != nil {
		return e
	}
	// This environment is explicit delegated session identity, not local-process
	// ancestry. It never enters ordinary gc bd's human/no-runtime branch.
	env := map[string]string{"GC_CITY_PATH": profile.CityPath, "GC_SESSION_ID": frame.Identity.SID, "GC_SESSION_NAME": frame.Identity.Handle, "GC_TEMPLATE": frame.Identity.Template, "GC_RUNTIME_EPOCH": frame.Identity.Generation, "GC_INSTANCE_TOKEN": frame.Identity.InstanceToken, "BEADS_HOLDER_TOKEN": frame.Identity.InstanceToken, "BEADS_ACTOR": ops.assignee, "GC_AGENT": ops.assignee, "GC_HOOK_CLAIM_WINDOW": "10s"}
	for k, v := range env {
		if e = os.Setenv(k, v); e != nil {
			return errRemoteWorkDenied
		}
	}
	// Ambient route/backend overrides are not caller policy. The root intake
	// must launch with a fixed environment; refuse them instead of sanitizing
	// identity to obtain an unowned bd lane.
	for _, s := range os.Environ() {
		k, _, _ := strings.Cut(s, "=")
		if strings.HasPrefix(k, "BEADS_") && k != "BEADS_ACTOR" && k != "BEADS_HOLDER_TOKEN" {
			return errRemoteWorkDenied
		}
		if strings.HasPrefix(k, "GC_") {
			if _, ok := env[k]; !ok {
				return errRemoteWorkDenied
			}
		}
	}
	scope := profile.ID + "/" + frame.Identity.SID + "/" + frame.Identity.Generation + "/" + frame.Identity.Attempt
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	out, e := runRemoteWork(ctx, scope, frame.Request, newRemoteWorkJournal(p.JournalDir), ops)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return errRemoteWorkDenied
	}
	raw = append(raw, '\n')
	_, e = stdout.Write(raw)
	return e
}
