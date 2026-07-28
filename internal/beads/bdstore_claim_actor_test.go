package beads

import (
	"context"
	"errors"
	"testing"
)

// TestBdStoreIsNotAtomicClaimer proves BdStore does not advertise the
// AtomicClaimer capability. bd update --claim commits to the runner's fixed
// BEADS_ACTOR and cannot bind the passed actor per call, so exposing it as a
// claimer would risk a wrong-owner mutation. The type assertion must fail so a
// controller store that resolves to a BdStore fails closed rather than claiming
// as the wrong actor.
func TestBdStoreIsNotAtomicClaimer(t *testing.T) {
	var s any = NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		return []byte("{}"), nil
	})
	if _, ok := s.(AtomicClaimer); ok {
		t.Fatal("BdStore satisfies AtomicClaimer; it must not, because bd cannot bind the claim actor per call")
	}
}

// TestCachingStoreOverBdStoreFailsClosedWithoutMutation proves the fail-closed
// path: a CachingStore whose backing is a BdStore reports ErrAtomicClaimUnsupported
// for ClaimBead and NEVER invokes the bd runner — so no mutation is committed for
// an actor the store cannot honor. This is the safety property the removed
// forwarding lacked: it mutated as BEADS_ACTOR before any actor check.
func TestCachingStoreOverBdStoreFailsClosedWithoutMutation(t *testing.T) {
	runnerCalls := 0
	backing := NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		runnerCalls++
		return []byte("{}"), nil
	})
	cache := NewCachingStoreForTest(backing, nil)

	_, ok, err := cache.ClaimBead(context.Background(), "gc-7", "actor-B")
	if ok {
		t.Fatal("ClaimBead ok = true over a BdStore backing; want false (unsupported)")
	}
	if !errors.Is(err, ErrAtomicClaimUnsupported) {
		t.Fatalf("ClaimBead err = %v, want ErrAtomicClaimUnsupported", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("bd runner was invoked %d times on an unsupported claim; want 0 (no wrong-owner mutation)", runnerCalls)
	}
}
