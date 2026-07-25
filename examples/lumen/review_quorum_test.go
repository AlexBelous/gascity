package lumen_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/lumen/ir"
)

const (
	latestLumenUpstreamCommit = "44da8a985688568ba04a07d350028e0ef9b1b3e6"
	reviewQuorumIRSHA256      = "ff20c581e915e43b0addc143d6f6fda7df21f3c07c3f57c62edb59b71738158a"
)

func TestReviewQuorumMatchesPinnedLatestUpstreamCompiler(t *testing.T) {
	checkedIn, err := os.ReadFile("review-quorum.lumen.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := hashBytes(checkedIn); got != reviewQuorumIRSHA256 {
		t.Fatalf("review-quorum IR SHA-256 = %s, want %s", got, reviewQuorumIRSHA256)
	}

	upstream := os.Getenv("LUMEN_UPSTREAM_REPO")
	if upstream == "" {
		t.Log("LUMEN_UPSTREAM_REPO is unset; pinned artifact hash checked, live compiler identity skipped")
		return
	}
	head, err := exec.Command("git", "-C", upstream, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("read upstream HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(head)); got != latestLumenUpstreamCommit {
		t.Fatalf("upstream HEAD = %s, want pinned %s", got, latestLumenUpstreamCommit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	check := exec.CommandContext(ctx, "npm", "-w", "@formula-language/core", "run", "check")
	check.Dir = upstream
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("build pinned upstream compiler: %v\n%s", err, output)
	}

	source, err := filepath.Abs("review-quorum.lumen")
	if err != nil {
		t.Fatal(err)
	}
	const compileScript = `
const fs = require("node:fs");
const { compileLumenFormulaLanguage } = require(process.argv[1] + "/packages/core/dist");
const result = compileLumenFormulaLanguage({
  uri: "review-quorum.lumen",
  text: fs.readFileSync(process.argv[2], "utf8"),
});
const errors = (result.diagnostics || []).filter((diagnostic) => diagnostic.severity === "error");
if (errors.length || !result.formula) {
  process.stderr.write(JSON.stringify({ diagnostics: result.diagnostics }, null, 2) + "\n");
  process.exit(1);
}
process.stdout.write(JSON.stringify(result.formula, null, 2) + "\n");
`
	compile := exec.CommandContext(ctx, "node", "-e", compileScript, upstream, source)
	compiled, err := compile.CombinedOutput()
	if err != nil {
		t.Fatalf("compile review-quorum with pinned upstream: %v\n%s", err, compiled)
	}
	if !bytes.Equal(compiled, checkedIn) {
		t.Fatalf("pinned upstream compiler output differs from checked-in review-quorum IR: got SHA-256 %s, want %s",
			hashBytes(compiled), reviewQuorumIRSHA256)
	}
}

func TestLumenJSONExamplesPassStrictAdmission(t *testing.T) {
	files, err := filepath.Glob("*.lumen.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Lumen JSON examples found")
	}
	for _, file := range files {
		file := file
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ir.Decode(data); err != nil {
				t.Fatalf("strict IR admission: %v", err)
			}
		})
	}
}

