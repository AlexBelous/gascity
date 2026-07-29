package beads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gastownhall/gascity/internal/beads/contract"
)

// NativePostgresReadActivated reports whether a scope should use the in-process
// native Postgres read path: the activation flag is set AND the scope's
// metadata declares a postgres backend with a native read endpoint
// (storage_endpoint). cmd/gc consults this at store-open time to choose the
// native store over the dolt native path.
//
// It reads the SAME predicate the preflight (hasPostgresNativeReadForm) and the
// opener (resolveNativePostgresDSN) key on — a storage_endpoint — so the three
// gates never disagree. BEADS_POSTGRES_URL is NOT an activation source here: it
// is a process-global override that would point every scope at one database, so
// it is honored only as a last-resort fallback for a scope that has no
// storage_endpoint of its own (resolveNativePostgresDSN), never as a reason to
// activate the native path.
func NativePostgresReadActivated(scopeRoot string) bool {
	if !contract.NativePostgresReadEnabled() {
		return false
	}
	data, err := os.ReadFile(filepath.Join(scopeRoot, ".beads", "metadata.json"))
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
	return strings.TrimSpace(meta.Backend) == "postgres" &&
		strings.TrimSpace(meta.StorageEndpoint) != ""
}

// BeadsPostgresURLEnv, when set, supplies the full connection DSN for the
// native Postgres read path. It is honored ONLY for a scope whose metadata has
// no storage_endpoint of its own; a per-scope storage_endpoint always wins, so
// a lingering process-global URL cannot silently redirect one scope's reads to
// another scope's database.
const BeadsPostgresURLEnv = "BEADS_POSTGRES_URL"

// BeadsStoreNameNativePostgresStore is the diagnostic store name for the
// native Postgres read store.
const BeadsStoreNameNativePostgresStore = "NativePostgresReadStore"

// nativePGExpectedSchemaVersion is the beads.metadata `pg_schema_version` the
// hardcoded read SQL is pinned to. bd stamps this row when it provisions the
// postgres backend; a mismatch means bd shipped a schema whose semantics the
// frozen SQL may no longer reproduce, so the native path disables and every
// read falls back to bd (which is always version-correct). Re-confirm and bump
// this only alongside the SQL when a new pg schema version is adopted.
const nativePGExpectedSchemaVersion = "1"

const (
	// nativePGMaxConns bounds the read pool; the native path is read-only and
	// bursts come from the controller reconcile scan, not sustained fan-out.
	nativePGMaxConns = 4
	// nativePGConnectTimeout bounds a single dial so a dead endpoint falls back
	// to BdStore quickly instead of stalling every read for the acquire budget.
	nativePGConnectTimeout = 10 * time.Second
	// nativePGOpTimeout caps one native read attempt end-to-end (dial + acquire
	// + query). On expiry the method falls back to the embedded BdStore.
	nativePGOpTimeout = 30 * time.Second
	// nativePGFallbackLogInterval rate-limits the fallback WARN so a sustained
	// outage logs periodically rather than once per read.
	nativePGFallbackLogInterval = 60 * time.Second

	// nativePGBreakerThreshold is the number of consecutive native failures
	// that trips the circuit breaker. Once tripped, reads go straight to bd
	// with zero pool wait until a half-open probe succeeds.
	nativePGBreakerThreshold = 3
	// nativePGBreakerCooldown is how long the breaker stays open before it
	// allows a single half-open probe of the native path.
	nativePGBreakerCooldown = 60 * time.Second
)

// pgxQuerier is the read surface the native store needs from its pool.
// *pgxpool.Pool satisfies it; tests inject a stub to exercise the fallback,
// identity, and schema paths without a live database.
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Close()
}

// PostgresEndpoint identifies the Postgres target a password resolution applies
// to. It mirrors internal/pgauth.Endpoint so cmd/gc can bridge the two without
// beads importing an upper layer.
type PostgresEndpoint struct {
	Host string
	Port string
	User string
}

// PostgresPasswordResolver resolves the password for endpoint within scopeRoot.
// cmd/gc injects the canonical internal/pgauth resolver (env tiers +
// $BEADS_CREDENTIALS_FILE + 0600 enforcement); tests inject a fake. It is
// injected rather than imported so the Layer-0 beads package does not depend on
// pgauth (which imports the event bus). A nil resolver disables the
// split-metadata credential path — only a full BEADS_POSTGRES_URL then works.
type PostgresPasswordResolver func(scopeRoot string, endpoint PostgresEndpoint) (string, error)

