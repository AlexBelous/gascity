//go:build darwin

package proctable

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strconv"

	"golang.org/x/sys/unix"
)

type callerDarwinReaders struct {
	info func(int) (*unix.KinfoProc, error)
	args func(int) ([]byte, error)
	boot func() ([]byte, error)
}

func readCallerPID(ctx context.Context, pid int) (CallerIncarnation, error) {
	return callerDarwinCapture(ctx, pid, callerDarwinReaders{
		info: func(pid int) (*unix.KinfoProc, error) { return unix.SysctlKinfoProc("kern.proc.pid", pid) },
		args: func(pid int) ([]byte, error) { return unix.SysctlRaw("kern.procargs2", pid) },
		boot: func() ([]byte, error) { return unix.SysctlRaw("kern.boottime") },
	})
}

func callerDarwinIdentity(info *unix.KinfoProc, pid int) (CallerIncarnation, error) {
	if info == nil || int(info.Proc.P_pid) != pid || pid <= 1 || info.Proc.P_stat == 5 || info.Proc.P_stat == 0 ||
		info.Eproc.Ppid < 0 || info.Eproc.Pgid <= 0 || info.Proc.P_starttime.Sec <= 0 ||
		info.Proc.P_starttime.Usec < 0 || info.Proc.P_starttime.Usec >= 1_000_000 {
		return CallerIncarnation{}, errCallerUnknown
	}
	sec, usec := info.Proc.P_starttime.Sec, int64(info.Proc.P_starttime.Usec)
	if sec > ((1<<63-1)-usec*1000)/1_000_000_000 {
		return CallerIncarnation{}, errCallerUnknown
	}
	return CallerIncarnation{
		PID: pid, PPID: int(info.Eproc.Ppid), PGID: int(info.Eproc.Pgid),
		Start: strconv.FormatInt(sec*1_000_000_000+usec*1000, 10), Platform: "darwin",
		UIDs: CallerUIDs{Real: info.Eproc.Pcred.P_ruid, Effective: info.Eproc.Ucred.Uid, Saved: info.Eproc.Pcred.P_svuid},
	}, nil
}

func callerDarwinCapture(ctx context.Context, pid int, r callerDarwinReaders) (CallerIncarnation, error) {
	if ctx == nil || ctx.Err() != nil || pid <= 1 || r.info == nil || r.args == nil || r.boot == nil {
		return CallerIncarnation{}, errCallerUnknown
	}
	boot, err := r.boot()
	if err != nil || len(boot) == 0 || len(boot) > 4096 {
		return CallerIncarnation{}, errCallerUnknown
	}
	var identity CallerIncarnation
	var env CallerEnvironment
	for pass := 0; pass < 2; pass++ {
		if ctx.Err() != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		first, err := r.info(pid)
		if err != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		before, err := callerDarwinIdentity(first, pid)
		if err != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		raw, err := r.args(pid)
		if err != nil || ctx.Err() != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		captured, parseErr := callerDarwinProcargs(raw)
		// Decoder retains private copies; raw OS buffers do not enter diagnostics.
		for i := range raw {
			raw[i] = 0
		}
		if parseErr != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		last, err := r.info(pid)
		if err != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		after, err := callerDarwinIdentity(last, pid)
		if err != nil || !callerSameIdentity(before, after) {
			return CallerIncarnation{}, errCallerUnknown
		}
		if pass == 0 {
			identity = before
			env = captured
		} else if !callerSameIdentity(identity, before) || !reflect.DeepEqual(env.entries, captured.entries) {
			return CallerIncarnation{}, errCallerUnknown
		}
	}
	again, err := r.boot()
	if err != nil || ctx.Err() != nil || !reflect.DeepEqual(boot, again) {
		return CallerIncarnation{}, errCallerUnknown
	}
	digest := sha256.Sum256(boot)
	identity.Domain = fmt.Sprintf("darwin-boot:%x", digest)
	identity.Environment = env
	return identity, nil
}
