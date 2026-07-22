package main

// Sessions-class store seam: the shadow-write gate (P4 slice 3). When
// [beads.classes.sessions] shadow=true (backend still "bd"),
// resolveSessionStore wraps the resolved bd store in the sessionsdb shadow
// tee: bd stays authoritative for every read and write, each committed
// sessions-class write is replayed onto .gc/store/sessions.db, and the
// sessions-shadow doctor check diffs the two — the design's
// zero-discrepancy soak that must hold before the backend flips (slice 4).
//
// Fail-OPEN, deliberately: shadow is an observability stage with bd
// authoritative, so a class store that cannot open logs and returns the
// unwrapped bd store instead of taking the city down. (The slice-4 routing
// flip is the opposite — fail-CLOSED — because there the class store IS
// the authority.)

import (
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/gastownhall/gascity/internal/beads"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// sessionShadowWrappers caches the shadow wrapper per (base store, city) so
// repeated resolveSessionStore calls return the SAME wrapper value —
// identity-keyed consumers (the messaging repair-city registry, replaced-
// handle close scheduling) see one stable handle per root, exactly like the
// unwrapped store they had before. Entries are bounded by store-opening
// roots (controller boot/reload handles plus CLI one-shot processes).
var sessionShadowWrappers struct {
	mu    sync.Mutex
	byKey map[string]beads.Store
}

// maybeShadowSessionStore wraps base in the sessions shadow tee when the
// city's config enables the shadow gate. A nil cfg (or empty cityPath)
// never shadows; a class-store open failure logs and falls back to the
// unwrapped base (fail-open — bd stays authoritative either way).
func maybeShadowSessionStore(base beads.Store, cfg *config.City, cityPath string) beads.Store {
	if base == nil || cfg == nil || cityPath == "" || !cfg.Beads.ClassShadow(config.BeadClassSessions) {
		return base
	}
	key := storeIdentityKey(base) + "|" + cityPath
	sessionShadowWrappers.mu.Lock()
	defer sessionShadowWrappers.mu.Unlock()
	if wrapped, ok := sessionShadowWrappers.byKey[key]; ok {
		return wrapped
	}
	class, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		log.Printf("sessions shadow: opening class store for %s: %v (shadow disabled this process; bd stays authoritative)", cityPath, err)
		return base
	}
	wrapped := sessionsdb.NewShadow(base, class)
	if sessionShadowWrappers.byKey == nil {
		sessionShadowWrappers.byKey = make(map[string]beads.Store)
	}
	sessionShadowWrappers.byKey[key] = wrapped
	return wrapped
}

// openSessionsShadowSeedStore is the bd-store open seam for the boot seed
// (overridden by tests, mirroring openMessagingClassMigrationStore).
var openSessionsShadowSeedStore func(cityPath string) (beads.Store, error) = func(cityPath string) (beads.Store, error) {
	return openStoreAtForCity(cityPath, cityPath)
}

// seedSessionsShadowAtBoot resets the sessions class store and re-imports
// the city's current sessions-class beads when the shadow gate is on, so
// the soak's diff starts from a converged baseline. Best-effort: a seed
// failure logs and the tee still converges rows organically (on-miss
// imports); the doctor diff surfaces whatever is left.
func seedSessionsShadowAtBoot(cityPath string, cfg *config.City, stderr io.Writer) {
	if cfg == nil || !cfg.Beads.ClassShadow(config.BeadClassSessions) {
		return
	}
	class, err := sessionsdb.SharedStoreFor(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions shadow seed: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	store, err := openSessionsShadowSeedStore(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions shadow seed: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close
	n, err := class.SeedFromPrimary(store)
	if err != nil {
		fmt.Fprintf(stderr, "gc start: sessions shadow seed: %v\n", err) //nolint:errcheck // best-effort stderr
		return
	}
	fmt.Fprintf(stderr, "gc start: sessions shadow seeded %d rows into %s\n", n, sessionsdb.StorePath(cityPath)) //nolint:errcheck // best-effort stderr
}

// sessionsShadowDoctorCheck is the shadow soak's zero-discrepancy oracle:
// it diffs the sessions class store against the bd truth. It diffs twice
// and reports only discrepancies present in BOTH passes, filtering
// in-flight write races on a live city.
type sessionsShadowDoctorCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
}

func (c *sessionsShadowDoctorCheck) Name() string { return "sessions-shadow" }

func (c *sessionsShadowDoctorCheck) CanFix() bool { return false }

func (c *sessionsShadowDoctorCheck) WarmupEligible() bool { return false }

func (c *sessionsShadowDoctorCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *sessionsShadowDoctorCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	r := &doctor.CheckResult{Name: c.Name(), Status: doctor.StatusOK, Message: "sessions shadow-write disabled"}
	if c == nil || c.cfg == nil || c.newStore == nil || !c.cfg.Beads.ClassShadow(config.BeadClassSessions) {
		return r
	}
	class, err := sessionsdb.SharedStoreFor(c.cityPath)
	if err != nil {
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("sessions shadow diff skipped: %v", err)
		return r
	}
	store, err := c.newStore(c.cityPath)
	if err != nil {
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("sessions shadow diff skipped: %v", err)
		return r
	}
	defer closeBeadStoreHandle(store) //nolint:errcheck // best-effort close
	first, err := class.DiffAgainstPrimary(store)
	if err != nil {
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("sessions shadow diff failed: %v", err)
		return r
	}
	if first.Clean() {
		r.Message = fmt.Sprintf("sessions shadow clean (%d open rows compared)", first.Compared)
		return r
	}
	second, err := class.DiffAgainstPrimary(store)
	if err != nil {
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("sessions shadow diff failed: %v", err)
		return r
	}
	stable := intersectShadowDiffs(first, second)
	if stable.Clean() {
		r.Message = fmt.Sprintf("sessions shadow clean after re-diff (%d open rows compared; first pass raced in-flight writes)", second.Compared)
		return r
	}
	r.Status = doctor.StatusWarning
	r.Message = fmt.Sprintf("sessions shadow DIVERGED: %d missing, %d extra, %d mismatched of %d compared — do not flip the backend",
		len(stable.Missing), len(stable.Extra), len(stable.Mismatched), stable.Compared)
	for _, id := range stable.Missing {
		r.Details = append(r.Details, fmt.Sprintf("missing from shadow: %s", id))
	}
	for _, id := range stable.Extra {
		r.Details = append(r.Details, fmt.Sprintf("extra open row in shadow: %s", id))
	}
	for _, m := range stable.Mismatched {
		r.Details = append(r.Details, fmt.Sprintf("mismatch %s: %s", m.ID, m.Detail))
	}
	return r
}

// intersectShadowDiffs keeps only the discrepancies present in both diff
// passes: a divergence that appears once and heals by the second pass was
// an in-flight write race, not real drift.
func intersectShadowDiffs(a, b sessionsdb.ShadowDiff) sessionsdb.ShadowDiff {
	out := sessionsdb.ShadowDiff{Compared: b.Compared}
	inB := func(ids []string, id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}
	for _, id := range a.Missing {
		if inB(b.Missing, id) {
			out.Missing = append(out.Missing, id)
		}
	}
	for _, id := range a.Extra {
		if inB(b.Extra, id) {
			out.Extra = append(out.Extra, id)
		}
	}
	bMismatch := make(map[string]bool, len(b.Mismatched))
	for _, m := range b.Mismatched {
		bMismatch[m.ID] = true
	}
	for _, m := range a.Mismatched {
		if bMismatch[m.ID] {
			out.Mismatched = append(out.Mismatched, m)
		}
	}
	return out
}
