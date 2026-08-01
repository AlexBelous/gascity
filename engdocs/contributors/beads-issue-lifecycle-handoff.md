# Beads issue-lifecycle work — handoff

Last updated 2026-08-01. Supersedes `beads-guarded-ops-handoff.md` for everything under epic `ga-ktn9pe.4`.

## What this is

A campaign to give beads one guarded issue-lifecycle surface, used by the `bd` CLI, linked-library
consumers (Gas City's `NativeDoltStore`), and eventually the HTTP/proxied path — so that close/reopen/update
policy is implemented once instead of three times. Two PRs have merged; a queue of follow-ups remains.

## Where it stands

### Merged upstream (gastownhall/beads)

| SHA | PR | What |
| --- | --- | --- |
| `b92442d1a` | #5191 | The facade: `issueops.Lifecycle` (Create/Update/Close/Reopen), reached via `store.IssueLifecycle()`. Three backends, a cross-backend conformance suite, CLI adoption, and ~10 bug fixes. |
| `ff6eeedbf` | #5206 | A generic status update crossing into a done category now enforces close policy; `bd update --force` gains a dual meaning. |

### Open

- **gastownhall/gascity#4885** — draft, deliberately. Routes `NativeDoltStore` writes through
  `store.IssueLifecycle()`. It **cannot compile** until beads publishes a release containing the accessor;
  that is expected and documented in the PR body. Do not add a `replace` directive to make it green.
  Correctness was proven out-of-tree by building against the upstream branch in a scratch dir.

### The public API, as merged

```go
// package issueops — a LEAF package: only non-stdlib deps are internal/types and itself.
// Declares the contract only. No constructor.
type Lifecycle interface { Create; Update; Close; Reopen }

// internal/storage
type Storage interface { /* ~71 legacy methods */; IssueLifecycle() (issueops.Lifecycle, error) }

// every consumer
store, err := beads.OpenBestAvailable(ctx, beadsDir)   // config -> substrate
ops,   err := store.IssueLifecycle()                   // substrate -> lifecycle verbs
```

**The naming rule, which matters more than the name:** a new capability gets a **new role and a new
accessor**, never a method appended to `Lifecycle`. `bd note`, `tag`, `label`, `assign`, `priority`,
`defer`, `done`, `set`, `claim` are all `Update` with a parameter — `IssuePatch` already carries
`Notes`, `Labels`, `Metadata`. Rejecting `AddLabel()` / `SetPriority()` / `AppendNote()`-shaped additions is
the whole discipline. `storage.Storage` reached 71 methods because it lacked this rule.

## Owner rulings — settled, do not relitigate

1. **Constructor** — the accessor *is* the API. No `issueops.New`, no `Source` handle, no `From*`
   constructors. `store.IssueLifecycle()`.
2. **`Claim` stays** in `UpdateRequest` (`bd update --claim` exists today).
3. **`persistence.go`'s issue↔wisp move stays** inside generic Update (main's `DoltStore.UpdateIssue`
   already does it; removing it regresses frozen semantics).
4. **`ForceIDPrefix` stays.** Note: an earlier claim that it had no consumer was **wrong** —
   `bd create --force` uses it at `cmd/bd/create.go:584`.
5. **`bd create --id <occupied>` refuses** instead of silently upserting. Shipped in #5191.
6. **A compound update is one atomic operation** — one hook (including bare `--claim`, which previously
   fired none), delta-only label events, one version commit, with the ID-bearing Dolt commit message kept.
7. **`bd update --parent` replaces all parent edges atomically** (previously removed only the first).
8. **`bd reopen` on a non-closed issue reports nothing-to-do** rather than printing "Reopened".
9. **A generic done-crossing update enforces close policy**, and **`bd update --force` overrides both**
   the assignee fence and close policy. Shipped in #5206.
