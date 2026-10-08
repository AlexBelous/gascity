package proctable

import (
	"bytes"
	"context"
	"reflect"
	"strconv"
	"strings"
)

// Linux decoding is platform-neutral so synthetic kernel records are exercised
// on the author host. The OS adapter supplies only private, bounded procfs reads.
type callerLinuxReaders struct {
	read      func(string) ([]byte, error)
	namespace func() (string, error)
	boot      func() ([]byte, error)
}

func callerDecimal(s string, bits int) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errCallerUnknown
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errCallerUnknown
		}
	}
	n, err := strconv.ParseUint(s, 10, bits)
	if err != nil {
		return 0, errCallerUnknown
	}
	return n, nil
}

func callerLinuxStat(data []byte, pid int) (CallerIncarnation, error) {
	if pid <= 1 || len(data) == 0 || len(data) > callerMaxBytes || bytes.IndexByte(data, 0) >= 0 {
		return CallerIncarnation{}, errCallerUnknown
	}
	text := string(data)
	open := strings.Index(text, " (")
	closing := strings.LastIndex(text, ") ")
	if open <= 0 || closing <= open+1 {
		return CallerIncarnation{}, errCallerUnknown
	}
	actual, err := callerDecimal(text[:open], 32)
	if err != nil || actual != uint64(pid) {
		return CallerIncarnation{}, errCallerUnknown
	}
	f := strings.Fields(text[closing+2:])
	if len(f) < 20 || len(f[0]) != 1 || !strings.ContainsRune("RSDTtIP", rune(f[0][0])) {
		return CallerIncarnation{}, errCallerUnknown
	}
	ppid, e1 := callerDecimal(f[1], 32)
	pgid, e2 := callerDecimal(f[2], 32)
	start, e3 := callerDecimal(f[19], 64)
	if e1 != nil || e2 != nil || e3 != nil || pgid == 0 || start == 0 {
		return CallerIncarnation{}, errCallerUnknown
	}
	return CallerIncarnation{PID: pid, PPID: int(ppid), PGID: int(pgid), Start: f[19], Platform: "linux"}, nil
}

func callerLinuxUID(data []byte) (CallerUIDs, error) {
	if len(data) == 0 || len(data) > callerMaxBytes || bytes.IndexByte(data, 0) >= 0 {
		return CallerUIDs{}, errCallerUnknown
	}
	found := false
	var out CallerUIDs
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		if found {
			return CallerUIDs{}, errCallerUnknown
		}
		found = true
		fields := strings.Fields(line[4:])
		if len(fields) != 4 {
			return CallerUIDs{}, errCallerUnknown
		}
		var values [4]uint32
		for i, f := range fields {
			n, err := callerDecimal(f, 32)
			if err != nil {
				return CallerUIDs{}, errCallerUnknown
			}
			values[i] = uint32(n)
		}
		out = CallerUIDs{Real: values[0], Effective: values[1], Saved: values[2], FileSystem: values[3], HasFileSystem: true}
	}
	if !found {
		return CallerUIDs{}, errCallerUnknown
	}
	return out, nil
}

func callerLinuxDomain(boot []byte, ns string) (string, error) {
	// Require the exact kernel UUID line and namespace-link grammar. No host
	// names, process roles, or inferred namespaces enter incarnation evidence.
	if len(boot) != 37 || boot[36] != '\n' || !strings.HasPrefix(ns, "pid:[") || !strings.HasSuffix(ns, "]") {
		return "", errCallerUnknown
	}
	for i, b := range boot[:36] {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return "", errCallerUnknown
			}
		} else if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return "", errCallerUnknown
		}
	}
	inode, err := callerDecimal(ns[5:len(ns)-1], 64)
	if err != nil || inode == 0 {
		return "", errCallerUnknown
	}
	return "linux-boot:" + string(boot[:36]) + "/" + ns, nil
}

