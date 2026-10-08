package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// These error-bearing seams compose the Linux persisted/provider/caller
// acquisition adapters. Ambient ENV or a caller-supplied verified switch can
// never construct authority.
type bdChildOperation uint8

const (
	bdChildUnknown bdChildOperation = iota
	bdChildSessionTool
	bdChildInfrastructure
)

type (
	bdChildTuple  struct{ sid, template, epoch, token string }
	bdChildRecord struct {
		tuple                                   bdChildTuple
		city, provider, handle, revision, state string
		closed, resetPending                    bool
	}
)

type bdChildProvider struct {
	tuple                                        bdChildTuple
	city, provider, handle, namespace, rootStart string
	rootPID                                      int
	uid                                          [4]uint32
}
type bdChildProcess struct {
	pid, ppid              int
	start, namespace, city string
	uid                    [4]uint32
	env                    []string
}
type (
	bdChildCaller  struct{ processes []bdChildProcess }
	bdChildReaders struct {
		record   func(context.Context, string) (bdChildRecord, error)
		provider func(context.Context, bdChildRecord) ([]bdChildProvider, error)
		caller   func(context.Context) (bdChildCaller, error)
	}
)

type bdChildTicket struct {
	valid    bool
	city     bdChildCityFields
	record   bdChildRecord
	provider bdChildProvider
	caller   bdChildCaller
}
type bdChildCityFields struct {
	path, alias       string
	hasPath, hasAlias bool
}

func (city bdChildCityFields) effective() string {
	if city.hasPath {
		return city.path
	}
	return city.alias
}

type (
	bdChildEnvironment struct{ entries []string }
	bdChildSpawn       func(context.Context, bdChildEnvironment) error
)

// Credentials never enter error text, String/GoString/formatting, argv or logs.
func (bdChildTuple) Format(s fmt.State, _ rune)  { _, _ = s.Write([]byte("[private identity]")) }
func (bdChildTicket) Format(s fmt.State, _ rune) { _, _ = s.Write([]byte("[private authority]")) }
func (bdChildEnvironment) Format(s fmt.State, _ rune) {
	_, _ = s.Write([]byte("[private child environment]"))
}

var (
	errBDChildAuthority   = errors.New("bd child authority unavailable")
	errBDChildEnvironment = errors.New("bd child environment invalid")
	errBDChildSpawn       = errors.New("bd child spawn failed")
)

func (bdChildRecord) Format(s fmt.State, _ rune)   { _, _ = s.Write([]byte("[private record]")) }
func (bdChildProvider) Format(s fmt.State, _ rune) { _, _ = s.Write([]byte("[private provider]")) }
func (bdChildProcess) Format(s fmt.State, _ rune)  { _, _ = s.Write([]byte("[private process]")) }
func (bdChildCaller) Format(s fmt.State, _ rune)   { _, _ = s.Write([]byte("[private caller]")) }

var bdChildIdentityKeys = [...]string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN", "GC_CITY_PATH", "GC_CITY"}

func bdChildSensitiveIdentityKey(key string) bool {
	for _, k := range bdChildIdentityKeys {
		if key == k {
			return true
		}
	}
	return false
}

