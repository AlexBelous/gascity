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

## Boundaries

- **`/data/cities/reconciler-campaign` is off-limits** to the deploy: its own
  sockets (`wd15-campaign`/`wd15-ops`) and supervisor port 8461, still live
  pending teardown preconditions. Do not adopt, restart, or reconfigure it.
- The final origin/main merge happens **after** the soak, not as part of this
  deploy (owner's sequencing).
