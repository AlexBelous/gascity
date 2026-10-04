package proctable

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
)

// ObservedRoot is a process incarnation read without the legacy scanner's
// best-effort permission fallback. The holder credential is never exported.
type ObservedRoot struct {
	Runtime             runtime.LiveRuntime
	Template            string
	StartIdentity       string
	InstanceTokenSHA256 string
}

// ObserveRoots reads host-wide process environments and start identities.
// A non-nil error means coverage is incomplete even when roots are returned.
// It is read-only and does not change orphan-reaping or admission behavior.
func ObserveRoots() ([]ObservedRoot, error) {
	if err := liveScanGuard(); err != nil {
		return nil, err
	}
	return observeRoots()
}

type observedProcess struct {
	record ProcessRecord
	env    map[string]string
}

func rootsFromObservedProcesses(processes []observedProcess) ([]ObservedRoot, error) {
	byPID := make(map[int]observedProcess, len(processes))
	var errs []error
	for _, p := range processes {
		if p.record.PID <= 0 || p.record.StartTime == "" {
			errs = append(errs, fmt.Errorf("process identity unavailable"))
			continue
		}
		if _, dup := byPID[p.record.PID]; dup {
			errs = append(errs, fmt.Errorf("duplicate process PID %d", p.record.PID))
			continue
		}
		byPID[p.record.PID] = p
	}
	out := []ObservedRoot{}
	for _, p := range processes {
		r := p.record
		sid := p.env["GC_SESSION_ID"]
		if r.PID <= 1 || sid == "" || isInfrastructureCommand(r.Name) {
			continue
		}
		parent, exists := byPID[r.PPID]
		if r.PPID > 1 && !exists {
			errs = append(errs, fmt.Errorf("parent identity unavailable for PID %d", r.PID))
			continue
		}
		if exists && parent.env["GC_SESSION_ID"] == sid && !isInfrastructureCommand(parent.record.Name) {
			continue
		}
		epoch, err := strconv.Atoi(p.env["GC_RUNTIME_EPOCH"])
		city := p.env["GC_CITY_PATH"]
		if city == "" {
			city = p.env["GC_CITY"]
		}
		token := p.env["GC_INSTANCE_TOKEN"]
		if err != nil || epoch < 1 || city == "" || token == "" || p.env["GC_TEMPLATE"] == "" || r.StartTime == "" {
			errs = append(errs, fmt.Errorf("incomplete incarnation identity for PID %d", r.PID))
			continue
		}
		digest := sha256.Sum256([]byte(token))
		out = append(out, ObservedRoot{Runtime: runtime.LiveRuntime{SessionID: sid, City: city, Epoch: epoch, PID: r.PID, PPID: r.PPID, Name: r.Name, ParentIsProviderInfrastructure: exists && isInfrastructureCommand(parent.record.Name)}, Template: p.env["GC_TEMPLATE"], StartIdentity: r.StartTime, InstanceTokenSHA256: hex.EncodeToString(digest[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Runtime.PID < out[j].Runtime.PID })
	return out, errors.Join(errs...)
}

func parseObservedEnvironment(data []byte) (map[string]string, error) {
	if len(data) > 0 && data[len(data)-1] != 0 {
		return nil, fmt.Errorf("unterminated process environment")
	}
	env := map[string]string{}
	for _, entry := range strings.Split(string(data), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("malformed process environment")
		}
		if _, dup := env[key]; dup && strings.HasPrefix(key, "GC_") {
			return nil, fmt.Errorf("ambiguous GC identity key")
		}
		env[key] = value
	}
	return env, nil
}
