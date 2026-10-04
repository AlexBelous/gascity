# Read-only per-SID runtime observation

`gc pool-admission-probe --per-session` emits the typed
`managed-session-observation/v1` contract. The existing command without this
flag retains its aggregate output. An incomplete observation is printed and
exits nonzero. This command neither opens a bead store nor spends a reservation,
starts, wakes, resumes, drains, or terminates a session.

## Identity and completeness

`Provider.ListRunning("")` supplies live provider handles, including every
configured template. Each handle's `GC_SESSION_ID`, `GC_TEMPLATE`,
`GC_RUNTIME_EPOCH`, and `GC_INSTANCE_TOKEN` metadata must be available. SID is
never inferred from a role alias or durable state. Two live SIDs of the same
template remain two rows. Multiple handles claiming one SID deny completeness.

The optional process capability is discovered through
`runtime.AsProcessTableScanner`, including `CanScanProcessTable` for composites.
A scannerless default behind `auto.Provider` is unavailable even though the
composite implements the Go interface. Partial scan results with an error are
incomplete.

A separate strict, read-only process census supplies PID, PPID, opaque kernel
start identity, city, template, epoch and a SHA-256 comparison value for the
instance token. The raw token is an ownership credential and is never emitted.
Process evidence must match provider metadata and tracked handles. Missing roots,
unmapped roots, a mismatched run, reused PID, or changed evidence between reads
deny completeness. PID/start identity and metadata are evidence at observation
time, not authority to signal that process later.

`observed_at`, `process_observed_at`, and `finished_at` are UTC timestamps of the
bounded reads. A reversed or greater-than-60-second read interval denies
completeness. Consumers must also enforce freshness when consuming the result;
republishing a result cannot renew its evidence time.

## Supported scan scope and deployment limitation

Linux strict coverage is **host-wide**, over every enumerated numeric `/proc`
entry. Environment, command and stat read failures are retained as incomplete
coverage. Stat identity is checked before and after environment reads. No PID is
excluded because its process name, UID, or environment is inconvenient to read.
Kernel/provider infrastructure is excluded from agent-root classification only
using the existing exact infrastructure predicate, after successful reads.

A non-privileged controller on a mixed-UID host may therefore remain incomplete:
inaccessible processes cannot be proven unrelated to the city. The currently
observed VPS permission denials are an explicit deployment blocker. A future
city/UID-scoped adapter needs a verified controller/provider ownership contract
and tests rejecting every inaccessible possibly-in-scope process. This package
introduces no UID assumption, privilege escalation, or permission waiver.

Darwin reports strict coverage unsupported. Existing `ps eww` output flattens
argv and environment, and `kern.procargs2` can omit restricted-process
environments. Neither establishes host-wide absence of `GC_SESSION_ID`.
See Apple's [XNU sysctl_procargsx implementation](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sysctl.c).
Other unsupported platforms also report incomplete coverage. Legacy process
scanners and orphan/termination semantics are unchanged.

## Wire and source agreement

Top-level fields are `schema`, `city_path`, `observed_at`,
`process_observed_at`, `finished_at`, `provider_complete`, `process_complete`,
`provider_type`, `sessions`, `processes`, and `unknown_reasons`. Every session
has `session_id`, `status_name` (the exact template), `template`, `runtime_name`,
`running`, `run_epoch`, and `instance_token_sha256`. Every process has the same
SID/template/runtime/run identity plus `city_path`, `pid`, `ppid`, and
`start_identity`.

The CLI adds `source_revision` from its existing build metadata and
`controller_binding: "not_observed"`. A CLI report does not attest the running
controller binary. Deployment must independently verify its executable hash and
build revision against the reviewed source. Unknown source/controller binding
must keep the deployment gate denied. Extra provenance fields are not currently
enforced by the external pure consumer; they do not replace that gate.

The external code-only consumer is
`AlexBelous/gascity-cloud/tools/managed_live_census.py`, originally accepted at
PR366 `9abe70c58056b4819bd6d167d91d36db02a09a13`, subsequently updated at
`06ca4e449a27581caea05e3c2efad8635b148b04`. Both use this observation contract.
The consumer supplies independently read status, durable identities and routes;
durable state never establishes liveness. `consumer-input.json` contains two
managed SIDs, one protected SID and one dead durable row. The producer tests
serialize real `Observe` results from controlled provider/process boundaries.
`tools/observation/check-consumer.py` checks those unchanged bytes against an
explicit, hash-pinned consumer source. Negative cases must return `count: null`,
not a free slot; protected roles stay covered, never drain candidates.

