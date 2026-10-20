package session

import (
	"context"
	"strings"
	"testing"
	"time"
)

type remoteAdmissionProfileTestProvider struct {
	*remoteAdmissionTestProvider
	profiles []RemoteAdmissionProfile
}

func (p *remoteAdmissionProfileTestProvider) RemoteAdmissionProfiles() []RemoteAdmissionProfile {
	return p.profiles
}

func TestRemoteAdmissionProviderProfileCapability(t *testing.T) {
	for _, mode := range []string{"provider", "explicit-wrong", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			city, store, b, p, a := remoteAdmissionFixture(t)
			wrapper := &remoteAdmissionProfileTestProvider{remoteAdmissionTestProvider: p, profiles: []RemoteAdmissionProfile{a.profile}}
			ctx := context.Background()
			if mode == "explicit-wrong" {
				wrong := a.profile
				wrong.Binding.PolicyDigest = strings.Repeat("0", 64)
				var e error
				ctx, e = WithRemoteAdmissionProfiles(ctx, wrong)
				if e != nil {
					t.Fatal(e)
				}
			}
			if mode == "conflict" {
				ctx = remoteAdmissionContext(t, a)
				wrapper.profiles[0].SourceSHA = strings.Repeat("0", 40)
			}
			_, ok, _ := AdmitCapacityStart(ctx, city, "worker", b.ID, time.Now(), wrapper, store)
			if mode == "provider" {
				if !ok || a.prepares != 1 {
					t.Fatal("trusted provider authority not carried to public admission")
				}
			} else if ok || a.prepares != 0 {
				t.Fatal("explicit context conflict fell back to provider authority")
			}
		})
	}
}
