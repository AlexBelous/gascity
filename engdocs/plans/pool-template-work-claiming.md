# Plan: Pool-template work claiming (`ga-8vz95k`)

> Owner: `gascity/pm` - Created: 2026-08-22
> Source: architecture ruling `ga-qv1d2d` (R2), with live repro `ga-3ex7s2`

## Goal

An idle member of a configured pool must be able to discover and claim ready
work assigned to the pool template. The first successful claimant becomes the
work's concrete owner, while in-progress ownership remains exact so sibling
members cannot adopt the same work.

The architecture ruling is settled: pool work is anycast to eligible members;
Gas City does not add per-instance sling targets or widen raw beads assignee
matching.

## Work packages

| Bead | Outcome | Routes to | Gate |
|---|---|---|---|
| `ga-8vz95k.1` | RED coverage for default shared-template discovery | validator | `needs-tests` |
| `ga-8vz95k.2` | Pool members discover template-assigned ready work by default | builder | `ready-to-build` |
| `ga-8vz95k.3` | RED concurrency and backend-contract coverage for claims | validator | `needs-tests` |
| `ga-8vz95k.4` | One winning member atomically owns claimed template work | builder | `ready-to-build` |
| `ga-8vz95k.5` | RED awake-set coverage separating ready demand from ownership | validator | `needs-tests` |
| `ga-8vz95k.6` | Eligible members wake for ready template work | builder | `ready-to-build` |
| `ga-8vz95k.7` | End-to-end compatibility and regression suite | validator | `needs-tests` |
| `ga-8vz95k.8` | Contributor architecture docs match shipped behavior | builder | `ready-to-build` |

## Dependency graph

```text
ga-8vz95k.1 ──> ga-8vz95k.2 ──┬──> ga-8vz95k.4 ──┐
                               │                   │
ga-8vz95k.3 ──────────────────┘                   │
                               │                   ├──> ga-8vz95k.7 ──> ga-8vz95k.8
ga-8vz95k.5 ──> ga-8vz95k.6 ──┘                   │
                               └───────────────────┘
```

The three validator beads can start together. Discovery lands before claim
and awake-set implementation so all Gas City surfaces use the same
configuration-derived membership boundary. Final validation and docs wait for
all behavior slices.

## Acceptance mapping

| Root criterion | Packages |
|---|---|
| Default single-store and federated discovery; custom queries unchanged | `.1`, `.2` |
| Hook visibility and one-winner atomic claim | `.3`, `.4` |
| Exact in-progress ownership and `ga-80pen8` regression | `.3`, `.4`, `.7` |
| Ready-work wake behavior | `.5`, `.6` |
| Production backend, wrapper, and class-binding contract | `.3`, `.4` |
| Existing routed dispatch, raw matching, and sling boundaries preserved | `.1`, `.2`, `.4`, `.7` |
| Focused tests, fast baseline, and `go vet ./...` | `.7` |
| Contributor documentation reflects verified behavior | `.8` |

## Product boundaries

- Pool membership comes from current configuration, not a numeric suffix.
- Shared ready-work discovery uses the pool template; recovery and ownership
  use the concrete instance identity.
- The compatibility layer belongs in Gas City. Raw beads assignee filtering
  remains exact.
- Existing unassigned work routed through `gc.routed_to` remains supported.
- Custom `work_query` behavior does not change.
- Per-instance `gc sling` remains unsupported.
- The separate report of an assignee becoming empty after sling is out of
  scope until independently reproduced.

## Risks and controls

- **Duplicate claims:** the claim package requires a store-boundary atomic
  precondition and a two-claimant test; read-then-write is not acceptable.
- **Backend drift:** the transfer contract must cover every production backend
  and wrapper before integration validation can close.
- **Identity widening:** tests cover non-pool sessions, different pools, and
  suffix lookalikes, and keep in-progress ownership exact.
- **Documentation drift:** `engdocs/architecture/life-of-a-bead.md` and
  `engdocs/architecture/dispatch.md` still contain older label-only wording;
  `.8` updates them only after code behavior is verified.

## Duplicate disposition

`ga-p49zjx` is closed as superseded by this architecture-ruling lineage. Its
proposed reverse match (a template query adopting instance-assigned work) is
not a second requirement: ready pool work uses the shared template, while a
concrete assignee represents exact ownership.

## Handoff

All children carry `source:actual-pm` and exactly one intake label. Each child
is slung to its target with `--nudge`; blockers in the bead graph determine
when downstream work becomes ready.
