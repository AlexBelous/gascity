//go:build linux

package proctable

import (
	"context"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// No PID1/protected-process dependency: actual SELF procfs numbering is bound
// to getpid and a SELF pidfd's kernel-rendered Pid/NSpid. Permission/omission,
// foreign/nested procfs domains, failed alive checks, and drift remain UNKNOWN.
func readCallerPID(ctx context.Context, pid int) (CallerIncarnation, error) {
	fail := func(stage string, err error) (CallerIncarnation, error) {
		return CallerIncarnation{}, callerFailure(stage, err)
	}
	if ctx == nil || ctx.Err() != nil || pid <= 1 {
		return fail("input", nil)
	}
	proc, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail("proc-open", err)
	}
	defer unix.Close(proc)
	var fs unix.Statfs_t
	if err = unix.Fstatfs(proc, &fs); err != nil {
		return fail("proc-type", err)
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		return fail("proc-type", nil)
	}
	selfPID := os.Getpid()
	selfFD, err := unix.PidfdOpen(selfPID, 0)
	if err != nil {
		return fail("self-pidfd", err)
	}
	defer unix.Close(selfFD)
	alive := func(fd int) bool {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, 0)
		return err == nil && n == 0 && poll[0].Revents == 0
	}
	read := func(base int, name, stage string) ([]byte, error) {
		if ctx.Err() != nil || !alive(selfFD) {
			return nil, callerFailure("deadline-or-self-loss", nil)
		}
		fd, err := unix.Openat(base, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, callerFailure(stage, err)
		}
		file := os.NewFile(uintptr(fd), "[private procfs input]")
		if file == nil {
			unix.Close(fd)
			return nil, callerFailure(stage, nil)
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, callerMaxBytes+1))
		if err != nil || len(data) > callerMaxBytes || ctx.Err() != nil {
			for i := range data {
				data[i] = 0
			}
			return nil, callerFailure(stage, err)
		}
		return data, nil
	}
	link := func(base int, name, stage string) (string, error) {
		b := make([]byte, 128)
		n, err := unix.Readlinkat(base, name, b)
		if err != nil || n == len(b) {
			return "", callerFailure(stage, err)
		}
		return string(b[:n]), nil
	}
	ownNS, err := link(proc, "self/ns/pid", "self-namespace")
	if err != nil {
		return fail("self-namespace", err)
	}
	selfProof := func() (CallerIncarnation, error) {
		st, err := read(proc, "self/stat", "self-stat")
		if err != nil {
			return fail("self-stat", err)
		}
		status, err := read(proc, "self/status", "self-status")
		if err != nil {
			return fail("self-status", err)
		}
		info, err := read(proc, "self/fdinfo/"+strconv.Itoa(selfFD), "self-pidfd-info")
		if err != nil {
			return fail("self-pidfd-info", err)
		}
		ns, err := link(proc, "self/ns/pid", "self-namespace")
		if err != nil {
			return fail("self-namespace", err)
		}
		p, err := callerLinuxSelfDomainProof(selfPID, st, status, info, ns)
		if err != nil || ns != ownNS || !alive(selfFD) {
			return fail("self-domain-proof", err)
		}
		return p, nil
	}
	before, err := selfProof()
	if err != nil {
		return fail("self-domain-proof", err)
	}
	dir, err := unix.Openat(proc, strconv.Itoa(pid), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fail("target-dir", err)
	}
	defer unix.Close(dir)
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fail("target-pidfd", err)
	}
	defer unix.Close(fd)
	targetProof := func() error {
		if !alive(fd) {
			return callerFailure("target-loss", nil)
		}
		info, err := read(proc, "self/fdinfo/"+strconv.Itoa(fd), "target-pidfd-info")
		if err != nil {
			return err
		}
		if callerLinuxPIDFDProof(pid, info) != nil {
			return callerFailure("target-numbering", nil)
		}
		ns, err := link(dir, "ns/pid", "target-namespace")
		if err != nil {
			return err
		}
		if ns != ownNS {
			return callerFailure("target-domain", nil)
		}
		return nil
	}
	if err = targetProof(); err != nil {
		return fail("target-proof", err)
	}
	p, err := callerLinuxCapture(ctx, pid, callerLinuxReaders{
		read: func(name string) ([]byte, error) {
			if !alive(fd) {
				return nil, callerFailure("target-loss", nil)
			}
			switch name {
			case "stat", "status":
				return read(dir, name, "target-identity-read")
			case "environ":
				return read(dir, name, "environment-read")
			}
			return nil, callerFailure("unsupported-input", nil)
		},
		namespace: func() (string, error) {
			if err := targetProof(); err != nil {
				return "", err
			}
			return ownNS, nil
		},
		boot: func() ([]byte, error) { return read(proc, "sys/kernel/random/boot_id", "boot-read") },
	})
	if err != nil {
		return fail("target-capture", err)
	}
	if err = targetProof(); err != nil {
		return fail("target-proof", err)
	}
	after, err := selfProof()
	if err != nil {
		return fail("self-recheck", err)
	}
	if !callerSameIdentity(before, after) || ctx.Err() != nil || !alive(fd) {
		return fail("final-recheck", nil)
	}
	return p, nil
}
