package beads

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// --- scripted pgx pool test harness ---------------------------------------

// pgResult is a canned response for one query class.
type pgResult struct {
	rows [][]any
	err  error
}

// scriptedPGPool is a pgxQuerier that answers by query class, records every
// query text (so tests can assert on the emitted SQL), and lets a test rewrite
// a class mid-run to simulate out-of-band mutation.
type scriptedPGPool struct {
	mu      sync.Mutex
	results map[string]pgResult
	queries []string
}

func newScriptedPGPool() *scriptedPGPool {
	return &scriptedPGPool{results: map[string]pgResult{}}
}

func (p *scriptedPGPool) set(class string, res pgResult) {
	p.mu.Lock()
	p.results[class] = res
	p.mu.Unlock()
}

func (p *scriptedPGPool) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	p.mu.Lock()
	p.queries = append(p.queries, sql)
	res, ok := p.results[classifyPGQuery(sql)]
	p.mu.Unlock()
	if ok {
		if res.err != nil {
			return nil, res.err
		}
		return &fakePGRows{rows: res.rows}, nil
	}
	switch classifyPGQuery(sql) {
	case "beads", "labels", "hydrate_deps", "deplist", "deferred_children":
		return &fakePGRows{}, nil
	default:
		return nil, fmt.Errorf("scriptedPGPool: unhandled query class %q for %q", classifyPGQuery(sql), firstLine(sql))
	}
}

func (p *scriptedPGPool) Close() {}

