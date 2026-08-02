package beads

import (
	"testing"

	"github.com/gastownhall/gascity/internal/rollout/gate"
)

func TestNativeDoltStoreDeclaresConditionalWriterAndProbesPinnedStorageContract(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())

	if _, ok := ConditionalWriterFor(store); !ok {
		t.Fatal("NativeDoltStore does not resolve a ConditionalWriter")
	}
	if _, ok := MetadataCASWriterFor(store); !ok {
		t.Fatal("NativeDoltStore does not resolve a MetadataCASWriter")
	}
	if capable, reason := store.probeConditionalWriteCapability(); !capable {
		t.Fatalf("pinned backend capability = false (%s), want true", reason)
	}

	compiledStorage := newNativeDoltStoreForTest(&nativeDoltStorageSpy{})
	if capable, reason := compiledStorage.probeConditionalWriteCapability(); !capable {
		t.Fatalf("compiled Storage capability = false (%s), want true", reason)
	}
}

// TestNativeDoltStoreConditionalWritesResolveForPinnedStorageModes pins the
// mode seam over the compile-time Storage contract. The pinned upstream
// interface requires checked update/close and transactions, so there is no
// runtime "older backend" shape hidden behind the same interface.
func TestNativeDoltStoreConditionalWritesResolveForPinnedStorageModes(t *testing.T) {
	for _, mode := range []gate.Mode{gate.Require, gate.Auto} {
		t.Run(string(mode), func(t *testing.T) {
			store := newNativeDoltStoreForTest(newNativeDoltMemStorage())
			store.stampConditionalWritesMode(mode, false)

			writer, diag, err := ResolveConditionalWriter(store)
			if writer == nil || diag != nil || err != nil {
				t.Fatalf("ResolveConditionalWriter = (%T, %+v, %v), want writer, nil, nil", writer, diag, err)
			}
		})
	}
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
