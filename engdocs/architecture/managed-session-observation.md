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

## Remaining release gates

This observation adapter is not an operational admission fix. The existing
native fence does not yet enforce snapshot v5 / census v1 / oldest-evidence
freshness of 60 seconds. That compatibility repair must have tests against
`claimPoolStartAdmission` before spending a grant. The existing reservation
ledger, lock, SID claims, and start spacing remain the authority boundary.
Named/dedicated starts are not covered merely because the pool-managed caller
uses that fence. Independent caller coverage, strict deployment scan scope,
review, and installation remain separate release gates. No production install
or worker start is authorized by this source package.
