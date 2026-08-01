# Next-session prompt

Paste everything below the line into a fresh session. It is self-contained.

---

Pick up the beads issue-lifecycle follow-up queue. Read the handoff first, in full, before touching anything:

    /data/projects/gascity/engdocs/contributors/beads-issue-lifecycle-handoff.md

Short version: four PRs have merged upstream — `b92442d1a` #5191 (the `issueops.Lifecycle` facade reached
via `store.IssueLifecycle()`, three backends, conformance suite, CLI adoption), `ff6eeedbf` #5206 (a
generic done-crossing status update now enforces close policy, with `bd update --force` overriding it),
and the two Wave 1 P1s, `532dadf98` #5211 and `29af03b8c` #5212. A queue of follow-up beads remains. Your
job is to work that queue.

Work in `/data/projects/beads-public-issueops-simple`, branching fresh off `origin/main` for each item.

## Process, per item — this is the owner's instruction, follow it

Fable for design/architecture → Opus for implementation → a **fable review council before commit** → open a
PR following `CONTRIBUTING.md` and `.github/PULL_REQUEST_TEMPLATE.md` → wait for green CI → merge.

One PR per bead. Never Sonnet; prefer fable for councils, fall back to Opus on 429. If a council returns an
empty findings array, check `agents_error` before believing it — an all-429 run is indistinguishable from a
clean pass.

Give the council three *distinct* lenses — blast-radius, test-adequacy, correctness — not three identical
reviewers. And mutation-check every new test before you open the PR: revert the production hunk, confirm
the test fails, restore it verbatim, confirm `git diff` is empty. Copy the file aside and back; never
`git stash`.

## Order

**Wave 1 (P1) is done** — #5211 and #5212 are merged. Do not re-open them.

**Start with Wave 2 (P2):** `ga-z0qmv`, `ga-kjkv1`, `ga-dpfii`, `ga-tsjxb`, `ga-e6h6i` — details in the
handoff. `ga-z0qmv` is the one to take first: the uow `Lifecycle` backend silently lost the
foreign-assignee transfer fence, and the conformance suite has no assignee-transfer case, which is
exactly why nothing caught it. Fix it *with* a cross-backend conformance case.

**Also queued, from the Wave 1 review council:** `ga-ktn9pe.4.8`, `.9`, `.10`, `.11`, `.12`. The first
three need an owner ruling before you implement — bring the options, do not pick. `.11` and `.12` are
straightforward and can go whenever.

**Wave 3:** `ga-c69el` plus two items that still need beads (unify partial-failure exit codes; unify the
`--json` contract) and four review findings owed beads. **Both unification items are breaking wire changes —
bring the owner exact before/after shapes and let them choose. Do not pick a direction yourself.**

## Read the handoff's "Failure patterns" section before writing code

Condensed, because each of these cost a revert or a red CI run:

1. **A policy check added at a shared layer reaches callers that cannot satisfy it.** This happened twice in
   this campaign, both times riding into a commit whose subject said "validate…". Before adding any check to
   a shared helper, write the caller table: every caller, does it enforce, can it override. Make the
   transport fail loudly when a caller forgets to plumb the override.
2. **Establish baselines from `origin/main`, never from inside your branch.** A test was labelled
   "pre-existing" and that label propagated through four agents before someone diffed against a real
   `origin/main` worktree and found the branch had broken it.
3. **This repo prints `ok` while running zero tests in several ways.** `-run TestSuite` matches nothing in
   `domain/db` (it is `TestDomainDB`); embedded tests SKIP silently without `BEADS_TEST_EMBEDDED_DOLT=1`.
   Always `-v`, always count `RUN/PASS/SKIP/FAIL`.
4. **Ambient environment leaks into verdicts.** `getOwner()` reads `GIT_AUTHOR_EMAIL`, which changes
   `bd create --json` output. Pin ambient inputs in test harnesses.
5. **After a squash-merge every SHA from the merged branch is dangling upstream.** Never cite branch-local
   hashes in shipped source or docs.

## Environment — non-obvious and load-bearing

```bash
export GOTOOLCHAIN=go1.26.5
export GOPROXY="file://$(go env GOMODCACHE)/cache/download"
export GOSUMDB=off
export GOFLAGS=-mod=readonly
export GIT_AUTHOR_NAME="Julian Knutsen" GIT_AUTHOR_EMAIL="julianknutsen@users.noreply.github.com"
export GIT_COMMITTER_NAME="Julian Knutsen" GIT_COMMITTER_EMAIL="julianknutsen@users.noreply.github.com"
```

`GOTOOLCHAIN` must be exported in the committing shell or the pre-commit hook's golangci-lint dies.
`GOPROXY` must be the file:// cache — the hook does a network lookup every run and `GOPROXY=off` fails it.
Never `git stash` (shared stack across worktrees). Do not change `core.hooksPath` (shared config); run the
gate by hand instead. `gh pr edit` is broken — use `gh api ... -X PATCH --input -`. Never `go clean -cache`.
Never run `./internal/storage/dolt` or `./internal/storage/embeddeddolt` unfiltered.

Commit trailers, exactly, after one blank line:

    Agent-Signature: claude-opus-5
    Co-authored-by: CI Bot <ci@beads.test>

## Baselines that must not regress

Count with `grep -cE '^[[:space:]]*--- PASS'`; `domain/db` nests four levels and a shallower pattern
undercounts badly.

    cmd/bd -run TestParity                                     40
    internal/storage/domain/db -run TestDomainDB               800
    internal/storage/dolt -run TestIssueOperations             73
    internal/storage/embeddeddolt -run TestEmbeddedIssueOperations  56   (needs BEADS_TEST_EMBEDDED_DOLT=1 CGO_ENABLED=1)
    internal/storage/uow                                       135
    internal/storage/issueops                                  338
    internal/validation                                        218

`go test ./cmd/bd/` has ~25 pre-existing top-level failures (init/config/doctor/completion) identical on
`origin/main` — compare the failing set **by name**, not by count. `make ci-pr-lint` fails on `origin/main`
itself (two gosec findings in a file none of this touches).

`cmd/bd/write_verbs_parity_test.go` is the CLI-equivalence evidence. **Do not edit its assertions** except
for an owner-approved behavior change, in the commit that causes it, with a comment naming the ruling.

## Owner rulings already made

Listed in the handoff. Do not relitigate them — most cost several rounds to settle. The one most likely to
tempt you: `issueops` exports **no constructor**; the accessor `store.IssueLifecycle()` *is* the API, and a
new capability gets a new role and a new accessor rather than a method on `Lifecycle`.

## Also open

`gastownhall/gascity#4885` is a **deliberate draft** — it routes Gas City's `NativeDoltStore` onto the
facade and cannot compile until beads publishes a release containing the accessor. Do not add a `replace`
directive to make it green. It unblocks when a release ships.
