package sessionsdb

// The fail-closed arm of sessions routing: a city marked migrated whose
// routing cannot be resolved or whose class store cannot open gets a store
// whose EVERY op fails with the routing error. Falling back to bd there
// would land writes where routed readers never look and read residue as
// truth (the #1939 shape) — the same discipline as
// nudgequeue.NewUnavailableQueue and the messaging reload nil-swap.

import (
	"github.com/gastownhall/gascity/internal/beads"
)

// unavailableStore is a beads.Store whose every operation returns err.
type unavailableStore struct{ err error }

// NewUnavailableStore returns a sessions-class store that fails every
// operation with err.
func NewUnavailableStore(err error) beads.Store { return unavailableStore{err: err} }

func (u unavailableStore) Create(beads.Bead) (beads.Bead, error) { return beads.Bead{}, u.err }
func (u unavailableStore) Get(string) (beads.Bead, error)        { return beads.Bead{}, u.err }
func (u unavailableStore) Update(string, beads.UpdateOpts) error { return u.err }
func (u unavailableStore) Close(string) error                    { return u.err }
func (u unavailableStore) Reopen(string) error                   { return u.err }
func (u unavailableStore) CloseAll([]string, map[string]string) (int, error) {
	return 0, u.err
}
func (u unavailableStore) List(beads.ListQuery) ([]beads.Bead, error) { return nil, u.err }
func (u unavailableStore) ListOpen(...string) ([]beads.Bead, error)   { return nil, u.err }
func (u unavailableStore) Ready(...beads.ReadyQuery) ([]beads.Bead, error) {
	return nil, u.err
}

func (u unavailableStore) Children(string, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, u.err
}

func (u unavailableStore) ListByLabel(string, int, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, u.err
}

func (u unavailableStore) ListByAssignee(string, string, int) ([]beads.Bead, error) {
	return nil, u.err
}

func (u unavailableStore) ListByMetadata(map[string]string, int, ...beads.QueryOpt) ([]beads.Bead, error) {
	return nil, u.err
}
func (u unavailableStore) SetMetadata(string, string, string) error         { return u.err }
func (u unavailableStore) SetMetadataBatch(string, map[string]string) error { return u.err }
func (u unavailableStore) Tx(string, func(tx beads.Tx) error) error         { return u.err }
func (u unavailableStore) Delete(string) error                              { return u.err }
func (u unavailableStore) Ping() error                                      { return u.err }
func (u unavailableStore) DepAdd(string, string, string) error              { return u.err }
func (u unavailableStore) DepRemove(string, string) error                   { return u.err }
func (u unavailableStore) DepList(string, string) ([]beads.Dep, error)      { return nil, u.err }
