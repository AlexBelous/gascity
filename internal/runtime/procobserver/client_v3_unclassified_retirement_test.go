package procobserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClientV3UnclassifiedRetirementKeepsOuterFences(t *testing.T) {
	for _, name := range []string{"exact", "caller", "root loss", "root retained", "wrong source", "wrong namespace", "stale"} {
		t.Run(name, func(t *testing.T) {
			p, r, now := clientV3Fixture()
			c, e := unclassifiedRetirementFixture()
			r.Census, r.Errors, r.ErrorsTotal, r.DurationMS = c, e, len(e), 8
			r.StartedAt = r.FinishedAt.Add(-8 * time.Millisecond)
			r.EnumeratedCountBefore, r.EnumeratedCountAfter = c.Scans[0].EnumeratedCount, c.Scans[2].EnumeratedCount
			r.EnumerationDigestBefore, r.EnumerationDigestAfter = c.Scans[0].EnumerationDigest, c.Scans[2].EnumerationDigest
			switch name {
			case "caller":
				r.Census.Proofs[0].PID, r.Errors[0].PID = r.CallerBinding.PID, r.CallerBinding.PID
			case "root loss":
				r.Roots = []Root{}
			case "root retained":
				r.Census.Proofs[0].PID, r.Errors[0].PID = r.Roots[0].PID, r.Roots[0].PID
			case "wrong source":
				r.HelperSourceRevision = strings.Repeat("f", 40)
			case "wrong namespace":
				r.PIDNamespaceIdentity = "pid:[998]"
			case "stale":
				now = now.Add(61 * time.Second)
			}
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeResponseV3(data, p, r.RequestNonce, now)
			if (err == nil) != (name == "exact") {
				t.Fatalf("accepted=%v: %v", err == nil, err)
			}
			if err == nil {
				anchors, err := got.RetirementAnchors()
				if err != nil || len(anchors) != 0 {
					t.Fatalf("unknown proof granted an anchor: %v", err)
				}
				if got.Errors[0].ResolvedBy != 0 || got.Errors[0].StartTicks == nil {
					t.Fatal("raw error changed")
				}
			}
		})
	}
}
