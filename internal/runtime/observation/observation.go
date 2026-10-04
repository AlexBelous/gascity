// Package observation exposes read-only per-incarnation runtime evidence.
// It makes no admission, ownership, drain, or lifecycle decisions.
package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

// Schema is the exact consumer contract version for per-SID observations.
const Schema = "managed-session-observation/v1"

// Session is a live provider handle attributed by its own incarnation metadata.
type Session struct {
	SessionID           string `json:"session_id"`
	StatusName          string `json:"status_name"`
	Template            string `json:"template"`
	RuntimeName         string `json:"runtime_name"`
	Running             bool   `json:"running"`
	RunEpoch            int    `json:"run_epoch"`
	InstanceTokenSHA256 string `json:"instance_token_sha256"`
}

// Process binds a provider incarnation to one exact process identity.
type Process struct {
	SessionID           string `json:"session_id"`
	CityPath            string `json:"city_path"`
	Template            string `json:"template"`
	RuntimeName         string `json:"runtime_name"`
	RunEpoch            int    `json:"run_epoch"`
	InstanceTokenSHA256 string `json:"instance_token_sha256"`
	PID                 int    `json:"pid"`
	PPID                int    `json:"ppid"`
	StartIdentity       string `json:"start_identity"`
}

// Observation is evidence, not permission to start or terminate a runtime.
// Complete booleans are false on ambiguity; partial rows remain inspectable.
type Observation struct {
	Schema            string    `json:"schema"`
	CityPath          string    `json:"city_path"`
	ObservedAt        time.Time `json:"observed_at"`
	ProcessObservedAt time.Time `json:"process_observed_at"`
	FinishedAt        time.Time `json:"finished_at"`
	ProviderComplete  bool      `json:"provider_complete"`
	ProcessComplete   bool      `json:"process_complete"`
	ProviderType      string    `json:"provider_type"`
	Sessions          []Session `json:"sessions"`
	Processes         []Process `json:"processes"`
	UnknownReasons    []string  `json:"unknown_reasons"`
}