func hashBytes(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestReviewQuorumIsARealDocumentWorkflow(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("review-quorum.lumen.json")
	if err != nil {
		t.Fatalf("reading compiled review quorum: %v", err)
	}
	doc, err := ir.Decode(data)
	if err != nil {
		t.Fatalf("decoding compiled review quorum: %v", err)
	}

	inputNames := make(map[string]bool, len(doc.Input.Fields))
	for _, field := range doc.Input.Fields {
		inputNames[field.Name] = true
	}
	for _, required := range []string{"document_path", "repository_path", "artifact_dir", "objective", "lane_one_id", "lane_two_id"} {
		if !inputNames[required] {
			t.Errorf("compiled formula input is missing %q", required)
		}
	}

	if len(doc.Nodes) != 3 {
		t.Fatalf("compiled formula top-level node count = %d, want 3", len(doc.Nodes))
	}
	topLevel := make(map[string]ir.Node, len(doc.Nodes))
	for _, node := range doc.Nodes {
		topLevel[node.ID] = node
	}

	lanes := requireReviewQuorumNode(t, topLevel, "lanes", ir.NodeScatter, nil)
	var scatter reviewQuorumScatterPayload
	decodeReviewQuorumNode(t, lanes, &scatter)
	if scatter.Form != "members" {
		t.Errorf("lanes.form = %q, want members", scatter.Form)
	}
	memberIDs := make([]string, 0, len(scatter.Members))
	for _, member := range scatter.Members {
		memberIDs = append(memberIDs, member.ID)
	}
	if want := []string{"reviewLaneOne", "reviewLaneTwo"}; !slices.Equal(memberIDs, want) {
		t.Fatalf("lanes.members = %v, want %v", memberIDs, want)
	}

	laneOne := requireReviewQuorumDo(t, scatter.Members[0], nil, "laneOneAgent")
	requireReviewQuorumBody(t, "reviewLaneOne", laneOne.Body.Raw,
		"{{ artifact_dir }}/lane-one.json", "{{ repository_path }}", ".lane-one.XXXXXX", "gc.output_json=$OUTPUT_JSON")
	laneTwo := requireReviewQuorumDo(t, scatter.Members[1], nil, "laneTwoAgent")
	requireReviewQuorumBody(t, "reviewLaneTwo", laneTwo.Body.Raw,
		"{{ artifact_dir }}/lane-two.json", "{{ repository_path }}", ".lane-two.XXXXXX", "gc.output_json=$OUTPUT_JSON")

	synthesisNode := requireReviewQuorumNode(t, topLevel, "synthesize", ir.NodeDo, []string{"lanes"})
	synthesis := requireReviewQuorumDo(t, synthesisNode, []string{"lanes"}, "")
	requireReviewQuorumBody(t, "synthesize", synthesis.Body.Raw,
		"{{ artifact_dir }}/lane-one.json", "{{ artifact_dir }}/lane-two.json",
		"{{ artifact_dir }}/synthesis.json", "{{ repository_path }}", "gc.output_json",
		"copy each finding ID byte-for-byte", "exactly once across incorporated_findings and deferred_findings",
		"($classified_ids | sort) == ($reviewer_ids | sort)")

	verifyNode := requireReviewQuorumNode(t, topLevel, "verify", ir.NodeDo, []string{"synthesize"})
	verification := requireReviewQuorumDo(t, verifyNode, []string{"synthesize"}, "verifierAgent")
	requireReviewQuorumBody(t, "verify", verification.Body.Raw,
		"{{ artifact_dir }}/verification.json", "{{ repository_path }}", "gc.output_json", "{{ synthesize }}",
		"no invented, combined, renamed, omitted, or duplicate finding IDs",
		"($classified_ids | sort) == ($reviewer_ids | sort)")
	if !verification.Body.hasTemplateRef("synthesize") {
		t.Error("verify compiled template does not consume the synthesize output")
	}

	workerPrompt, err := os.ReadFile("review-quorum-live/prompts/lumen-worker.md")
	if err != nil {
		t.Fatalf("reading live worker prompt: %v", err)
	}
	if !strings.Contains(string(workerPrompt), "gc runtime drain-ack") {
		t.Error("live worker prompt does not require the runtime return handshake")
	}
}

func TestReviewQuorumIROriginsArePortable(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("review-quorum.lumen.json")
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	count := 0
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if origin, ok := typed["origin"].(map[string]any); ok {
				if uri, ok := origin["uri"].(string); ok {
					count++
					if filepath.IsAbs(uri) || uri != "review-quorum.lumen" {
						t.Errorf("compiled origin URI = %q, want portable review-quorum.lumen", uri)
					}
				}
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(document)
	if count == 0 {
		t.Fatal("compiled review quorum contains no origin URIs")
	}
}

func TestReviewQuorumLiveCodexAgentsUseSupportedModel(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"laneTwoAgent", "verifierAgent"} {
		path := filepath.Join("review-quorum-live", "agents", name, "agent.toml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		text := string(data)
		if !strings.Contains(text, `provider = "codex"`) {
			t.Errorf("%s does not route through the Codex provider", path)
		}
		if !strings.Contains(text, `model = "gpt-5.5"`) {
			t.Errorf("%s does not pin the live-gateway-supported gpt-5.5 model", path)
		}
	}
}

func TestReviewQuorumLivePackDoesNotDuplicateRoutedWorkers(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("review-quorum-live", "pack.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[[named_session]]") {
		t.Fatal("live pack declares named sessions in addition to Lumen-routed ephemeral workers")
	}
}

type reviewQuorumScatterPayload struct {
	Form    string    `json:"form"`
	Members []ir.Node `json:"members"`
}

type reviewQuorumDoPayload struct {
	Source struct {
		Kind string `json:"kind"`
	} `json:"source"`
	Interpreter struct {
		Kind string `json:"kind"`
		Mode struct {
			Kind string `json:"kind"`
		} `json:"mode"`
		Agent *struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"agent"`
	} `json:"interpreter"`
	Body reviewQuorumBody `json:"body"`
}

type reviewQuorumBody struct {
	Raw      string `json:"raw"`
	Template struct {
		Parts []struct {
			Kind string `json:"kind"`
			Expr struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"expr"`
		} `json:"parts"`
	} `json:"template"`
}

func (body reviewQuorumBody) hasTemplateRef(name string) bool {
	for _, part := range body.Template.Parts {
		if part.Kind == "interp" && part.Expr.Kind == "ref" && part.Expr.Name == name {
			return true
		}
	}
	return false
}

func requireReviewQuorumNode(t *testing.T, nodes map[string]ir.Node, id string, kind ir.NodeKind, after []string) ir.Node {
	t.Helper()
	node, ok := nodes[id]
	if !ok {
		t.Fatalf("compiled formula is missing top-level node %q", id)
	}
	if node.Kind != kind {
		t.Errorf("%s.kind = %q, want %q", id, node.Kind, kind)
	}
	if !slices.Equal(node.After, after) {
		t.Errorf("%s.after = %v, want %v", id, node.After, after)
	}
	return node
}

func requireReviewQuorumDo(t *testing.T, node ir.Node, after []string, agentName string) reviewQuorumDoPayload {
	t.Helper()
	if node.Kind != ir.NodeDo {
		t.Fatalf("%s.kind = %q, want %q", node.ID, node.Kind, ir.NodeDo)
	}
	if !slices.Equal(node.After, after) {
		t.Errorf("%s.after = %v, want %v", node.ID, node.After, after)
	}
	var payload reviewQuorumDoPayload
	decodeReviewQuorumNode(t, node, &payload)
	if payload.Source.Kind != "prompt" {
		t.Errorf("%s.source.kind = %q, want prompt", node.ID, payload.Source.Kind)
	}
	if payload.Interpreter.Kind != "agent" || payload.Interpreter.Mode.Kind != "do" {
		t.Errorf("%s interpreter = %q/%q, want agent/do", node.ID, payload.Interpreter.Kind, payload.Interpreter.Mode.Kind)
	}
	if agentName == "" {
		if payload.Interpreter.Agent != nil {
			t.Errorf("%s compiled Agent route = %q, want unbound", node.ID, payload.Interpreter.Agent.Name)
		}
		return payload
	}
	if payload.Interpreter.Agent == nil {
		t.Fatalf("%s compiled Agent route is unbound, want %q", node.ID, agentName)
	}
	if payload.Interpreter.Agent.Kind != "ref" || payload.Interpreter.Agent.Name != agentName {
		t.Errorf("%s compiled Agent route = %q/%q, want ref/%q",
			node.ID, payload.Interpreter.Agent.Kind, payload.Interpreter.Agent.Name, agentName)
	}
	return payload
}

func decodeReviewQuorumNode(t *testing.T, node ir.Node, dst any) {
	t.Helper()
	data, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshaling compiled node %q: %v", node.ID, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decoding compiled node %q payload: %v", node.ID, err)
	}
}

func requireReviewQuorumBody(t *testing.T, nodeID, body string, contracts ...string) {
	t.Helper()
	for _, contract := range contracts {
		if !strings.Contains(body, contract) {
			t.Errorf("%s compiled prompt is missing contract %q", nodeID, contract)
		}
	}
}
