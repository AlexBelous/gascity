package beads

import (
	"testing"

	"github.com/gastownhall/gascity/internal/rollout/gate"
)

func TestNativeDoltStoreDeclaresConditionalWriterAndProbesBackendGuard(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())

	if _, ok := ConditionalWriterFor(store); !ok {
		t.Fatal("NativeDoltStore does not resolve a ConditionalWriter")
	}
	if _, ok := MetadataCASWriterFor(store); !ok {
		t.Fatal("NativeDoltStore does not resolve a MetadataCASWriter")
	}
	if capable, reason := store.probeConditionalWriteCapability(); !capable {
		t.Fatalf("guarded backend capability = false (%s), want true", reason)
	}

	incapable := newNativeDoltStoreForTest(&nativeDoltStorageSpy{})
	if capable, reason := incapable.probeConditionalWriteCapability(); capable || reason == "" {
		t.Fatalf("unguarded backend capability = (%t, %q), want false with reason", capable, reason)
	}
}

// TestNativeDoltStoreConditionalWritesStillRefuseOrDegrade pins the seam
// behavior the condWritesStamp comment in native_dolt_store.go guarantees:
// capable native stores resolve, while an older unguarded backend still
// refuses under require and degrades loudly under auto.
func TestNativeDoltStoreConditionalWritesStillRefuseOrDegrade(t *testing.T) {
	t.Run("require_resolves_capable_backend", func(t *testing.T) {
		store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
		store.stampConditionalWritesMode(gate.Require, false)

		writer, diag, err := ResolveConditionalWriter(store)
		if writer == nil || diag != nil || err != nil {
			t.Fatalf("ResolveConditionalWriter = (%T, %+v, %v), want writer, nil, nil", writer, diag, err)
		}
	})

	t.Run("require_refuses_unguarded_backend", func(t *testing.T) {
		store := newNativeDoltStoreForTest(&nativeDoltStorageSpy{})
		store.stampConditionalWritesMode(gate.Require, false)

		writer, diag, err := ResolveConditionalWriter(store)
		if writer != nil || diag == nil || !IsConditionalWritesRequired(err) {
			t.Fatalf("ResolveConditionalWriter = (%T, %+v, %v), want nil, diagnostic, required error", writer, diag, err)
		}
	})

	t.Run("auto_degrades_unguarded_backend", func(t *testing.T) {
		store := newNativeDoltStoreForTest(&nativeDoltStorageSpy{})
		store.stampConditionalWritesMode(gate.Auto, false)

		writer, diag, err := ResolveConditionalWriter(store)
		if writer != nil {
			t.Fatalf("writer = %T, want nil (auto must take the legacy path)", writer)
		}
		if err != nil {
			t.Fatalf("err = %v, want nil (auto degrades, it does not refuse)", err)
		}
		if diag == nil {
			t.Fatal("diagnostic = nil, want a loud-degrade diagnostic")
		}
	})
}

// TestCachingStoreOverNativeDoltStoreForwardsConditionalWrites covers the
// production wrapper shape: the cache must preserve both metadata CAS and the
// guarded whole-row writer advertised by its native backing.
func TestCachingStoreOverNativeDoltStoreForwardsConditionalWrites(t *testing.T) {
	backing := newNativeDoltStoreForTest(newNativeDoltMemStorage())
	cache := NewCachingStore(backing, nil)

	b, err := cache.Create(Bead{Title: "cache-over-native-cas"})
	if err != nil {
		t.Fatal(err)
	}

	writer, ok := MetadataCASWriterFor(cache)
	if !ok {
		t.Fatal("CachingStore over a narrow-CAS backing does not resolve a MetadataCASWriter")
	}
	if swapped, err := writer.CompareAndSetMetadataKey(b.ID, "lease", "", "holder-1"); err != nil || !swapped {
		t.Fatalf("claim through cache: (%v, %v), want (true, nil)", swapped, err)
	}
	// A stale expectation loses cleanly rather than erroring.
	if swapped, err := writer.CompareAndSetMetadataKey(b.ID, "lease", "", "holder-2"); err != nil || swapped {
		t.Fatalf("stale claim through cache: (%v, %v), want (false, nil)", swapped, err)
	}
	// The winner's value is visible through the cache (the CAS evicted, so the
	// next read consults the backing).
	got, err := cache.Get(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["lease"] != "holder-1" {
		t.Fatalf("lease through cache = %q, want %q", got.Metadata["lease"], "holder-1")
	}

	if capable, reason := cache.probeConditionalWriteCapability(); !capable {
		t.Fatalf("CachingStore reports conditional-write capability = false (%s)", reason)
	}
	if _, ok := ConditionalWriterFor(cache); !ok {
		t.Fatal("CachingStore over NativeDoltStore does not resolve a ConditionalWriter")
	}
}
