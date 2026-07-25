package main

import (
	"os"
	"strings"
	"testing"

	nudgesdb "github.com/gastownhall/gascity/internal/classdb/nudges"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

func classMigrationConfig(t *testing.T, body string) *config.City {
	t.Helper()
	cfg, err := config.Parse([]byte("[workspace]\nname = \"doctor-migration\"\n" + body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func runClassMigrationCheck(t *testing.T, cityPath string, cfg *config.City) *doctor.CheckResult {
	t.Helper()
	check := &classMigrationDoctorCheck{cfg: cfg, cityPath: cityPath}
	if check.Name() != "infra-class-migration" {
		t.Fatalf("Name = %q", check.Name())
	}
	return check.Run(&doctor.CheckContext{CityPath: cityPath})
}

func TestClassMigrationCheckAllBD(t *testing.T) {
	r := runClassMigrationCheck(t, t.TempDir(), classMigrationConfig(t, ""))
	if r.Status != doctor.StatusOK || r.Severity != doctor.SeverityAdvisory {
		t.Fatalf("all-bd city = (%v, %v), want advisory OK: %s", r.Status, r.Severity, r.Message)
	}
	if !strings.Contains(r.Message, "all infra classes on bd") {
		t.Fatalf("message %q", r.Message)
	}
	if len(r.Details) != 6 {
		t.Fatalf("details = %d lines, want one per class plus the work line: %v", len(r.Details), r.Details)
	}
	if r.Details[0] != "orders: backend=bd marker=absent routing=bd" {
		t.Fatalf("orders detail %q", r.Details[0])
	}
	if r.Details[5] != "work: scope=scoped target=managed markers=absent residue=0" {
		t.Fatalf("work detail %q", r.Details[5])
	}
}

func TestClassMigrationCheckWorkTopologyLine(t *testing.T) {
	cityPath := t.TempDir()
	// Unified marker with one undrained residue source.
	if err := writeWorkTopologyMarker(workUnifiedMarkerPath(cityPath), &workTopologyMarker{
		Kind: workMarkerKindUnified,
		ResidueSources: []workResidueSource{
			{Scope: "fe", Database: "fe"},
			{Scope: "be", Database: "be", Drained: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	r := runClassMigrationCheck(t, cityPath, classMigrationConfig(t, `
[beads.work]
scope = "unified"
`))
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, "work: scope=unified target=managed markers=work.unified residue=1") {
		t.Fatalf("work topology line missing/incorrect:\n%s", joined)
	}
}

func TestClassMigrationCheckWorkMarkerUnstatableIsError(t *testing.T) {
	cityPath := t.TempDir()
	if err := os.MkdirAll(nudgesdb.StoreDir(cityPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// An unparseable work marker fails closed, same discipline as the class
	// markers.
	if err := os.WriteFile(workRemoteMarkerPath(cityPath), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runClassMigrationCheck(t, cityPath, classMigrationConfig(t, ""))
	if r.Status != doctor.StatusError || r.Severity != doctor.SeverityBlocking {
		t.Fatalf("unstatable work marker = (%v, %v), want blocking error: %s", r.Status, r.Severity, r.Message)
	}
}

func TestClassMigrationCheckRoutedAndPending(t *testing.T) {
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	cfg := classMigrationConfig(t, `
[beads.classes.orders]
backend = "sqlite"

[beads.classes.nudges]
backend = "sqlite"
`)
	r := runClassMigrationCheck(t, cityPath, cfg)
	if r.Status != doctor.StatusOK {
		t.Fatalf("status = %v: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "1 class(es) awaiting first-boot migration") || !strings.Contains(r.Message, "1 routed") {
		t.Fatalf("message %q", r.Message)
	}
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, "orders: backend=sqlite marker=present routing=sqlite") ||
		!strings.Contains(joined, "nudges: backend=sqlite marker=absent routing=bd") {
		t.Fatalf("details:\n%s", joined)
	}
}

func TestClassMigrationCheckRollbackWarns(t *testing.T) {
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	r := runClassMigrationCheck(t, cityPath, classMigrationConfig(t, ""))
	if r.Status != doctor.StatusWarning {
		t.Fatalf("rollback status = %v, want warning: %s", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "escape hatch") {
		t.Fatalf("message %q", r.Message)
	}
}

func TestClassMigrationCheckShadowAnnotated(t *testing.T) {
	r := runClassMigrationCheck(t, t.TempDir(), classMigrationConfig(t, `
[beads.classes.sessions]
shadow = true
`))
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, "sessions: backend=bd marker=absent routing=bd shadow=on") {
		t.Fatalf("details:\n%s", joined)
	}
}

func TestClassMigrationCheckUnstatableMarkerIsError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based stat failures do not apply to root")
	}
	cityPath := t.TempDir()
	writeOrdersMigratedMarker(t, cityPath)
	if err := os.Chmod(nudgesdb.StoreDir(cityPath), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(nudgesdb.StoreDir(cityPath), 0o755) })
	r := runClassMigrationCheck(t, cityPath, classMigrationConfig(t, ""))
	if r.Status != doctor.StatusError || r.Severity != doctor.SeverityBlocking {
		t.Fatalf("unstatable marker = (%v, %v), want blocking error: %s", r.Status, r.Severity, r.Message)
	}
	if !strings.Contains(r.Message, "fails closed") {
		t.Fatalf("message %q", r.Message)
	}
}
