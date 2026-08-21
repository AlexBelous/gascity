# Keyed-reconciler cutover brief (for win3 / mc-operator)

Owner-signed 2026-08-19 (`WE-SIGNOFF.md` §6, this directory). Coordination
bead: **ga-f7v2ft.161** — append your deploy state, soak observations, and
questions there (`BEADS_DIR=/data/projects/gascity/.beads`).

## What is being swapped

The session reconciler. A 7-day parity campaign (79,382 joined comparisons,
zero shadow-effect violations, zero unclassified divergences, every acting
family ≥99.5% match — full artifact chain in `evidence/` here and on bead
ga-f7v2ft.122) proved the keyed detector-sweep reconciler semantically
equivalent to the legacy fleet-wide god function under coexistence. The owner
authorized swapping the local cities now, soaking a few days, then merging to
origin/main.

## What you integrate

- **Branch**: `rec/ga88-continue` @ **`3f699d3473`** (julianknutsen fork).
  Contains the keyed reconciler (already merged main as of ~Aug-14), the
  parity instruments, and the campaign evidence. Integrate into your deploy
  line however you normally do; nothing in the branch assumes the campaign
  city's config.
- **Config**, per city `city.toml`:

  ```toml
  [daemon]
  session_reconciler = "auto"
  ```

  `"auto"` = keyed exact-start ownership with graceful degradation to legacy
  when a family's requirements are unavailable (that degradation is itself
  campaign-tested). Do NOT use `"require"` for this swap — the campaign
  evidence is for `"auto"`. The value is **boot-latched: each city's
  controller must restart** to take it.

## Soak: what to watch

- Pipeline health first: merge recency on the review pipeline (>12h drought
  = incident, per standing ops rule).
- Session lifecycle: no stall storms, no unexplained session churn, health
  patrol steady.
- Known-benign signatures (do not alarm on these):
  - A burst of `detector_no_wake_fleet_only` sleep-family records right
    after a controller restart — startup transient, campaign-classified.
  - On provider (tmux) flaps, the detector **fails closed** on
    `running_set_unavailable` for that cycle; under `"auto"` legacy covers
    the row. Campaign-observed during a real tmux-server death; fleet
    self-rebuilt in ~2 min.
  - Keyed `start` is ~2.5× legacy in a hermetic microbenchmark (p50 1.30ms
    vs 0.52ms, worst sample 4.9ms — ~0.016% of the 30s debounce budget).
    Invisible at fleet scale; noted so a profiler doesn't rediscover it.

## Rollback

`session_reconciler = "off"` + controller restart (boot-latched), or revert
to your previous deploy binary. Both engines coexist in every build; rollback
is config-only.

## Merge workflow: the origin/main sync

Merge 5ec88b1535 lost meaning three times while compiling clean and running
green. All three losses were set differences against the merge base, and all
three are now checked. Run at **every** origin/main sync — once before merging,
once after:

```bash
make check-merge-integrity          # --self-test, then the real audit
```

Or against a specific merge, which is how a past merge is audited:

```bash
bash scripts/check-merge-integrity.sh --merged <merge-commit>
```

**Check 1 — deleted-symbol resurrection.** Top-level Go symbols present at the
merge base and absent from origin/main are symbols upstream retired. None may
survive into the merged tree. This is `ga-f7v2ft.167`: the merge kept
`controllerDemandRouteTarget` alive to satisfy six lane call sites after
origin/main deleted it in #5250, so every ready epic and hold-parked row spawned
a pool seat that read empty and drained. Run at `5ec88b1535` the check reports
exactly that symbol, and only that symbol, out of 126 upstream retirements.

**Check 2 — vanished-test census.** Every `Test*` present at the merge base must
exist after the merge, be named in a commit message on `base..merged`, or be
listed in the allowlist. A conflict resolution that drops a behaviour drops its
test in the same hunk, so the suite that would have caught it is deleted by the
commit that broke the code.

