package api

import (
	"os"
	"testing"
)

func TestResolveClaimAdmissionSlots(t *testing.T) {
	t.Run("unset uses default", func(t *testing.T) {
		// Snapshot + restore so unsetting the override does not leak to sibling
		// tests; exercises the LookupEnv-absent branch.
		if prev, ok := os.LookupEnv(claimAdmissionSlotsEnv); ok {
			t.Cleanup(func() { _ = os.Setenv(claimAdmissionSlotsEnv, prev) })
		}
		if err := os.Unsetenv(claimAdmissionSlotsEnv); err != nil {
			t.Fatalf("unset: %v", err)
		}
		if got := resolveClaimAdmissionSlots(); got != defaultClaimAdmissionSlots {
			t.Fatalf("resolveClaimAdmissionSlots = %d, want default %d", got, defaultClaimAdmissionSlots)
		}
	})

	overrides := []struct {
		name string
		val  string
		want int
	}{
		{"valid override", "8", 8},
		{"non-integer falls back", "not-a-number", defaultClaimAdmissionSlots},
		{"zero falls back", "0", defaultClaimAdmissionSlots},
		{"negative falls back", "-4", defaultClaimAdmissionSlots},
	}
	for _, tc := range overrides {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(claimAdmissionSlotsEnv, tc.val)
			if got := resolveClaimAdmissionSlots(); got != tc.want {
				t.Fatalf("resolveClaimAdmissionSlots = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNewClaimAdmitterClampsNonPositive(t *testing.T) {
	// A non-positive size must clamp to a serializing gate of 1, never a nil
	// (always-saturated) or panicking channel.
	a := newClaimAdmitter(0)
	release, ok := a.tryAcquire()
	if !ok {
		t.Fatal("first acquire on clamped gate failed, want ok")
	}
	if _, ok := a.tryAcquire(); ok {
		t.Fatal("second acquire succeeded on a size-1 gate, want saturated")
	}
	release()
	if _, ok := a.tryAcquire(); !ok {
		t.Fatal("acquire after release failed, want ok")
	}
}