func (p *scriptedPGPool) sawQueryContaining(sub string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, q := range p.queries {
		if strings.Contains(q, sub) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func classifyPGQuery(sql string) string {
	switch {
	case strings.Contains(sql, "pg_schema_version"):
		return "probe"
	case strings.Contains(sql, "WITH deferred"):
		return "deferred_children"
	case strings.Contains(sql, "tier_rank"):
		return "beads"
	case strings.Contains(sql, "FROM beads.labels"):
		return "labels"
	case strings.Contains(sql, "beads.dependencies WHERE issue_id = ANY"):
		return "hydrate_deps"
	case strings.Contains(sql, "FROM beads.dependencies"):
		return "deplist"
	default:
		return "unknown"
	}
}

// probeRow builds the identity/schema probe result. projectID/schemaVersion may
// be nil to model an absent sentinel.
func probeRow(projectID, schemaVersion any, colCount, tblCount int) pgResult {
	return pgResult{rows: [][]any{{projectID, schemaVersion, colCount, tblCount}}}
}

// goodProbe is a probe that matches projectID with the pinned schema and full
// schema shape.
func goodProbe(projectID string) pgResult {
	return probeRow(projectID, nativePGExpectedSchemaVersion, nativePGRequiredColumnCount(), len(nativePGRequiredTables))
}

// beadRow builds one bead result row in nativePGBeadColumns order plus tier_rank.
func beadRow(id, status, issueType string, tier int16) []any {
	now := time.Now().UTC()
	return []any{
		id, "title-" + id, "", status, 1, issueType,
		nil, now, now, nil, int16(0), int16(0), int16(0), []byte(nil), tier,
	}
}

// fakePGRows is a minimal pgx.Rows backed by [][]any. Only Next/Scan/Err/Close
// are exercised by the store; the rest satisfy the interface.
type fakePGRows struct {
	rows [][]any
	i    int
	err  error
}

func (r *fakePGRows) Close()                                       {}
func (r *fakePGRows) Err() error                                   { return r.err }
func (r *fakePGRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakePGRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakePGRows) Values() ([]any, error)                       { return r.rows[r.i-1], nil }
func (r *fakePGRows) RawValues() [][]byte                          { return nil }
func (r *fakePGRows) Conn() *pgx.Conn                              { return nil }

func (r *fakePGRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *fakePGRows) Scan(dest ...any) error {
	row := r.rows[r.i-1]
	if len(dest) != len(row) {
		return fmt.Errorf("fakePGRows.Scan: %d dest for %d columns", len(dest), len(row))
	}
	for j := range dest {
		if err := assignScanValue(dest[j], row[j]); err != nil {
			return fmt.Errorf("fakePGRows.Scan col %d: %w", j, err)
		}
	}
	return nil
}

// assignScanValue assigns val into the pointer dest, allocating for
// pointer-to-pointer destinations (nullable columns) and converting numeric
// widths, so a single canned row drives every scan shape the store uses.
func assignScanValue(dest, val any) error {
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return fmt.Errorf("dest is not a non-nil pointer: %T", dest)
	}
	target := dv.Elem()
	if val == nil {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}
	src := reflect.ValueOf(val)
	if target.Kind() == reflect.Pointer {
		p := reflect.New(target.Type().Elem())
		if err := setConvert(p.Elem(), src); err != nil {
			return err
		}
		target.Set(p)
		return nil
	}
	return setConvert(target, src)
}

func setConvert(dst, src reflect.Value) error {
	switch {
	case src.Type().AssignableTo(dst.Type()):
		dst.Set(src)
	case src.Type().ConvertibleTo(dst.Type()):
		dst.Set(src.Convert(dst.Type()))
	default:
		return fmt.Errorf("cannot assign %s to %s", src.Type(), dst.Type())
	}
	return nil
}

func newNativeStoreWithPool(t *testing.T, pool pgxQuerier, projectID string, runner CommandRunner) *NativePostgresReadStore {
	t.Helper()
	bd := NewBdStore(t.TempDir(), runner)
	s, err := OpenNativePostgresReadStore(context.Background(), t.TempDir(), bd,
		withNativePostgresPool(pool),
		withNativePostgresProjectID(projectID),
		WithNativePostgresLogger(nil))
	if err != nil {
		t.Fatalf("open native store: %v", err)
	}
	return s
}

// failRunner fails the test if bd is invoked; used to prove a read was served
// natively without falling back.
func failRunner(t *testing.T) CommandRunner {
	return func(_, name string, args ...string) ([]byte, error) {
		t.Errorf("unexpected bd invocation: %s %v", name, args)
		return nil, fmt.Errorf("bd must not be called")
	}
}

// quietBdRunner makes every bd call fail without failing the test. It is used
// where a code path legitimately probes bd best-effort (e.g. the caching store's
// ready-projection version gate) but the reads under test are served natively.
func quietBdRunner() CommandRunner {
	return func(_, _ string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("bd unavailable in test")
	}
}

// --- circuit breaker ------------------------------------------------------

func TestNativePGBreaker(t *testing.T) {
	var b nativePGBreaker
	now := time.Now()

	if !b.usable(now) {
		t.Fatal("fresh breaker must be usable")
	}
	for i := 0; i < nativePGBreakerThreshold-1; i++ {
		b.recordFailure(now)
		if !b.usable(now) {
			t.Fatalf("breaker opened early after %d failures", i+1)
		}
	}
	b.recordFailure(now) // reaches threshold
	if b.usable(now) {
		t.Fatal("breaker should be open at the threshold")
	}
	if b.usable(now.Add(nativePGBreakerCooldown - time.Second)) {
		t.Fatal("breaker should stay open within the cooldown")
	}
	if !b.usable(now.Add(nativePGBreakerCooldown)) {
		t.Fatal("breaker should allow a half-open probe after the cooldown")
	}
	// A failed probe re-arms the cooldown.
	b.recordFailure(now.Add(nativePGBreakerCooldown))
	if b.usable(now.Add(nativePGBreakerCooldown + time.Second)) {
		t.Fatal("failed probe must re-arm the cooldown")
	}
	// A success closes the breaker.
	b.recordSuccess()
	if !b.usable(now.Add(nativePGBreakerCooldown)) {
		t.Fatal("success must close the breaker")
	}
}

// --- identity / schema gate (pure) ----------------------------------------

func TestNativePGProbeEvaluate(t *testing.T) {
	full := nativePGRequiredColumnCount()
	tbl := len(nativePGRequiredTables)
	cases := []struct {
		name   string
		scope  string
		probe  nativePGProbe
		wantOK bool
	}{
		{"match", "prj", nativePGProbe{"prj", "1", full, tbl}, true},
		{"identity mismatch", "prj", nativePGProbe{"other", "1", full, tbl}, false},
		{"absent db sentinel", "prj", nativePGProbe{"", "1", full, tbl}, false},
		{"absent scope id", "", nativePGProbe{"prj", "1", full, tbl}, false},
		{"schema version skew", "prj", nativePGProbe{"prj", "2", full, tbl}, false},
		{"absent schema version", "prj", nativePGProbe{"prj", "", full, tbl}, false},
		{"missing columns", "prj", nativePGProbe{"prj", "1", full - 1, tbl}, false},
		{"missing tables", "prj", nativePGProbe{"prj", "1", full, tbl - 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := tc.probe.evaluate(tc.scope)
			if ok != tc.wantOK {
				t.Fatalf("evaluate ok=%v want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if !ok {
				if reason == "" {
					t.Error("disable path must give a reason")
				}
				// Reason must never leak the actual identity values.
				if strings.Contains(reason, tc.scope) && tc.scope != "" {
					t.Errorf("reason leaks project_id: %q", reason)
				}
			}
		})
	}
}

func TestNativePostgresIdentityGateDisablesOnMismatch(t *testing.T) {
	var calls [][]string
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("db-project-id")) // database belongs to a DIFFERENT project
	store := newNativeStoreWithPool(t, pool, "scope-project-id", fallbackBdRunner(&calls))

	got, err := store.Get("mc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "mc-1" {
		t.Fatalf("Get returned %q via fallback", got.ID)
	}
	if !store.disabled.Load() {
		t.Fatal("identity mismatch must permanently disable the native path")
	}
	assertRan(t, calls, "show")

	// A second read must not re-probe: the native path is latched off.
	before := len(pool.queries)
	if _, err := store.List(ListQuery{AllowScan: true}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pool.queries) != before {
		t.Errorf("disabled store re-queried the pool: %d new queries", len(pool.queries)-before)
	}
}

func TestNativePostgresSchemaVersionMismatchDisables(t *testing.T) {
	var calls [][]string
	pool := newScriptedPGPool()
	pool.set("probe", probeRow("prj", "2", nativePGRequiredColumnCount(), len(nativePGRequiredTables)))
	store := newNativeStoreWithPool(t, pool, "prj", fallbackBdRunner(&calls))

	if _, err := store.List(ListQuery{AllowScan: true}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !store.disabled.Load() {
		t.Fatal("schema-version skew must disable the native path")
	}
	assertRan(t, calls, "list")
}

func TestNativePostgresIdentityGatePassesAndServesNative(t *testing.T) {
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("prj"))
	pool.set("beads", pgResult{rows: [][]any{beadRow("mc-1", "open", "task", 0)}})
	store := newNativeStoreWithPool(t, pool, "prj", failRunner(t))

	got, err := store.Get("mc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "mc-1" {
		t.Fatalf("Get returned %q", got.ID)
	}
	if store.disabled.Load() {
		t.Fatal("matching identity must not disable the native path")
	}
}

// --- exact-Get delegation (gcy-g4o) ---------------------------------------

func TestNativePostgresGetDelegatesMissToBd(t *testing.T) {
	var calls [][]string
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("prj"))
	pool.set("beads", pgResult{rows: nil}) // exact miss: zero rows
	store := newNativeStoreWithPool(t, pool, "prj", fallbackBdRunner(&calls))

	// A native miss must DELEGATE to bd (which runs the substring-collision
	// guard) rather than returning a bare ErrNotFound from the native path.
	got, err := store.Get("mc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "mc-1" {
		t.Fatalf("miss did not delegate to bd: got %q", got.ID)
	}
	assertRan(t, calls, "show")
	if store.disabled.Load() {
		t.Fatal("a healthy miss must not disable the native path")
	}
}

