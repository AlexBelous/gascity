package main

// gc session show: the session-class replacement for probing session beads
// with `gc bd show` (the design's bd-surface story, item 2). On a migrated
// city the session bead lives in the sessions class store where `gc bd
// show` no longer sees it; this command reads through the routed session
// front door, so orphan-sweep.sh's liveness probe (and any operator
// by-hand check) works identically on bd and sqlite backends. The --json
// shape deliberately mirrors the bead fields the script's jq reads
// (issue_type/status/metadata.{state,session_name,alias,agent_name}).

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/spf13/cobra"
)

// sessionShowJSON is the `gc session show --json` wire shape: the persisted
// session projection under bd-show-compatible field names.
type sessionShowJSON struct {
	ID        string                  `json:"id"`
	IssueType string                  `json:"issue_type"`
	Status    string                  `json:"status"`
	Title     string                  `json:"title,omitempty"`
	Metadata  sessionShowMetadataJSON `json:"metadata"`
}

// sessionShowMetadataJSON carries the metadata keys the liveness probes
// consume.
type sessionShowMetadataJSON struct {
	State       string `json:"state,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Alias       string `json:"alias,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
	Template    string `json:"template,omitempty"`
}

// newSessionShowCmd creates the "gc session show <id-or-alias>" command.
func newSessionShowCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show <session-id-or-alias>",
		Short: "Show a session's persisted state",
		Long: `Show the persisted state of a session bead: id, status, lifecycle state,
and the identity fields (session_name, alias, agent_name, template).

Reads through the session-class store, so it works identically whether the
city keeps sessions on the bd store or has migrated them to the embedded
sessions store.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdSessionShow(args, jsonOutput, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
		ValidArgsFunction: completeSessionIDs,
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

func cmdSessionShow(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	store, code := openCityStore(stderr, "gc session show")
	if store == nil {
		return code
	}
	cityPath, cityErr := resolveCity()
	var cfg *config.City
	if cityErr == nil {
		cfg, _ = loadCityConfig(cityPath, stderr)
	}
	sessStore := cliSessionStore(store, cfg, cityPath)

	id, err := resolveSessionIDWithConfig(cityPath, cfg, sessStore, args[0])
	if err != nil {
		fmt.Fprintf(stderr, "gc session show: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	front := sessionFrontDoor(sessStore)
	info, err := front.Get(id)
	if err != nil {
		fmt.Fprintf(stderr, "gc session show: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	status := "open"
	if info.Closed {
		status = "closed"
	}
	if jsonOutput {
		out := sessionShowJSON{
			ID:        info.ID,
			IssueType: info.Type,
			Status:    status,
			Title:     info.Title,
			Metadata: sessionShowMetadataJSON{
				State:       info.MetadataState,
				SessionName: info.SessionNameMetadata,
				Alias:       info.Alias,
				AgentName:   info.AgentName,
				Template:    info.Template,
			},
		}
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(stderr, "gc session show: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "id:           %s\n", info.ID)            //nolint:errcheck // best-effort stdout
	fmt.Fprintf(stdout, "status:       %s\n", status)             //nolint:errcheck // best-effort stdout
	fmt.Fprintf(stdout, "state:        %s\n", info.MetadataState) //nolint:errcheck // best-effort stdout
	fmt.Fprintf(stdout, "session_name: %s\n", info.SessionName)   //nolint:errcheck // best-effort stdout
	if info.Alias != "" {
		fmt.Fprintf(stdout, "alias:        %s\n", info.Alias) //nolint:errcheck // best-effort stdout
	}
	if info.AgentName != "" {
		fmt.Fprintf(stdout, "agent_name:   %s\n", info.AgentName) //nolint:errcheck // best-effort stdout
	}
	if info.Template != "" {
		fmt.Fprintf(stdout, "template:     %s\n", info.Template) //nolint:errcheck // best-effort stdout
	}
	return 0
}