// NativePostgresReadStore is a hybrid Store: it serves hot READ methods from a
// direct, in-process Postgres connection pool and delegates every write (and any
// unimplemented method) to the embedded per-call *BdStore. It exists to kill the
// ~4.2s-per-call `gc → bd` shell on postgres-backed work stores while keeping the
// write path — and the fallback for any read it cannot serve — byte-for-byte on
// bd.
//
// Safety layers, cheapest first:
//   - disabled: a permanent latch set when open-time verification proves the
//     pool reads the wrong database (project-identity mismatch) or an
//     incompatible schema. Once set, every read goes straight to bd forever.
//   - breaker: a consecutive-failure circuit breaker; while open, reads skip
//     the pool entirely (zero acquire wait) and use bd until a half-open probe
//     succeeds.
//   - per-read fallback: on any connection/SQL/scan error a method logs once
//     (rate-limited, no secrets) and returns the embedded BdStore's answer.
//
// Get resolves exact hits natively and DELEGATES every miss to bd so bd's
// substring-collision guard (gcy-g4o) still runs — the native path never emits
// a bare ErrNotFound.
type NativePostgresReadStore struct {
	*BdStore
	pool             pgxQuerier
	scopeRoot        string
	logger           *slog.Logger
	passwordResolver PostgresPasswordResolver

	// projectID is the scope's declared project_id (metadata.json). The
	// open-time identity gate compares it to the database's _project_id
	// sentinel; both must be present and equal for the native path to activate.
	projectID string

	closeOnce sync.Once

	// disabled is the permanent native-off latch (identity/schema mismatch).
	disabled atomic.Bool

	// verifyMu guards the one-time open verification. verified caches a
	// confirmed-good database so the probe runs once, not per read; on an
	// inconclusive (connection) failure it stays false and retries. It is an
	// atomic so the post-verification hot path skips the mutex entirely.
	verifyMu sync.Mutex
	verified atomic.Bool

	breaker nativePGBreaker

	logMu   sync.Mutex
	lastLog time.Time
}

// Interface guards: the native store is a Store (via the embedded BdStore),
// a Counter, and carries the conditional-writes stamp (promoted from BdStore).
var (
	_ Store                        = (*NativePostgresReadStore)(nil)
	_ Counter                      = (*NativePostgresReadStore)(nil)
	_ conditionalWritesModeCarrier = (*NativePostgresReadStore)(nil)
)

// NativePostgresReadStoreOption customizes OpenNativePostgresReadStore.
type NativePostgresReadStoreOption func(*NativePostgresReadStore)

// WithNativePostgresLogger sets the logger used for rate-limited fallback
// diagnostics. A nil logger disables fallback logging.
func WithNativePostgresLogger(logger *slog.Logger) NativePostgresReadStoreOption {
	return func(s *NativePostgresReadStore) {
		s.logger = logger
	}
}

// WithNativePostgresPasswordResolver injects the credential resolver used for
// the split-metadata (storage_endpoint) form. cmd/gc wires the canonical
// internal/pgauth resolver here.
func WithNativePostgresPasswordResolver(resolver PostgresPasswordResolver) NativePostgresReadStoreOption {
	return func(s *NativePostgresReadStore) {
		s.passwordResolver = resolver
	}
}

// withNativePostgresPool injects a pool directly, bypassing DSN resolution and
// dialing. It is unexported and used by tests to supply a stub querier.
func withNativePostgresPool(pool pgxQuerier) NativePostgresReadStoreOption {
	return func(s *NativePostgresReadStore) {
		s.pool = pool
	}
}

// withNativePostgresProjectID sets the scope project_id used by the identity
// gate. Unexported; the production path sets it from resolved metadata, and
// tests use it alongside a stub pool.
func withNativePostgresProjectID(projectID string) NativePostgresReadStoreOption {
	return func(s *NativePostgresReadStore) {
		s.projectID = projectID
	}
}