func callerLinuxCapture(ctx context.Context, pid int, r callerLinuxReaders) (CallerIncarnation, error) {
	if ctx == nil || ctx.Err() != nil || pid <= 1 || r.read == nil || r.namespace == nil || r.boot == nil {
		return CallerIncarnation{}, errCallerUnknown
	}
	boot, err := r.boot()
	if err != nil {
		return CallerIncarnation{}, callerFailure("capture-read", err)
	}
	ns, err := r.namespace()
	if err != nil {
		return CallerIncarnation{}, callerFailure("capture-read", err)
	}
	domain, err := callerLinuxDomain(boot, ns)
	if err != nil {
		return CallerIncarnation{}, callerFailure("capture-read", err)
	}
	var identity CallerIncarnation
	var env CallerEnvironment
	for pass := 0; pass < 2; pass++ {
		if ctx.Err() != nil {
			return CallerIncarnation{}, errCallerUnknown
		}
		stat, err := r.read("stat")
		if err != nil {
			return CallerIncarnation{}, callerFailure("capture-validation", err)
		}
		before, err := callerLinuxStat(stat, pid)
		if err != nil {
			return CallerIncarnation{}, callerFailure("capture-validation", err)
		}
		status, err := r.read("status")
		if err != nil {
			return CallerIncarnation{}, callerFailure("capture-validation", err)
		}
		before.UIDs, err = callerLinuxUID(status)
		if err != nil {
			return CallerIncarnation{}, callerFailure("capture-validation", err)
		}
		before.Domain = domain
		raw, readErr := r.read("environ")
		captured, parseErr := callerStrictNUL(raw)
		for i := range raw {
			raw[i] = 0
		}
		if readErr != nil {
			return CallerIncarnation{}, callerFailure("environment-read", readErr)
		}
		if parseErr != nil {
			return CallerIncarnation{}, callerFailure("environment-decode", parseErr)
		}
		if ctx.Err() != nil {
			return CallerIncarnation{}, callerFailure("deadline", nil)
		}
		status, err = r.read("status")
		if err != nil {
			return CallerIncarnation{}, callerFailure("capture-validation", err)
		}
		uids, err := callerLinuxUID(status)
		if err != nil || uids != before.UIDs {
			return CallerIncarnation{}, errCallerUnknown
		}
		stat, err = r.read("stat")
		if err != nil {
			return CallerIncarnation{}, callerFailure("capture-validation", err)
		}
		after, err := callerLinuxStat(stat, pid)
		after.UIDs = uids
		after.Domain = domain
		if err != nil || !callerSameIdentity(before, after) {
			return CallerIncarnation{}, errCallerUnknown
		}
		nextNS, err := r.namespace()
		if err != nil || nextNS != ns {
			return CallerIncarnation{}, callerFailure("namespace-recheck", err)
		}
		if pass == 0 {
			identity = before
			env = captured
		} else if !callerSameIdentity(identity, before) || !reflect.DeepEqual(env.entries, captured.entries) {
			return CallerIncarnation{}, errCallerUnknown
		}
	}
	again, err := r.boot()
	if err != nil || ctx.Err() != nil || !bytes.Equal(boot, again) {
		return CallerIncarnation{}, callerFailure("boot-recheck", err)
	}
	env.complete = true
	identity.Environment = env
	return identity, nil
}

func callerLinuxPIDField(data []byte, key string, pid int) error {
	if pid <= 1 || len(data) == 0 || len(data) > 65536 || bytes.IndexByte(data, 0) >= 0 {
		return errCallerUnknown
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, key+":") {
			continue
		}
		count++
		fields := strings.Fields(line[len(key)+1:])
		if len(fields) != 1 {
			return errCallerUnknown
		}
		n, err := callerDecimal(fields[0], 32)
		if err != nil || n != uint64(pid) {
			return errCallerUnknown
		}
	}
	if count != 1 {
		return errCallerUnknown
	}
	return nil
}

func callerLinuxPIDFDProof(pid int, info []byte) error {
	if callerLinuxPIDField(info, "Pid", pid) != nil || callerLinuxPIDField(info, "NSpid", pid) != nil {
		return errCallerUnknown
	}
	return nil
}

func callerLinuxSelfDomainProof(pid int, stat, status, fdinfo []byte, ns string) (CallerIncarnation, error) {
	p, err := callerLinuxStat(stat, pid)
	if err != nil || callerLinuxPIDField(status, "NSpid", pid) != nil || callerLinuxPIDFDProof(pid, fdinfo) != nil {
		return CallerIncarnation{}, errCallerUnknown
	}
	if !strings.HasPrefix(ns, "pid:[") || !strings.HasSuffix(ns, "]") {
		return CallerIncarnation{}, errCallerUnknown
	}
	n, err := callerDecimal(ns[5:len(ns)-1], 64)
	if err != nil || n == 0 {
		return CallerIncarnation{}, errCallerUnknown
	}
	return p, nil
}
