# Release gate: cancelled-run CI verdict watchdog

- Deploy bead: `ga-ovr2ic`
- Source bead: `ga-r0fbe5`
- Reviewed source: `5c86cd7542076c46e685896a81a086f553ff111b`
- Gate base: `origin/main@6fd8f97c4042bcbf37b734278ef4df24035f5436`
- Evaluation date: 2026-07-31
- Disposition: **PASS**

`docs/PROJECT_MANIFEST.md` is not present in this repository at the evaluated
commit. This checklist applies the deployer role's release criteria and the
repository's documented CI-equivalent test policy.

## Gate checklist

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | The deploy bead records reviewer PASS for exact source `5c86cd7542076c46e685896a81a086f553ff111b`. The source bead's notes contain `REVIEW VERDICT: PASS` after independent syntax, security, scope, build, vet, and CI checks. |
| 2 | Acceptance criteria met | **PASS** | The new default-branch workflow listens only for completed `CI` workflow runs, gates on a `cancelled` conclusion plus a `push` event, and grants only `checks: write` and `actions: read`. Its single `gh api` POST creates a completed, neutral check run on the cancelled run's `head_sha`, with the original run URL in both the details field and summary. It uses `workflow_run`, not `pull_request_target`, and reads no pull-request-authored content. YAML parsing passed and the workflow name matches `.github/workflows/ci.yml`'s `name: CI`. The true `workflow_run` behavior cannot execute until this file is on the default branch; priority-3 follow-up `ga-4ceq5w` tracks both the first live trigger and softer wording for mid-flight manual cancellation. |
| 3 | Tests pass | **PASS** | The authoritative GitHub CI run for exact head `5c86cd7542076c46e685896a81a086f553ff111b` ([run 30612404654](https://github.com/gastownhall/gascity/actions/runs/30612404654)) completed with **55 jobs PASS, 0 FAIL, 3 SKIP**, including `CI / required`, static checks, acceptance A, all 12 non-short `cmd/gc` process shards, worker suites, package/tmux/bdstore/REST-smoke integration lanes, and the dashboard, release, security, and generated-artifact checks forced by `.github/workflows/**`. The three skips are the two push-only coverage jobs and push-only REST-full; the PR graph runs the corresponding required functional suites and explicitly allows those coverage/release lanes to defer to post-merge. Locally, `make test-fast-parallel` on the reviewed source completed **10 job groups PASS, 0 FAIL, 0 SKIP**; `make test-ci-policy` completed **20 Python tests plus 2 Go package suites PASS, 0 FAIL, 0 SKIP**; `go build ./...`, `go vet ./...`, YAML parsing, and `git diff --check` all passed. A later pre-push invocation on the gate-only commit retained **9 groups PASS, 1 infrastructure FAIL, 0 SKIP**: `TestCustomTypesCheck_TableDrift` reached the fleet's long-lived shared Dolt server on port 3308 and found no test-owned `tst` database. Listener PID, config, and cwd prove the server predates and is outside this branch; the failure was not retried, and follow-up `ga-zxpfic` owns the isolation defect. |
| 4 | No high-severity review findings open | **PASS** | Unresolved HIGH/CRITICAL finding count: 0. The reviewer filed one explicitly non-blocking priority-3 wording/production-smoke follow-up (`ga-4ceq5w`); the emitted check conclusion remains neutral rather than falsely successful. |
| 5 | Final branch is clean | **PASS** | Before adding this gate, `git status --porcelain=v1 --untracked-files=no` produced no output, `git diff --name-only --diff-filter=U` produced no output, and `git diff --check origin/main...5c86cd7542076c46e685896a81a086f553ff111b` exited 0. The only untracked paths are provider-materialized skill metadata under `.claude/skills/`; they are not staged or part of the deploy branch. This gate file is the sole deployer-authored change and will be committed before push. |
| 6 | Branch diverges cleanly from main | **PASS** | Evaluated first. `git merge-tree --write-tree origin/main 5c86cd7542076c46e685896a81a086f553ff111b` exited 0 against `origin/main@6fd8f97c4042bcbf37b734278ef4df24035f5436` and produced tree `a48ba4d704df8f1459390bd3192f321b630294e2`. The source is one commit ahead and two behind current main with no content conflict; no self-rebase was needed. |
| 7 | Single feature theme | **PASS** | The commit adds one file, `.github/workflows/ci-verdict-watchdog.yml` (54 lines), implementing one behavior: make a cancelled main-push CI run visible as an explicit neutral non-verdict. |

## Test evidence

```text
GitHub CI run 30612404654 at 5c86cd7542076c46e685896a81a086f553ff111b
55 jobs PASS, 0 FAIL, 3 SKIP
CI / required: PASS

make test-fast-parallel
10 job groups PASS, 0 FAIL, 0 SKIP

git push --dry-run origin HEAD (pre-push make test-fast-parallel)
9 job groups PASS, 1 infrastructure FAIL, 0 SKIP
FAIL: TestCustomTypesCheck_TableDrift reached fleet shared Dolt server
      127.0.0.1:3308 and found database "tst" absent

make test-ci-policy
20 Python tests + 2 Go package suites PASS, 0 FAIL, 0 SKIP

go build ./...
PASS

go vet ./...
PASS

PYTHONDONTWRITEBYTECODE=1 python3 -c '<parse workflow with yaml.safe_load>'
1 workflow PASS, 0 FAIL, 0 SKIP
```

The three CI skips are intentional by workflow condition: both unit-coverage
jobs and the 16-way REST-full lane are `push`-only. They do not represent
missing PR evidence for this workflow-only change: `.github/workflows/**` is a
shared path that forced the complete required PR union, including acceptance,
all 12 `cmd/gc` process shards, package and runtime integration, bdstore, and
REST smoke. The exact-SHA `CI / required` aggregate passed.

The later pre-push failure is retained as infrastructure evidence rather than
retried into green. `lsof` identified port 3308 as PID 142645; `/proc` showed
the long-lived `/home/jaword/.local/bin/dolt sql-server` using
`/home/jaword/.beads/shared-server/dolt-server-config.yaml`, with cwd
`/home/jaword/.beads/shared-server/dolt`. It was started before this deploy
gate and is the fleet-wide beads server, not a test-owned process. The failed
test is unrelated to the workflow-only diff, all other fast groups passed,
and the exact-source local run plus exact-source required CI graph are clean.
Follow-up `ga-zxpfic` tracks making that doctor test hermetic.

## Scope evidence

```text
.github/workflows/ci-verdict-watchdog.yml | 54 ++++++++++++++++++++++++++++++++
1 file changed, 54 insertions(+)
```
