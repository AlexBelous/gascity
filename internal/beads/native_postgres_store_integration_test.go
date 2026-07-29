//go:build integration

package beads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/pgauth"
)

// TestNativePostgresDifferential is the correctness judge for the native
// Postgres read store: for every overridden read method it compares the native
// result against the per-call BdStore on the live work store and asserts
// identical normalized results. It skips cleanly when the scope, bd binary, or
// database/credentials are unreachable so it is safe in environments without the
// live store.
//
// Configuration is env-var first; the hardcoded values are documented
// last-resort fallbacks for the maintainer machine only:
//
//   - GC_NATIVE_PG_TEST_SCOPE — the postgres-backed scope to compare against
//     (default: /data/projects/maintainer-city).
//   - BD_BIN — the reference bd binary whose output native results are compared
//     to. BD_BIN is the authoritative knob for pinning an exact reference build;
//     the default resolves the newest mc-postgres release on disk so it does not
//     silently rot to a skip when a hash-stamped release is rotated (see
//     defaultBDBin).
//   - GC_NATIVE_PG_TEST_SAMPLE — per-id sample size (default 12).
func TestNativePostgresDifferential(t *testing.T) {
	scope := envOrDefault("GC_NATIVE_PG_TEST_SCOPE", "/data/projects/maintainer-city")
	bdBin := envOrDefault("BD_BIN", defaultBDBin())

	if _, err := os.Stat(filepath.Join(scope, ".beads", "metadata.json")); err != nil {
		t.Skipf("scope %s has no .beads/metadata.json: %v", scope, err)
	}
	if !NativePostgresReadActivatedForScope(scope) {
		t.Skipf("scope %s is not a postgres native-read scope", scope)
	}
	if _, err := os.Stat(bdBin); err != nil {
		t.Skipf("reference bd binary %s unavailable: %v", bdBin, err)
	}

	bd := NewBdStore(scope, ExecCommandRunnerWithEnv(map[string]string{"BD_BIN": bdBin}))
	native, err := OpenNativePostgresReadStore(context.Background(), scope, bd,
		WithNativePostgresLogger(nil),
		// The native read plane resolves its password through the same canonical
		// pgauth chain gc's bd write plane uses.
		WithNativePostgresPasswordResolver(func(scopeRoot string, endpoint PostgresEndpoint) (string, error) {
			resolved, rerr := pgauth.ResolveFromEnv(nil, scopeRoot, pgauth.Endpoint{
				Host: endpoint.Host, Port: endpoint.Port, User: endpoint.User,
			})
			if rerr != nil {
				return "", rerr
			}
			return resolved.Password, nil
		}),
	)
	if err != nil {
		t.Skipf("cannot open native postgres store (db/credentials unreachable?): %v", err)
	}
	defer native.CloseStore() //nolint:errcheck

	// Connectivity probe: nativeListCtx does NOT fall back, so a failure here
	// means the database or credentials are unreachable → skip, don't fail.
	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := native.nativeListCtx(probeCtx, ListQuery{AllowScan: true, IncludeClosed: true, TierMode: TierBoth}); err != nil {
		t.Skipf("native postgres unreachable, skipping differential: %v", err)
	}

	listShapes := []struct {
		name string
		q    ListQuery
	}{
		{"issues-open", ListQuery{AllowScan: true}},
		{"issues-all", ListQuery{AllowScan: true, IncludeClosed: true}},
		{"both-open", ListQuery{AllowScan: true, TierMode: TierBoth}},
		{"both-all", ListQuery{AllowScan: true, TierMode: TierBoth, IncludeClosed: true}},
		{"wisps-all", ListQuery{AllowScan: true, TierMode: TierWisps, IncludeClosed: true}},
		{"status-open", ListQuery{Status: "open"}},
		{"status-closed", ListQuery{Status: "closed"}},
		{"type-task", ListQuery{Type: "task", AllowScan: true, IncludeClosed: true}},
		{"type-molecule-both", ListQuery{Type: "molecule", AllowScan: true, IncludeClosed: true, TierMode: TierBoth}},
		{"sorted-desc", ListQuery{AllowScan: true, IncludeClosed: true, Sort: SortCreatedDesc, Limit: 25}},
	}
	for _, shape := range listShapes {
		t.Run("List/"+shape.name, func(t *testing.T) {
			bdRows, bdErr := bd.List(shape.q)
			nvRows, nvErr := native.List(shape.q)
			if (bdErr == nil) != (nvErr == nil) {
				t.Fatalf("error mismatch: bd=%v native=%v", bdErr, nvErr)
			}
			compareBeadSets(t, bdRows, nvRows)
		})
	}

	// Sample ids across types for Get / DepList / Children.
	sampleAll, err := bd.List(ListQuery{AllowScan: true, IncludeClosed: true, TierMode: TierBoth})
	if err != nil {
		t.Fatalf("sample list: %v", err)
	}
	sampleIDs := pickSampleIDs(sampleAll, sampleSize())

	t.Run("Get", func(t *testing.T) {
		for _, id := range sampleIDs {
			bdBead, bdErr := bd.Get(id)
			nvBead, nvErr := native.Get(id)
			if (bdErr == nil) != (nvErr == nil) {
				t.Errorf("%s: error mismatch bd=%v native=%v", id, bdErr, nvErr)
				continue
			}
			if bdErr != nil {
				continue
			}
			if bp, np := project(bdBead), project(nvBead); !reflect.DeepEqual(bp, np) {
				t.Errorf("Get %s mismatch:\n bd=%+v\n nv=%+v", id, bp, np)
			}
		}
	})

	t.Run("DepList", func(t *testing.T) {
		for _, id := range sampleIDs {
			for _, dir := range []string{"down", "up"} {
				bdDeps, bdErr := bd.DepList(id, dir)
				nvDeps, nvErr := native.DepList(id, dir)
				if (bdErr == nil) != (nvErr == nil) {
					t.Errorf("%s/%s: error mismatch bd=%v native=%v", id, dir, bdErr, nvErr)
					continue
				}
				if !reflect.DeepEqual(sortedDeps(bdDeps), sortedDeps(nvDeps)) {
					t.Errorf("DepList %s/%s mismatch:\n bd=%v\n nv=%v", id, dir, sortedDeps(bdDeps), sortedDeps(nvDeps))
				}
			}
		}
	})

	t.Run("Children", func(t *testing.T) {
		for _, id := range sampleIDs {
			bdKids, _ := bd.Children(id, IncludeClosed)
			nvKids, _ := native.Children(id, IncludeClosed)
			compareBeadSets(t, bdKids, nvKids)
		}
	})

	t.Run("Ready", func(t *testing.T) {
		for _, tier := range []TierMode{TierIssues, TierBoth} {
			bdReady, bdErr := bd.Ready(ReadyQuery{TierMode: tier})
			nvReady, nvErr := native.Ready(ReadyQuery{TierMode: tier})
			if (bdErr == nil) != (nvErr == nil) {
				t.Errorf("tier %d: error mismatch bd=%v native=%v", tier, bdErr, nvErr)
				continue
			}
			compareBeadSets(t, bdReady, nvReady)
		}
	})

	t.Run("ListByLabel", func(t *testing.T) {
		label := firstLabel(sampleAll)
		if label == "" {
			t.Skip("no labels present in sample")
		}
		bdRows, bdErr := bd.ListByLabel(label, 0, IncludeClosed)
		nvRows, nvErr := native.ListByLabel(label, 0, IncludeClosed)
		if bdErr != nil || nvErr != nil {
			t.Fatalf("ListByLabel errors: bd=%v native=%v", bdErr, nvErr)
		}
		compareBeadSets(t, bdRows, nvRows)
	})

	t.Run("ListByMetadata", func(t *testing.T) {
		key, value := firstMetadata(sampleAll)
		if key == "" {
			t.Skip("no metadata present in sample")
		}
		bdRows, bdErr := bd.ListByMetadata(map[string]string{key: value}, 0, IncludeClosed)
		nvRows, nvErr := native.ListByMetadata(map[string]string{key: value}, 0, IncludeClosed)
		if bdErr != nil || nvErr != nil {
			t.Fatalf("ListByMetadata errors: bd=%v native=%v", bdErr, nvErr)
		}
		compareBeadSets(t, bdRows, nvRows)
	})

	t.Run("ListByAssignee", func(t *testing.T) {
		assignee := firstAssignee(sampleAll)
		if assignee == "" {
			t.Skip("no assignee present in sample")
		}
		bdRows, bdErr := bd.ListByAssignee(assignee, "closed", 0)
		nvRows, nvErr := native.ListByAssignee(assignee, "closed", 0)
		if bdErr != nil || nvErr != nil {
			t.Fatalf("ListByAssignee errors: bd=%v native=%v", bdErr, nvErr)
		}
		compareBeadSets(t, bdRows, nvRows)
	})

	t.Run("Count", func(t *testing.T) {
		ctx := context.Background()
		for _, shape := range []ListQuery{
			{AllowScan: true, IncludeClosed: true},
			{AllowScan: true, IncludeClosed: true, TierMode: TierBoth},
			{Type: "task", AllowScan: true, IncludeClosed: true},
		} {
			bdRows, err := bd.List(shape)
			if err != nil {
				t.Fatalf("bd list for count: %v", err)
			}
			n, err := native.Count(ctx, shape)
			if err != nil {
				t.Fatalf("native count: %v", err)
			}
			if n != len(bdRows) {
				t.Errorf("Count %+v = %d, bd List cardinality = %d", shape, n, len(bdRows))
			}
		}
	})

	reportTiming(t, bd, native, sampleIDs)
}

