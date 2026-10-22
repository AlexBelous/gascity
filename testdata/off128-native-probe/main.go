// A manual opt-in probe against the isolated pinned native bd/Dolt fixture.
// It is not a production command and is excluded from ordinary ./... discovery.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/beads"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func native(r beads.CommandRunner, dir, id string) map[string]any {
	out, err := r(dir, "bd", "--sandbox", "show", id, "--json")
	must(err)
	var rows []map[string]any
	must(json.Unmarshal(out, &rows))
	if len(rows) != 1 {
		panic("expected exact native row")
	}
	return rows[0]
}

func main() {
	root := "/srv/gc-build/workspaces/off128-pinned-claim-20261022"
	dir := filepath.Join(root, "repo")
	exact := beads.ExecCommandRunnerWithExactEnvContext(context.Background(), map[string]string{
		"HOME": filepath.Join(root, "home"), "PATH": filepath.Join(root, "bin") + ":/usr/bin:/bin",
		"BEADS_ACTOR": "off128-sdk-probe", "BD_SYNC_DISABLE": "1", "CI": "true", "GOMAXPROCS": "2",
	})
	runner := func(d, name string, args ...string) ([]byte, error) {
		if d != dir || name != "bd" {
			panic("unexpected target or command")
		}
		fmt.Printf("native argv: %q\n", args)
		return exact(d, name, args...)
	}
	store := beads.NewBdStore(dir, runner)
	for _, status := range []string{"open", "blocked", "deferred"} {
		created, err := store.Create(beads.Bead{
			Title: "SDK native fixture " + status, Status: status,
			Labels: []string{"hold:external"}, Metadata: beads.StringMap{"gc.reservation": "pending"},
		})
		must(err)
		before := native(exact, dir, created.ID)
		if before["status"] != status {
			panic("native initial status differs")
		}
		claimed, ok, claimErr := store.Claim(created.ID)
		after := native(exact, dir, created.ID)
		record := map[string]any{
			"requested_status": status, "id": created.ID, "sdk_create_status": created.Status,
			"native_before_status": before["status"], "sdk_claim_ok": ok, "sdk_claim_status": claimed.Status,
			"native_after_status": after["status"], "native_after_assignee": after["assignee"],
		}
		if claimErr != nil {
			record["sdk_claim_error"] = claimErr.Error()
		}
		out, err := json.Marshal(record)
		must(err)
		fmt.Println(string(out))
		if status == "open" {
			if claimErr != nil || !ok || after["status"] != "in_progress" {
				panic("positive native control failed")
			}
		} else if ok || after["status"] != status || after["assignee"] != nil {
			panic("reserved native bead was claimed")
		}
	}
	fmt.Println("PASS: exact SDK8084 create/status -> pinned native bd/Dolt -> direct Claim controls")
	_ = os.Stdout.Sync()
}