10. **`bd close` pinned check**: refuse if `issue.Pinned` **OR** `status == pinned`. Strictly additive.
    This was decided by audit: Gas Town pins by *status* at 21 sites (`b.List(ListOptions{Status:
    StatusPinned})` in `hook_check.go`, `prime_output.go`, `molecule_step.go`, `up.go`), so a
    boolean-only check would strip its protection. Gas City reads the boolean
    (`compute_awake_set.go:443,467`).
11. **Commit identity**: author `Julian Knutsen <julianknutsen@users.noreply.github.com>`; trailers
    `Agent-Signature: <model>` and `Co-authored-by: CI Bot <ci@beads.test>`. A council suggested the repo's
    `<agent> on behalf of <human>` form instead — the owner's choice stands.
12. **PR batching**: one PR per bead, opened in waves, P1s first.

## Process for each remaining item

Fable for design/architecture → Opus for implementation → a fable review council before commit → PR
following `CONTRIBUTING.md` + `.github/PULL_REQUEST_TEMPLATE.md` → wait for green CI → merge.

Never Sonnet. Prefer fable for red-teams/councils; fall back to Opus on 429, never Sonnet. **Check
`agents_error` / `<failures>` before trusting an empty findings array** — an all-429 council returns
`{"findings":[]}` which is indistinguishable from a clean pass.

## Remaining work

### Wave 1 (P1)

- **`ga-2kkue`** — `bd create -t <infra-type>` lands in `issues` on the proxied path.
  `cmd/bd/create_proxied_server.go:142` routes on `Ephemeral || NoHistory` and never consults infra types,
  while the embedded path (`create_atomic.go:44`) checks `IsInfraTypeCtx`. `domain.CreateContext` now
  carries an `InfraTypes` field, making this ~2 lines.
- **`ga-z3vht`** — `bd close` ignores `issue.Pinned`. `internal/validation/issue.go:53-63` checks
  `Status == StatusPinned` rather than the boolean. Implement ruling 10 (boolean OR status).

### Wave 2 (P2)

- **`ga-z0qmv`** — the uow `Lifecycle` backend no longer enforces the foreign-assignee transfer fence.
  It delegated to `domain.ApplyUpdate`, whose fence was removed as a rider. dolt/embeddeddolt enforce via
  `issueops.AuthorizeAssigneeTransfer` (`aggregate.go:142`, reached from `execution.go:133`). Fix by
  relocating the check into `uow.issueOperations.Update` at the facade layer, **with a cross-backend
  conformance case** — the suite has no assignee-transfer case today, which is why nothing caught it.
- **`ga-kjkv1`** — `closed_at` / `close_reason` / `closed_by_session` are writable as standalone
  generic-update fields, so close metadata can be stamped without a `status` key. #5206's gate keys off the
  status change, so this is now the visible seam in an otherwise closed hole. Confirm pre-existing against
  `origin/main` before fixing.
- **`ga-dpfii`** — federated cross-prefix dependency targets classify differently across write plumbings.
  `ExecuteCreate` treats cross-prefix as external and skips the existence check; the uow path only treats a
  literal `external:` prefix as external. Share one classifier, mirroring how `ClassifyPublicCreateError`
  became the single funnel.
- **`ga-tsjxb`** — domain `issue_type` validation skips typed `types.IssueType` values (type-asserts only
  `string`). `TestIssueUpdateAcceptsLegacyIssueTypeRepresentations` pins that typed values round-trip, so
  widening changes accept/reject behavior and needs cross-backend verification.
- **`ga-e6h6i`** — `cmd/bd` leaks `issue-prefix` into viper across tests, surviving `initConfigForTest`.

### Wave 3 — CLI consistency (all four approved by the owner)

