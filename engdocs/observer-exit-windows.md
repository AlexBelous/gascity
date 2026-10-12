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

## M/R original-capture extension (Source27/32)

`deferred_managed_retirement` retains the immutable first-ENV fault and its
first-pass, bound-live, initial-zero, no-first-identity, no-exit-witness diagnostic.
The producer reads and compares current comm before this ENV attempt without
marking the identity FULL or altering `fields_pass`. It derives the exact prior
acyclic inherited managed chain to a declared root, retaining PID/start, UID4,
comm, PPID/PGID and ownership tuple. Continuing ancestors/root must remain FULL.
Genuine later retirement creates a protected ENUM/retirement pair and certificate;
the distinct typed relation names the original error, prior, certificate and
actual later selected seal. Legacy certificate own-fault same-scan rules remain.

`managed_birth_retirement` is a separate original FULL birth→protected certificate
relation. It uses the birth's own original custody, leaving the certificate's
actual second-pass ENV or nullable fault and index unchanged. Exactly one birth
and one independently valid certificate-source fault may share a retirement.

`direct_reaped_birth` is distinct from unreaped-Z C. Historical FULL metadata
binds the original incarnation; no post-reap comm/stat/UID read is claimed. It
requires actual same-original pidfd terminal flags, fdinfo PID=-1, completed
positive numeric absence and a fresh independent flags0 pidfd_open ESRCH. Ever
managed/protected/root/controller/tmux identities remain ineligible.

Original capture carries a source-owned immutable handle. The helper meter tracks
all newly owned descriptor generations, not merely counts or pidfd dev/inode.
A handle records the exact original descriptor numbers and generations before
fault/FULL completion. Mutable raw-fault aliases cannot replace them. Proof checks
both owned generations; release closes only those original generations, once.
OTHER-REAPED substitution and close/reopen numeric-FD ABA are rejected even when
both pidfds share inode2083 and return PID=-1/POLLIN. Initial inherited descriptors
are inventoried as borrowed and cannot form capture handles. All captures and
checks retain existing FD32/time/memory/slot limits; any failed take still closes
the already opened own descriptor and refuses.

Timing is not repaired retroactively. A witness after a completed SEAL cannot
select that same seal. The historical first-ENV scan9 / first numeric absence10 /
positive proof after SEAL10 has no later seal under ten slots and remains UNKNOWN.
Direct-reaped births with enough remaining closings/seal can resolve; late exits,
missing original custody, unstable surviving sets, incomplete authority or budget
exhaustion can still legitimately refuse. This extension is not universal host
admission and does not regrade the installed67 ordinary history.

Persistent controlled Linux tests: `census-mr-kernel-test.c`,
`census-original-custody-kernel-test.c`, `mr-selftest.py` and shared reader matrix.
They inspect only owned identities, actual kernel FD/exit/UID witnesses and numeric
membership; the whole census uses an explicitly owned fixture enumeration, with
independent genuine getdents verification of target presence/absence. ENV failures
and ordinary provider/release metadata are synthetic; two root frames are captured
independently. These are code-mechanism proofs, not live FULLcaps/RPC, release or
business acceptance. Root alone reviews the final source/consumer freeze before
any runtime build or publication. References: gascity-kb#27/#32 and Root6123281786,
pre-freeze correction6123470386.