// --- dual-residence dedup -------------------------------------------------

func TestNativePostgresListDedupesDualResidenceById(t *testing.T) {
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("prj"))
	// Same id in both tiers: issues (durable, tier 0) and wisps (tier 1).
	pool.set("beads", pgResult{rows: [][]any{
		beadRow("dup-1", "open", "task", 0),
		beadRow("dup-1", "in_progress", "task", 1),
	}})
	store := newNativeStoreWithPool(t, pool, "prj", failRunner(t))

	rows, err := store.List(ListQuery{AllowScan: true, IncludeClosed: true, TierMode: TierBoth})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	n := 0
	for _, b := range rows {
		if b.ID == "dup-1" {
			n++
			if b.Status != "open" {
				t.Errorf("dedup kept the ephemeral copy (status=%q), want durable issues copy", b.Status)
			}
		}
	}
	if n != 1 {
		t.Fatalf("dual-residence id returned %d times, want 1", n)
	}
}

// --- Ready parity: pinned/status/is_blocked in SQL, templates included -----

func TestNativePostgresReadySQLMirrorsBd(t *testing.T) {
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("prj"))
	pool.set("beads", pgResult{rows: [][]any{beadRow("r-1", "open", "task", 0)}})
	store := newNativeStoreWithPool(t, pool, "prj", failRunner(t))

	if _, err := store.Ready(); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	// bd ready's server-side WHERE, on raw columns.
	for _, want := range []string{"status = 'open'", "(pinned = 0 OR pinned IS NULL)", "is_blocked = 0"} {
		if !pool.sawQueryContaining(want) {
			t.Errorf("Ready query missing %q", want)
		}
	}
	// bd ready returns templates: the candidate query must NOT filter them.
	if pool.sawQueryContaining("is_template") {
		t.Error("Ready candidate query filters templates; bd ready returns them")
	}
}