func reportTiming(t *testing.T, bd *BdStore, native *NativePostgresReadStore, ids []string) {
	if len(ids) == 0 {
		return
	}
	id := ids[0]

	start := time.Now()
	_, _ = bd.Get(id)
	bdGet := time.Since(start)
	start = time.Now()
	_, _ = native.Get(id)
	nvGet := time.Since(start)

	q := ListQuery{AllowScan: true, IncludeClosed: true, TierMode: TierBoth}
	start = time.Now()
	_, _ = bd.List(q)
	bdList := time.Since(start)
	start = time.Now()
	_, _ = native.List(q)
	nvList := time.Since(start)

	t.Logf("timing Get:  bd=%s native=%s (%.1fx)", bdGet, nvGet, ratio(bdGet, nvGet))
	t.Logf("timing List: bd=%s native=%s (%.1fx)", bdList, nvList, ratio(bdList, nvList))
}

func ratio(a, b time.Duration) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// cmpBead is the normalized projection compared across stores; it canonicalizes
// slice/map ordering and formats times so bd and native answers are comparable.
type cmpBead struct {
	ID, Title, Status, Type, Assignee, From, ParentID, Description string
	Priority                                                       *int
	Created, Updated, Defer                                        string
	Ephemeral, NoHistory                                           bool
	IsBlocked                                                      *bool
	Labels                                                         []string
	Metadata                                                       map[string]string
	Deps                                                           []string
}

