package main

import (
	"context"
	"strings"
)

type bdChildAcquire func(context.Context) ([]string, bdChildReaders, error)

func bdOwnerKey(key string) bool {
	switch key {
	case "GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN":
		return true
	}
	return false
}

// Presence, including present-empty, selects the managed lane. UNKNOWN is
// never a human/infrastructure fallback. A truly absent context keeps the
// established human/no-runtime behavior authorized for this Linux release.
func bdOwnerContextAbsent(raw []string) bool {
	for _, e := range raw {
		key, _, _ := strings.Cut(e, "=")
		if bdOwnerKey(key) {
			return false
		}
	}
	return true
}

func projectBDManagedChild(raw, final []string) ([]string, error) {
	tuple, city, err := parseBDChildEnv(raw)
	if err != nil {
		return nil, err
	}
	originals := map[string]string{}
	for _, e := range raw {
		k, v, _ := strings.Cut(e, "=")
		if bdChildSensitiveIdentityKey(k) {
			originals[k] = v
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(final)+7)
	if len(final) > 4096 {
		return nil, errBDChildEnvironment
	}
	for _, e := range final {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" || strings.ContainsRune(e, 0) {
			return nil, errBDChildEnvironment
		}
		if bdChildSensitiveIdentityKey(k) {
			if seen[k] {
				return nil, errBDChildEnvironment
			}
			seen[k] = true
			if bdOwnerKey(k) {
				if v != originals[k] {
					return nil, errBDChildAuthority
				}
			} else if v != city.effective() {
				return nil, errBDChildAuthority
			}
			continue
		}
		out = append(out, e)
	}
	// Restore only original keys/values, never backfill from the store/provider.
	// The generic filter remains unchanged. This exception is private to a
	// positively composed session ticket, never process-global or argv based.
	for _, e := range raw {
		k, _, _ := strings.Cut(e, "=")
		if bdChildSensitiveIdentityKey(k) {
			out = append(out, e)
		}
	}
	parsed, projectedCity, err := parseBDChildEnv(out)
	if err != nil || parsed != tuple || projectedCity != city {
		return nil, errBDChildAuthority
	}
	return out, nil
}

func executeBDChild(ctx context.Context, platform string, ambient, final []string, acquire bdChildAcquire, spawn bdChildSpawn) error {
	if ctx == nil || ctx.Err() != nil || spawn == nil {
		return errBDChildAuthority
	}
	parent := append([]string(nil), ambient...)
	projected := append([]string(nil), final...)
	if platform == "linux" {
		if len(parent) > 4096 {
			return errBDChildEnvironment
		}
		total := 0
		for _, entry := range parent {
			total += len(entry)
			key, _, ok := strings.Cut(entry, "=")
			if !ok || key == "" || strings.ContainsRune(entry, 0) || total > 16<<20 {
				return errBDChildEnvironment
			}
			for _, b := range []byte(key) {
				if b <= 32 || b == 127 {
					return errBDChildEnvironment
				}
			}
		}
	}
	if platform != "linux" || bdOwnerContextAbsent(parent) {
		return spawn(ctx, bdChildEnvironment{entries: projected})
	}
	// The ordinary Go ENV view is only an operation discriminator, never proof.
	// OS acquisition must independently preserve raw duplicates and incarnation.
	claimed, claimedCity, err := parseBDChildEnv(parent)
	if err != nil {
		return err
	}
	if acquire == nil {
		return errBDChildAuthority
	}
	raw, readers, err := acquire(ctx)
	if err != nil || ctx.Err() != nil {
		return errBDChildAuthority
	}
	measured, measuredCity, err := parseBDChildEnv(raw)
	if err != nil || measured != claimed || measuredCity != claimedCity {
		return errBDChildAuthority
	}
	ticket, err := authorizeBDSessionChild(ctx, bdChildSessionTool, raw, readers)
	if err != nil {
		return err
	}
	projected, err = projectBDManagedChild(raw, projected)
	if err != nil {
		return err
	}
	if err = recheckBDSessionChild(ctx, ticket, projected, readers); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errBDChildAuthority
	}
	// Preserve the child's original exit/error behavior; authority errors are
	// raised BEFORE this callback. Existing downstream holder fencing remains.
	return spawn(ctx, bdChildEnvironment{entries: projected})
}
