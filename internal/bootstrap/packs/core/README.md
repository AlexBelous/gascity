# Core pack housekeeping orders

Deterministic housekeeping for a Gas City, shipped as part of the bundled
core pack. Every order here is mechanical — timer comparisons and dependency
lookups — so the controller runs them directly via `exec` instead of spending
agent context. No LLM judgment, no wisps, no agent pipeline.

Cities that include the core pack get every order below automatically; none
requires per-city configuration.

## Orders

| Order | Trigger | What it does |
| ----- | ------- | ------------ |
| `gate-sweep` | cooldown 30s | Evaluate and close pending gates (timer, GitHub) |
| `orphan-sweep` | cooldown 5m | Reset beads assigned to dead agents back to the work pool |
| `cross-rig-deps` | cooldown 5m | Convert satisfied cross-rig `blocks` deps to `related` |
| `order-tracking-sweep` | cooldown | Close stale order-tracking beads and prune expired tracking history |
| `spawn-storm-detect` | cooldown | Detect beads repeatedly bouncing back to pool |
| `prune-branches` | cooldown | Clean stale `gc/*` branches from all rigs |
| `wisp-compact` | cooldown | TTL-based cleanup of expired ephemeral beads (wisps) |

Ready routed and assigned work is dispatched by the controller's reconciler.
It starts a compatible cold session or prompts a compatible running session at
the same dependency-gated readiness boundary, so the core pack does not use
event-order nudge workarounds.
