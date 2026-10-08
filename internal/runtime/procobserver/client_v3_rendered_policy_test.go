package procobserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The optional input contains unmodified bytes from the actual pure rebind
// renderer. No deployment files, process bindings or caller pins are refreshed.
func TestClientV3ActualRenderedSelector(t *testing.T) {
	dir := os.Getenv("GC_TEST_RENDERED_POLICY_DIR")
	if dir == "" {
		t.Skip("actual pure renderer fixture not supplied")
	}
	for _, name := range []string{"v3-client.json", "legacy-client.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			p, err := decodePolicyV3(raw, strings.Repeat("a", 40))
			if name == "legacy-client.json" {
				if err == nil {
					t.Fatal("legacy renderer bytes granted V3 authority")
				}
				return
			}
			if err != nil {
				t.Fatalf("actual V3 renderer bytes rejected: %v", err)
			}
			if p.EvidenceSchema != ResponseSchemaV3 || p.HelperUID != 62027 || p.CallerBinding.PID != 42 || p.CallerBinding.UID != 1000 || p.CallerBinding.StartTicks != "17" || p.HelperBinarySHA256 != strings.Repeat("c", 64) {
				t.Fatal("renderer changed reviewed identity or selector")
			}
			if _, err := decodePolicyV3(raw, strings.Repeat("f", 40)); err == nil {
				t.Fatal("actual rendered selector bypassed source pin")
			}
		})
	}
}
