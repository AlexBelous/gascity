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

Merge 5ec88b1535 lost meaning twice while compiling clean and running green.
Both losses were set differences against the merge base, and both are now
checked. Run at **every** origin/main sync — once before merging, once after:

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

Deliberate keeps and retirements go in `scripts/merge-integrity-allow.txt`, one
per line with the reason. **A blank reason fails.** Regenerating the file to
absorb a finding defeats the guard — the diff is the review. Open findings
(`ga-f7v2ft.182`, `ga-f7v2ft.183`) stay out of it until they are adjudicated;
the guard is red on them on purpose.

The guard proves its own bite through `--self-test`: nine cases on real temp git
repos covering a resurrection, an allowlisted resurrection, an allowlist entry
with no reason, a symbol that merely moved files, a vanished test, a
commit-body retirement, Go source inside a raw string, an unresolvable ref and
an unreadable allowlist.

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