// Keep the original slice intact. Reject ambiguous identity entries BEFORE
// projecting a map; unrelated ordering and multiplicity belong to the caller.
func parseBDChildEnv(entries []string) (bdChildTuple, bdChildCityFields, error) {
	if len(entries) > 4096 {
		return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
	}
	fields := make(map[string]string, len(bdChildIdentityKeys))
	total := 0
	for _, entry := range entries {
		total += len(entry)
		if total > 16<<20 {
			return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) {
			return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
		}
		if !bdChildSensitiveIdentityKey(key) {
			continue
		}
		if _, duplicate := fields[key]; duplicate {
			return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
		}
		if len(value) > 4096 {
			return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
		}
		fields[key] = value
	}
	tuple := bdChildTuple{fields["GC_SESSION_ID"], fields["GC_TEMPLATE"], fields["GC_RUNTIME_EPOCH"], fields["GC_INSTANCE_TOKEN"]}
	if !validBDChildTuple(tuple) || fields["BEADS_HOLDER_TOKEN"] == "" || fields["BEADS_HOLDER_TOKEN"] != tuple.token {
		return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
	}
	path, hasPath := fields["GC_CITY_PATH"]
	alias, hasAlias := fields["GC_CITY"]
	city := bdChildCityFields{path: path, alias: alias, hasPath: hasPath, hasAlias: hasAlias}
	// There is no canonical-alias equivalence adapter in Stage0. Validate EACH
	// present key and refuse different raw values; never guess normalization.
	if (!hasPath && !hasAlias) || (hasPath && !validBDChildIdentityText(path)) ||
		(hasAlias && !validBDChildIdentityText(alias)) || (hasPath && hasAlias && path != alias) {
		return bdChildTuple{}, bdChildCityFields{}, errBDChildEnvironment
	}
	return tuple, city, nil
}

func parseBDChildRawEnv(entries []string) (bdChildTuple, string, error) {
	tuple, city, err := parseBDChildEnv(entries)
	return tuple, city.effective(), err
}

func validBDChildIdentityText(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n")
}

func validBDChildNumber(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == value
}

func validBDChildTuple(tuple bdChildTuple) bool {
	return validBDChildIdentityText(tuple.sid) && validBDChildIdentityText(tuple.template) &&
		validBDChildNumber(tuple.epoch) && validBDChildIdentityText(tuple.token)
}

func cloneBDChildCaller(caller bdChildCaller) bdChildCaller {
	out := bdChildCaller{processes: append([]bdChildProcess(nil), caller.processes...)}
	for i := range out.processes {
		out.processes[i].env = append([]string(nil), caller.processes[i].env...)
	}
	return out
}

func readBDChildAuthority(ctx context.Context, tuple bdChildTuple, city string, readers bdChildReaders) (bdChildTicket, error) {
	if ctx == nil || ctx.Err() != nil || readers.record == nil || readers.provider == nil || readers.caller == nil {
		return bdChildTicket{}, errBDChildAuthority
	}
	record, err := readers.record(ctx, tuple.sid)
	if err != nil || ctx.Err() != nil || record.tuple != tuple || record.city != city ||
		record.closed || record.resetPending || !validBDChildIdentityText(record.revision) ||
		!validBDChildIdentityText(record.provider) || !validBDChildIdentityText(record.handle) {
		return bdChildTicket{}, errBDChildAuthority
	}
	switch record.state {
	case "active", "awake", "creating", "start-pending":
	default:
		return bdChildTicket{}, errBDChildAuthority
	}
	providers, err := readers.provider(ctx, record)
	if err != nil || ctx.Err() != nil || len(providers) == 0 || len(providers) > 4096 {
		return bdChildTicket{}, errBDChildAuthority
	}
	var selected bdChildProvider
	matches := 0
	seenPID := map[int]bool{}
	seenSID := map[string]bool{}
	seenHandle := map[string]bool{}
	for _, p := range providers {
		if !validBDChildTuple(p.tuple) || !validBDChildIdentityText(p.city) ||
			!validBDChildIdentityText(p.provider) || !validBDChildIdentityText(p.handle) ||
			!validBDChildIdentityText(p.namespace) || p.rootPID <= 1 || p.rootPID > 2147483647 || !validBDChildNumber(p.rootStart) ||
			seenPID[p.rootPID] || seenSID[p.tuple.sid] || seenHandle[p.handle] {
			return bdChildTicket{}, errBDChildAuthority
		}
		seenPID[p.rootPID] = true
		seenSID[p.tuple.sid] = true
		seenHandle[p.handle] = true
		if p.tuple.sid == tuple.sid {
			if p.tuple != tuple || p.city != city || p.provider != record.provider || p.handle != record.handle {
				return bdChildTicket{}, errBDChildAuthority
			}
			selected = p
			matches++
		}
	}
	if matches != 1 {
		return bdChildTicket{}, errBDChildAuthority
	}
	caller, err := readers.caller(ctx)
	if err != nil || ctx.Err() != nil || len(caller.processes) == 0 || len(caller.processes) > 128 {
		return bdChildTicket{}, errBDChildAuthority
	}
	caller = cloneBDChildCaller(caller)
	seen := map[int]bool{}
	for i, p := range caller.processes {
		if p.pid <= 1 || p.pid > 2147483647 || seen[p.pid] || !validBDChildNumber(p.start) ||
			p.namespace != selected.namespace || p.uid != selected.uid || p.city != city {
			return bdChildTicket{}, errBDChildAuthority
		}
		seen[p.pid] = true
		captured, capturedCity, parseErr := parseBDChildRawEnv(p.env)
		if parseErr != nil || captured != tuple || capturedCity != city {
			return bdChildTicket{}, errBDChildAuthority
		}
		if i+1 < len(caller.processes) {
			if p.ppid != caller.processes[i+1].pid || p.pid == selected.rootPID {
				return bdChildTicket{}, errBDChildAuthority
			}
		} else if p.pid != selected.rootPID || p.start != selected.rootStart {
			return bdChildTicket{}, errBDChildAuthority
		}
	}
	return bdChildTicket{valid: true, record: record, provider: selected, caller: caller}, nil
}

