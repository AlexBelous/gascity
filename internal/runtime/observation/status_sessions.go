package observation

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// RuntimeSessionsSchema identifies the independent SID status projection.
const RuntimeSessionsSchema = "gascity.runtime-sessions/v1"

// NativeStatusIdentity retains recorded native identity, never derived handles.
type NativeStatusIdentity struct {
	ID, Template, AgentName, RuntimeName string
}

// RuntimeSessionStatus is status evidence for one open native SID.
type RuntimeSessionStatus struct {
	ID          string `json:"id"`
	Template    string `json:"template"`
	AgentName   string `json:"agent_name"`
	RuntimeName string `json:"runtime_name"`
	Provider    string `json:"provider"`
	Running     bool   `json:"running"`
}

// RuntimeSessions is an identity projection, not kernel or admission authority.
type RuntimeSessions struct {
	Schema           string                 `json:"schema"`
	ObservedAt       time.Time              `json:"observed_at"`
	ProviderComplete bool                   `json:"provider_complete"`
	Sessions         []RuntimeSessionStatus `json:"sessions"`
}

// ProviderBoundaryType preserves the authenticated observation's backend label.
func ProviderBoundaryType(sp runtime.Provider) string { return fmt.Sprintf("%T", sp) }

// ProjectRuntimeSessions joins complete native records to actual live handles.
// Diagnostic codes are bounded and carry no metadata values or credentials.
func ProjectRuntimeSessions(ctx context.Context, records []NativeStatusIdentity, storeComplete bool, sp runtime.Provider, now time.Time) (RuntimeSessions, []string) {
	p := RuntimeSessions{Schema: RuntimeSessionsSchema, ObservedAt: now.UTC(), Sessions: []RuntimeSessionStatus{}}
	errors := []string{}
	fail := func(code string) { errors = append(errors, "runtime_sessions:"+code) }
	if !storeComplete {
		fail("native_store_partial")
	}
	if sp == nil {
		fail("provider_unavailable")
		return p, errors
	}
	byID, byHandle := map[string]int{}, map[string]int{}
	for _, record := range records {
		if record.ID == "" || record.Template == "" {
			fail("native_identity_invalid")
			continue
		}
		if _, exists := byID[record.ID]; exists {
			fail("duplicate_sid")
			continue
		}
		byID[record.ID] = len(p.Sessions)
		row := RuntimeSessionStatus{ID: record.ID, Template: record.Template, AgentName: record.AgentName, RuntimeName: record.RuntimeName, Provider: ProviderBoundaryType(sp)}
		if row.RuntimeName != "" {
			if _, exists := byHandle[row.RuntimeName]; exists {
				fail("duplicate_handle")
			}
			byHandle[row.RuntimeName] = len(p.Sessions)
		}
		p.Sessions = append(p.Sessions, row)
	}
	if ctx.Err() != nil {
		fail("canceled")
		return p, errors
	}
	live, err := sp.ListRunning("")
	if err != nil {
		fail("provider_enumeration_failed")
		return p, errors
	}
	if reporter, ok := sp.(interface{ StatusPartial() bool }); ok && reporter.StatusPartial() {
		fail("provider_enumeration_partial")
	}
	seenHandle, seenSID := map[string]bool{}, map[string]bool{}
	for _, handle := range live {
		if ctx.Err() != nil {
			fail("canceled")
			break
		}
		if handle == "" || seenHandle[handle] {
			fail("live_handle_invalid")
			continue
		}
		seenHandle[handle] = true
		sid, e1 := sp.GetMeta(handle, "GC_SESSION_ID")
		if ctx.Err() != nil {
			fail("canceled")
			break
		}
		template, e2 := sp.GetMeta(handle, "GC_TEMPLATE")
		if e1 != nil || e2 != nil || sid == "" || template == "" {
			fail("live_identity_unavailable")
			continue
		}
		if seenSID[sid] {
			fail("duplicate_live_sid")
			continue
		}
		seenSID[sid] = true
		index, exists := byID[sid]
		if !exists {
			fail("live_sid_missing")
			continue
		}
		row := &p.Sessions[index]
		if row.RuntimeName == "" {
			fail("live_recorded_handle_missing")
			continue
		}
		if row.RuntimeName != handle || row.Template != template {
			fail("live_identity_conflict")
			continue
		}
		row.Running = true
	}
	if ctx.Err() != nil {
		fail("canceled")
	}
	p.ProviderComplete = len(errors) == 0
	sort.Slice(p.Sessions, func(i, j int) bool { return p.Sessions[i].ID < p.Sessions[j].ID })
	return p, errors
}
