package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bdEmbeddedSQLRefusal is the verbatim failure a bd serving a non-Dolt backend
// returns for `bd sql`, as gc's runner composes it: bd writes
// `Error: 'bd sql' is not yet supported in embedded mode` to stderr
// (cmd/bd/sql.go HandleError) and classifyBDExecResult wraps it onto the exit
// status.
const bdEmbeddedSQLRefusal = "exit status 1: Error: 'bd sql' is not yet supported in embedded mode"

// bdBlockedRefusal is how a bd too old to carry the blocked verb — or one whose
// storage cannot answer it — fails gc's runner.
const bdBlockedRefusal = "exit status 1: Error: unknown command \"blocked\" for \"bd\""

const bdForwardSchemaSkewSQLFailure = "exit status 1: Warning: schema skew ignored — database (v59) is ahead of binary (v53); some queries may fail\n[mysql] read tcp 127.0.0.1:55892->127.0.0.1:30029: i/o timeout"

// noProjectionDoorRunner answers `bd version` like bd 1.1.0 and refuses BOTH
// projection doors: `bd sql` the way a bd serving a backend it cannot open a
// SQL session against does, and `bd blocked` the way a bd that has no such verb
// does. It is the only state in which a scope is genuinely out of doors, which
// is what the latch has always meant.
func noProjectionDoorRunner() *recordingRunner {
	r := &recordingRunner{}
	r.reply = func(args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "version":
			return []byte("bd version 1.1.0\n"), nil
		case len(args) > 0 && args[0] == "sql":
			return nil, errors.New(bdEmbeddedSQLRefusal)
		case len(args) > 0 && args[0] == "blocked":
			return nil, errors.New(bdBlockedRefusal)
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	return r
}

// blockedDoorRunner is maintainer-city's live shape: `bd sql` is not
// implemented on the backend bd opened, and `bd blocked` answers.
func blockedDoorRunner(reply string) *recordingRunner {
	r := &recordingRunner{}
	r.reply = func(args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "version":
			return []byte("bd version 1.1.0\n"), nil
		case len(args) > 0 && args[0] == "sql":
			return nil, errors.New(bdEmbeddedSQLRefusal)
		case len(args) > 0 && args[0] == "blocked":
			return []byte(reply), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	return r
}

func writeScopeMetadata(t *testing.T, scope string, meta map[string]any) {
	t.Helper()
	beadsDir := filepath.Join(scope, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
}

func activeWorkBeads() []Bead {
	return []Bead{
		{ID: "mc-1", Type: "task", Status: "open"},
		{ID: "mc-2", Type: "task", Status: "open"},
	}
}

// TestReadyProjectionLatchesOnTheEmbeddedModeRefusal is the live maintainer-city
// defect: bd serves that city's work class from hosted Postgres, `bd sql` is not
// implemented there, and the capability gate only ever asked bd its VERSION — so
// every cache prime and every reconcile spent a guaranteed-failing 6-16s
// subprocess, forever.
//
// The refusal is a permanent property of the ledger in front of the process, so
// it is latched: each door is tried exactly once and the operator is told once.
// The latch silences the SUBPROCESS and the NOTICE, not the verdict — every
// later enrichment still reports the degrade, because that error is how each
// cache over this scope learns to send its readiness reads to the live backing
// (CachingStore.readyReadsMustGoLive).
func TestReadyProjectionLatchesOnTheEmbeddedModeRefusal(t *testing.T) {
	runner := noProjectionDoorRunner()
	notices := &bytes.Buffer{}
	s := NewBdStore(t.TempDir(), runner.run, WithBdStoreNoticeSink(notices))

	first, err := s.enrichReadyProjectionForCache(activeWorkBeads())
	if !errors.Is(err, ErrReadyProjectionUnsupported) {
		t.Fatalf("first enrich error = %v, want ErrReadyProjectionUnsupported", err)
	}
	for _, want := range []string{"not yet supported in embedded mode", `unknown command "blocked"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("degrade does not carry %q; both doors must be named or an operator cannot tell which one to fix: %v", want, err)
		}
	}
	for _, b := range first {
		if b.IsBlocked != nil {
			t.Errorf("bead %s was enriched from a failed projection: %v", b.ID, *b.IsBlocked)
		}
	}

	var silentCycles []int
	for i := 2; i <= 5; i++ {
		if _, err := s.enrichReadyProjectionForCache(activeWorkBeads()); !errors.Is(err, ErrReadyProjectionUnsupported) {
			silentCycles = append(silentCycles, i)
		}
	}

	for _, verb := range []string{"sql", "blocked"} {
		calls := 0
		for _, call := range runner.calls {
			if len(call) > 1 && call[1] == verb {
				calls++
			}
		}
		if calls != 1 {
			t.Fatalf("bd %s ran %d times across 5 enrichments (calls=%v); the latch must spend exactly one of each door", verb, calls, runner.calls)
		}
	}
	if len(silentCycles) != 0 {
		t.Fatalf("enrich cycles %v stopped naming the degrade; a cache that primes on one of them would serve readiness from a projection it does not have", silentCycles)
	}
	if got := strings.Count(notices.String(), "ready-projection enrichment disabled"); got != 1 {
		t.Fatalf("operator notice printed %d times, want exactly 1:\n%s", got, notices.String())
	}
	if !strings.Contains(notices.String(), "not yet supported in embedded mode") {
		t.Errorf("operator notice does not name the cause:\n%s", notices.String())
	}
}

// TestReadyProjectionOnAnUnimplementedBackendTakesTheBlockedDoor is the
// capability gate proper, restated now that the gate selects a door rather than
// switching the projection off.
//
// `bd sql` support is a property of the BACKEND, and the gate's reason for
// withholding it on a backend gc does not implement is that gc cannot assume
// that backend's SCHEMA carries gc's issues/wisps projection. That reason does
// not reach `bd blocked`, which is bd's own verb over its own blocked role, so
// the scope gets its column from there instead of losing it. `bd sql` is still
// never spent — that is the part of the gate that must not regress.
func TestReadyProjectionOnAnUnimplementedBackendTakesTheBlockedDoor(t *testing.T) {
	scope := t.TempDir()
	writeScopeMetadata(t, scope, map[string]any{
		"database":   "dolt",
		"backend":    "postgres",
		"dolt_mode":  "server",
		"project_id": "d2e95604-e869-478c-ad1a-ddee6e8bc3fc",
	})
	runner := blockedDoorRunner(`[{"id":"mc-2","blocked_by_count":1,"blocked_by":["mc-1"]}]`)
	notices := &bytes.Buffer{}
	s := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(notices))

	out, err := s.enrichReadyProjectionForCache(activeWorkBeads())
	if err != nil {
		t.Fatalf("enrichReadyProjectionForCache on an unimplemented backend = %v, want the blocked door to serve it", err)
	}
	byID := make(map[string]Bead, len(out))
	for _, b := range out {
		byID[b.ID] = b
	}
	// mc-1 is absent from bd's answer, which means NOT BLOCKED and must be
	// written as such: left nil it falls to the direct-dependency predicate the
	// projection exists to replace.
	for id, wantBlocked := range map[string]bool{"mc-1": false, "mc-2": true} {
		got := byID[id].IsBlocked
		if got == nil || *got != wantBlocked {
			t.Errorf("bead %s is_blocked = %v, want &%v", id, got, wantBlocked)
		}
	}
	for _, call := range runner.calls {
		if len(call) > 1 && call[1] == "sql" {
			t.Fatalf("an unimplemented backend spent %v; gc cannot assume that backend's schema", runner.calls)
		}
	}
	if notices.Len() != 0 {
		t.Errorf("a scope that can answer the projection printed a degrade notice:\n%s", notices.String())
	}
}

// TestReadyProjectionLatchesAnUnimplementedBackendWhoseBlockedDoorAlsoFails is
// the fail-closed half: when neither door answers, the scope reaches exactly the
// verdict it reached before this door existed, with the same bound — one notice,
// one attempt per door.
func TestReadyProjectionLatchesAnUnimplementedBackendWhoseBlockedDoorAlsoFails(t *testing.T) {
	scope := t.TempDir()
	writeScopeMetadata(t, scope, map[string]any{
		"database":   "dolt",
		"backend":    "postgres",
		"dolt_mode":  "server",
		"project_id": "d2e95604-e869-478c-ad1a-ddee6e8bc3fc",
	})
	runner := noProjectionDoorRunner()
	notices := &bytes.Buffer{}
	s := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(notices))

	for i := 1; i <= 3; i++ {
		out, err := s.enrichReadyProjectionForCache(activeWorkBeads())
		if !errors.Is(err, ErrReadyProjectionUnsupported) {
			t.Fatalf("enrich #%d error = %v, want ErrReadyProjectionUnsupported on every cycle", i, err)
		}
		if !strings.Contains(err.Error(), `unknown command "blocked"`) {
			t.Errorf("degrade #%d does not name the door that failed: %v", i, err)
		}
		for _, b := range out {
			if b.IsBlocked != nil {
				t.Errorf("bead %s was enriched by a scope with no working door", b.ID)
			}
		}
	}

	blockedCalls := 0
	for _, call := range runner.calls {
		if len(call) > 1 && call[1] == "sql" {
			t.Fatalf("an unimplemented backend spent %v; gc cannot assume that backend's schema", runner.calls)
		}
		if len(call) > 1 && call[1] == "blocked" {
			blockedCalls++
		}
	}
	if blockedCalls != 1 {
		t.Fatalf("bd blocked ran %d times across 3 enrichments (calls=%v); the latch must spend exactly one", blockedCalls, runner.calls)
	}
	if got := strings.Count(notices.String(), "ready-projection enrichment disabled"); got != 1 {
		t.Fatalf("operator notice printed %d times, want exactly 1:\n%s", got, notices.String())
	}
}

// TestReadyProjectionVerdictIsPerScopeAcrossStoreRebuilds is the bound that
// makes "once" mean anything.
//
// Nothing in gc holds one BdStore per scope for the life of the process:
// cmd/gc's scoped stores are built per request, and the control-dispatcher
// readiness scan rebuilds a store per scope every controlReadyCacheTTL (3s) and
// primes it immediately, so a verdict memoized on the store object is
// re-derived — and re-announced — a few times a minute, forever. That is the
// same defect the sibling unread-store notice already had to fix, so this
// reuses its registry pattern.
//
// The verdict is still REPORTED to every rebuilt store, because each one backs a
// fresh cache that must learn to send readiness reads live; what the scope bound
// removes is the repeated notice and the repeated failing subprocess.
func TestReadyProjectionVerdictIsPerScopeAcrossStoreRebuilds(t *testing.T) {
	rebuild := func(t *testing.T, scope string, notices *bytes.Buffer) [][]string {
		t.Helper()
		var calls [][]string
		for i := 1; i <= 5; i++ {
			runner := noProjectionDoorRunner()
			s := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(notices))
			if _, err := s.enrichReadyProjectionForCache(activeWorkBeads()); !errors.Is(err, ErrReadyProjectionUnsupported) {
				t.Fatalf("rebuild #%d enrich error = %v, want ErrReadyProjectionUnsupported", i, err)
			}
			calls = append(calls, runner.calls...)
		}
		return calls
	}

	t.Run("backend gate", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, map[string]any{"database": "dolt", "backend": "postgres"})
		notices := &bytes.Buffer{}
		calls := rebuild(t, scope, notices)
		blockedCalls := 0
		for _, call := range calls {
			if len(call) > 1 && call[1] == "sql" {
				t.Fatalf("rebuilt stores spent %v; the gate must never let `bd sql` reach an unimplemented backend", calls)
			}
			if len(call) > 1 && call[1] == "blocked" {
				blockedCalls++
			}
		}
		if blockedCalls != 1 {
			t.Fatalf("bd blocked ran %d times across 5 stores over one scope (calls=%v); the latch must survive the rebuild", blockedCalls, calls)
		}
		if got := strings.Count(notices.String(), "ready-projection enrichment disabled"); got != 1 {
			t.Fatalf("operator notice printed %d times across 5 stores over one scope, want exactly 1:\n%s", got, notices.String())
		}
	})

	t.Run("runtime latch", func(t *testing.T) {
		scope := t.TempDir()
		writeScopeMetadata(t, scope, map[string]any{"database": "dolt", "backend": "dolt", "dolt_mode": "server"})
		notices := &bytes.Buffer{}
		calls := rebuild(t, scope, notices)
		sqlCalls := 0
		for _, call := range calls {
			if len(call) > 1 && call[1] == "sql" {
				sqlCalls++
			}
		}
		if sqlCalls != 1 {
			t.Fatalf("bd sql ran %d times across 5 stores over one scope (calls=%v); the latch must survive the rebuild", sqlCalls, calls)
		}
		if got := strings.Count(notices.String(), "ready-projection enrichment disabled"); got != 1 {
			t.Fatalf("operator notice printed %d times across 5 stores over one scope, want exactly 1:\n%s", got, notices.String())
		}
	})
}

// TestReadyProjectionRuntimeBlockedDoorSurvivesStoreRebuilds bounds the OTHER
// runtime verdict: not "this scope is out of doors" (the latch above), but "this
// scope's `bd sql` is refused while `bd blocked` answers", which must persist
// per scope exactly as the degrade does.
//
// The metadata names a backend this build registers (dolt), so the capability
// gate returns the SQL door — `bd sql` is not withheld up front. Only bd's
// runtime refusal ("not yet supported in embedded mode") reveals the backend was
// opened embedded, and `bd blocked` answers in its place. A choice recorded only
// on the store object is re-derived on every rebuild: cmd/gc builds a store per
// request and the control-ready scan rebuilds one per scope every
// controlReadyCacheTTL (3s), so the SQL door would be re-picked and the failing
// 6-16s `bd sql` re-spent a few times a minute, forever — the exact pathology
// this door was added to remove. Latching the choice in the scope guard lets a
// fresh store start on the blocked door with no `bd sql` spent, while still
// serving the column on every rebuild.
func TestReadyProjectionRuntimeBlockedDoorSurvivesStoreRebuilds(t *testing.T) {
	scope := t.TempDir()
	// A registered backend: the gate returns the SQL door, so `bd sql` is
	// attempted and only bd's runtime refusal routes to the blocked door.
	writeScopeMetadata(t, scope, map[string]any{"database": "dolt", "backend": "dolt", "dolt_mode": "server"})
	notices := &bytes.Buffer{}

	var calls [][]string
	for i := 1; i <= 5; i++ {
		runner := blockedDoorRunner(`[{"id":"mc-2","blocked_by_count":1,"blocked_by":["mc-1"]}]`)
		s := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(notices))
		out, err := s.enrichReadyProjectionForCache(activeWorkBeads())
		if err != nil {
			t.Fatalf("rebuild #%d enrich = %v, want the blocked door to serve the column", i, err)
		}
		byID := make(map[string]Bead, len(out))
		for _, b := range out {
			byID[b.ID] = b
		}
		for id, wantBlocked := range map[string]bool{"mc-1": false, "mc-2": true} {
			got := byID[id].IsBlocked
			if got == nil || *got != wantBlocked {
				t.Errorf("rebuild #%d bead %s is_blocked = %v, want &%v", i, id, got, wantBlocked)
			}
		}
		calls = append(calls, runner.calls...)
	}

	sqlCalls, blockedCalls := 0, 0
	for _, call := range calls {
		if len(call) > 1 && call[1] == "sql" {
			sqlCalls++
		}
		if len(call) > 1 && call[1] == "blocked" {
			blockedCalls++
		}
	}
	// Exactly one: the runtime refusal is proven once on the first store, then
	// the blocked-door choice persists across rebuilds. More than one is the
	// re-spend defect; zero would mean the runtime-refusal path was never
	// exercised (a vacuous pass).
	if sqlCalls != 1 {
		t.Fatalf("bd sql ran %d times across 5 stores over one scope (calls=%v); want exactly 1 — the refused `bd sql` must be spent once and the blocked-door choice must survive the rebuild", sqlCalls, calls)
	}
	if blockedCalls != 5 {
		t.Fatalf("bd blocked ran %d times across 5 stores over one scope (calls=%v); every rebuild must still serve the column through the blocked door", blockedCalls, calls)
	}
	if notices.Len() != 0 {
		t.Errorf("a scope that answers through the blocked door printed a degrade notice:\n%s", notices.String())
	}
}

// Use the real BdStore projection and Ready doors with in-memory snapshot rows.
// This owns only the wiring from a forward-schema verdict to canonical Ready.
type forwardSkewCacheBacking struct {
	Store
	source *BdStore
}

func (s *forwardSkewCacheBacking) enrichReadyProjectionForCache(items []Bead) ([]Bead, error) {
	return s.source.enrichReadyProjectionForCache(items)
}

func (s *forwardSkewCacheBacking) Ready(query ...ReadyQuery) ([]Bead, error) {
	return s.source.Ready(query...)
}

func TestReadyProjectionForwardSchemaSkewUsesCanonicalReady(t *testing.T) {
	for _, reply := range []string{"ready", "empty", "empty envelope", "malformed", "null", "null envelope", "object", "null row", "empty object", "id-less", "blank id", "status-less", "blank status", "mixed invalid", "error"} {
		t.Run(reply, func(t *testing.T) {
			scope := t.TempDir()
			writeScopeMetadata(t, scope, map[string]any{"database": "dolt", "backend": "dolt", "dolt_mode": "server"})
			mem := NewMemStore()
			ids := map[string]string{}
			for _, name := range []string{"unattributable", "parent", "child", "ephemeral", "safe"} {
				bead, err := mem.Create(Bead{Type: "task", Status: "open", Title: name, Ephemeral: name == "ephemeral"})
				if err != nil {
					t.Fatal(err)
				}
				ids[name] = bead.ID
			}
			if err := mem.DepAdd(ids["child"], ids["parent"], "parent-child"); err != nil {
				t.Fatal(err)
			}
			notices := &bytes.Buffer{}
			sqlCalls, blockedCalls, readyCalls := 0, 0, 0
			runner := &recordingRunner{}
			runner.reply = func(args []string) ([]byte, error) {
				switch args[0] {
				case "version":
					return []byte("bd version 1.2.2\n"), nil
				case "sql":
					sqlCalls++
					return nil, errors.New(bdForwardSchemaSkewSQLFailure)
				case "blocked":
					blockedCalls++
					return []byte(`[]`), nil // Cannot attribute the hidden blocked work.
				case "ready":
					readyCalls++
					switch reply {
					case "empty":
						return []byte(`[]`), nil
					case "empty envelope":
						return []byte(`{"issues":[]}`), nil
					case "null envelope":
						return []byte(`{"issues":null}`), nil
					case "object":
						return []byte(`{"id":"wrong-shape"}`), nil
					case "blank id":
						return []byte(`[{"id":"   ","status":"open"}]`), nil
					case "blank status":
						return []byte(`[{"id":"blank-status","status":"   "}]`), nil
					case "malformed":
						return []byte(`[{`), nil
					case "null":
						return []byte(`null`), nil
					case "null row":
						return []byte(`[null]`), nil
					case "empty object":
						return []byte(`[{}]`), nil
					case "id-less":
						return []byte(`[{"issue_type":"task","status":"open"}]`), nil
					case "status-less":
						return []byte(`[{"id":"statusless","issue_type":"task"}]`), nil
					case "mixed invalid":
						return []byte(fmt.Sprintf(`[{"id":%q,"issue_type":"task","status":"open"},{"status":"open"}]`, ids["safe"])), nil
					case "error":
						return nil, errors.New("canonical ready unavailable")
					}
					return []byte(fmt.Sprintf(`[{"id":%q,"title":"safe","issue_type":"task","status":"open"}]`, ids["safe"])), nil
				}
				return nil, fmt.Errorf("unexpected command: %v", args)
			}
			for rebuild := 0; rebuild < 2; rebuild++ {
				source := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(notices))
				cache := NewCachingStoreForTest(&forwardSkewCacheBacking{Store: mem, source: source}, nil)
				if err := cache.Prime(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !cache.readyReadsMustGoLive() {
					t.Fatal("forward schema must disable cached readiness")
				}
				if _, ok := cache.CachedReady(); ok {
					t.Fatal("cache offered a readiness verdict without complete is_blocked")
				}
				for _, query := range []ReadyQuery{{}, {TierMode: TierBoth}, {Limit: 1}} {
					rows, err := cache.Ready(query)
					switch reply {
					case "ready":
						if err != nil || len(rows) != 1 || rows[0].ID != ids["safe"] {
							t.Fatalf("canonical ready lost: rows=%+v err=%v", rows, err)
						}
					case "empty", "empty envelope":
						if err != nil || len(rows) != 0 {
							t.Fatalf("valid empty ready: rows=%+v err=%v", rows, err)
						}
					default:
						if err == nil {
							t.Fatalf("%s ready became success: %+v", reply, rows)
						}
					}
					if reply == "mixed invalid" {
						var partial *PartialResultError
						if !errors.As(err, &partial) || len(rows) != 1 || rows[0].ID != ids["safe"] {
							t.Fatalf("cache lost partial error or retained corrupt row: rows=%+v err=%v", rows, err)
						}
					}
				}
			}
			if sqlCalls != 1 || blockedCalls != 0 || readyCalls != 6 {
				t.Fatalf("calls sql=%d blocked=%d ready=%d", sqlCalls, blockedCalls, readyCalls)
			}
			if strings.Count(notices.String(), "ready-projection enrichment disabled") != 1 || !strings.Contains(notices.String(), "ahead of binary") {
				t.Fatalf("missing one causal notice: %s", notices)
			}
		})
	}
}

func TestReadyProjectionTransientSQLFailureDoesNotLatch(t *testing.T) {
	scope := t.TempDir()
	runner := &recordingRunner{}
	sqlCalls := 0
	runner.reply = func(args []string) ([]byte, error) {
		if args[0] == "version" {
			return []byte("bd version 1.2.2\n"), nil
		}
		if args[0] == "sql" {
			sqlCalls++
			return nil, errors.New("temporary transport failure")
		}
		return nil, fmt.Errorf("unexpected command: %v", args)
	}
	for rebuild := 0; rebuild < 2; rebuild++ {
		source := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(&bytes.Buffer{}))
		_, err := source.enrichReadyProjectionForCache(activeWorkBeads())
		if err == nil || errors.Is(err, ErrReadyProjectionUnsupported) || source.latchedReadyProjectionDegrade() != nil {
			t.Fatalf("transient failure latched or disappeared: %v", err)
		}
	}
	if sqlCalls != 2 {
		t.Fatalf("SQL calls=%d, want retry on rebuild", sqlCalls)
	}
}

func TestReadyProjectionForwardSchemaSkewClassifierIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"exact production shape", errors.New(bdForwardSchemaSkewSQLFailure), true},
		{"ordinary timeout", errors.New("read tcp: i/o timeout"), false},
		{"warning without binary verdict", errors.New("schema skew ignored"), false},
		{"ahead without explicit override", errors.New("database is ahead of binary"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBdSQLForwardSchemaSkewFailure(tc.err); got != tc.want {
				t.Fatalf("classifier = %v, want %v for %v", got, tc.want, tc.err)
			}
		})
	}
}

// TestReadyProjectionNoticeDoesNotClaimReadinessIsUnaffected pins the operator
// line to what actually happens. An earlier draft said "no work is lost", which
// named the wrong risk: the degraded predicate is permissive, not lossy, so the
// cache would OFFER work whose gate has not opened. The notice must say where
// readiness comes from instead.
func TestReadyProjectionNoticeDoesNotClaimReadinessIsUnaffected(t *testing.T) {
	scope := t.TempDir()
	writeScopeMetadata(t, scope, map[string]any{"database": "dolt", "backend": "postgres"})
	notices := &bytes.Buffer{}
	s := NewBdStore(scope, noProjectionDoorRunner().run, WithBdStoreNoticeSink(notices))
	if _, err := s.enrichReadyProjectionForCache(activeWorkBeads()); !errors.Is(err, ErrReadyProjectionUnsupported) {
		t.Fatalf("enrich error = %v, want ErrReadyProjectionUnsupported", err)
	}

	notice := notices.String()
	for _, banned := range []string{"no work is lost", "dependency-derived readiness"} {
		if strings.Contains(notice, banned) {
			t.Errorf("notice claims %q, which is not what the degrade does:\n%s", banned, notice)
		}
	}
	for _, want := range []string{"live `bd ready`", "other cached reads keep serving", "no further projection subprocess is spent"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice does not say %q:\n%s", want, notice)
		}
	}
}

// TestReadyProjectionOnAnImplementedBackendIsUnchanged is the byte-identity
// guard for the five Dolt cities. Their metadata names a backend this build
// implements, so the gate is inert: the exact same two commands run, in the same
// order, and the rows come back enriched.
//
// Mutation proof: flipping "dolt" to any backend this build does not register
// empties runner.calls (the sibling test above), and deleting the latch in
// fetchReadyProjection re-runs `bd sql` on every call
// (TestReadyProjectionLatchesOnTheEmbeddedModeRefusal).
func TestReadyProjectionOnAnImplementedBackendIsUnchanged(t *testing.T) {
	for _, backend := range []string{"dolt", "doltlite", ""} {
		t.Run("backend="+backend, func(t *testing.T) {
			scope := t.TempDir()
			meta := map[string]any{"database": "dolt", "dolt_mode": "server", "dolt_database": "hq"}
			if backend != "" {
				meta["backend"] = backend
			}
			writeScopeMetadata(t, scope, meta)
			runner := readyProjectionRunner(`[{"id":"mc-1","is_blocked":false},{"id":"mc-2","is_blocked":true}]`)
			notices := &bytes.Buffer{}
			s := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(notices))

			out, err := s.enrichReadyProjectionForCache(activeWorkBeads())
			if err != nil {
				t.Fatalf("enrichReadyProjectionForCache: %v", err)
			}
			want := [][]string{
				{"bd", "version"},
				{"bd", "sql", readyProjectionSQL(), "--json"},
			}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("bd invocations = %v, want %v", runner.calls, want)
			}
			byID := make(map[string]Bead, len(out))
			for _, b := range out {
				byID[b.ID] = b
			}
			for id, wantBlocked := range map[string]bool{"mc-1": false, "mc-2": true} {
				got := byID[id].IsBlocked
				if got == nil || *got != wantBlocked {
					t.Errorf("bead %s is_blocked = %v, want &%v", id, got, wantBlocked)
				}
			}
			if notices.Len() != 0 {
				t.Errorf("an implemented backend printed a degrade notice:\n%s", notices.String())
			}
		})
	}
}

// TestReadyProjectionUnreadableMetadataFallsThroughToTheLatch keeps the gate
// fail-open: metadata gc cannot read is not evidence about the backend, so the
// version probe still runs and the runtime latch is what catches the refusal.
func TestReadyProjectionUnreadableMetadataFallsThroughToTheLatch(t *testing.T) {
	scope := t.TempDir()
	beadsDir := filepath.Join(scope, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	runner := noProjectionDoorRunner()
	s := NewBdStore(scope, runner.run, WithBdStoreNoticeSink(&bytes.Buffer{}))

	if _, err := s.enrichReadyProjectionForCache(activeWorkBeads()); !errors.Is(err, ErrReadyProjectionUnsupported) {
		t.Fatalf("enrich error = %v, want the runtime latch verdict", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %v, want the version probe, one refused sql and the refused blocked door behind it", runner.calls)
	}
}