// OpenNativePostgresReadStore resolves the scope's Postgres connection, opens a
// lazy read pool, and returns a store that embeds bd for writes and fallback.
// The pool is lazy: construction does not dial, so an unreachable endpoint does
// not fail the open — the first failing read falls back to bd instead. The
// project-identity and schema gate runs lazily on the first native read.
func OpenNativePostgresReadStore(ctx context.Context, scopeRoot string, bd *BdStore, opts ...NativePostgresReadStoreOption) (*NativePostgresReadStore, error) {
	if bd == nil {
		return nil, fmt.Errorf("native postgres read store at %s: embedded bd store is required", scopeRoot)
	}
	s := &NativePostgresReadStore{
		BdStore:   bd,
		scopeRoot: scopeRoot,
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.pool != nil {
		// A pre-supplied pool (tests) skips DSN resolution and dialing.
		return s, nil
	}
	dsn, err := resolveNativePostgresDSN(scopeRoot, os.Getenv, s.passwordResolver)
	if err != nil {
		return nil, fmt.Errorf("native postgres read store at %s: %w", scopeRoot, err)
	}
	if s.projectID == "" {
		s.projectID = dsn.projectID
	}
	pool, err := openNativePostgresPool(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("native postgres read store at %s: %w", scopeRoot, err)
	}
	s.pool = pool
	return s, nil
}

// nativePostgresDSN is the resolved connection material for a scope. password
// and database are injected onto the parsed config separately so a full URL
// (BEADS_POSTGRES_URL) and the split metadata form share one open path. The
// password is only ever held in memory here and on the pool config; it is never
// logged or embedded in an error.
type nativePostgresDSN struct {
	baseURL   string // storage_endpoint, or the full BEADS_POSTGRES_URL
	password  string // "" when the URL already carries credentials
	database  string // storage_database; "" when the URL carries the dbname
	projectID string // scope metadata project_id, for the identity gate
}

// resolveNativePostgresDSN resolves the scope's connection. A per-scope
// storage_endpoint ALWAYS wins: the process-global BEADS_POSTGRES_URL override
// is consulted only when the scope has no storage_endpoint of its own, so a
// lingering URL cannot redirect a scope's reads to the wrong database. The
// password for the split (endpoint) form comes from the injected resolver
// (the canonical internal/pgauth chain in production). getenv and resolver are
// injectable for tests.
func resolveNativePostgresDSN(scopeRoot string, getenv func(string) string, resolver PostgresPasswordResolver) (nativePostgresDSN, error) {
	metaPath := filepath.Join(scopeRoot, ".beads", "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nativePostgresDSN{}, fmt.Errorf("reading %s: %w", metaPath, err)
	}
	var meta struct {
		StorageEndpoint string `json:"storage_endpoint"`
		StorageDatabase string `json:"storage_database"`
		ProjectID       string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nativePostgresDSN{}, fmt.Errorf("parsing %s: %w", metaPath, err)
	}
	endpoint := strings.TrimSpace(meta.StorageEndpoint)
	projectID := strings.TrimSpace(meta.ProjectID)
	if endpoint == "" {
		// No per-scope endpoint: fall back to the process URL override (which
		// carries its own credentials), if present.
		if getenv != nil {
			if dsn := strings.TrimSpace(getenv(BeadsPostgresURLEnv)); dsn != "" {
				return nativePostgresDSN{baseURL: dsn, projectID: projectID}, nil
			}
		}
		return nativePostgresDSN{}, fmt.Errorf("metadata %s: storage_endpoint is missing", metaPath)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nativePostgresDSN{}, fmt.Errorf("parsing storage_endpoint: %w", err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	if resolver == nil {
		return nativePostgresDSN{}, fmt.Errorf("native postgres: no credential resolver configured for %s", metaPath)
	}
	password, err := resolver(scopeRoot, PostgresEndpoint{Host: host, Port: port, User: u.User.Username()})
	if err != nil {
		return nativePostgresDSN{}, fmt.Errorf("resolving postgres credentials: %w", err)
	}
	return nativePostgresDSN{
		baseURL:   endpoint,
		password:  password,
		database:  strings.TrimSpace(meta.StorageDatabase),
		projectID: projectID,
	}, nil
}

// openNativePostgresPool builds a lazy read pool from the resolved DSN. The pool
// does not dial until the first query, so an unreachable endpoint surfaces at
// read time (and falls back) rather than at open time.
func openNativePostgresPool(ctx context.Context, dsn nativePostgresDSN) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing postgres endpoint: %w", err)
	}
	if dsn.password != "" {
		cfg.ConnConfig.Password = dsn.password
	}
	if dsn.database != "" {
		cfg.ConnConfig.Database = dsn.database
	}
	cfg.MaxConns = nativePGMaxConns
	cfg.ConnConfig.ConnectTimeout = nativePGConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening postgres pool: %w", err)
	}
	return pool, nil
}

// CloseStore closes the read pool. Idempotent. The embedded BdStore holds no
// closable handle, so only the pool is released.
func (s *NativePostgresReadStore) CloseStore() error {
	s.closeOnce.Do(func() {
		if s.pool != nil {
			s.pool.Close()
		}
	})
	return nil
}

