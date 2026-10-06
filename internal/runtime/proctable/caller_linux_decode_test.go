package proctable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func callerLinuxStatFixture(start, ppid, state string) []byte {
	fields := []string{state, ppid, "777"}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, start)
	return []byte("777 (fixture ) name) " + strings.Join(fields, " ") + "\n")
}

func TestCallerLinuxStatStrict(t *testing.T) {
	valid := callerLinuxStatFixture("12345", "10", "S")
	p, err := callerLinuxStat(valid, 777)
	if err != nil || p.Start != "12345" || p.PPID != 10 || p.PGID != 777 {
		t.Fatal("valid stat rejected")
	}
	for i, data := range [][]byte{nil, []byte("777 (x) S"), callerLinuxStatFixture("0", "10", "S"), callerLinuxStatFixture("+12", "10", "S"), callerLinuxStatFixture("12", "-1", "S"), callerLinuxStatFixture("12", "10", "Z"), []byte(strings.Replace(string(valid), "777 (", "778 (", 1))} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := callerLinuxStat(data, 777); err == nil {
				t.Fatal("malformed stat accepted")
			}
		})
	}
}

func TestCallerLinuxStateAllowset(t *testing.T) {
	for _, tc := range []struct {
		state    string
		accepted bool
	}{
		{"R", true},
		{"S", true},
		{"D", true},
		{"T", true},
		{"t", true},
		{"I", true},
		{"P", true},
		{"Z", false},
		{"X", false},
		{"W", false},
		{"", false},
		{"SS", false},
		{"é", false},
		{"\x80", false},
	} {
		t.Run(fmt.Sprintf("%x", tc.state), func(t *testing.T) {
			_, err := callerLinuxStat(callerLinuxStatFixture("12345", "10", tc.state), 777)
			if (err == nil) != tc.accepted {
				t.Fatal("kernel state allowset changed")
			}
		})
	}
}

func TestCallerLinuxUIDStrict(t *testing.T) {
	u, err := callerLinuxUID([]byte("Name:\tfixture\nUid:\t1000 1000 1000 1001\n"))
	if err != nil || !u.HasFileSystem || u.FileSystem != 1001 {
		t.Fatal("four native UIDs not retained")
	}
	for i, data := range []string{"", "Uid: 1 1 1\n", "Uid: 1 1 1 1 1\n", "Uid: 1 1 1 1\nUid: 1 1 1 1\n", "Uid: +1 1 1 1\n", "Uid: 1 -1 1 1\n", "Uid: 1 1 1 4294967296\n", "Uid: 01 1 1 1\n"} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := callerLinuxUID([]byte(data)); err == nil {
				t.Fatal("ambiguous UID accepted")
			}
		})
	}
}

func TestCallerLinuxCaptureInjected(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "stat-drift", "uid-drift", "env-drift", "namespace-drift", "boot-drift", "env-denied", "no-env", "duplicate", "bad-boot", "bad-namespace", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			stats, statuses, envs, namespaces, boots := 0, 0, 0, 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			r := callerLinuxReaders{
				read: func(name string) ([]byte, error) {
					switch name {
					case "stat":
						stats++
						if mode == "missing" {
							return nil, errors.New("fixture denied")
						}
						start := "12345"
						if stats == 2 && mode == "stat-drift" {
							start = "12346"
						}
						return callerLinuxStatFixture(start, "10", "S"), nil
					case "status":
						statuses++
						uid := "1000"
						if statuses == 2 && mode == "uid-drift" {
							uid = "1001"
						}
						return []byte("Uid: " + uid + " 1000 1000 1000\n"), nil
					case "environ":
						envs++
						if mode == "env-denied" {
							return nil, errors.New("fixture denied")
						}
						if mode == "no-env" {
							return nil, nil
						}
						if mode == "duplicate" {
							return []byte("GC_SESSION_ID=x\x00GC_SESSION_ID=x\x00"), nil
						}
						value := "fixture-only"
						if envs == 2 && mode == "env-drift" {
							value = "changed"
						}
						return []byte("GC_SESSION_ID=fixture\x00GC_INSTANCE_TOKEN=" + value + "\x00"), nil
					}
					return nil, errors.New("unexpected fixture read")
				},
				namespace: func() (string, error) {
					namespaces++
					if mode == "bad-namespace" {
						return "pid:[unknown]", nil
					}
					if namespaces > 1 && mode == "namespace-drift" {
						return "pid:[124]", nil
					}
					return "pid:[123]", nil
				},
				boot: func() ([]byte, error) {
					boots++
					if mode == "bad-boot" {
						return []byte("bad"), nil
					}
					if boots > 1 && mode == "boot-drift" {
						return []byte("11111111-1111-1111-1111-111111111111\n"), nil
					}
					return []byte("00000000-0000-0000-0000-000000000001\n"), nil
				},
			}
			p, err := callerLinuxCapture(ctx, 777, r)
			if (err == nil) != (mode == "valid") {
				t.Fatal("incorrect Linux capture verdict")
			}
			if mode == "valid" && (p.Platform != "linux" || !p.UIDs.HasFileSystem || p.Start != "12345" || len(p.Environment.Entries()) != 2) {
				t.Fatal("Linux capture incomplete")
			}
		})
	}
}

func TestCallerLinuxSelfDomainProof(t *testing.T) {
	selfStat := callerLinuxStatFixture("12345", "10", "S")
	status := []byte("NSpid:\t777\n")
	info := []byte("pos:\t0\nflags:\t02000002\nmnt_id:\t4\nino:\t123\nPid:\t777\nNSpid:\t777\n")
	for _, mode := range []string{"valid", "bad-self-pid", "nested-status", "nested-fd", "wrong-fd-pid", "missing-status", "duplicate-fd-pid", "bad-ns"} {
		t.Run(mode, func(t *testing.T) {
			st, ss, fd := append([]byte(nil), selfStat...), append([]byte(nil), status...), append([]byte(nil), info...)
			ns := "pid:[123]"
			switch mode {
			case "bad-self-pid":
				st = []byte(strings.Replace(string(st), "777 (", "778 (", 1))
			case "nested-status":
				ss = []byte("NSpid: 1000 777\n")
			case "nested-fd":
				fd = []byte("Pid: 777\nNSpid: 1000 777\n")
			case "wrong-fd-pid":
				fd = []byte("Pid: 778\nNSpid: 777\n")
			case "missing-status":
				ss = nil
			case "duplicate-fd-pid":
				fd = append(fd, []byte("Pid: 777\n")...)
			case "bad-ns":
				ns = "pid:[unknown]"
			}
			_, err := callerLinuxSelfDomainProof(777, st, ss, fd, ns)
			if (err == nil) != (mode == "valid") {
				t.Fatal("SELF procfs/pidfd domain proof verdict wrong")
			}
		})
	}
}
