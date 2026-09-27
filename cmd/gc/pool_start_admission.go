package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// claimPoolStartAdmission spends a reader-issued capacity reservation for a
// specific session before the reconciler can start its provider. The reader
// and this writer use the same flock; claims remain in the reader's ledger so
// another resume candidate cannot reuse the grant on the next tick. A failed
// prepare/start intentionally keeps its claim until the reader's three-minute
// reservation expiry: retrying too early could produce a second process from
// one admission after an ambiguous provider start.
func claimPoolStartAdmission(cityPath, route, sessionID string, now time.Time, sp runtime.Provider, store beads.Store) (bool, string) {
	if cityPath == "" {
		return true, "not_configured"
	}
	if route == "" || sessionID == "" {
		return false, "capacity_identity_missing"
	}
	var scope struct {
		Queue struct {
			ManagedExactRoutes   []string `toml:"managed_exact_routes"`
			ManagedRoutePrefixes []string `toml:"managed_route_prefixes"`
		} `toml:"queue"`
	}
	scopePath := filepath.Join(cityPath, "config", "capacity-scheduler-v2.toml")
	_, scopeErr := toml.DecodeFile(scopePath, &scope)
	if scopeErr != nil && !errors.Is(scopeErr, os.ErrNotExist) {
		return false, "capacity_scope_invalid"
	}
	scopeConfigured := scopeErr == nil
	if scopeConfigured && !managedCapacityRoute(route, scope.Queue.ManagedExactRoutes, scope.Queue.ManagedRoutePrefixes) {
		return true, "capacity_route_unmanaged"
	}
	snapshotPath := filepath.Join(cityPath, ".gc", "runtime", "capacity-scheduler-v2", "snapshot.json")
	ledgerPath := filepath.Join(cityPath, ".gc", "runtime", "pool-capacity-admission", "reservations.json")
	_, snapshotErr := os.Stat(snapshotPath)
	_, ledgerErr := os.Stat(ledgerPath)
	_, snapshotDirErr := os.Stat(filepath.Dir(snapshotPath))
	_, ledgerDirErr := os.Stat(filepath.Dir(ledgerPath))
	if !scopeConfigured && errors.Is(snapshotErr, os.ErrNotExist) && errors.Is(ledgerErr, os.ErrNotExist) &&
		errors.Is(snapshotDirErr, os.ErrNotExist) && errors.Is(ledgerDirErr, os.ErrNotExist) {
		return true, "not_configured"
	}
	if snapshotErr != nil || ledgerErr != nil {
		return false, "capacity_state_unavailable"
	}
	lock, err := os.OpenFile(ledgerPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, "capacity_lock_unavailable"
	}
	defer func() {
		if err := lock.Close(); err != nil {
			log.Printf("pool admission: close lock: %v", err)
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return false, "capacity_lock_unavailable"
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	var snap struct {
		GeneratedAt string `json:"generated_at"`
		Zone        string `json:"zone"`
		Hysteresis  struct {
			GreenStreak   int    `json:"green_streak"`
			CooldownUntil string `json:"cooldown_until"`
		} `json:"hysteresis"`
		Capacity struct {
			ActiveCount int `json:"managed_active_count"`
			Cap         int `json:"managed_worker_cap"`
			OverCap     int `json:"over_cap_by"`
			Active      []struct {
				ID    string `json:"id"`
				Route string `json:"route"`
				State string `json:"state"`
			} `json:"active"`
		} `json:"capacity"`
	}
	data, err := os.ReadFile(snapshotPath)
	if err != nil || json.Unmarshal(data, &snap) != nil {
		return false, "capacity_snapshot_invalid"
	}
	generated, err := time.Parse(time.RFC3339Nano, snap.GeneratedAt)
	if err != nil || now.Sub(generated) < -time.Minute || now.Sub(generated) > 15*time.Minute {
		return false, "capacity_snapshot_stale"
	}
	cooldown, err := time.Parse(time.RFC3339Nano, snap.Hysteresis.CooldownUntil)
	if err != nil || snap.Zone != "green" || snap.Hysteresis.GreenStreak < 2 || now.Before(cooldown) || snap.Capacity.OverCap != 0 || snap.Capacity.ActiveCount < 0 || snap.Capacity.Cap <= snap.Capacity.ActiveCount || len(snap.Capacity.Active) != snap.Capacity.ActiveCount {
		return false, "capacity_not_admissible"
	}
	seenActive := make(map[string]bool, len(snap.Capacity.Active))
	for _, active := range snap.Capacity.Active {
		if active.ID == "" || active.Route == "" || active.State != "active" || seenActive[active.ID] {
			return false, "capacity_active_invalid"
		}
		seenActive[active.ID] = true
	}
	if scopeConfigured {
		if sp == nil || store == nil {
			return false, "capacity_live_census_unavailable"
		}
		infos, err := loadOpenSessionInfos(store)
		if err != nil {
			return false, "capacity_live_census_unavailable"
		}
		byRuntimeName := make(map[string]struct{ id, route string }, len(infos))
		bySessionID := make(map[string]string, len(infos))
		for _, info := range infos {
			identity := struct{ id, route string }{info.ID, info.Template}
			bySessionID[info.ID] = info.Template
			if info.SessionName != "" {
				byRuntimeName[info.SessionName] = identity
			}
			if info.SessionNameMetadata != "" {
				byRuntimeName[info.SessionNameMetadata] = identity
			}
		}
		running, err := sp.ListRunning("")
		if err != nil {
			return false, "capacity_live_census_unavailable"
		}
		for _, name := range running {
			identity, known := byRuntimeName[name]
			if !known {
				return false, "capacity_live_census_unmapped"
			}
			if managedCapacityRoute(identity.route, scope.Queue.ManagedExactRoutes, scope.Queue.ManagedRoutePrefixes) && !seenActive[identity.id] {
				return false, "capacity_live_census_discrepancy"
			}
		}
		// Provider registry liveness can miss a real agent process. The optional
		// process-table scanner independently finds roots by GC_SESSION_ID, so an
		// undercounted DE still blocks admission even if gc status says false.
		scanner, ok := sp.(runtime.ProcessTableScanner)
		if !ok {
			return false, "capacity_process_census_unavailable"
		}
		processes, err := scanner.FindRuntimesBySessionID("")
		if err != nil {
			return false, "capacity_process_census_unavailable"
		}
		for _, process := range processes {
			if process.City == "" {
				return false, "capacity_process_city_unknown"
			}
			if normalizePathForCompare(process.City) != normalizePathForCompare(cityPath) {
				continue
			}
			processRoute, known := bySessionID[process.SessionID]
			if !known {
				return false, "capacity_process_census_unmapped"
			}
			if managedCapacityRoute(processRoute, scope.Queue.ManagedExactRoutes, scope.Queue.ManagedRoutePrefixes) && !seenActive[process.SessionID] {
				return false, "capacity_process_census_discrepancy"
			}
		}
	}
	var ledger struct {
		Schema       int                        `json:"schema"`
		Reservations map[string]json.RawMessage `json:"reservations"`
	}
	data, err = os.ReadFile(ledgerPath)
	if err != nil || json.Unmarshal(data, &ledger) != nil || ledger.Schema != 1 || ledger.Reservations == nil {
		return false, "capacity_ledger_invalid"
	}
	entryRaw, ok := ledger.Reservations[route]
	if !ok {
		return false, "capacity_grant_missing"
	}
	var entry map[string]json.RawMessage
	if json.Unmarshal(entryRaw, &entry) != nil {
		return false, "capacity_grant_invalid"
	}
	var slots, activeAtReservation int
	var createdAt string
	var claims map[string]string
	if json.Unmarshal(entry["slots"], &slots) != nil || json.Unmarshal(entry["active_at_reservation"], &activeAtReservation) != nil || json.Unmarshal(entry["created_at"], &createdAt) != nil || activeAtReservation < 0 {
		return false, "capacity_grant_invalid"
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || now.Sub(created) < -time.Minute || now.Sub(created) > 3*time.Minute || slots < 1 {
		return false, "capacity_grant_expired"
	}
	if raw := entry["start_claims"]; len(raw) != 0 && json.Unmarshal(raw, &claims) != nil {
		return false, "capacity_claims_invalid"
	}
	if claims == nil {
		claims = make(map[string]string)
	}
	claimedAcrossRoutes := 0
	for _, raw := range ledger.Reservations {
		var grant struct {
			StartClaims map[string]string `json:"start_claims"`
		}
		if json.Unmarshal(raw, &grant) != nil {
			return false, "capacity_claims_invalid"
		}
		claimedAcrossRoutes += len(grant.StartClaims)
	}
	if _, exists := claims[sessionID]; exists {
		return false, "capacity_session_already_claimed"
	}
	if len(claims) >= slots {
		return false, "capacity_grant_consumed"
	}
	if snap.Capacity.ActiveCount+claimedAcrossRoutes >= snap.Capacity.Cap {
		return false, "capacity_global_grants_consumed"
	}
	claims[sessionID] = now.UTC().Format(time.RFC3339Nano)
	entry["start_claims"], _ = json.Marshal(claims)
	ledger.Reservations[route], _ = json.Marshal(entry)
	// Preserve top-level fields written by pool_demand_reader.py.
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return false, "capacity_ledger_invalid"
	}
	top["reservations"], _ = json.Marshal(ledger.Reservations)
	updated, err := json.Marshal(top)
	if err != nil {
		return false, "capacity_ledger_invalid"
	}
	tmp, err := os.CreateTemp(filepath.Dir(ledgerPath), ".reservations.*")
	if err != nil {
		return false, "capacity_ledger_write_failed"
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("pool admission: remove temporary ledger: %v", err)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			log.Printf("pool admission: close temporary ledger after chmod failure: %v", closeErr)
		}
		return false, "capacity_ledger_write_failed"
	}
	if _, err := tmp.Write(append(updated, '\n')); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			log.Printf("pool admission: close temporary ledger after write failure: %v", closeErr)
		}
		return false, "capacity_ledger_write_failed"
	}
	if err := tmp.Sync(); err != nil {
		if closeErr := tmp.Close(); closeErr != nil {
			log.Printf("pool admission: close temporary ledger after sync failure: %v", closeErr)
		}
		return false, "capacity_ledger_write_failed"
	}
	if err := tmp.Close(); err != nil {
		return false, "capacity_ledger_write_failed"
	}
	if err := os.Rename(tmp.Name(), ledgerPath); err != nil {
		return false, "capacity_ledger_write_failed"
	}
	return true, fmt.Sprintf("capacity_grant_claimed:%s", strings.TrimSpace(route))
}

func managedCapacityRoute(route string, exact, prefixes []string) bool {
	for _, value := range exact {
		if route == value {
			return true
		}
	}
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(route, prefix) {
			return true
		}
	}
	return false
}
