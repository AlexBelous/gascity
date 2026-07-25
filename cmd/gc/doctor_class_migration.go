package main

// infra-class-migration surfaces the per-class bd->sqlite migration state
// (engdocs/design/infra-class-sqlite-stores.md, "Doctor / storehealth /
// maintenance"): for each relocatable class it reports the configured
// backend, the migrated-marker state, and the routing that follows from the
// two-key activation (backend=sqlite AND marker present). The marker stat
// honors the ENOENT-only discipline — an unstatable marker means routing
// fails closed everywhere, so it surfaces here as an error, not as "bd".

import (
	"errors"
	"fmt"
	"os"
	"strings"

	messagingdb "github.com/gastownhall/gascity/internal/classdb/messaging"
	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	sessionsdb "github.com/gastownhall/gascity/internal/classdb/sessions"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// classMigrationState is one class's resolved migration/routing state.
type classMigrationState struct {
	class   string
	backend string
	marker  string // "present" | "absent"
	routing string // "sqlite" | "bd" | "unknown"
	statErr error
	shadow  bool
}

// classMigrationStates resolves the migration state of every relocatable
// class, in the design's cutover order.
func classMigrationStates(cityPath string, cfg *config.City) []classMigrationState {
	classes := []struct {
		name       string
		markerPath string
	}{
		{config.BeadClassOrders, ordersMigratedMarkerPath(cityPath)},
		{config.BeadClassNudges, nudgesdb.MigratedMarkerPath(cityPath)},
		{config.BeadClassMessaging, messagingdb.MigratedMarkerPath(cityPath)},
		{config.BeadClassSessions, sessionsdb.MigratedMarkerPath(cityPath)},
		{config.BeadClassGraph, graphMigratedMarkerPath(cityPath)},
	}
	states := make([]classMigrationState, 0, len(classes))
	for _, cl := range classes {
		st := classMigrationState{
			class:   cl.name,
			backend: cfg.Beads.ClassBackend(cl.name),
			marker:  "absent",
			routing: "bd",
			shadow:  cfg.Beads.ClassShadow(cl.name),
		}
		if _, err := os.Stat(cl.markerPath); err == nil {
			st.marker = "present"
			if st.backend == config.BeadsClassBackendSQLite {
				st.routing = "sqlite"
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			st.statErr = err
			st.routing = "unknown"
		}
		states = append(states, st)
	}
	return states
}

// workTopologyDoctorState returns the work-scope topology line for the
// infra-class-migration check (deliverable F): the configured scope/target,
// which durable work markers are present, and how many recorded residue
// sources are still undrained. An unstatable marker is a blocking error — the
// same ENOENT-only discipline as the class markers, because routing fails
// closed until the stat error is fixed.
func workTopologyDoctorState(cityPath string, cfg *config.City) (string, error) {
	scope := cfg.Beads.Work.EffectiveScope()
	target := "managed"
	if cfg.Beads.Work.IsRemote() {
		target = "remote"
	}
	var present []string
	residue := 0
	var statErr error
	if m, ok, err := readWorkTopologyMarker(workUnifiedMarkerPath(cityPath)); err != nil {
		statErr = err
	} else if ok {
		present = append(present, "work.unified")
		residue += m.undrainedResidueCount()
	}
	if m, ok, err := readWorkTopologyMarker(workRemoteMarkerPath(cityPath)); err != nil {
		if statErr == nil {
			statErr = err
		}
	} else if ok {
		present = append(present, "work.remote")
		residue += m.undrainedResidueCount()
	}
	markers := "absent"
	if len(present) > 0 {
		markers = strings.Join(present, ",")
	}
	detail := fmt.Sprintf("work: scope=%s target=%s markers=%s residue=%d", scope, target, markers, residue)
	// Surface the F.4 managed-local keep-alive linkage so a forever-undrained source
	// is diagnosable beyond per-tick stderr: a remote city keeps its managed-LOCAL
	// Dolt alive until residue drains.
	if keep, kerr := workTopologyManagedDoltKeepAlive(cityPath); kerr == nil && keep {
		detail += " local-dolt=kept-alive"
	}
	// On a remote-target city, surface remote-auth reachability and the org DB
	// allowed_prefixes presence check (deliverable G) — cached/rate-limited so the
	// doctor line never becomes a hot-path org-DB scan.
	if cfg.Beads.Work.IsRemote() {
		st := workRemoteDoctorProbe(cityPath, cfg)
		auth := "unreachable"
		if st.reachable {
			auth = "ok"
		}
		detail += fmt.Sprintf(" remote-auth=%s", auth)
		if !st.reachable && strings.TrimSpace(st.authDetail) != "" {
			detail += fmt.Sprintf(" (%s)", st.authDetail)
		}
		prefixes := "present"
		if !st.prefixesPresent {
			prefixes = "missing"
			if len(st.missing) > 0 {
				prefixes += ":" + strings.Join(st.missing, ",")
			}
		}
		detail += fmt.Sprintf(" prefixes=%s", prefixes)
	}
	if statErr != nil {
		detail += fmt.Sprintf(" (marker stat: %v)", statErr)
	}
	return detail, statErr
}

// classMigrationDoctorCheck reports the migration/routing state of the four
// relocatable infra classes. Advisory in the healthy shapes (fully bd, fully
// routed, migration pending first boot, deliberate rollback), error when a
// marker is unstatable — that city's class routing fails closed until the
// stat error is fixed.
type classMigrationDoctorCheck struct {
	cfg      *config.City
	cityPath string
}

func (c *classMigrationDoctorCheck) Name() string { return "infra-class-migration" }

func (c *classMigrationDoctorCheck) CanFix() bool { return false }

func (c *classMigrationDoctorCheck) WarmupEligible() bool { return false }

func (c *classMigrationDoctorCheck) Fix(_ *doctor.CheckContext) error { return nil }

func (c *classMigrationDoctorCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	r := &doctor.CheckResult{Name: c.Name(), Status: doctor.StatusOK, Severity: doctor.SeverityAdvisory}
	states := classMigrationStates(c.cityPath, c.cfg)
	routed, pending, rollback, broken := 0, 0, 0, 0
	for _, st := range states {
		detail := fmt.Sprintf("%s: backend=%s marker=%s routing=%s", st.class, st.backend, st.marker, st.routing)
		if st.shadow {
			detail += " shadow=on"
		}
		if st.statErr != nil {
			detail += fmt.Sprintf(" (marker stat: %v)", st.statErr)
		}
		r.Details = append(r.Details, detail)
		switch {
		case st.statErr != nil:
			broken++
		case st.routing == "sqlite":
			routed++
		case st.backend == config.BeadsClassBackendSQLite && st.marker == "absent":
			pending++
		case st.backend != config.BeadsClassBackendSQLite && st.marker == "present":
			rollback++
		}
	}
	workDetail, workStatErr := workTopologyDoctorState(c.cityPath, c.cfg)
	r.Details = append(r.Details, workDetail)
	if workStatErr != nil {
		broken++
	}
	switch {
	case broken > 0:
		r.Status = doctor.StatusError
		r.Severity = doctor.SeverityBlocking
		r.Message = fmt.Sprintf("%d class(es) with unstatable migrated marker — routing fails closed until the stat error is fixed", broken)
	case rollback > 0:
		r.Status = doctor.StatusWarning
		r.Message = fmt.Sprintf("%d migrated class(es) rolled back to bd (marker present, backend=bd) — the escape hatch is active; bd writes are invisible to the class store", rollback)
	case pending > 0:
		r.Message = fmt.Sprintf("%d class(es) awaiting first-boot migration (backend=sqlite, marker absent); %d routed to sqlite", pending, routed)
	case routed == len(states):
		r.Message = "all infra classes routed to sqlite class stores"
	case routed > 0:
		r.Message = fmt.Sprintf("%d of %d infra classes routed to sqlite", routed, len(states))
	default:
		r.Message = "all infra classes on bd (no class migrated)"
	}
	return r
}
