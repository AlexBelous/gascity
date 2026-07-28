# Release Gate: Dolt compact quarantine alert deduplication

Date: 2026-07-28  
Deployer: `gascity/deployer`  
Deploy bead: `ga-zm49qq`  
Reviewed commit: `6108f7b6a8b045c8ea970f408d5f034c968cdd31`  
Base checked: `origin/main` at `311effd094d3a5085c364d4cab017f65442d43b8`

`docs/PROJECT_MANIFEST.md` is not present in this checkout. This evaluation
uses the deployer release criteria and the repository's canonical
`TESTING.md` policy.

## Release Criteria

| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | Review PASS present | PASS | Review bead `ga-dfuwzf` is closed with `REVIEW VERDICT: PASS` for the exact reviewed commit. |
| 2 | Acceptance criteria met | PASS | Existing quarantine markers now emit their event every cycle but mail only when their state has not already been notified. Notify bookkeeping is atomically updated in the marker, and failures remain fail-open toward notifying. The new three-cycle regression test observes exactly one mail and three quarantine events. |
| 3 | Tests pass | PASS | `go test -count=1 ./examples/bd/dolt/...`, `go build ./...`, `go vet ./...`, and `make test-fast-parallel` all passed. The sharded fast run completed 9/9 jobs successfully. `gofmt` is clean. Shellcheck reports the same five warning codes at the reviewed commit and merge-base, so this change adds zero warnings. |
| 4 | No high-severity review findings open | PASS | The review records no blockers and no HIGH or CRITICAL findings. Its sole observation is explicitly non-blocking defense-in-depth. |
| 5 | Final branch is clean | PASS | `git status --porcelain` was empty on `deploy/ga-zm49qq-gate` before this checklist was written. This checklist is the deployer's only additional change and will be committed separately. |
| 6 | Branch diverges cleanly from main | PASS | Evaluated first and rechecked after tests. `git merge-tree --write-tree origin/main HEAD` succeeded against current main, producing tree `5b2a8c9fcd9c591c9132391ff7e77c78b39fd66b`. No self-rebase was needed. |
| 7 | Single feature theme | PASS | The reviewed commit changes only the Dolt compact quarantine alert path and its direct execution-script regression test. |

## Acceptance Evidence

- A fresh quarantine still emits its event and mail immediately.
- Repeated compact cycles over an unchanged quarantine emit an event every
  time but send one operator mail total.
- Existing and old-format markers fail open: missing or unreadable notification
  bookkeeping cannot suppress a real alert.
- `record_quarantine_notify_state` updates only `seen_count`, `notify_count`,
  `last_notified_ts`, and `last_notified_reason` through the existing
  restrictive-umask and atomic-rename pattern.
- The reviewed range contains one commit and changes exactly:
  `examples/bd/dolt/commands/compact/run.sh` and
  `examples/bd/dolt/dog_exec_scripts_test.go`.

## Commands Run

```text
git fetch origin main
git merge-tree --write-tree origin/main HEAD
git diff --check <merge-base>..HEAD
gofmt -l examples/bd/dolt/dog_exec_scripts_test.go
shellcheck -f json examples/bd/dolt/commands/compact/run.sh
git show <merge-base>:examples/bd/dolt/commands/compact/run.sh | shellcheck -s sh -f json -
go test -count=1 ./examples/bd/dolt/...
go build ./...
go vet ./...
make test-fast-parallel
```

## Decision

PASS. The isolated deploy branch is ready for merge-authority review.
