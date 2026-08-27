package scripts_test

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// Rule (a-d): the GREP census.
//
// It counts the store-enumeration vocabulary declared in
// scripts/residency-boundary-patterns.txt, per (file, enclosing function,
// pattern), and ratchets those counts against the shared baseline.
// scripts/check-residency-boundary.sh is the shell rendering of the same rule
// over the same pattern file; residency_halves_agree_test.go pins that the two
// police the same tree with the same exemptions.
//
// See residency_boundary_test.go for the contract this rule is one quarter of.

type residencyPattern struct {
	name  string
	regex *regexp.Regexp

	// core is a literal string every line this pattern can match MUST contain,
	// derived from the parsed pattern. strings.Contains(line, core) is
	// therefore a sound screen in front of the regex: it can only skip lines
	// the regex would have missed anyway.
	core string

	// witness is a synthesized line the pattern matches. It is the load-time
	// proof that core was derived from a REQUIRED position, and it is what the
	// equivalence control builds its on-disk fixture out of — so a vocabulary
	// row added to the pattern file is exercised by that control on the day it
	// lands, with no second hand-kept list.
	witness string
}

// residencyPrefilter selects whether the census screens each line with a
// pattern's required literal before running that pattern's regex.
//
// It is a parameter rather than a constant because the prefilter's entire
// contract is that it changes NOTHING: the equivalence control runs the same
// census both ways over the same on-disk tree and refuses a single differing
// row. "It got faster" is not evidence that it still counts the same sites,
// and a guard that has quietly stopped biting is worse than a slow one.
type residencyPrefilter bool

const (
	residencyPrefilterOn  residencyPrefilter = true
	residencyPrefilterOff residencyPrefilter = false
)

// loadResidencyPatterns reads the forbidden vocabulary from the file the shell
// guard reads. One source of truth: two hand-kept copies of "what is forbidden"
// would be this guard's own bug class, one level up.
//
// It also derives each row's prefilter core and self-checks the derivation. A
// derivation that cannot be made fails the whole suite here rather than
// degrading to an unscreened scan: the census is the product, the prefilter is
// only its cost, and a guard is allowed to be slow but never allowed to be
// quietly narrower than its own pattern file.
func loadResidencyPatterns(t *testing.T, dir string) []residencyPattern {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "residency-boundary-patterns.txt"))
	if err != nil {
		t.Fatalf("opening the pattern file: %v", err)
	}
	defer f.Close() //nolint:errcheck // read-only

	var out []residencyPattern
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, expr, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("pattern row %q is not name<TAB>regex", line)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			t.Fatalf("pattern %q: %v", name, err)
		}
		parsed, err := syntax.Parse(expr, syntax.Perl)
		if err != nil {
			t.Fatalf("pattern %q: parsing for the prefilter core: %v", name, err)
		}
		core, err := residencyPatternCore(parsed)
		if err != nil {
			t.Fatalf("pattern %q: %v", name, err)
		}
		witness, err := residencyPatternWitness(parsed)
		if err != nil {
			t.Fatalf("pattern %q: %v", name, err)
		}
		if strings.ContainsAny(witness, "\n\r") {
			t.Fatalf("pattern %q: the synthesized witness %q spans lines, and this census is line-oriented; neither the self-check below nor the equivalence fixture can carry it", name, witness)
		}
		if !re.MatchString(witness) {
			t.Fatalf("pattern %q: the synthesized witness %q does not match its own pattern; the witness generator does not understand this row, so nothing it certifies about the core %q can be trusted", name, witness, core)
		}
		if !strings.Contains(witness, core) {
			t.Fatalf("pattern %q: the derived core %q is absent from %q, a line the pattern matches; the prefilter would drop a real enumeration site", name, core, witness)
		}
		out = append(out, residencyPattern{name: name, regex: re, core: core, witness: witness})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the pattern file: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the pattern file declares no pattern; the guard is evaluating nothing")
	}
	return out
}