// TokenDigest returns a non-reversible comparison value for an incarnation
// credential. An absent credential remains absent, not a hash of emptiness.
func TokenDigest(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Observe joins real provider liveness and process-table evidence. readRoots
// must provide strict host-wide coverage, not the legacy best-effort scan.
// Neither durable state nor a role's running aggregate establishes a SID.
func Observe(city string, sp runtime.Provider, readRoots func() ([]proctable.ObservedRoot, error), now func() time.Time) Observation {
	out := Observation{Schema: Schema, CityPath: city, ObservedAt: now().UTC(), ProviderType: fmt.Sprintf("%T", sp), Sessions: []Session{}, Processes: []Process{}, UnknownReasons: []string{}}
	fail := func(reason string) { out.UnknownReasons = append(out.UnknownReasons, reason) }
	if city == "" || !filepath.IsAbs(city) || sp == nil || readRoots == nil {
		fail("observation context unavailable")
		out.FinishedAt = now().UTC()
		return out
	}
	city = filepath.Clean(city)
	out.CityPath = city
	names, err := sp.ListRunning("")
	providerOK := err == nil
	if err != nil {
		fail("provider list unavailable")
	}
	names = append([]string{}, names...)
	sort.Strings(names)
	bySID := map[string]Session{}
	seenNames := map[string]bool{}
	for _, name := range names {
		row, err := readProviderSession(sp, name)
		if err != nil || seenNames[name] {
			providerOK = false
			fail("provider incarnation identity unavailable or ambiguous")
			continue
		}
		seenNames[name] = true
		if _, dup := bySID[row.SessionID]; dup {
			providerOK = false
			fail("multiple provider handles for one SID")
			continue
		}
		bySID[row.SessionID] = row
		out.Sessions = append(out.Sessions, row)
	}
	scanner, capable := runtime.AsProcessTableScanner(sp)
	processOK := capable
	if !capable {
		fail("provider lacks process-table coverage")
	}
	out.ProcessObservedAt = now().UTC()
	strict, strictErr := readRoots()
	if strictErr != nil {
		processOK = false
		fail("strict process scan incomplete: " + strictErr.Error())
	}
	var scanned []runtime.LiveRuntime
	if capable {
		scanned, err = scanner.FindRuntimesBySessionID("")
		if err != nil {
			processOK = false
			fail("provider process scan incomplete")
		}
	}
	byPID := map[int]runtime.LiveRuntime{}
	for _, r := range scanned {
		if _, dup := byPID[r.PID]; dup || r.PID <= 0 {
			processOK = false
			fail("ambiguous provider process PID")
			continue
		}
		byPID[r.PID] = r
	}
	seenPID := map[int]bool{}
	liveSID := map[string]bool{}
	for _, root := range strict {
		r := root.Runtime
		if r.City == "" {
			processOK = false
			fail("process city unknown")
			continue
		}
		if filepath.Clean(r.City) != city {
			continue
		}
		reported, exists := byPID[r.PID]
		seenPID[r.PID] = true
		row, mapped := bySID[r.SessionID]
		if !mapped || !exists || !reported.IsTracked || reported.ProviderName != row.RuntimeName || reported.SessionID != r.SessionID || filepath.Clean(reported.City) != city || reported.Epoch != r.Epoch || reported.PPID != r.PPID || r.Epoch != row.RunEpoch || root.Template != row.Template || root.InstanceTokenSHA256 != row.InstanceTokenSHA256 || root.StartIdentity == "" || r.PID <= 0 || liveSID[r.SessionID] {
			processOK = false
			fail("provider/process incarnation discrepancy")
			continue
		}
		liveSID[r.SessionID] = true
		out.Processes = append(out.Processes, Process{SessionID: r.SessionID, CityPath: city, Template: root.Template, RuntimeName: row.RuntimeName, RunEpoch: r.Epoch, InstanceTokenSHA256: root.InstanceTokenSHA256, PID: r.PID, PPID: r.PPID, StartIdentity: root.StartIdentity})
	}
	for _, r := range scanned {
		if r.City == "" || (filepath.Clean(r.City) == city && !seenPID[r.PID]) {
			processOK = false
			fail("provider process lacks strict identity")
		}
	}
	if len(liveSID) != len(bySID) {
		processOK = false
		fail("live provider SID lacks process identity")
	}
	// Fence changes during the observation without turning durable rows into
	// liveness. A restarted run with the same provider handle must also deny.
	after, listErr := sp.ListRunning("")
	after = append([]string{}, after...)
	sort.Strings(after)
	if listErr != nil || !reflect.DeepEqual(names, after) {
		providerOK = false
		fail("provider changed during observation")
	}
	for _, row := range out.Sessions {
		current, readErr := readProviderSession(sp, row.RuntimeName)
		if readErr != nil || current != row {
			providerOK = false
			fail("provider incarnation changed during observation")
		}
	}
	rechecked, recheckErr := readRoots()
	if recheckErr != nil || !reflect.DeepEqual(strict, rechecked) {
		processOK = false
		fail("process identities changed or became unavailable during observation")
	}
	out.FinishedAt = now().UTC()
	if out.ProcessObservedAt.Before(out.ObservedAt) || out.ProcessObservedAt.After(out.FinishedAt) || out.FinishedAt.Before(out.ObservedAt) || out.FinishedAt.Sub(out.ObservedAt) > 60*time.Second {
		providerOK = false
		processOK = false
		fail("observation window invalid or exceeds 60 seconds")
	}
	out.ProviderComplete = providerOK
	out.ProcessComplete = processOK
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].SessionID < out.Sessions[j].SessionID })
	sort.Slice(out.Processes, func(i, j int) bool { return out.Processes[i].PID < out.Processes[j].PID })
	return out
}

func readProviderSession(sp runtime.Provider, name string) (Session, error) {
	row := Session{RuntimeName: name, Running: true}
	if name == "" {
		return row, fmt.Errorf("empty provider handle")
	}
	for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN"} {
		value, err := sp.GetMeta(name, key)
		if err != nil || value == "" {
			return row, fmt.Errorf("incarnation metadata missing")
		}
		switch key {
		case "GC_SESSION_ID":
			row.SessionID = value
		case "GC_TEMPLATE":
			row.Template = value
			row.StatusName = value
		case "GC_RUNTIME_EPOCH":
			row.RunEpoch, err = strconv.Atoi(value)
			if err != nil || row.RunEpoch < 1 {
				return row, fmt.Errorf("runtime epoch invalid")
			}
		case "GC_INSTANCE_TOKEN":
			row.InstanceTokenSHA256 = TokenDigest(value)
		}
	}
	return row, nil
}
