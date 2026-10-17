# Proposal: sixteen immutable observation-retention slots

**PENDING БАА — source proposal only; not approved for activation.**

Tracks <https://github.com/AlexBelous/gascity-kb/issues/142>.
Proposal record (not quota approval):
<https://github.com/AlexBelous/gascity-kb/issues/142#issuecomment-6228875364>.
The base is the
installed Native revision `cdd64a3b2ba954f987aed038c0137bfa25202636`.
This branch contains no poller-environment or BD-diagnostic changes. It does not
change the active SOP, select a deployment window, or authorize installation.

## Problem and proposed boundary

The existing shared `.gc/runtime/observer-v3-reacquisition` directory has eight
occupied failed-attempt slots and three candidate audits. The ordinary
02:51 UTC sample reached the retention gate with retry-eligible coverage, but
could not durably retain another failed attempt. Its first observation remains
incomplete; no second observation or successful admission is established.

The proposed change uses **one fixed global limit of sixteen slots in the same
directory**, shared by `Save` and `SaveCandidate`. It creates no generations,
per-caller directories, configuration switches, cleanup APIs, or rotation.
Existing failed-attempt indices 0–7 and all existing candidate files remain
byte-for-byte and metadata-identical. Every occupied or partial file continues
to consume its slot through `O_EXCL`; nothing is truncated, removed, renamed,
or reused. With the current eight occupied failed slots, only indices 8–15
are available for new failed attempts. Index 16 is never valid.

The global disk bound, including partial files, is:

- `16 × 33 MiB = 528 MiB` for failed attempts;
- `16 × 16 KiB = 256 KiB` for candidate audits.

The candidate bound is deliberately for all sixteen indices, not merely the
three existing audits plus eight possible new audits. It is a disk bound;
the encoder's memory behavior is unchanged.

All selection, eligibility, complete-proof, provider/root/caller, cancellation,
fsync, deadline, TTL, cadence, capacity, hold and admission predicates remain
unchanged. An initial COMPLETE transaction still needs no retained failed
attempt. A later incomplete transaction gets no retry when all sixteen slots
are occupied. Sixteen slots do not promise sixteen future collector ticks:
all callers share the same store.

## Required decision and coordinated delivery

This Source is a concrete option for the Owner to review. **No quota extension
or runtime effect is authorized by its tests, commit, or review.** The active
`VPS-OPS-CAPACITY-01` sections 1102–1119 still limit the installed store to eight.
Explicit new approval from БАА must precede any coordinated SOP/Native/consumer
release. The canonical SOP is in `~/Projects/gascity-kb`; this proposal is not a
replacement rule.

If approved, the delivery plan must bind the exact Source and policy, inventory
the original eight failed files and three audits without mutation, verify PAIR,
backup and disk space for the full proposed bound plus the existing additional
1 GiB reserve, and use a fresh reviewed deployment window. At fourteen occupied
failed slots (six additional slots consumed), Root must make a separate
continuation decision; sixteen occupied slots refuse the next retention.
Rollback preserves every old and new retention file. It does not clear slots
or create a seventeenth slot. Without approval, the existing eight-slot policy
and its fail-closed retry refusal remain in force.

## Owning verification

The existing real-filesystem cap test now seeds eight old files and three
audits, then races independent Store instances for the eight remaining slots.
It checks receipt hashes, modes, no duplicate winner, rejection after sixteen,
and unchanged original bytes and full file metadata. The existing partial-file
test fills the sixteen-slot bound and proves a partial slot is never reused.
Candidate-boundary cases accept indices 8–15, reject index 16 before creating
an audit, and reject replacement of an existing audit. Existing permission,
symlink, cancellation, candidate-readback and whole-attempt tests remain.
