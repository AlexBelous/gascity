---
title: Turn On Keyed Session Reconciliation
description: What `[daemon] session_reconciler = "auto"` changes about how a city keeps its sessions correct, how to switch onto it, and how to switch back — one line of config, latched at boot, with nothing on disk to convert.
---

Every city keeps its running sessions matching what its config asks for: start
what is missing, stop what has finished, wake what has work waiting. By default
that runs as **one pass over every session in the city** on each patrol tick.

`session_reconciler` selects a second engine that does the same job **by key**:
each session that needs attention is handled against its own identity, rather
than re-derived from a pass over the whole fleet. Both engines ship in every
build, and choosing between them is one line of `city.toml`.

This is a change of engine, not of behaviour. The same triggers start, stop and
wake the same sessions under the same config; what changes is which code decides.

## What each value means

| Value | What the city does |
|---|---|
| `off` (the default) | One fleet-wide pass per patrol tick decides for every session. |
| `auto` | Keyed handling per session, with the fleet-wide pass covering anything the keyed engine does not take. This is the value to switch to. |
| `require` | Keyed handling, and the city refuses to start when its requirements are unavailable rather than covering the gap. A hard guarantee for a city already running `auto` happily — not the value to switch to first. |

`auto` never leaves a session uncovered. A session the keyed engine declines is
decided the way `off` decides it, on the same tick, which is why `auto` is the
safe end of the switch: the worst case is the behaviour you have today.

## Switch a city

```toml
[daemon]
session_reconciler = "auto"
```

The value is read once, when the city starts. Restart to take it:

```
gc stop
gc start
```

Nothing else changes. No state is converted, no format is rewritten, and no
session is stamped as belonging to one engine.

## Sessions that were already in flight

You do not have to quiet the city first, and there is no drain-and-wait step
before the restart.

A session that was mid-drain when you restarted is re-evaluated against the
state it is actually in when the city comes back, so a drain already under way
finishes normally. The engine acts on where each session is now, not on a
decision taken before the restart — which means there is nothing to reconcile by
hand and nothing left half-applied.

## Rolling back

Rollback is config-only and always available:

```toml
[daemon]
session_reconciler = "off"
```

then `gc stop` and `gc start` again. Because neither engine writes anything the
other cannot read, a city can move back and forth as often as you like, and a
rollback costs exactly one restart. Reverting to your previous build works too:
a build without the setting behaves the same as `off`.

## What to watch after the switch

- **Session lifecycle.** Sessions start, stop and wake when you expect them to,
  and health patrol stays steady. `gc status` is the fastest read.
- **Provider flaps.** If the session provider is briefly unreachable, the keyed
  engine declines that cycle rather than guessing at what it cannot see. Under
  `auto` the fleet-wide pass covers those sessions until the provider answers
  again.

The setting and its enum live in the
[configuration reference](/reference/config) under `[daemon]`.
