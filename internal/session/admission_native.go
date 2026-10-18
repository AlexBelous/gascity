package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

const (
	nativeManagedAdmissionMode = "native-managed-v1"
	nativeCapacitySchema       = "managed-runtime-admission/v1"
)

type nativeCapacityConfig struct {
	Admission struct {
		Mode string `toml:"mode"`
	} `toml:"admission"`
	Queue struct {
		Exact    []string `toml:"managed_exact_routes"`
		Prefixes []string `toml:"managed_route_prefixes"`
	} `toml:"queue"`
	Limits struct {
		Cap int `toml:"max_managed_workers"`
	} `toml:"limits"`
	Thresholds struct {
		MemoryGiB float64 `toml:"mem_green_gib"`
		MemoryPSI float64 `toml:"memory_psi_green"`
		IOPSI     float64 `toml:"io_psi_green"`
	} `toml:"thresholds"`
}

// NativeManagedAdmission reports the opt-in policy for this route. Absence is
// legacy; malformed or unknown policy must never silently select another mode.
func NativeManagedAdmission(city, route string) (bool, error) {
	c, enabled, err := readNativeCapacityConfig(city)
	return enabled && managedCapacityRoute(route, c.Queue.Exact, c.Queue.Prefixes), err
}

func readNativeCapacityConfig(city string) (nativeCapacityConfig, bool, error) {
	var c nativeCapacityConfig
	if city == "" {
		return c, false, nil
	}
	data, err := os.ReadFile(filepath.Join(city, "config", "capacity-scheduler-v2.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("capacity scope invalid")
	}
	// Legacy admission has never decoded limits/thresholds from this file.
	// Inspect only the opt-in header first, preserving that old behavior.
	var header struct {
		Admission struct {
			Mode string `toml:"mode"`
		} `toml:"admission"`
	}
	meta, err := toml.Decode(string(data), &header)
	if err != nil {
		return c, false, fmt.Errorf("capacity scope invalid")
	}
	if !meta.IsDefined("admission", "mode") {
		return c, false, nil
	}
	c.Admission.Mode = header.Admission.Mode
	switch c.Admission.Mode {
	case "legacy-census-v1":
		return c, false, nil
	case nativeManagedAdmissionMode:
		if _, err := toml.Decode(string(data), &c); err != nil {
			return c, false, fmt.Errorf("native capacity scope invalid")
		}
		if c.Limits.Cap < 1 || c.Limits.Cap > 3 || len(c.Queue.Exact) == 0 || len(c.Queue.Prefixes) != 0 {
			return c, false, fmt.Errorf("native capacity scope invalid")
		}
		for _, x := range []float64{c.Thresholds.MemoryGiB, c.Thresholds.MemoryPSI, c.Thresholds.IOPSI} {
			if math.IsNaN(x) || math.IsInf(x, 0) || x <= 0 {
				return c, false, fmt.Errorf("native capacity thresholds invalid")
			}
		}
		return c, true, nil
	default:
		return c, false, fmt.Errorf("capacity admission mode invalid")
	}
}

// CapacityAdmissionMode is the read-only city capability projection. It does
// not promise legacy route reservations when native-owned seats are selected.
func CapacityAdmissionMode(city string) (string, error) {
	_, enabled, err := readNativeCapacityConfig(city)
	if err != nil {
		return "", err
	}
	if enabled {
		return nativeManagedAdmissionMode, nil
	}
	return "legacy-census-v1", nil
}

type nativeKernelIdentity struct {
	BootID            string `json:"boot_id"`
	PIDNamespace      string `json:"pid_namespace"`
	CgroupNamespace   string `json:"cgroup_namespace"`
	CgroupMountDevice uint64 `json:"cgroup_mount_device"`
	CgroupMountInode  uint64 `json:"cgroup_mount_inode"`
}

// This is admission state, not a cached claim of runtime liveness. Every
// unresolved seat counts. Only positive kernel emptiness retires a new seat.
type nativeCapacityLedger struct {
	Schema   string                        `json:"schema"`
	CityPath string                        `json:"city_path"`
	Identity nativeKernelIdentity          `json:"identity"`
	Seats    map[string]nativeCapacitySeat `json:"seats"`
	Retired  []nativeCapacityRetirement    `json:"retired,omitempty"`
}

type nativeCapacitySeat struct {
	Route           string `json:"route"`
	Handle          string `json:"handle"`
	Kind            string `json:"kind"`
	Attempt         string `json:"attempt"`
	Unit            string `json:"unit,omitempty"`
	State           string `json:"state"`
	CreatedAt       string `json:"created_at"`
	LaunchFinalized bool   `json:"launch_finalized"`
	CgroupPath      string `json:"cgroup_path,omitempty"`
	CgroupDevice    uint64 `json:"cgroup_device,omitempty"`
	CgroupInode     uint64 `json:"cgroup_inode,omitempty"`
}

type nativeCapacityRetirement struct {
	SessionID string `json:"session_id"`
	Attempt   string `json:"attempt"`
	Reason    string `json:"reason"`
	At        string `json:"at"`
}

type nativeScopeObservation struct {
	Populated, Empty bool
	CgroupPath       string
	Device, Inode    uint64
}

type nativeCapacityHost interface {
	Identity() (nativeKernelIdentity, error)
	Resources(nativeCapacityConfig) error
	Command(string) (runtime.CommandScope, error)
	Observe(nativeCapacitySeat) (nativeScopeObservation, error)
}

type nativeCapacityProof struct {
	config  nativeCapacityConfig
	host    nativeCapacityHost
	attempt string
}

func nativeSeatRetirable(seat nativeCapacitySeat, observed nativeScopeObservation) bool {
	return seat.Kind == "scope" && seat.LaunchFinalized && seat.CgroupPath != "" && observed.Empty && !observed.Populated
}

func nativeCapacityPath(city string) string {
	return filepath.Join(city, ".gc", "runtime", "pool-capacity-admission", "native-managed.json")
}

func nativeCapacityUnit(city, sid, attempt string) string {
	h := sha256.Sum256([]byte(city + "\x00" + sid + "\x00" + attempt))
	return "gc-managed-" + hex.EncodeToString(h[:16]) + ".scope"
}

func validNativeSeat(sid string, seat nativeCapacitySeat, city string, c nativeCapacityConfig) bool {
	if sid == "" || seat.Handle == "" || !managedCapacityRoute(seat.Route, c.Queue.Exact, c.Queue.Prefixes) || strings.ContainsAny(sid+seat.Handle, "\x00\r\n\t") {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, seat.CreatedAt); err != nil {
		return false
	}
	if seat.Kind == "legacy" {
		return seat.State == "legacy" && seat.LaunchFinalized && seat.Unit == "" && seat.CgroupPath == "" && seat.Attempt != ""
	}
	if seat.Kind != "scope" || len(seat.Attempt) != 32 || seat.Unit != nativeCapacityUnit(city, sid, seat.Attempt) {
		return false
	}
	if _, err := hex.DecodeString(seat.Attempt); err != nil {
		return false
	}
	if seat.State != "reserved" && seat.State != "launching" && seat.State != "active" && seat.State != "ambiguous" {
		return false
	}
	if seat.CgroupPath != "" && (!strings.HasPrefix(seat.CgroupPath, "/") || filepath.Clean(seat.CgroupPath) != seat.CgroupPath || filepath.Base(seat.CgroupPath) != seat.Unit || seat.CgroupDevice == 0 || seat.CgroupInode == 0) {
		return false
	}
	return true
}

// The baseline must be published explicitly during a quiesced cutover. Missing,
// corrupt, foreign-boot or foreign-city state is never initialized to zero.
func withNativeCapacityLedger(ctx context.Context, city string, c nativeCapacityConfig, host nativeCapacityHost, fn func(*nativeCapacityLedger) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return fmt.Errorf("native capacity canceled")
	}
	path := nativeCapacityPath(city)
	lock, err := openNativeCapacityFile(path+".lock", syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return fmt.Errorf("native capacity lock unavailable")
	}
	defer lock.Close() //nolint:errcheck
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("native capacity lock unavailable")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("native capacity lock canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	file, err := openNativeCapacityFile(path, syscall.O_RDONLY)
	if err != nil {
		return fmt.Errorf("native capacity baseline unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	_ = file.Close()
	var ledger nativeCapacityLedger
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err != nil || len(data) > 1<<20 || decoder.Decode(&ledger) != nil || decoder.Decode(new(any)) != io.EOF || ledger.Schema != nativeCapacitySchema || ledger.CityPath != city || ledger.Seats == nil || len(ledger.Seats) > 1024 {
		return fmt.Errorf("native capacity baseline invalid")
	}
	identity, err := host.Identity()
	if err != nil || identity != ledger.Identity || identity.BootID == "" || identity.PIDNamespace == "" || identity.CgroupNamespace == "" || identity.CgroupMountDevice == 0 || identity.CgroupMountInode == 0 {
		return fmt.Errorf("native capacity kernel identity unavailable")
	}
	for sid, seat := range ledger.Seats {
		if !validNativeSeat(sid, seat, city, c) {
			return fmt.Errorf("native capacity seat invalid")
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("native capacity canceled")
	}
	if err := fn(&ledger); err != nil {
		return err
	}
	held, err := lock.Stat()
	current, statErr := os.Lstat(path + ".lock")
	if err != nil || statErr != nil || !os.SameFile(held, current) || ctx.Err() != nil {
		return fmt.Errorf("native capacity lock changed or canceled")
	}
	updated, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return fmt.Errorf("native capacity ledger encode failed")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".native-managed-*")
	if err != nil {
		return fmt.Errorf("native capacity ledger write failed")
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(append(updated, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil || closeErr != nil || os.Rename(tmp.Name(), path) != nil {
		return fmt.Errorf("native capacity ledger write failed")
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("native capacity ledger directory unavailable")
	}
	defer dir.Close() //nolint:errcheck
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("native capacity ledger sync failed")
	}
	return nil
}

func openNativeCapacityFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("native capacity file permissions invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("native capacity file ownership invalid")
	}
	return file, nil
}

func retireNativeSeat(ledger *nativeCapacityLedger, sid, reason string, now time.Time) {
	seat := ledger.Seats[sid]
	ledger.Retired = append(ledger.Retired, nativeCapacityRetirement{sid, seat.Attempt, reason, now.UTC().Format(time.RFC3339Nano)})
	if len(ledger.Retired) > 128 {
		ledger.Retired = ledger.Retired[len(ledger.Retired)-128:]
	}
	delete(ledger.Seats, sid)
}

func refreshNativeSeats(ledger *nativeCapacityLedger, host nativeCapacityHost, now time.Time) {
	for sid, seat := range ledger.Seats {
		if seat.Kind == "legacy" {
			continue
		}
		observed, err := host.Observe(seat)
		if err != nil {
			continue
		} // Unknown remains an occupied seat, never zero.
		if seat.CgroupPath == "" && observed.Populated && observed.CgroupPath != "" {
			seat.CgroupPath, seat.CgroupDevice, seat.CgroupInode = observed.CgroupPath, observed.Device, observed.Inode
		}
		if nativeSeatRetirable(seat, observed) {
			retireNativeSeat(ledger, sid, "kernel_cgroup_empty", now)
			continue
		}
		if observed.Populated && seat.LaunchFinalized {
			seat.State = "active"
		}
		ledger.Seats[sid] = seat
	}
}

func claimNativeCapacityStart(ctx context.Context, city, route, sid string, now time.Time, c nativeCapacityConfig, host nativeCapacityHost, sp runtime.Provider, store beads.Store) (*nativeCapacityProof, error) {
	if store == nil || sp == nil || sid == "" {
		return nil, fmt.Errorf("native capacity identity unavailable")
	}
	info, err := NewStore(beads.SessionStore{Store: store}).Get(sid)
	if err != nil || info.Closed || info.Template != route || info.SessionName == "" {
		return nil, fmt.Errorf("native capacity identity unavailable")
	}
	support, ok := sp.(runtime.CommandScopeProvider)
	if !ok || !support.SupportsCommandScope(info.SessionName) {
		return nil, fmt.Errorf("native capacity provider unsupported")
	}
	attempt := NewInstanceToken()[:32]
	proof := &nativeCapacityProof{c, host, attempt}
	err = withNativeCapacityLedger(ctx, city, c, host, func(ledger *nativeCapacityLedger) error {
		refreshNativeSeats(ledger, host, now)
		if _, exists := ledger.Seats[sid]; exists {
			return fmt.Errorf("native capacity session occupied")
		}
		if len(ledger.Seats) >= c.Limits.Cap {
			return fmt.Errorf("native capacity full")
		}
		if err := validateNativeRegistry(ledger, sp, store, c); err != nil {
			return err
		}
		if err := host.Resources(c); err != nil {
			return fmt.Errorf("native capacity resources unavailable")
		}
		if _, err := host.Command(nativeCapacityUnit(city, sid, attempt)); err != nil {
			return fmt.Errorf("native capacity scope unavailable")
		}
		ledger.Seats[sid] = nativeCapacitySeat{Route: route, Handle: info.SessionName, Kind: "scope", Attempt: attempt, Unit: nativeCapacityUnit(city, sid, attempt), State: "reserved", CreatedAt: now.UTC().Format(time.RFC3339Nano)}
		return nil
	})
	return proof, err
}

func validateNativeRegistry(ledger *nativeCapacityLedger, sp runtime.Provider, store beads.Store, c nativeCapacityConfig) error {
	infos, err := NewStore(beads.SessionStore{Store: store}).ListAll(ListAllOptions{})
	if err != nil {
		return fmt.Errorf("native capacity registry unavailable")
	}
	known := make(map[string]Info, len(infos))
	for _, info := range infos {
		if info.SessionName != "" {
			known[info.SessionName] = info
		}
	}
	names, err := sp.ListRunning("")
	if err != nil {
		return fmt.Errorf("native capacity registry unavailable")
	}
	for _, name := range names {
		info, ok := known[name]
		if !ok {
			return fmt.Errorf("native capacity runtime unmapped")
		}
		if managedCapacityRoute(info.Template, c.Queue.Exact, c.Queue.Prefixes) {
			seat, present := ledger.Seats[info.ID]
			if !present || seat.Handle != name || seat.Route != info.Template {
				return fmt.Errorf("native capacity runtime unowned")
			}
		}
	}
	return nil
}

func beginNativeCapacityStart(ctx context.Context, proof *capacityAdmissionProof, name string, cfg runtime.Config) (runtime.Config, error) {
	native := proof.native
	err := withNativeCapacityLedger(ctx, proof.city, native.config, native.host, func(ledger *nativeCapacityLedger) error {
		seat, ok := ledger.Seats[proof.id]
		if !ok || seat.Attempt != native.attempt || seat.State != "reserved" || seat.Handle != name || seat.Route != proof.route || cfg.Command == "" {
			return fmt.Errorf("native capacity proof invalid")
		}
		if err := native.host.Resources(native.config); err != nil {
			return fmt.Errorf("native capacity resources unavailable")
		}
		scope, err := native.host.Command(seat.Unit)
		if err != nil {
			return fmt.Errorf("native capacity scope unavailable")
		}
		cfg.CommandScope = &scope
		seat.State = "launching"
		ledger.Seats[proof.id] = seat
		return nil
	})
	return cfg, err
}

func finishNativeCapacityStart(ctx context.Context, proof *capacityAdmissionProof, startErr error, now time.Time) error {
	native := proof.native
	return withNativeCapacityLedger(ctx, proof.city, native.config, native.host, func(ledger *nativeCapacityLedger) error {
		seat, ok := ledger.Seats[proof.id]
		if !ok || seat.Attempt != native.attempt || seat.State != "launching" {
			return fmt.Errorf("native capacity finalization identity mismatch")
		}
		// A failed/timeout Start can have an unconfirmed queued launch. It
		// never seals the attempt or frees its seat, even if a scope vanished.
		seat.LaunchFinalized, seat.State = startErr == nil, "ambiguous"
		ledger.Seats[proof.id] = seat
		refreshNativeSeats(ledger, native.host, now)
		return nil
	})
}

// AbandonCapacityStart cancels only an invocation-local, never-launched native
// reservation. The one-shot proof is consumed first; a concurrent actual start
// or any durable launching/ambiguous seat can never be freed by this operation.
func AbandonCapacityStart(ctx context.Context) error {
	proof, ok := ctx.Value(capacityAdmissionKey{}).(*capacityAdmissionProof)
	if !ok || proof.native == nil || !proof.used.CompareAndSwap(false, true) {
		return nil
	}
	return abandonNativeReservedSeat(proof)
}

func abandonNativeReservedSeat(proof *capacityAdmissionProof) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return withNativeCapacityLedger(ctx, proof.city, proof.native.config, proof.native.host, func(ledger *nativeCapacityLedger) error {
		seat, ok := ledger.Seats[proof.id]
		if ok && seat.Attempt == proof.native.attempt && seat.State == "reserved" {
			retireNativeSeat(ledger, proof.id, "canceled_before_provider_start", time.Now())
		}
		return nil
	})
}