## Shared start admission

The session-owned `ClaimCapacityStart` fence now requires snapshot v5,
`managed-live-census/v1`, complete non-null counts, matching full row objects,
unique managed SIDs, cap 1..2, and UTC publication/observation/oldest evidence
ages in 0..60 seconds. It rejects these before spending any grant. A recent
publication cannot refresh old underlying evidence.

`AdmitCapacityStart` spends one SID claim under the existing reader/writer flock
before continuation reset or orphan cleanup. An opaque invocation-local context
proof carries that same claim from controller preparation to the manager. The
proof is bound to city, exact logical route and SID; it expires at oldest
evidence +60s and is consumed once before the provider call. It is never an
environment credential or durable-row permission. Failed preparation or an
ambiguous provider start retain the ledger claim until the reader expires it;
a managed stale-key retry cannot reuse that consumed claim. Cities without
scheduler state keep existing fresh-retry behavior.

Controller pool, exact named and dedicated candidates share this boundary.
Direct manager create, ordinary wake/resume, runtime-only resume and fresh-retry
paths admit before start effects. A local denial releases ACP routing and does
not reset conversation identity, terminate an orphan, drain a target, or become
a controller wake failure. Runtime-only cold targets with no durable SID cannot
spend a managed grant. Missing authoritative `GC_TEMPLATE` is unknown rather
than an unmanaged runtime alias; an explicit protected template remains allowed.
The ledger schema, shared lock, retained SID claims and start-spacing policy are
unchanged. New-start CLI/API calls still use the canonical worker factory.

## Scan-scope decision required before deployment

The Cloud readback at 2026-10-04T08:13:21Z proves controller PID435977 and currently
observed launchers/agent roots use UID1000. It does not prove the ownership scope
of future launches. The same inspection found seven unreadable environments
under UID1000, in addition to cross-UID failures. UID-only filtering therefore
neither fixes completeness nor establishes safe exclusions. Generic subprocess,
tmux and ACP launchers can execute configured commands; this source does not
promise those commands cannot change credentials or escape an inferred tree.

Options for owner review (neither applied by this package):

| Option | Required reviewed work | Consequence |
|---|---|---|
| Keep strict host-wide observation and the deployment hold | No privileges or exclusions change | Current permission failures remain UNKNOWN; no autonomous managed start acceptance |
| Separate bounded observation helper (recommended design to review) | A fixed reviewed executable using only the required process-read capabilities in the host namespaces, a caller-restricted local interface, typed/redacted output, syscall restrictions excluding signalling, tracing, memory writes and process launch, plus exact source/controller binding and complete live readback | Retains host-wide orphan coverage; requires explicit privilege/interface approval and a separate implementation/test/recovery review |
| Enforced launcher ownership boundary | Prove and enforce every provider/launcher/future child and orphan's membership, including credential changes and reparenting; test unreadable in-scope processes as UNKNOWN | Larger launch-contract change; current UID/cgroup snapshots alone are insufficient |

Linux documents `/proc/PID/environ` access as a ptrace `PTRACE_MODE_READ_FSCREDS`
check ([proc_pid_environ(5)](https://man7.org/linux/man-pages/man5/proc_pid_environ.5.html)).
`CAP_SYS_PTRACE` has powers beyond read-only observation, including tracing and
process memory writes ([capabilities(7)](https://man7.org/linux/man-pages/man7/capabilities.7.html)).
It must not be granted to the whole controller or treated as a read-only switch.
The proposed helper still needs actual privilege/namespace/LSM verification:
capability names alone do not establish complete coverage. Any unreadable or
raced potentially relevant PID continues to make the result incomplete.

## Remaining release gates

Observer and common start fence are source work under #27, not production
acceptance. Required gates still include exact-source independent review,
bounded broader checks, an approved and verified strict deployment scan scope,
source/controller binary agreement, recovery and coordinated installation.
Darwin remains unsupported for strict coverage. No production install, privilege
change, worker start or #32 cutover is authorized by this source package.
