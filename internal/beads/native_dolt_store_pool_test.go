package beads

import (
	"database/sql"
	"testing"
)

// openUnconnectedDB returns a *sql.DB whose driver is registered but which never
// dials — sql.Open is lazy, so no server is needed to inspect pool config.
func openUnconnectedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", "u:p@tcp(127.0.0.1:0)/d")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestBoundNativeDoltPoolCapsOpenConnections proves a bounded pool holds at most
// one open connection, so a scoped/CLI store cannot fan a database/sql pool of
// connections onto the shared managed-Dolt server.
func TestBoundNativeDoltPoolCapsOpenConnections(t *testing.T) {
	db := openUnconnectedDB(t)
	boundNativeDoltPool(db)
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

// TestBoundNativeDoltPoolNilSafe proves a nil handle is a no-op, not a panic (a
// storage backend that does not expose a *sql.DB must degrade quietly).
func TestBoundNativeDoltPoolNilSafe(_ *testing.T) {
	boundNativeDoltPool(nil) // must not panic
}

// boundableTestStorage is a mem storage that also exposes a *sql.DB, so the
// WithBoundedConnections option's rawDBGetter path can be exercised.
type boundableTestStorage struct {
	*nativeDoltMemStorage
	db *sql.DB
}

func (b boundableTestStorage) DB() *sql.DB { return b.db }

// TestWithBoundedConnectionsBoundsStorePool proves the option caps the pool of a
// store whose storage exposes a raw *sql.DB.
func TestWithBoundedConnectionsBoundsStorePool(t *testing.T) {
	db := openUnconnectedDB(t)
	store := newNativeDoltStoreForTest(boundableTestStorage{
		nativeDoltMemStorage: newNativeDoltMemStorage(),
		db:                   db,
	})
	WithBoundedConnections()(store)
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d after WithBoundedConnections, want 1", got)
	}
}