func project(b Bead) cmpBead {
	c := cmpBead{
		ID: b.ID, Title: b.Title, Status: b.Status, Type: b.Type,
		Assignee: b.Assignee, From: b.From, ParentID: b.ParentID, Description: b.Description,
		Priority: b.Priority, Ephemeral: b.Ephemeral, NoHistory: b.NoHistory, IsBlocked: b.IsBlocked,
		Created: b.CreatedAt.UTC().Format(time.RFC3339),
		Updated: b.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if b.DeferUntil != nil {
		c.Defer = b.DeferUntil.UTC().Format(time.RFC3339)
	}
	if len(b.Labels) > 0 {
		c.Labels = append([]string(nil), b.Labels...)
		sort.Strings(c.Labels)
	}
	if len(b.Metadata) > 0 {
		c.Metadata = canonicalizeMetadata(b.Metadata)
	}
	c.Deps = sortedDeps(b.Dependencies)
	return c
}

// canonicalizeMetadata normalizes JSON-valued metadata so bd and native compare
// equal. bd pretty-prints nested-object metadata values (like the shared-memory
// _memory blob) in its --json output; the native store returns the jsonb
// canonical compact form. The two are semantically identical, so each value that
// parses as JSON is re-marshaled to Go's canonical (sorted-key, compact) form;
// plain-string values are left untouched.
func canonicalizeMetadata(m StringMap) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = canonicalJSONValue(v)
	}
	return out
}

func canonicalJSONValue(v string) string {
	var x interface{}
	if err := json.Unmarshal([]byte(v), &x); err != nil {
		return v
	}
	b, err := json.Marshal(x)
	if err != nil {
		return v
	}
	return string(b)
}

func sortedDeps(deps []Dep) []string {
	if len(deps) == 0 {
		return nil
	}
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.IssueID+"|"+d.DependsOnID+"|"+d.Type)
	}
	sort.Strings(out)
	return out
}

