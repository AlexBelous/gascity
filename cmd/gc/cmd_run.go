package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	lumenstore "github.com/gastownhall/gascity/internal/lumen"
	"github.com/gastownhall/gascity/internal/lumen/engine"
	"github.com/gastownhall/gascity/internal/lumen/ir"
	"github.com/spf13/cobra"
)

const runPollInterval = 100 * time.Millisecond

type lumenRunCompletion struct {
	view engine.RunView
	run  engine.RunResult
}

func newRunCmd(stdout, stderr io.Writer) *cobra.Command {
	var inputJSON string
	var route string
	cmd := &cobra.Command{
		Use:   "run <formula.lumen|formula.lumen.json>",
		Short: "Run a formula through the beta Lumen runtime",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return exitForCode(doRun(ctx, args[0], inputJSON, route, stdout, stderr))
		},
	}
	cmd.Flags().StringVar(&inputJSON, "input", "", "formula input as a JSON object")
	cmd.Flags().StringVar(&route, "route", "", "default agent route for unbound do steps")
	return cmd
}

func doRun(ctx context.Context, sourcePath, inputJSON, route string, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc run: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: loading city config: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !cfg.Daemon.LumenBetaEnabled() {
		fmt.Fprintln(stderr, "gc run: Lumen beta runtime is disabled for this city; set [daemon] lumen_beta = true to opt in") //nolint:errcheck // best-effort stderr
		return 1
	}

	irPath, err := resolveLumenIRPath(sourcePath)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	raw, err := os.ReadFile(irPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: reading %s: %v\n", irPath, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	doc, err := ir.Decode(raw)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: decoding %s: %v\n", irPath, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	input, err := parseRunInput(inputJSON)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	store, err := lumenstore.Open(ctx, cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	defer func() { _ = store.Close() }()

	streamID, err := store.Enqueue(ctx, doc, input, irPath, route, engine.DriverController)
	if err != nil {
		fmt.Fprintf(stderr, "gc run: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	fmt.Fprintf(stdout, "lumen run: %s  (stream %s)\n", doc.Name, streamID) //nolint:errcheck // best-effort stdout

	if err := pokeController(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc run: controller poke failed (%v); the run will advance on the next controller tick\n", err) //nolint:errcheck // best-effort stderr
	}
	completion, err := waitForLumenRun(ctx, store, streamID, runPollInterval)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "gc run: detached from %s; the durable run continues in %s\n", streamID, cityPath) //nolint:errcheck // best-effort stderr
			return 130
		}
		fmt.Fprintf(stderr, "gc run: waiting for %s: %v\n", streamID, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := printLumenRunCompletion(stdout, completion); err != nil {
		fmt.Fprintf(stderr, "gc run: displaying %s: %v\n", streamID, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !engine.IsSucceededOutcome(completion.run.Outcome) {
		return 1
	}
	return 0
}

func resolveLumenIRPath(sourcePath string) (string, error) {
	var irPath string
	switch {
	case strings.HasSuffix(sourcePath, ".lumen.json"):
		irPath = sourcePath
	case strings.HasSuffix(sourcePath, ".lumen"):
		irPath = sourcePath + ".json"
	default:
		return "", fmt.Errorf("formula path %q must end in .lumen or .lumen.json", sourcePath)
	}
	if _, err := os.Stat(irPath); err != nil {
		return "", fmt.Errorf("compiled IR %q: %w", irPath, err)
	}
	return irPath, nil
}

func parseRunInput(inputJSON string) (map[string]any, error) {
	if strings.TrimSpace(inputJSON) == "" {
		return nil, nil
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return nil, fmt.Errorf("parsing --input as a JSON object: %w", err)
	}
	if input == nil {
		return nil, fmt.Errorf("parsing --input as a JSON object: value must be an object")
	}
	return input, nil
}

func waitForLumenRun(
	ctx context.Context,
	store *lumenstore.Store,
	streamID string,
	pollInterval time.Duration,
) (lumenRunCompletion, error) {
	if pollInterval <= 0 {
		return lumenRunCompletion{}, fmt.Errorf("poll interval must be positive")
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		view, err := engine.FoldRunView(ctx, store.Journal(), streamID)
		if err != nil {
			return lumenRunCompletion{}, err
		}
		if view.Closed {
			if view.Result == nil {
				return lumenRunCompletion{}, fmt.Errorf("terminal run has no typed result")
			}
			events, err := store.Journal().ReadStream(ctx, streamID, 1, 0)
			if err != nil {
				return lumenRunCompletion{}, fmt.Errorf("reading terminal journal: %w", err)
			}
			return lumenRunCompletion{
				view: view,
				run: engine.RunResult{
					StreamID: streamID,
					Outcome:  view.Outcome,
					Result:   *view.Result,
					Events:   events,
				},
			}, nil
		}
		select {
		case <-ctx.Done():
			return lumenRunCompletion{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func printLumenRunCompletion(stdout io.Writer, completion lumenRunCompletion) error {
	outputs := make(map[string]string)
	for _, event := range completion.run.Events {
		if event.Type != engine.EventOutcomeSettled {
			continue
		}
		var payload struct {
			Activation string `json:"activation"`
			Output     string `json:"output"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decoding %s at seq %d: %w", event.Type, event.Seq, err)
		}
		outputs[payload.Activation] = payload.Output
	}

	for _, activation := range completion.view.Activations {
		if !activation.Settled {
			continue
		}
		fmt.Fprintf(stdout, "  %s  [%s]  %s\n", activation.NodeID, activation.Kind, activation.Outcome) //nolint:errcheck // best-effort stdout
		for _, line := range outputLines(outputs[activation.Activation]) {
			fmt.Fprintf(stdout, "    %s\n", line) //nolint:errcheck // best-effort stdout
		}
	}
	resultJSON, err := json.Marshal(completion.run.Result)
	if err != nil {
		return fmt.Errorf("encoding typed result: %w", err)
	}
	fmt.Fprintf(stdout, "result: %s\n", resultJSON)              //nolint:errcheck // best-effort stdout
	fmt.Fprintf(stdout, "outcome: %s\n", completion.run.Outcome) //nolint:errcheck // best-effort stdout
	return nil
}

func outputLines(output string) []string {
	if output == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(output, "\n"), "\n")
}
