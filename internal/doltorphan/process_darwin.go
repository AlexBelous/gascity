//go:build darwin

package doltorphan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type darwinProcessIdentity struct {
	ppid int
	uid  uint32
	name string
}

// snapshotProcesses uses Darwin's native process table and KERN_PROCARGS2
// interfaces so candidate paths containing spaces retain exact argv
// boundaries. Only current-user dolt sql-server ancestry can reference the
// current user's temporary test stores; lsof remains the independent guard
// against holders owned by other users.
func snapshotProcesses() ([]Process, error) {
	kinfo, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("read Darwin process table: %w", err)
	}

	currentUID := uint32(os.Geteuid())
	identities := make(map[int]darwinProcessIdentity, len(kinfo))
	var doltPIDs []int
	for i := range kinfo {
		pid := int(kinfo[i].Proc.P_pid)
		if pid <= 0 {
			continue
		}
		identity := darwinProcessIdentity{
			ppid: int(kinfo[i].Eproc.Ppid),
			uid:  kinfo[i].Eproc.Ucred.Uid,
			name: filepath.Base(darwinCString(kinfo[i].Proc.P_comm[:])),
		}
		identities[pid] = identity
		if identity.uid == currentUID && strings.EqualFold(identity.name, "dolt") {
			doltPIDs = append(doltPIDs, pid)
		}
	}

	wanted := make(map[int]bool)
	for _, doltPID := range doltPIDs {
		for pid := doltPID; pid > 1; {
			identity, ok := identities[pid]
			if !ok || identity.uid != currentUID {
				break
			}
			wanted[pid] = true
			if identity.ppid <= 1 || identity.ppid == pid {
				break
			}
			pid = identity.ppid
		}
	}

	pids := make([]int, 0, len(wanted))
	for pid := range wanted {
		pids = append(pids, pid)
	}
	sort.Ints(pids)

	processes := make([]Process, 0, len(pids))
	for _, pid := range pids {
		rawArgs, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			return nil, fmt.Errorf("read Darwin process %d argv: %w", pid, err)
		}
		argv, err := parseDarwinProcArgs(rawArgs)
		if err != nil {
			return nil, fmt.Errorf("parse Darwin process %d argv: %w", pid, err)
		}
		processes = append(processes, Process{
			PID:  pid,
			PPID: identities[pid].ppid,
			Argv: argv,
		})
	}
	return processes, nil
}

func darwinCString(raw []byte) string {
	buf := make([]byte, 0, len(raw))
	for _, value := range raw {
		if value == 0 {
			break
		}
		buf = append(buf, value)
	}
	return string(buf)
}
