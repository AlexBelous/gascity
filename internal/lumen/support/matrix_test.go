package support

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestGenerateMatchesCheckedInWhenUpstreamProvided(t *testing.T) {
	root := os.Getenv("LUMEN_UPSTREAM")
	if root == "" {
		t.Skip("set LUMEN_UPSTREAM to the pinned formula-language checkout")
	}
	first, err := Generate(root)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	second, err := Generate(root)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Generate output is not deterministic")
	}
	if !bytes.Equal(append(first, '\n'), embedded) {
		t.Fatal("Generate output differs from checked-in matrix")
	}
}

func TestEmbeddedMatrixClosesPinnedCorpus(t *testing.T) {
	matrix, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if matrix.Upstream.Commit != UpstreamCommit {
		t.Fatalf("upstream commit = %q, want %q", matrix.Upstream.Commit, UpstreamCommit)
	}
	if got := matrix.Counts(); got != (Counts{Total: 129, Selected: 94, SelectedUnimplemented: 26, Deferred: 1, RetiredDiagnostic: 8, Intended: 120}) {
		t.Fatalf("Counts() = %+v", got)
	}
	if err := matrix.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestBuildRejectsDuplicateAndUnreconciledSourceRows(t *testing.T) {
	manifest := []byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[{"id":"one","status":"selected","anchorSource":"syntax.lumen"}]}`)
	duplicate := []byte("/// form-id: one\nthing\n/// form-id: one\nother\n")
	if _, err := Build(duplicate, manifest); err == nil {
		t.Fatal("Build accepted duplicate syntax form ID")
	}
	missing := []byte("/// form-id: one\nthing\n/// form-id: two\nother\n")
	if _, err := Build(missing, manifest); err == nil {
		t.Fatal("Build accepted an unreconciled source row missing from the derivative manifest")
	}
}

func TestBuildRejectsInvalidDerivativeRows(t *testing.T) {
	syntax := []byte("/// form-id: one\nthing\n")
	for _, derivative := range [][]byte{
		[]byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[{"id":"one","status":"selected","anchorSource":"syntax.lumen"},{"id":"extra","status":"selected","anchorSource":"syntax.lumen"}]}`),
		[]byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[{"id":"one","status":"selected","anchorSource":"syntax.lumen"},{"id":"old","status":"selected","anchorSource":"parser-diagnostic"}]}`),
		[]byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[{"id":"one","status":"selected","anchorSource":"unknown"}]}`),
	} {
		if _, err := Build(syntax, derivative); err == nil {
			t.Fatal("Build accepted an invalid derivative row")
		}
	}
}

func TestBuildRequiresExactKnownReconciliationOmissions(t *testing.T) {
	syntax := []byte("/// form-id: syntax.scope-anchor.caller\nthing\n/// form-id: type.handle\nthing\n/// form-id: expr.handle-construct\nthing\n/// form-id: expr.channel-sink\nthing\n")
	if _, err := Build(syntax, []byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[]}`)); err != nil {
		t.Fatalf("Build exact omissions: %v", err)
	}
	derivative := []byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[{"id":"type.handle","status":"selected","anchorSource":"syntax.lumen"}]}`)
	if _, err := Build(syntax, derivative); err == nil {
		t.Fatal("Build accepted a stale reconciliation omission")
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	syntax := []byte("/// form-id: one\n/// form-status: selected\nthing\n")
	manifest := []byte(`{"schemaVersion":"lumen-selected-surface-manifest/v0","release":"0.2.3","status":"canonical-seed","sourceTruth":["docs/spec/ir.lumen","docs/spec/syntax.lumen"],"rows":[{"id":"one","status":"selected","anchorSource":"syntax.lumen"}]}`)
	first, err := Build(syntax, manifest)
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	second, err := Build(syntax, manifest)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("Build output differs:\nfirst: %s\nsecond: %s", first, second)
	}
}

func TestGenerateRejectsWrongUpstreamHead(t *testing.T) {
	if err := validateUpstreamHead("not-the-pinned-commit"); err == nil {
		t.Fatal("validateUpstreamHead accepted a mismatched upstream HEAD")
	}
}

func TestAuthorityInputRejectsPinnedContentDrift(t *testing.T) {
	const path = "docs/spec/syntax.lumen"
	if err := validateAuthorityInput(path, []byte("drift")); err == nil {
		t.Fatal("validateAuthorityInput accepted content that differs from its pinned hash")
	}
}

func TestEnsureCleanAuthorityRejectsDirtyTrackedInput(t *testing.T) {
	bin := t.TempDir()
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("WriteFile fake git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := ensureCleanAuthority(t.TempDir()); err == nil {
		t.Fatal("ensureCleanAuthority accepted a dirty authority input")
	}
}