func TestNativePostgresReadyExcludesDeferredParentChildren(t *testing.T) {
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("prj"))
	pool.set("beads", pgResult{rows: [][]any{
		beadRow("parent", "open", "task", 0),
		beadRow("child", "open", "task", 0),
	}})
	// child's parent is future-deferred: bd ready hides it.
	pool.set("deferred_children", pgResult{rows: [][]any{{"child"}}})
	store := newNativeStoreWithPool(t, pool, "prj", failRunner(t))

	ready, err := store.Ready()
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	for _, b := range ready {
		if b.ID == "child" {
			t.Fatal("child of a future-deferred parent must be excluded from Ready")
		}
	}
	if len(ready) != 1 || ready[0].ID != "parent" {
		t.Fatalf("Ready = %v, want [parent]", ready)
	}
}

// --- SQL injection: status is bound, never concatenated -------------------

func TestNativeListStatusPredicateBindsStatus(t *testing.T) {
	const payload = `x' OR pg_sleep(10)-- `
	clause, args := nativeListStatusPredicate(ListQuery{Status: payload})
	if strings.Contains(clause, payload) || strings.Contains(clause, "pg_sleep") {
		t.Fatalf("status was concatenated into SQL: %q", clause)
	}
	if !strings.Contains(clause, "status = $1") {
		t.Errorf("status not bound as $1: %q", clause)
	}
	if len(args) != 1 || args[0] != payload {
		t.Errorf("payload not passed as a bound arg: %v", args)
	}
	// Non-closed explicit status also excludes the pinned column (bd parity).
	if !strings.Contains(clause, "(pinned = 0 OR pinned IS NULL)") {
		t.Errorf("explicit non-closed status must exclude pinned column: %q", clause)
	}

	// Default list hides closed + pinned status AND the pinned column.
	def, defArgs := nativeListStatusPredicate(ListQuery{})
	if len(defArgs) != 0 {
		t.Errorf("default predicate should bind no args: %v", defArgs)
	}
	if !strings.Contains(def, "(pinned = 0 OR pinned IS NULL)") || !strings.Contains(def, "NOT IN ('closed', 'pinned')") {
		t.Errorf("default predicate missing pinned exclusion: %q", def)
	}
}

// --- fallback WARN carries no DSN coordinates -----------------------------

func TestNativePGErrorClassOmitsCoordinates(t *testing.T) {
	err := fmt.Errorf("failed to connect to `user=ro_user database=bd_prj_ab12`: db.internal:5432 dial error")
	if got := nativePGErrorClass(err); strings.Contains(got, "ro_user") || strings.Contains(got, "bd_prj_ab12") || strings.Contains(got, "5432") {
		t.Errorf("error class leaks DSN coordinates: %q", got)
	}
	if nativePGErrorClass(context.DeadlineExceeded) != "timeout" {
		t.Error("deadline should classify as timeout")
	}
}

// --- reconcile: out-of-band dep removal stays removed (finding: resurrect) --

func TestCachingStoreReconcileNativeWrapperPropagatesDepRemoval(t *testing.T) {
	pool := newScriptedPGPool()
	pool.set("probe", goodProbe("prj"))
	pool.set("beads", pgResult{rows: [][]any{
		beadRow("blocked", "open", "task", 0),
		beadRow("blocker", "open", "task", 0),
	}})
	// Initially "blocked" depends on "blocker".
	pool.set("hydrate_deps", pgResult{rows: [][]any{{"blocked", "blocker", "blocks"}}})

	store := newNativeStoreWithPool(t, pool, "prj", quietBdRunner())
	cs := NewCachingStoreForTest(store, nil)
	if err := cs.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	cs.mu.RLock()
	primed := len(cs.deps["blocked"])
	cs.mu.RUnlock()
	if primed != 1 {
		t.Fatalf("prime cached %d deps for blocked, want 1", primed)
	}

	// Out-of-band `bd dep remove` (bypasses the cache write path): the dep is
	// gone from the store, so the next reconcile List hydrates empty deps.
	pool.set("hydrate_deps", pgResult{rows: nil})
	cs.runReconciliation()

	cs.mu.RLock()
	after := cs.deps["blocked"]
	cs.mu.RUnlock()
	if len(after) != 0 {
		t.Fatalf("reconcile resurrected removed dep with a native-wrapper backing: %v", after)
	}
}
