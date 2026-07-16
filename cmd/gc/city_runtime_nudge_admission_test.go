package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

func TestCityRuntimeNudgeAuthorityAdmitsThenWakesExactCommand(t *testing.T) {
	binding := &recordingCityRuntimeNudgeBinding{
		result: nudgequeue.NudgeIngressResult{Entry: nudgequeue.CommandIndexEntry{
			Command: &nudgequeue.Command{ID: "command-a"},
		}},
	}
	var woke []nudgeWakeHint
	authority := &cityRuntimeSessionNudgeAuthority{
		cr:      &CityRuntime{nudgeEffectOwnership: nudgeEffectOwnershipKeyed},
		binding: binding,
		wake: func(_ context.Context, hint nudgeWakeHint) {
			woke = append(woke, hint)
		},
	}

	request := nudgequeue.NudgeIngressRequest{RequestID: "request-a"}
	result, err := authority.Admit(t.Context(), request)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if result.Entry.Command == nil || result.Entry.Command.ID != "command-a" {
		t.Fatalf("result = %#v, want command-a", result)
	}
	if got := binding.admissionCount(); got != 1 {
		t.Fatalf("binding admissions = %d, want 1", got)
	}
	if len(woke) != 1 || woke[0].CommandID != "command-a" {
		t.Fatalf("wake hints = %#v, want exact command-a", woke)
	}
}

func TestCityRuntimeNudgeAuthorityLegacyOwnershipIsExplicitAndEffectFree(t *testing.T) {
	binding := &recordingCityRuntimeNudgeBinding{}
	woke := false
	authority := &cityRuntimeSessionNudgeAuthority{
		cr:      &CityRuntime{nudgeEffectOwnership: nudgeEffectOwnershipLegacy},
		binding: binding,
		wake:    func(context.Context, nudgeWakeHint) { woke = true },
	}

	_, err := authority.Admit(t.Context(), nudgequeue.NudgeIngressRequest{RequestID: "request-a"})
	if !errors.Is(err, errNudgeLegacyEffectOwnership) || !errors.Is(err, nudgequeue.ErrLocalNudgeAuthorityUnavailable) {
		t.Fatalf("Admit error = %v, want explicit legacy ownership + unavailable", err)
	}
	if got := binding.admissionCount(); got != 0 {
		t.Fatalf("binding admissions = %d, want 0", got)
	}
	if woke {
		t.Fatal("legacy ownership emitted a keyed wake")
	}
}

func TestCityRuntimeNudgeAuthorityFailedAdmissionDoesNotWake(t *testing.T) {
	binding := &recordingCityRuntimeNudgeBinding{err: nudgequeue.ErrNudgeAuthorizationDenied}
	woke := false
	authority := &cityRuntimeSessionNudgeAuthority{
		cr:      &CityRuntime{nudgeEffectOwnership: nudgeEffectOwnershipKeyed},
		binding: binding,
		wake:    func(context.Context, nudgeWakeHint) { woke = true },
	}

	if _, err := authority.Admit(t.Context(), nudgequeue.NudgeIngressRequest{RequestID: "request-a"}); !errors.Is(err, nudgequeue.ErrNudgeAuthorizationDenied) {
		t.Fatalf("Admit error = %v, want authorization denied", err)
	}
	if woke {
		t.Fatal("failed admission emitted a keyed wake")
	}
}

func TestCityRuntimeNudgeAuthorityRequesterScopeDelegatesToBinding(t *testing.T) {
	binding := &recordingCityRuntimeNudgeBinding{
		tenant: "tenant-a", city: "city-a", credential: "credential-a",
	}
	authority := &cityRuntimeSessionNudgeAuthority{
		cr:      &CityRuntime{nudgeEffectOwnership: nudgeEffectOwnershipKeyed},
		binding: binding,
	}

	tenant, city, credential := authority.RequesterScope()
	if tenant != "tenant-a" || city != "city-a" || credential != "credential-a" {
		t.Fatalf("RequesterScope = %q/%q/%q", tenant, city, credential)
	}
}

type recordingCityRuntimeNudgeBinding struct {
	mu         sync.Mutex
	tenant     string
	city       string
	credential string
	result     nudgequeue.NudgeIngressResult
	err        error
	admissions int
}

func (b *recordingCityRuntimeNudgeBinding) RequesterScope() (string, string, string) {
	return b.tenant, b.city, b.credential
}

func (b *recordingCityRuntimeNudgeBinding) Admit(_ context.Context, _ nudgequeue.NudgeIngressRequest) (nudgequeue.NudgeIngressResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.admissions++
	return b.result, b.err
}

func (b *recordingCityRuntimeNudgeBinding) admissionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.admissions
}