func TestRuntimeImportGuardRejectsSupportDependency(t *testing.T) {
	filesystem := fstest.MapFS{
		"internal/lumen/kernel/kernel.go":  &fstest.MapFile{Data: []byte("package kernel\nimport _ \"github.com/gastownhall/gascity/internal/lumen/support\"\n")},
		"internal/lumen/support/matrix.go": &fstest.MapFile{Data: []byte("package support\n")},
	}
	if err := CheckRuntimeImports(filesystem); err == nil {
		t.Fatal("CheckRuntimeImports accepted a runtime dependency on support")
	}
	if err := CheckRuntimeImports(fstest.MapFS{"internal/lumen/kernel/kernel.go": &fstest.MapFile{Data: []byte("package kernel\n")}}); err != nil {
		t.Fatalf("CheckRuntimeImports without support dependency: %v", err)
	}
	if err := CheckRuntimeImports(fstest.MapFS{"internal/lumen/kernel/kernel_test.go": &fstest.MapFile{Data: []byte("package kernel\nimport _ \"github.com/gastownhall/gascity/internal/lumen/support\"\n")}}); err == nil {
		t.Fatal("CheckRuntimeImports accepted a test dependency on support")
	}
}

func TestRepositoryRuntimeImportGuard(t *testing.T) {
	if err := CheckRuntimeImports(os.DirFS("../../..")); err != nil {
		t.Fatalf("CheckRuntimeImports(repository): %v", err)
	}
}

func TestMatrixFailsClosedOnUnresolvedAndConflictingSurfaces(t *testing.T) {
	matrix, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	routes, err := loadRoutes()
	if err != nil {
		t.Fatalf("loadRoutes: %v", err)
	}
	for _, claim := range routes.NonClaims {
		if !matrix.HasFailClosedNonClaim(claim.ID) {
			t.Errorf("missing fail-closed non-claim %q", claim.ID)
		}
	}
}

func TestOwnerTableAssignsEveryIntendedRowOnce(t *testing.T) {
	matrix, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	routes, err := loadRoutes()
	if err != nil {
		t.Fatalf("loadRoutes: %v", err)
	}
	routeByID := map[string]Route{}
	for _, route := range routes.Routes {
		routeByID[route.ID] = route
	}
	seen := map[string]bool{}
	for _, row := range matrix.Rows {
		if row.Status != "selected" && row.Status != "selected-unimplemented" {
			continue
		}
		if routeByID[row.ID].PlanKey != row.PlanKey {
			t.Errorf("route[%q] = %q, matrix = %q", row.ID, routeByID[row.ID].PlanKey, row.PlanKey)
		}
		seen[row.ID] = true
	}
	if len(seen) != len(routeByID) {
		t.Fatalf("matrix intended rows = %d, route rows = %d", len(seen), len(routeByID))
	}
	for _, want := range []struct{ id, owner string }{
		{"step.block", "A5"},
		{"step.exec", "A5"},
		{"expr.literal", "A5"},
		{"expr.builtins", "B3"},
		{"expr.fn.indexOf", "B3"},
		{"expr.operator.as", "B8"},
		{"expr.operator.is", "B8"},
		{"expr.outcomeof", "B9"},
		{"event.send", "B11"},
	} {
		if got := routeByID[want.id].PlanKey; got != want.owner {
			t.Errorf("route[%q] = %q, want %q", want.id, got, want.owner)
		}
	}
}

func TestMatrixRejectsArtifactAndLawDrift(t *testing.T) {
	matrix, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	matrix.Artifacts[0].SHA256 = "wrong"
	if err := matrix.Validate(); err == nil {
		t.Fatal("Validate accepted pinned artifact drift")
	}
	matrix, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	matrix.HostLaws = append(matrix.HostLaws, matrix.HostLaws[0])
	if err := matrix.Validate(); err == nil {
		t.Fatal("Validate accepted duplicate host law")
	}
	matrix, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	matrix.NonClaims[0].Citations = nil
	if err := matrix.Validate(); err == nil {
		t.Fatal("Validate accepted an uncited non-claim")
	}
}