**Check 3 — restored lane deletion**, the mirror of check 1. Symbols present at
the merge base and absent from the LANE are symbols *this branch* retired; none
may come back through the merge. Check 1 is blind to this class: when upstream
never touched the symbol it is byte-identical between base and head, so there is
no upstream retirement to compare against. This is `ga-f7v2ft.184`: the lane
retired `readyDemandSnapshotFingerprint` when the scan was promoted to the
sweep's routed-work view, two incoming upstream *test* files called it, the merge
broke the compile, and the compile was fixed by taking the symbol back — shipping
a caller-less island. The check needs a lane ref, which it takes from `^1` of a
merge commit or from `--lane`; in the pre-merge mode the lane *is* the tree, so
it reports NOT APPLICABLE rather than passing mute. Run at `5ec88b1535` it
reports three of the lane's twenty retirements: the two island symbols, plus
`teardownServerForStop`, a restoration the merge got *right* (the lane had lost
upstream #5175/#5196's managed-stop teardown; the merged tree calls it in
production) and which is allowlisted under the `restored` kind with that caller
named.

Deliberate keeps and retirements go in `scripts/merge-integrity-allow.txt`, one
per line with the reason. **A blank reason fails.** The three kinds are separate
(`symbol`, `test`, `restored`) so a waiver written for one class cannot silence
another. Regenerating the file to absorb a finding defeats the guard — the diff
is the review. A finding stays out of the file until it is adjudicated, and the
guard stays red on it on purpose in the meantime.

**Status at the release cut: green**, re-verified against the cut commit itself
rather than its parent (see the second trap below). `ga-f7v2ft.182` was
adjudicated by ADOPTING upstream's retirement (cherry-pick `c2da2c1624`), not by
a waiver, so check 1 reports no resurrection; `ga-f7v2ft.183`'s thirteen tests
were each recovered and run against lane code before being ruled on — two were
genuine losses and were restored, eleven are allowlisted retirements; the three
modal-dismissal tests the council's B1 fix re-sited were ruled the same way,
with their replacements run (including on real tmux); and B4's own
`TestUndecodedPathsWithEmptyRetiredRegistry` was recovered, RUN, and shown to
fail on its first line — `len(retiredKeys) != 0` is exactly what F2 changed —
so it was re-expressed rather than restored. Nothing is parked awaiting a
decision. Re-run this at the merge commit anyway: pre-merge mode audits against
whatever `origin/main` is TODAY, so a sync that lands after this line is written
can surface findings this run could not have seen.

Two operational traps, both hit for real on this cut:

**A shallow `origin/main` looks like a broken script.** Pre-merge mode resolves
its base as `merge-base(HEAD, origin/main)`, and a depth-1 `origin/main` has
none, so the guard fails closed on an unresolvable base. Run
`git fetch --unshallow origin` (or pass `--base`) first.

**Run it on a commit, not on your edits.** Every tree is materialized with
`git archive REF`, so uncommitted work is invisible. A green run taken before
committing certifies the tree you are about to change. Here a pre-commit run
passed check 2 at 34 vanished tests, and the very next commit's test rename made
it 35 — the guard was right both times, and the operator was reading the wrong
one. Commit, then run.

The guard proves its own bite through `--self-test`: sixteen cases on real temp
git repos covering a resurrection, an allowlisted resurrection, an allowlist
entry with no reason, a symbol that merely moved files, a vanished test, a
commit-body retirement, Go source inside a raw string, a restored lane deletion,
a check-1 waiver that must *not* silence check 3, the two controls that make
check 3's failure attributable (a lane that kept the symbol, and no lane ref at
all), an unresolvable ref, an unresolvable lane ref and an unreadable allowlist.

Twin drift is the sibling failure — a shared decider gains a rung upstream and
the lane's keyed copy silently never learns it (`ga-f7v2ft.166`). That one is
checked continuously by the census in `cmd/gc/twin_drift_census_test.go`, which
runs with the ordinary suite and needs no merge to be useful.

## Boundaries

- **`/data/cities/reconciler-campaign` is off-limits** to the deploy: its own
  sockets (`wd15-campaign`/`wd15-ops`) and supervisor port 8461, still live
  pending teardown preconditions. Do not adopt, restart, or reconfigure it.
- The final origin/main merge happens **after** the soak, not as part of this
  deploy (owner's sequencing).