// This composition alone cannot establish acquisition trust. The ticket is private and
// constructed only by the error-bearing reader composition; no ENV-only or
// handled=false constructor exists. Only the Linux acquisition adapters supply
// production readers; non-Linux CLI behavior remains on the existing branch.
func authorizeBDSessionChild(ctx context.Context, op bdChildOperation, raw []string, readers bdChildReaders) (bdChildTicket, error) {
	if op != bdChildSessionTool {
		return bdChildTicket{}, errBDChildAuthority
	}
	tuple, city, err := parseBDChildEnv(raw)
	if err != nil {
		return bdChildTicket{}, err
	}
	ticket, err := readBDChildAuthority(ctx, tuple, city.effective(), readers)
	if err != nil {
		return bdChildTicket{}, err
	}
	ticket.city = city
	return ticket, nil
}

func recheckBDSessionChild(ctx context.Context, ticket bdChildTicket, finalRaw []string, readers bdChildReaders) error {
	if !ticket.valid {
		return errBDChildAuthority
	}
	tuple, city, err := parseBDChildEnv(finalRaw)
	if err != nil || tuple != ticket.record.tuple || city != ticket.city || city.effective() != ticket.record.city {
		return errBDChildAuthority
	}
	current, err := readBDChildAuthority(ctx, tuple, city.effective(), readers)
	if err != nil || current.record != ticket.record || current.provider != ticket.provider ||
		!reflect.DeepEqual(current.caller, ticket.caller) {
		return errBDChildAuthority
	}
	return nil
}

func runBDSessionChild(ctx context.Context, op bdChildOperation, raw, finalRaw []string, readers bdChildReaders, spawn bdChildSpawn) error {
	if spawn == nil {
		return errBDChildAuthority
	}
	parent := append([]string(nil), raw...)
	projected := append([]string(nil), finalRaw...)
	ticket, err := authorizeBDSessionChild(ctx, op, parent, readers)
	if err != nil {
		return err
	}
	if err = recheckBDSessionChild(ctx, ticket, projected, readers); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errBDChildAuthority
	}
	// No pre-spawn read/check can claim a cross-process transaction. Existing
	// downstream holder fencing remains necessary for a race AFTER this recheck.
	if err := spawn(ctx, bdChildEnvironment{entries: projected}); err != nil {
		return errBDChildSpawn
	}
	return nil
}