// nativeUsable reports whether the native path may be attempted at all: it is
// not permanently disabled and the circuit breaker is not open. It performs no
// I/O, so a disabled/open store adds zero pool-acquire latency.
func (s *NativePostgresReadStore) nativeUsable() bool {
	return !s.disabled.Load() && s.breaker.usable(time.Now())
}

// ensureVerified runs the one-time open verification: it reads the database's
// project-identity sentinel and schema markers and compares them to the scope's
// declared identity and the schema the read SQL is pinned to. On a conclusive
// mismatch it permanently disables the native path (logged once) and returns an
// error. On an inconclusive connection failure it returns the error without
// disabling so a later read retries. A confirmed-good database is cached so the
// probe runs once.
func (s *NativePostgresReadStore) ensureVerified(ctx context.Context) error {
	if s.verified.Load() {
		return nil
	}
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	if s.verified.Load() {
		return nil
	}
	if s.disabled.Load() {
		return errors.New("native postgres disabled")
	}
	probe, err := s.probeDatabase(ctx)
	if err != nil {
		// Inconclusive (could not read the sentinel): do NOT disable — this is
		// a transient connectivity failure that the breaker/fallback handles.
		return fmt.Errorf("native postgres verify: %w", err)
	}
	if reason, ok := probe.evaluate(s.projectID); !ok {
		s.disabled.Store(true)
		s.logDisabled(reason)
		return fmt.Errorf("native postgres disabled: %s", reason)
	}
	s.verified.Store(true)
	return nil
}

// nativePGProbe is the result of the open verification query.
type nativePGProbe struct {
	dbProjectID   string
	schemaVersion string
	columnCount   int
	tableCount    int
}

// evaluate compares a probe against the scope's declared identity and the
// pinned schema. It returns a generic reason (never containing the actual
// project_id, database, or endpoint) and ok=false when the native path must be
// disabled. Absent-sentinel and mismatch both fail closed.
func (p nativePGProbe) evaluate(scopeProjectID string) (string, bool) {
	if strings.TrimSpace(scopeProjectID) == "" {
		return "scope metadata declares no project_id; cannot verify database identity", false
	}
	if strings.TrimSpace(p.dbProjectID) == "" {
		return "database project-identity sentinel is absent", false
	}
	if p.dbProjectID != scopeProjectID {
		return "database project identity does not match the scope", false
	}
	if strings.TrimSpace(p.schemaVersion) == "" {
		return "database schema-version marker is absent", false
	}
	if p.schemaVersion != nativePGExpectedSchemaVersion {
		return "database schema version differs from the pinned read-SQL version", false
	}
	if p.columnCount < nativePGRequiredColumnCount() || p.tableCount < len(nativePGRequiredTables) {
		return "database schema shape is missing required tables or columns", false
	}
	return "", true
}

// nativePGRequiredColumns are the columns the read SQL selects or filters on;
// the schema-shape probe requires every one to exist in BOTH the issues and
// wisps tables.
var nativePGRequiredColumns = []string{
	"id", "title", "description", "status", "priority", "issue_type",
	"assignee", "created_at", "updated_at", "defer_until", "ephemeral",
	"no_history", "is_blocked", "metadata", "pinned", "is_template",
}

// nativePGRequiredTables are the auxiliary tables the read SQL joins.
var nativePGRequiredTables = []string{
	"issues", "wisps", "dependencies", "wisp_dependencies",
	"labels", "wisp_labels", "metadata",
}

// nativePGRequiredColumnCount is the expected column-existence count: every
// required column present in both bead tables.
func nativePGRequiredColumnCount() int {
	return len(nativePGRequiredColumns) * 2
}

