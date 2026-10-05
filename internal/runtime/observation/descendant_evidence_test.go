package observation

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

func TestDescendantAnchorsRequireBothFreshProviderJoins(t *testing.T) {
	for _, mode := range []string{"verified", "anchor PID wrong", "anchor start wrong", "anchor token wrong", "anchor epoch wrong", "anchor city wrong", "anchor template wrong", "anchor SID wrong", "second frame contract stripped", "second frame root lost", "provider owner lost", "city context without provider ownership"} {
		t.Run(mode, func(t *testing.T) {
			p, roots := fixture(t)
			ep := &evidenceProvider{observedProvider: p}
			now := time.Date(2026, 10, 4, 22, 45, 0, 0, time.UTC)
			calls := 0
			read := func() ProcessEvidence {
				calls++
				anchor := roots[0]
				e := ProcessEvidence{Roots: append([]proctable.ObservedRoot{}, roots...), StartedAt: now, FinishedAt: now, CensusContract: "bounded-process-census/v3"}
				switch mode {
				case "anchor PID wrong":
					anchor.Runtime.PID++
				case "anchor start wrong":
					anchor.StartIdentity = "different"
				case "anchor token wrong":
					anchor.InstanceTokenSHA256 = TokenDigest("different")
				case "anchor epoch wrong":
					anchor.Runtime.Epoch++
				case "anchor city wrong":
					anchor.Runtime.City = "/other"
				case "anchor template wrong":
					anchor.Template = "other"
				case "anchor SID wrong":
					anchor.Runtime.SessionID = "other"
				case "second frame contract stripped":
					if calls == 2 {
						e.CensusContract = ""
					}
				case "second frame root lost":
					if calls == 2 {
						e.Roots = e.Roots[1:]
					}
				case "provider owner lost":
					ep.roots[0].IsTracked = false
				case "city context without provider ownership":
					// A proc root under a context-only parent still needs the
					// provider's session owner in both fresh joins.
					if err := ep.SetMeta(ep.names[0], "GC_SESSION_ID", ""); err != nil {
						t.Fatal(err)
					}
				}
				e.RetirementAnchors = []proctable.ObservedRoot{anchor}
				return e
			}
			got := ObserveProcessEvidence("/city", ep, read, func() time.Time { return now })
			if mode == "city context without provider ownership" {
				if got.ProviderComplete {
					t.Fatal("context/proc roots manufactured provider ownership")
				}
			} else if got.ProcessComplete != (mode == "verified") {
				t.Fatalf("mode=%s complete=%v reasons=%v", mode, got.ProcessComplete, got.UnknownReasons)
			}
			if (got.CertificateDisposition == "provider_verified") != (mode == "verified") {
				t.Fatalf("false final disposition: %s %+v", mode, got)
			}
		})
	}
}