- **`ga-c69el`** covers two: `bd reopen` never records last-touched (unlike create/update/close — and
  `close.go:29-31`'s own doc claims it does), and prints a bare ID where the others use `formatFeedbackID`.
- **Unify partial-failure exit codes** — `bd close` exits 0, `bd update` exits 1 for the same shape.
  **Needs a bead.** Breaking either way it is unified.
- **Unify the `--json` contract** — create emits a sorted-key object with `schema_version`;
  update/close/reopen emit struct-order arrays without it. **Needs a bead.** Breaking for anything parsing it.

> **Owner input required before implementing the last two.** Bring exact before/after wire shapes and let
> the owner choose; do not pick a direction unilaterally.

### Wave 3 — review findings still owed beads

- Version-commit messages for create/close/reopen lost the issue ID (breaks `bd dolt log | grep <id>`);
  update retains its ID via `updateCommitMessage`.
- A claim-verify replay can double-apply `--append-notes` in a compound facade update
  (`internal/storage/dolt/issue_operations.go:51,65-78`, `claim_verify.go:161-190`).
- No batch-mode HEAD-advance test for `bd close` / `bd reopen` (create and update are covered).
- `issueops` contract docs reference internal symbols rather than the four verbs;
  `CreateDependency.Metadata` is a bare string that must secretly be valid JSON; `CloseRequest.Session`
  is undocumented.
- **`ga-tbr0w`** (filed) — `cmd/bd/ado.go:914-918` writes the non-allowlisted key `source_system` and
  swallows the error.

### Blocked

- **`ga-ktn9pe.4.2`** (Gas City `NativeDoltStore`) and **`ga-jvr4ef`** (HTTP handler thinning) need a
  proxy-resolvable beads release. Local `replace` directives are barred.
- The **proxied-server rewire onto the facade** — it changes Dolt commit granularity (today
  `bd close a b c` is one commit; the facade commits per issue) and the parity suite explicitly could not
  pin the proxied output/exit contract. Needs proxied contract-pin tests committed and green **first**.
- **`ga-ktn9pe.4.7`** gates the OSS publish on `ga-ktn9pe.3` (hosted multibackend rebase). Consider
  splitting: publishing the OSS pseudo-version is what unblocks the two consumers, and neither needs the
  hosted rebase.

## Failure patterns this campaign actually hit

Read this section before writing code. Every item cost a revert or a red CI run.

1. **A policy check added at a shared layer reaches callers that cannot satisfy it.** Happened twice.
   A closed-boundary guard in `UpdateIssueInTx` broke the proxied server and `bd batch`, which had no way to
   translate its refusal. An assignee fence in `domain.ApplyUpdate` read a spec field the proxied caller
   never populated, so `--force` was silently ignored. **Both rode in under commit messages that said
   "validate…".** Before adding any check to a shared helper, write the caller table: every caller, does it
   enforce, can it override. #5206 does this and its transport is deliberately fail-loud — a caller that
   forgets to plumb the override is refused by name rather than silently losing it.
2. **"Unchanged from baseline" is only as good as the baseline.** `TestReopenCommand` was labelled
   pre-existing and that label propagated to four agents; it had actually been broken by commit 1 of the
   branch. It surfaced only when someone diffed the failing set against a real `origin/main` worktree.
   **Always establish a baseline from `origin/main`, not from inside the branch.**
3. **This repo has several ways to print `ok` while running zero tests.** `-run TestSuite` matches nothing
   in `domain/db` (the entry point is `TestDomainDB`). Embedded tests all SKIP without
   `BEADS_TEST_EMBEDDED_DOLT=1`. Always use `-v` and count `RUN/PASS/SKIP/FAIL`.
4. **Ambient environment leaks into test verdicts.** `getOwner()` reads `GIT_AUTHOR_EMAIL`, so
   `bd create --json` gains an `owner` key when it is set — which every agent had exported for commit
   authorship, and CI sets too. A parity test asserting an exact key set passed locally and failed in
   agents. Pin ambient inputs in the harness.
5. **After a squash-merge, every SHA from the merged branch is dangling upstream.** Do not cite branch-local
   commit hashes in shipped source or docs.

## Environment and tooling gotchas

```bash
export GOTOOLCHAIN=go1.26.5
export GOPROXY="file://$(go env GOMODCACHE)/cache/download"
export GOSUMDB=off
export GOFLAGS=-mod=readonly
```

- **`GOTOOLCHAIN` must be exported in the committing shell** or the pre-commit hook's golangci-lint
  self-builds under go1.25 and dies.
- **`GOPROXY` must be the file:// module cache.** The hook runs `go run golangci-lint@v2.10.1`, which does a
  network deprecation lookup on *every* invocation and dies on a TLS blip. `GOPROXY=off` does **not** work —
  it fails the lookup instead.
- **Never `git stash`.** The stash stack is shared across all worktrees of a repo; another session can pop
  your entry. Use a WIP commit or a tarball.
- **`core.hooksPath` lives in the shared git config** (`/data/projects/beads/.git/config`) and has been seen
  pointing at a sibling checkout. Do not "fix" it — it affects other worktrees. Run the gate by hand:
  `gofmt -l internal cmd . | grep -v vendor` and
  `CGO_ENABLED=0 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1 run --new-from-rev=HEAD`.
- **`gh pr edit` fails** with a Projects-classic deprecation error. Use
  `gh api repos/OWNER/REPO/pulls/N -X PATCH --input -` with a JSON body.
- **Push targets**: `gascity/beads` is a genuine fork of `gastownhall/beads` — PRs go fork → upstream.
  For Gas City, the `julianknutsen` remote **301-redirects to `gastownhall/gascity`**; it is not a fork, so
  pushing there writes to upstream directly.
- **Gas City has a pre-push gate** that runs `make test-fast-parallel` behind a slot mechanism. On a branch
  that cannot compile (e.g. #4885) it can only be bypassed with `--no-verify`, which requires owner consent.
- **Never `go clean -cache`**; never set `GOCACHE`/`TMPDIR`. Never run `./internal/storage/dolt` or
  `./internal/storage/embeddeddolt` unfiltered — 816 and 114 test functions.
- **`go test ./cmd/bd/` has ~25 pre-existing top-level failures** (init/config/doctor/completion), identical
  on `origin/main`. Compare the failing set **by name**, not by count.
- **`make ci-pr-lint` fails on `origin/main` itself** — two gosec findings in `cmd/bd/main.go`. Pre-existing.

## Verification baselines

As of `ff6eeedbf`. Count with `grep -cE '^[[:space:]]*--- PASS'` — `domain/db` nests four levels deep and a
shallower pattern undercounts by an order of magnitude.

```bash
go test -v -count=1 -run TestParity ./cmd/bd/                                    # 40
go test -v -count=1 -run TestDomainDB ./internal/storage/domain/db/              # 800
go test -v -count=1 -run TestIssueOperations ./internal/storage/dolt/            # 73
BEADS_TEST_EMBEDDED_DOLT=1 CGO_ENABLED=1 \
  go test -v -count=1 -run TestEmbeddedIssueOperations ./internal/storage/embeddeddolt/   # 56
go test -v -count=1 ./internal/storage/uow/                                      # 135
go test -v -count=1 ./internal/storage/issueops/                                 # 338
BASE_SHA=origin/main bash scripts/check-migration-hygiene.sh                     # clean
```

`cmd/bd/write_verbs_parity_test.go` (40 tests) is the CLI-equivalence evidence. **Its assertions may not be
edited** except for an owner-approved behavior change, in the commit that causes it, with a comment naming
the ruling. It is proven non-vacuous and is order- and environment-independent.

## Key paths

- Beads working tree: `/data/projects/beads-public-issueops-simple` (a worktree of `/data/projects/beads`)
- Gas City consumer: `/data/projects/gascity-native-dolt-issueops-simple`
- Contract: `issueops/issueops.go` · engine: `internal/storage/issueops/` · backends:
  `internal/storage/{dolt,embeddeddolt,uow}/issue_operations*.go` · conformance:
  `internal/storage/conformance/issue_operations_contract.go`
- Design docs from this campaign: `/var/tmp/w46-final-design.md`, `/var/tmp/w46-yagni.md`,
  `/var/tmp/w46-cli-swap-plan.md`, `/var/tmp/w46-closepolicy-workorder.md`
- Recovery backups: `/var/tmp/w46-recovery-backup-20260730T195534`