// probeDatabase reads the identity sentinel, schema-version marker, and the
// schema-shape existence counts in a single parameterized round trip.
func (s *NativePostgresReadStore) probeDatabase(ctx context.Context) (nativePGProbe, error) {
	const query = `SELECT
  (SELECT value FROM beads.metadata WHERE key = '_project_id'),
  (SELECT value FROM beads.metadata WHERE key = 'pg_schema_version'),
  (SELECT count(*) FROM information_schema.columns
     WHERE table_schema = 'beads' AND table_name IN ('issues','wisps') AND column_name = ANY($1)),
  (SELECT count(*) FROM information_schema.tables
     WHERE table_schema = 'beads' AND table_name = ANY($2))`
	rows, err := s.pool.Query(ctx, query, nativePGRequiredColumns, nativePGRequiredTables)
	if err != nil {
		return nativePGProbe{}, fmt.Errorf("identity/schema probe: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nativePGProbe{}, fmt.Errorf("identity/schema probe: %w", err)
		}
		return nativePGProbe{}, errors.New("identity/schema probe returned no row")
	}
	var (
		projectID     *string
		schemaVersion *string
		colCount      int
		tblCount      int
	)
	if err := rows.Scan(&projectID, &schemaVersion, &colCount, &tblCount); err != nil {
		return nativePGProbe{}, fmt.Errorf("identity/schema probe scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nativePGProbe{}, fmt.Errorf("identity/schema probe: %w", err)
	}
	return nativePGProbe{
		dbProjectID:   derefString(projectID),
		schemaVersion: derefString(schemaVersion),
		columnCount:   colCount,
		tableCount:    tblCount,
	}, nil
}

// recordNativeFailure books a native failure against the breaker (unless the
// store is already permanently disabled) and emits a rate-limited fallback WARN.
func (s *NativePostgresReadStore) recordNativeFailure(method string, err error) {
	if s.disabled.Load() {
		// Permanent disable already logged its one reason line; the per-read
		// fallback is silent thereafter.
		return
	}
	s.breaker.recordFailure(time.Now())
	s.logFallback(method, err)
}

// logFallback emits a rate-limited WARN when a native read falls back to bd. It
// logs only the method, scope, and a coarse error CLASS — never the raw pgx
// error, which carries the DB user, per-project database name, and endpoint
// host:port as connection coordinates.
func (s *NativePostgresReadStore) logFallback(method string, err error) {
	if s.logger == nil {
		return
	}
	s.logMu.Lock()
	if time.Since(s.lastLog) < nativePGFallbackLogInterval {
		s.logMu.Unlock()
		return
	}
	s.lastLog = time.Now()
	s.logMu.Unlock()
	s.logger.Warn("native_postgres_read_fallback",
		slog.String("method", method),
		slog.String("scope", s.scopeRoot),
		slog.String("error_class", nativePGErrorClass(err)))
}

// logDisabled emits the single WARN that records a permanent native-off
// decision. It logs the scope and a generic reason only — never the identity
// values, database name, or endpoint that produced the decision.
func (s *NativePostgresReadStore) logDisabled(reason string) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("native_postgres_read_disabled",
		slog.String("scope", s.scopeRoot),
		slog.String("reason", reason))
}

// nativePGErrorClass reduces a native error to a coarse, secret-free label
// suitable for logs and aggregators. It deliberately does not surface the pgx
// error text, which embeds DSN coordinates.
func nativePGErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrQueryRequiresScan):
		return "query_requires_scan"
	default:
		return "native_read_error"
	}
}

// opContext derives a bounded context for one native read attempt so a stalled
// endpoint cannot hold a caller past the fallback budget.
func (s *NativePostgresReadStore) opContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), nativePGOpTimeout)
}

// nativePGBreaker is a consecutive-failure circuit breaker guarding the native
// path. After nativePGBreakerThreshold consecutive failures it opens; while
// open, nativeUsable returns false so reads skip the pool entirely (zero
// acquire wait) and use bd. After nativePGBreakerCooldown it allows one
// half-open probe; a success closes it, a failure re-arms the cooldown.
type nativePGBreaker struct {
	mu          sync.Mutex
	consecutive int
	openedAt    time.Time
}

// usable reports whether the native path may be attempted. It is a pure read
// with no state mutation, so concurrent callers see a consistent gate.
func (b *nativePGBreaker) usable(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.consecutive < nativePGBreakerThreshold {
		return true
	}
	// Open: permit a single half-open probe once the cooldown elapses.
	return now.Sub(b.openedAt) >= nativePGBreakerCooldown
}

// recordSuccess closes the breaker.
func (b *nativePGBreaker) recordSuccess() {
	b.mu.Lock()
	b.consecutive = 0
	b.openedAt = time.Time{}
	b.mu.Unlock()
}

// recordFailure counts a failure and (re)arms the cooldown window when the
// breaker is at or past the trip threshold, so a failed half-open probe holds
// the breaker open for another full cooldown.
func (b *nativePGBreaker) recordFailure(now time.Time) {
	b.mu.Lock()
	b.consecutive++
	if b.consecutive >= nativePGBreakerThreshold {
		b.openedAt = now
	}
	b.mu.Unlock()
}
