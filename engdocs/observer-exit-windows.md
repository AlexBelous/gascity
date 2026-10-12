# Bounded exit windows (#27/#32)

Implementation/review authority: gascity-kb#27 comments 6114688862 and
6118394868. This source and the strict Go/Python readers are one reviewed release
unit. Code/test acceptance does not grant provider admission or business start.

The original errors, diagnostic annotation, scan counts/digests and chronological
receipts remain intact. A resolution is separate positive kernel evidence:

| Path | Required observation | Typed link |
| --- | --- | --- |
| A | FULL nonmanaged prior; original bound ENV-failure pidfd/procdir; later real Z/start/UID/fdinfo/poll witness | `known_nonmanaged_terminal` with `bound_first_environ` witness |
| B | FULL nonmanaged prior; original pre-stat first-poll pidfd/procdir; real Z/start/UID/fdinfo/poll witness | `known_nonmanaged_terminal` with `initial_pidfd_poll` witness |
| C | Original FULL seal-birth pidfd/procdir; unreaped Z; exact readable comm/PPID/PGID/start/all four UIDs and fdinfo before/after | `bound_seal_birth_terminal`, plus zero/one compatible ENV error |
| D | FULL nonmanaged prior; complete numeric enumeration missing its PID; fresh independent flags0 pidfd_open ESRCH | `coverage_retirements` linking the indexed enum-absence/retirement pair, without a fabricated error |

Each new terminal proof requires `exit_witness`; unrelated/legacy proof kinds
forbid it. C additionally requires `full_identity_revalidated=true`. A/B require
false. Numeric PID lists are optional for legacy receipts but mandatory for C's
previous enumeration and D's missing/following enumerations. Readers recompute
ordered length32BE+UTF8 enumeration hashes and check verified-row membership.

Custody is transferred once without reopening, remains charged to the existing
FD meter, and closes on resolution or source teardown (including UNKNOWN/cancel).
No sleep, helper thread, scan, memory or runtime-profile budget is added. A pending
owned ENV window may consume only remaining original slots; no witness by slot 10
remains UNKNOWN. Positive first polls use ESTALE rather than ambient errno.

One incarnation is retired once. C's one birth plus zero/one ENV group shares one
proof and one count removal; every raw error index is used once. A proof after a
completed scan cannot select that scan as the final seal. After seal 7, actual
8/9/10 are required. An original C FULL row may precede its terminal witness in
the same completed scan; only the explicitly linked original birth row is allowed
in that same-index exception. Later FULL rows/reuse refute the new retirement.
Survivors and provider roots still require BOTH selected closings; provider/caller,
namespace, source and capability authority are checked by existing boundaries.

Unsupported coverage remains UNKNOWN: direct-reaped C without the original
unreaped-Z observation; unseen/unbound or protected/managed/root/tmux exits without
existing authorized certificates; changed metadata/UID/ownership/reuse; unreadable
or incomplete inputs; missing numeric receipts; exhausted bounds; and remaining
nonconvergence. An unreaped process that produces further incompatible faults is
not silently filtered. This improves admissible exits, not availability under
arbitrary churn, and does not regrade historical failures.

Controlled fixture `census-exit-window-kernel-test.c automatic` uses five owned
children, synthetic ENV errors explicitly labelled as inputs, and real kernel
FD/Z/UID/exit observations. It exercises the actual continuation selector through
A+B+C+D, preserves four errors, uses five proofs, selects 8/9/10 and joins a surviving
FULL nonmanaged child in BOTH closings/seal. Controlled legacy-rule RED checks,
whole-ledger negative guards, original-FD live/reap/custody negatives and exact
shared Go/Python matrices accompany its receipt. These are semantic/kernel tests,
not host FULL-capability or production admission receipts.