func compareBeadSets(t *testing.T, bdRows, nvRows []Bead) {
	t.Helper()
	bdMap := indexByID(bdRows)
	nvMap := indexByID(nvRows)
	if len(bdMap) != len(nvMap) {
		t.Errorf("cardinality mismatch: bd=%d native=%d; %s", len(bdMap), len(nvMap), setDiff(bdMap, nvMap))
	}
	for id, bp := range bdMap {
		np, ok := nvMap[id]
		if !ok {
			t.Errorf("bead %s present in bd, absent in native", id)
			continue
		}
		if !reflect.DeepEqual(bp, np) {
			t.Errorf("bead %s mismatch:\n bd=%+v\n nv=%+v", id, bp, np)
		}
	}
	for id := range nvMap {
		if _, ok := bdMap[id]; !ok {
			t.Errorf("bead %s present in native, absent in bd", id)
		}
	}
}

func indexByID(rows []Bead) map[string]cmpBead {
	m := make(map[string]cmpBead, len(rows))
	for _, b := range rows {
		m[b.ID] = project(b)
	}
	return m
}

func setDiff(bdMap, nvMap map[string]cmpBead) string {
	var onlyBd, onlyNv []string
	for id := range bdMap {
		if _, ok := nvMap[id]; !ok {
			onlyBd = append(onlyBd, id)
		}
	}
	for id := range nvMap {
		if _, ok := bdMap[id]; !ok {
			onlyNv = append(onlyNv, id)
		}
	}
	sort.Strings(onlyBd)
	sort.Strings(onlyNv)
	return "only-bd=" + shortList(onlyBd) + " only-native=" + shortList(onlyNv)
}

func shortList(ids []string) string {
	if len(ids) > 8 {
		ids = append(ids[:8:8], "...")
	}
	out := "["
	for i, id := range ids {
		if i > 0 {
			out += " "
		}
		out += id
	}
	return out + "]"
}

func pickSampleIDs(rows []Bead, n int) []string {
	ids := make([]string, 0, len(rows))
	for _, b := range rows {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	if len(ids) <= n {
		return ids
	}
	step := len(ids) / n
	out := make([]string, 0, n)
	for i := 0; i < len(ids) && len(out) < n; i += step {
		out = append(out, ids[i])
	}
	return out
}

func firstLabel(rows []Bead) string {
	for _, b := range rows {
		if len(b.Labels) > 0 {
			return b.Labels[0]
		}
	}
	return ""
}

func firstMetadata(rows []Bead) (string, string) {
	for _, b := range rows {
		for k, v := range b.Metadata {
			return k, v
		}
	}
	return "", ""
}

func firstAssignee(rows []Bead) string {
	for _, b := range rows {
		if b.Assignee != "" {
			return b.Assignee
		}
	}
	return ""
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// defaultBDBin resolves the reference bd binary for the maintainer machine as a
// documented fallback only — BD_BIN is the authoritative override. It prefers
// the newest mc-postgres release on disk so a rotated hash-stamped build does
// not silently turn the differential into a skip; the historical pinned path is
// the final fallback.
func defaultBDBin() string {
	if matches, _ := filepath.Glob("/opt/beads/releases/mc-postgres-*/bd"); len(matches) > 0 {
		sort.Strings(matches)
		return matches[len(matches)-1]
	}
	return "/opt/beads/releases/mc-postgres-p0-1ac61d049704/bd"
}

// sampleSize bounds the per-id bd calls (Get/DepList/Children) so the
// differential runs in a few minutes against the ~4s bd shell. Override with
// GC_NATIVE_PG_TEST_SAMPLE to widen coverage.
func sampleSize() int {
	if v := os.Getenv("GC_NATIVE_PG_TEST_SAMPLE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 12
}

// NativePostgresReadActivatedForScope reports whether scope declares a postgres
// backend with a native endpoint, independent of the activation flag (the
// integration test constructs the native store directly).
func NativePostgresReadActivatedForScope(scope string) bool {
	data, err := os.ReadFile(filepath.Join(scope, ".beads", "metadata.json"))
	if err != nil {
		return false
	}
	var meta struct {
		Backend         string `json:"backend"`
		StorageEndpoint string `json:"storage_endpoint"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.Backend == "postgres" && meta.StorageEndpoint != ""
}