// residencyPatternCore derives, from a parsed vocabulary pattern, a literal
// string every line that pattern can match must contain.
//
// It reads the PARSE TREE rather than editing the pattern text, because the
// property the prefilter needs is not "the longest run of ordinary characters"
// but "a literal the match cannot avoid", and the two part company on the rows
// this file already carries. Strip the metacharacters from
// `cliSoleClassBinding(Store)?\(` textually and the answer is
// `cliSoleClassBindingStore(` — a core no call of the un-suffixed accessor
// contains, so the census would stop counting exactly the sites that row exists
// to count, and stop silently. That is this guard's own bug class one level up:
// a rule that still runs, still passes, and no longer sees anything.
//
// Only literals on the unconditional path are eligible — a concatenation's own,
// and those of the groups it captures unconditionally. Everything under an
// alternation or a quantifier is skipped: for an alternation or an optional
// group that is required, since a match need not go through it; for a `+` it is
// merely conservative. Conservative is the safe direction — a screen weaker than
// it could be still counts every site, while one stronger than the pattern
// justifies stops counting, and stops silently. The longest eligible literal is
// simply the strongest of the sound choices.
func residencyPatternCore(re *syntax.Regexp) (string, error) {
	best := ""
	var walk func(*syntax.Regexp)
	walk = func(r *syntax.Regexp) {
		switch r.Op {
		case syntax.OpLiteral:
			// A case-folded literal is not a substring requirement for the
			// case-sensitive strings.Contains the scan uses, and Go parses
			// (?i)abc as the literal "ABC" — using those runes verbatim would
			// screen out every real line.
			if r.Flags&syntax.FoldCase != 0 {
				return
			}
			if lit := string(r.Rune); len(lit) > len(best) {
				best = lit
			}
		case syntax.OpConcat, syntax.OpCapture:
			for _, sub := range r.Sub {
				walk(sub)
			}
		}
	}
	walk(re)
	if best == "" {
		return "", fmt.Errorf("no required literal could be derived from %q, so the prefilter has no sound screen for it; give the row a literal core or teach residencyPatternCore this shape — do not scan without one", re.String())
	}
	return best, nil
}

// residencyPatternWitness synthesizes one line the pattern matches, taking the
// shortest route through every choice the pattern offers.
//
// It has two jobs. At load time it is the self-check: a core absent from a line
// its own pattern matches was not derived from a required position, and the
// suite fails rather than censusing the tree through a screen nobody checked.
// In the controls it is the equivalence fixture, so the vocabulary file stays
// the single source of truth for what the equivalence row exercises.
//
// An operator it does not understand is an error, never a best guess: a bogus
// witness would let both jobs report a success they did not earn.
func residencyPatternWitness(re *syntax.Regexp) (string, error) {
	switch re.Op {
	case syntax.OpLiteral:
		// A folded literal's runes are the folding orbit's representatives
		// ((?i)abc parses as "ABC"), so emitting them verbatim would not be a
		// witness. residencyPatternCore refuses folded literals for the same
		// reason; there are none in the pattern file and there is no honest
		// way to invent one here.
		if re.Flags&syntax.FoldCase != 0 {
			return "", fmt.Errorf("case-folded literal %q has no verbatim witness", string(re.Rune))
		}
		return string(re.Rune), nil
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary:
		return "", nil
	case syntax.OpQuest, syntax.OpStar:
		return "", nil
	case syntax.OpCapture, syntax.OpPlus:
		return residencyPatternWitness(re.Sub[0])
	case syntax.OpRepeat:
		sub, err := residencyPatternWitness(re.Sub[0])
		if err != nil {
			return "", err
		}
		return strings.Repeat(sub, re.Min), nil
	case syntax.OpConcat:
		var b strings.Builder
		for _, part := range re.Sub {
			w, err := residencyPatternWitness(part)
			if err != nil {
				return "", err
			}
			b.WriteString(w)
		}
		return b.String(), nil
	case syntax.OpAlternate:
		for _, branch := range re.Sub {
			if w, err := residencyPatternWitness(branch); err == nil {
				return w, nil
			}
		}
		return "", fmt.Errorf("no branch of %q has a witness", re.String())
	case syntax.OpCharClass:
		// The first PRINTABLE ASCII rune in the class. A negated identifier
		// class such as [^A-Za-z0-9_] begins at NUL, and a NUL in a fixture
		// line would be a witness no source file could carry.
		for i := 0; i+1 < len(re.Rune); i += 2 {
			for r := re.Rune[i]; r <= re.Rune[i+1] && r < 0x7f; r++ {
				if r >= 0x20 {
					return string(r), nil
				}
			}
		}
		return "", fmt.Errorf("class %q holds no printable ASCII rune", re.String())
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return "x", nil
	}
	return "", fmt.Errorf("unsupported operator %v in %q", re.Op, re.String())
}