func TestReviewedCatalogCohortsAreExactAndOrdered(t *testing.T) {
	var catalog routeCatalog
	if err := json.Unmarshal(embeddedRoutes, &catalog); err != nil {
		t.Fatalf("decode embedded routes: %v", err)
	}
	if got, want := lawIDs(catalog.EngineLaws), []string{
		"engine.identity.fresh", "engine.outcome.four-arm-projection", "engine.value.subshape-binding", "engine.outcome.authored-scope", "engine.expression.scalar-pure", "engine.expression.structural-ordered", "engine.scheduling.prefix-after", "engine.prompt.render-or-invoke", "engine.loop.retry-repeat-timeout", "engine.handlers.recover-cleanup", "engine.dispatch.first-match-exhaustive", "engine.async.await-detach", "engine.ownership.draining-freeze", "engine.cancellation.advisory-terminal-wins", "engine.channel.delivery-cardinality", "engine.channel.terminal-owner-quiescence", "engine.channel.lines-framing", "engine.run.target-environment-input", "engine.run.durability-mode", "engine.run.metadata", "engine.map.structural-outcome-order", "engine.reduce.collector-lifecycle", "engine.agent-session.prompt-target", "engine.module-package.binding", "engine.macro-include.normalization", "engine.function.call-default-scope", "engine.evaluation.resource-budget", "engine.exec.batch-result",
	}; !equalStrings(got, want) {
		t.Fatalf("engine laws = %v, want %v", got, want)
	}
	if got, want := lawIDs(catalog.HostLaws), []string{
		"host.journal.committed-prefix", "host.journal.single-writer-recovery", "host.capture.inline-by-value", "host.recovery.private-host-run-key", "host.controller.exclusive-admission", "host.attached-run.explicit-formula-typed-sse", "host.outcome.shell-status", "host.cancellation.private-in-language", "host.detach.ownership", "host.nested-run.modes", "host.effect.exec-live", "host.effect.prompt-durable", "host.effect.commit-window-honesty", "host.effect.sealed-deterministic", "host.budget.durable-capture", "host.package.immutable-snapshot", "host.telemetry.private-authored",
	}; !equalStrings(got, want) {
		t.Fatalf("host laws = %v, want %v", got, want)
	}
	if got, want := nonClaimIDs(catalog.NonClaims), []string{
		"async-handle.render.direct", "async-handle.render.nested", "async-handle.render.interpolated", "split.empty-separator", "parseLumen", "channel.tee", "channel.isClosed", "channel.isFailed", "channel.top-level-runtime-identity", "public.run-id", "public.run-event", "public.run-edition-pin", "public.provenance", "public.attach", "public.replay", "run.with-agent", "run.with-session", "detached-root.address", "events.subscription.events-equals", "cli.detach", "cli.reattach", "cli.sigint-host-cancellation", "daemon.auto-start", "portable.runtime-state", "cli.lumen-serve", "cli.lumen-repl", "cli.lumen-c", "cli.lumen-runtime", "operator.cancel", "operator.suspend", "operator.resume", "operator.recovery", "public.telemetry", "public.control", "privacy.encrypted-carrier", "privacy.external-carrier", "privacy.redacted-carrier", "capacity.pending", "capacity.scheduled", "capacity.suspended", "public.run-event.seven-arm-lifecycle",
	}; !equalStrings(got, want) {
		t.Fatalf("non-claims = %v, want %v", got, want)
	}
}

func TestLoadRoutesRejectsAnyReviewedCohortMutation(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(*routeCatalog)
	}{
		{"missing engine law", func(c *routeCatalog) { c.EngineLaws = c.EngineLaws[1:] }},
		{"extra engine law", func(c *routeCatalog) { c.EngineLaws = append(c.EngineLaws, c.EngineLaws[0]) }},
		{"reordered engine laws", func(c *routeCatalog) { c.EngineLaws[0], c.EngineLaws[1] = c.EngineLaws[1], c.EngineLaws[0] }},
		{"missing host law", func(c *routeCatalog) { c.HostLaws = c.HostLaws[1:] }},
		{"duplicate host law", func(c *routeCatalog) { c.HostLaws = append(c.HostLaws, c.HostLaws[0]) }},
		{"reordered host laws", func(c *routeCatalog) { c.HostLaws[0], c.HostLaws[1] = c.HostLaws[1], c.HostLaws[0] }},
		{"missing non-claim", func(c *routeCatalog) { c.NonClaims = c.NonClaims[1:] }},
		{"extra non-claim", func(c *routeCatalog) { c.NonClaims = append(c.NonClaims, c.NonClaims[0]) }},
		{"reordered non-claims", func(c *routeCatalog) { c.NonClaims[0], c.NonClaims[1] = c.NonClaims[1], c.NonClaims[0] }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			original := embeddedRoutes
			t.Cleanup(func() { embeddedRoutes = original })
			var catalog routeCatalog
			if err := json.Unmarshal(original, &catalog); err != nil {
				t.Fatalf("decode embedded routes: %v", err)
			}
			mutate.apply(&catalog)
			data, err := json.Marshal(catalog)
			if err != nil {
				t.Fatalf("marshal mutated routes: %v", err)
			}
			embeddedRoutes = data
			if _, err := loadRoutes(); err == nil {
				t.Fatal("loadRoutes accepted a changed reviewed cohort")
			}
		})
	}
}

func lawIDs(laws []Law) []string {
	ids := make([]string, len(laws))
	for i, law := range laws {
		ids[i] = law.ID
	}
	return ids
}

func nonClaimIDs(claims []NonClaim) []string {
	ids := make([]string, len(claims))
	for i, claim := range claims {
		ids[i] = claim.ID
	}
	return ids
}

func equalStrings(got, want []string) bool {
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}