// The equivalence control's fixture: one file, one function, one line per
// vocabulary row.
const (
	residencyWitnessFixturePath = "cmd/gc/witness_census.go"
	residencyWitnessFixtureFunc = "everyPattern"
)

// residencyWitnessFixture renders a fixture file carrying every pattern's own
// synthesized witness, one per line.
//
// The body is deliberately not compilable Go. This census is textual by design
// (see the enclosingFuncRe comment below), and deriving the fixture from the
// pattern file rather than hand-writing call sites is what keeps the
// equivalence control exhaustive as the vocabulary grows: a row nobody wrote a
// fixture for would make that control vacuous for exactly the row most likely
// to be mis-derived — the newest one.
func residencyWitnessFixture(patterns []residencyPattern) string {
	var b strings.Builder
	b.WriteString("package main\n\nfunc " + residencyWitnessFixtureFunc + "() {\n")
	for _, p := range patterns {
		b.WriteString("\t" + p.witness + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// residencyCensusDiff reports every key whose count differs between two
// censuses of the same tree, naming each side so the failure says which scan
// lost the row.
func residencyCensusDiff(a, b map[string]int, aLabel, bLabel string) []string {
	var out []string
	for _, key := range sortedKeys(a) {
		if a[key] != b[key] {
			out = append(out, fmt.Sprintf("%s: %d with %s, %d with %s", strings.ReplaceAll(key, "\t", " "), a[key], aLabel, b[key], bLabel))
		}
	}
	for _, key := range sortedKeys(b) {
		if _, ok := a[key]; !ok {
			out = append(out, fmt.Sprintf("%s: absent with %s, %d with %s", strings.ReplaceAll(key, "\t", " "), aLabel, b[key], bLabel))
		}
	}
	return out
}

// enclosingFuncRe and topLevelCloseRe implement the enclosing-function rule the
// shell guard's awk pass implements, line for line. gofmt guarantees a
// top-level declaration starts at column 0 and only a top-level body closes
// with a `}` at column 0, so a two-line state machine attributes every line to
// its top-level function — a closure's hits belong to the function containing
// it — or to (file-scope). `make fmt-check` is what keeps that guarantee true.
//
// The rule is textual rather than go/ast on purpose: the shell half cannot
// parse Go, and a guard whose two halves key their baseline differently is the
// drift this whole lane exists to prevent. Exact agreement with go/ast is not
// required; exact agreement WITH EACH OTHER is, and both halves scan the real
// tree against the one baseline, so any divergence fails one of them.
var (
	enclosingFuncRe = regexp.MustCompile(`^func[ \t]+(?:\([^)]*\)[ \t]*)?([A-Za-z0-9_]+)`)
	topLevelCloseRe = regexp.MustCompile(`^\}`)
	commentLineRe   = regexp.MustCompile(`^[ \t]*(//|\*|/\*)`)
)

const residencyFileScope = "(file-scope)"

// scanResidencyGrepSites counts, per (path, enclosing function, pattern), the
// non-test source LINES carrying the forbidden vocabulary.
//
// The enclosing function is part of the key because a count keyed by file alone
// is MASKABLE: delete one call and add a different one in another function of
// the same file, and the count is level. Family (a) — the bulk of the baseline
// — is consumption-shaped, so the signature-level AST half cannot see that swap
// either. The residual, stated honestly, is a swap within one function.
//
// prefilter screens each line with that pattern's required literal before
// running its regex, which is what keeps a full-tree census off the seconds
// scale. Off is not a production mode: it exists so the equivalence control can
// scan one tree both ways and prove the screen is invisible.
func scanResidencyGrepSites(root string, dirs []string, allowlist map[string]bool, patterns []residencyPattern, prefilter residencyPrefilter) (map[string]int, error) {
	found := map[string]int{}
	for _, dir := range dirs {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(abs, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			name := entry.Name()
			if entry.IsDir() {
				if path != abs && residencyPruned(name) {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if allowlist[rel] {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			fn := residencyFileScope
			for _, line := range strings.Split(string(data), "\n") {
				if m := enclosingFuncRe.FindStringSubmatch(line); m != nil {
					fn = m[1]
				} else if topLevelCloseRe.MatchString(line) {
					fn = residencyFileScope
				}
				if commentLineRe.MatchString(line) || strings.Contains(line, residencyAllowMarker) {
					continue
				}
				for _, p := range patterns {
					// p.core is a literal every line this pattern can match
					// must contain, so a line without it is a line the regex
					// would have rejected. The census-equivalence control is
					// what turns that argument into evidence.
					if prefilter == residencyPrefilterOn && !strings.Contains(line, p.core) {
						continue
					}
					if p.regex.MatchString(line) {
						found[rel+"\t"+fn+"\t"+p.name]++
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// TestResidencyBoundaryGrepRatchet is the CI-visible grep half: no new
// store-enumeration site, and no baseline entry the tree no longer reaches.
func TestResidencyBoundaryGrepRatchet(t *testing.T) {
	root := repoRoot(t)
	patterns := loadResidencyPatterns(t, residencyScriptsDir(t))
	found, err := scanResidencyGrepSites(root, residencyScanDirs, residencyAllowlist, patterns, residencyPrefilterOn)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("the census found no enumeration site at all; the guard is evaluating nothing")
	}
	baseline, err := readResidencyBaseline(residencyBaselinePath(t), func(p string) bool { return !residencyIsASTPattern(p) })
	if err != nil {
		t.Fatalf("reading the baseline: %v", err)
	}
	if len(baseline) == 0 {
		t.Fatal("the baseline pins no grep row; the ratchet has no denominator")
	}
	assertRatchet(t, found, baseline)
}

// TestResidencyBoundaryGrepControls falsifies the grep half on real files.
func TestResidencyBoundaryGrepControls(t *testing.T) {
	patterns := loadResidencyPatterns(t, residencyScriptsDir(t))
	root := t.TempDir()
	writeResidencyFixture(t, root, "cmd/gc/pinned.go", "package main\n\nfunc a() { _ = BeadStores() }\n")
	base := map[string]int{"cmd/gc/pinned.go\ta\ta:BeadStores": 1}
	scan := func(t *testing.T, baseline map[string]int) []string {
		t.Helper()
		found, err := scanResidencyGrepSites(root, residencyScanDirs, nil, patterns, residencyPrefilterOn)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return ratchetViolations(found, baseline)
	}
	census := func(t *testing.T, tree string, with []residencyPattern, prefilter residencyPrefilter) map[string]int {
		t.Helper()
		found, err := scanResidencyGrepSites(tree, residencyScanDirs, nil, with, prefilter)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		return found
	}

	t.Run("baselined site passes", func(t *testing.T) {
		if v := scan(t, base); len(v) != 0 {
			t.Fatalf("a baselined site was reported: %v", v)
		}
	})

	t.Run("new site fails", func(t *testing.T) {
		writeResidencyFixture(t, root, "cmd/gc/eleventh.go", "package main\n\nfunc b() { _ = rigBeadStores() }\n")
		v := scan(t, base)
		if len(v) == 0 || !strings.Contains(strings.Join(v, " "), "eleventh.go") {
			t.Fatalf("the guard accepted a NEW enumeration site: %v", v)
		}
	})

	t.Run("marker suppresses", func(t *testing.T) {
		writeResidencyFixture(t, root, "cmd/gc/eleventh.go",
			"package main\n\nfunc b() { _ = rigBeadStores() } // "+residencyAllowMarker+" tested escape hatch\n")
		if v := scan(t, base); len(v) != 0 {
			t.Fatalf("the marker did not suppress the hit: %v", v)
		}
	})

	t.Run("a comment is not a site", func(t *testing.T) {
		writeResidencyFixture(t, root, "cmd/gc/eleventh.go", "package main\n\n// b would call rigBeadStores() but only in prose.\nfunc b() {}\n")
		if v := scan(t, base); len(v) != 0 {
			t.Fatalf("prose was counted as a site: %v", v)
		}
	})

	t.Run("stale baseline forces a shrink", func(t *testing.T) {
		if err := os.Remove(filepath.Join(root, "cmd/gc/eleventh.go")); err != nil {
			t.Fatalf("remove: %v", err)
		}
		stale := map[string]int{"cmd/gc/pinned.go\ta\ta:BeadStores": 1, "cmd/gc/gone.go\tgone\ta:BeadStores": 1}
		v := scan(t, stale)
		if len(v) == 0 || !strings.Contains(strings.Join(v, " "), "gone.go") {
			t.Fatalf("the guard accepted a baseline entry the tree no longer reaches: %v", v)
		}
	})

	// The laundering wrapper. cmd_storage.go used to be allowlisted, and an
	// allowlist filters the file BEFORE counting — so a helper written there
	// re-exported the enumeration to callers anywhere in the tree and no half of
	// the guard saw it. Now the call is censused like any other.
	//
	// It scans with the REAL residencyAllowlist rather than the nil one the rows
	// above use: an allowlist that no longer holds cmd_storage.go is the whole
	// subject, and a nil allowlist would make this row green either way.
	t.Run("a wrapper in a formerly-allowlisted file is censused", func(t *testing.T) {
		wrapped := t.TempDir()
		writeResidencyFixture(t, wrapped, "cmd/gc/cmd_storage.go",
			"package main\n\nfunc launder() map[string]beads.Store { return BeadStores() }\n")
		// The exemption that survives: the fork-only router exists in no upstream
		// tree, so it can have no baseline row to be pinned by.
		writeResidencyFixture(t, wrapped, "cmd/gc/cmd_bd_topology.go",
			"package main\n\nfunc workAxis() { _ = BeadStores() }\n")
		found, err := scanResidencyGrepSites(wrapped, residencyScanDirs, residencyAllowlist, patterns, residencyPrefilterOn)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		v := ratchetViolations(found, map[string]int{})
		joined := strings.Join(v, " ")
		if !strings.Contains(joined, "cmd_storage.go") {
			t.Errorf("a wrapper laundering the enumeration out of a formerly-exempt file was accepted: %v", v)
		}
		if strings.Contains(joined, "cmd_bd_topology.go") {
			t.Errorf("the fork-only work-axis router lost its exemption; it has no upstream baseline row to be pinned by: %v", v)
		}
		pinned := map[string]int{"cmd/gc/cmd_storage.go\tlaunder\ta:BeadStores": 1}
		if v := ratchetViolations(found, pinned); len(v) != 0 {
			t.Errorf("the same site, pinned, must pass — the exemption moved to the call, it did not vanish: %v", v)
		}
	})

	// The mask the file-keyed baseline let through: delete one call and add a
	// different one in ANOTHER function of the SAME file. The counts stay level
	// per file, and family (a) is consumption-shaped so no signature changes —
	// nothing but the enclosing-function key can see this.
	t.Run("a same-file swap into another function fails", func(t *testing.T) {
		writeResidencyFixture(t, root, "cmd/gc/swap.go",
			"package main\n\nfunc keeper() { _ = BeadStores() }\n\nfunc other() {}\n")
		swapBase := map[string]int{
			"cmd/gc/pinned.go\ta\ta:BeadStores":    1,
			"cmd/gc/swap.go\tkeeper\ta:BeadStores": 1,
		}
		if v := scan(t, swapBase); len(v) != 0 {
			t.Fatalf("the pre-swap fixture must be clean: %v", v)
		}
		writeResidencyFixture(t, root, "cmd/gc/swap.go",
			"package main\n\nfunc keeper() {}\n\nfunc other() { _ = BeadStores() }\n")
		v := scan(t, swapBase)
		if len(v) == 0 {
			t.Fatal("a new consumption site paired with a removal in the same file was MASKED; the file-keyed baseline is not enough")
		}
		joined := strings.Join(v, " ")
		if !strings.Contains(joined, "other") || !strings.Contains(joined, "keeper") {
			t.Fatalf("the violation must name both the new function and the retired one: %v", v)
		}
	})

	// The prefilter must never be able to switch itself off quietly. An empty
	// core screens every line IN, so a census that lost its screens would stay
	// perfectly correct — which is precisely why no other row in this file can
	// see it, and why residencyPatternCore returns an error rather than "".
	t.Run("every vocabulary row carries a screen", func(t *testing.T) {
		for _, p := range patterns {
			if p.core == "" {
				t.Errorf("pattern %q has an empty prefilter core: it screens nothing, so the prefilter is off for that row and the equivalence row below cannot tell", p.name)
			}
		}
	})

	// The prefilter's whole contract, on a real on-disk tree: it is a COST
	// change, not a census change. Scan the same tree both ways and refuse a
	// single differing row.
	//
	// The fixture carries one line per vocabulary row, so the row is not
	// vacuous for the pattern most likely to be mis-derived — the newest one —
	// and the reachability check below is what says so out loud: an
	// equivalence that held because both scans found nothing would be the
	// vacuous green this contract exists to prevent.
	t.Run("the census is identical with the prefilter off", func(t *testing.T) {
		tree := t.TempDir()
		writeResidencyFixture(t, tree, residencyWitnessFixturePath, residencyWitnessFixture(patterns))
		off := census(t, tree, patterns, residencyPrefilterOff)
		for _, p := range patterns {
			if off[residencyWitnessFixturePath+"\t"+residencyWitnessFixtureFunc+"\t"+p.name] == 0 {
				t.Fatalf("pattern %q was never censused in the witness fixture, so the equivalence row proves nothing about it; its synthesized witness %q does not survive being written as a fixture line", p.name, p.witness)
			}
		}
		on := census(t, tree, patterns, residencyPrefilterOn)
		if diff := residencyCensusDiff(off, on, "the prefilter off", "the prefilter on"); len(diff) != 0 {
			t.Fatalf("the prefilter changed the census, so it is not a prefilter but a second, narrower rule: %s", strings.Join(diff, "; "))
		}
	})

	// The control of the control. The equivalence row above is evidence only if
	// it can fail, and the failure it exists to catch is a core NARROWER than
	// its pattern — the screen skipping a line the regex would have counted.
	// Narrow every core and the row must bite; if it does not, it is decoration
	// and the derivation is unguarded.
	t.Run("the equivalence row bites an over-narrow core", func(t *testing.T) {
		tree := t.TempDir()
		writeResidencyFixture(t, tree, residencyWitnessFixturePath, residencyWitnessFixture(patterns))
		off := census(t, tree, patterns, residencyPrefilterOff)
		on := census(t, tree, residencyNarrowedCores(patterns), residencyPrefilterOn)
		if diff := residencyCensusDiff(off, on, "the prefilter off", "over-narrow cores"); len(diff) == 0 {
			t.Fatal("an over-narrow core changed no census row: the equivalence row cannot see a prefilter that drops sites, so it certifies nothing about the real derivation")
		}
	})

	// And the other half of that argument: the OFF side has to actually be off.
	// If it quietly screened too, the equivalence row would be comparing a
	// screened census with a screened census — green, and vacuous. Hand the off
	// scan a core no line can contain and it must count the sites anyway.
	t.Run("the prefilter-off census ignores the core", func(t *testing.T) {
		tree := t.TempDir()
		writeResidencyFixture(t, tree, residencyWitnessFixturePath, residencyWitnessFixture(patterns))
		plain := census(t, tree, patterns, residencyPrefilterOff)
		narrowed := census(t, tree, residencyNarrowedCores(patterns), residencyPrefilterOff)
		if diff := residencyCensusDiff(plain, narrowed, "the derived cores", "over-narrow cores"); len(diff) != 0 {
			t.Fatalf("the prefilter-off census consulted the core, so it is not an unscreened baseline to compare against: %s", strings.Join(diff, "; "))
		}
	})
}

// residencyOverNarrowSuffix turns a derived core into one that is narrower than
// its own pattern. A NUL byte appears in no Go source line, so appending it
// makes every screen reject lines its regex would have matched — which is the
// exact failure mode the equivalence row has to be able to see.
const residencyOverNarrowSuffix = "\x00"

// residencyNarrowedCores copies patterns with every screen sabotaged past what
// its pattern requires. It is how both halves of the equivalence argument are
// falsified: on the ON side a narrowed core must change the census, and on the
// OFF side it must not.
func residencyNarrowedCores(patterns []residencyPattern) []residencyPattern {
	out := make([]residencyPattern, len(patterns))
	copy(out, patterns)
	for i := range out {
		out[i].core += residencyOverNarrowSuffix
	}
	return out
}

// TestResidencyPatternCoreIsARequiredLiteral pins the derivation on the shapes
// where "the longest run of ordinary characters" and "a literal the match
// cannot avoid" part company.
//
// This is the census-equivalence row's unit twin, and it is deliberately built
// from synthetic patterns rather than the vocabulary file: the shapes below
// must stay guarded on the day someone retires the row that happens to carry
// one today.
func TestResidencyPatternCoreIsARequiredLiteral(t *testing.T) {
	for _, row := range []struct {
		name string
		expr string
		want string
	}{
		{
			// The shape a text-level derivation gets wrong. Strip the
			// metacharacters from the real b:cliSoleClassBinding row and the
			// answer is cliSoleClassBindingStore(, which the accessor's own
			// call sites do not contain.
			name: "an optional group is not required",
			expr: `(^|[^A-Za-z0-9_])probeBinding(Store)?\(`,
			want: "probeBinding",
		},
		{
			name: "a boundary wrapper contributes nothing",
			expr: `(^|[^A-Za-z0-9_])probeBinding\(`,
			want: "probeBinding(",
		},
		{
			name: "the longest alternation branch is not required",
			expr: `(alpha|betaLongerBranch)probe\(`,
			want: "probe(",
		},
		{
			name: "a repeated group is not required",
			expr: `probeStores(All)*\(`,
			want: "probeStores",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			parsed, err := syntax.Parse(row.expr, syntax.Perl)
			if err != nil {
				t.Fatalf("parsing %s: %v", row.expr, err)
			}
			got, err := residencyPatternCore(parsed)
			if err != nil {
				t.Fatalf("deriving a core for %s: %v", row.expr, err)
			}
			if got != row.want {
				t.Fatalf("core(%s) = %q, want %q — a core wider than the pattern requires silently narrows the census", row.expr, got, row.want)
			}
		})
	}

	// The two shapes with no sound screen at all. Both must ERROR rather than
	// hand back "", because "" screens every line in: the scan would keep
	// passing, the census would keep looking right, and the guard would have
	// silently reverted to the slow rule it was supposed to be equivalent to —
	// or, worse, a future caller would read "" as "no screen needed".
	//
	// A case-folded literal is the subtler one: Go parses (?i)abc as the
	// literal "ABC", so taking its runes verbatim would screen out every real
	// line instead of none.
	for _, row := range []struct{ name, expr string }{
		{"a case-folded pattern has no sound core", `(?i)probeStores`},
		{"a pattern with no literal has no core", `[a-z]+`},
	} {
		t.Run(row.name, func(t *testing.T) {
			parsed, err := syntax.Parse(row.expr, syntax.Perl)
			if err != nil {
				t.Fatalf("parsing %s: %v", row.expr, err)
			}
			if got, err := residencyPatternCore(parsed); err == nil {
				t.Fatalf("%s was handed the screen %q instead of an error; an unsound screen must fail the suite, never disable itself", row.expr, got)
			}
		})
	}
}
