# Read-only per-SID runtime observation

`gc pool-admission-probe --per-session` emits the typed
`managed-session-observation/v2` contract. The existing command without this
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
observed VPS permission denials are an explicit deployment blocker. A city/UID-scoped adapter would need a verified controller/provider ownership contract
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
The separate helper source described below still needs actual privilege/namespace/LSM verification:
capability names alone do not establish complete coverage. Any unreadable or
raced potentially relevant PID continues to make the result incomplete.

## Remaining release gates

Observer and common start fence are source work under #27, not production
acceptance. Required gates still include exact-source independent review,
bounded broader checks, an approved and verified strict deployment scan scope,
source/controller binary agreement, recovery and coordinated installation.
Darwin remains unsupported for strict coverage. No production install, privilege
change, worker start or #32 cutover is authorized by this source package.

## Separate bounded Linux helper and positive provider PID join

`tools/observation/proc-helper/` contains a single-thread static Linux x86_64
helper, its strict framed protocol, synthetic fixtures, syscall denial probes
and staging-only service/socket examples. The full contract is
`engdocs/architecture/process-observer-helper.md`. Its fixed operation reads host proc
identity in an initial census and two closing censuses, with a final numeric/stat seal; it cannot accept client paths, PID/UID filters, plugins or
commands. The capability proposal is **CAP_SYS_PTRACE plus
CAP_DAC_READ_SEARCH**, solely for the dedicated helper service. Neither is a
read-only capability. A default-deny syscall filter excludes tracing, signals,
process memory operations, process launch and FD passing after startup. Fixed
read-only openat2 code supplies the path boundary; seccomp cannot inspect pointer
pathnames/flags. Baseline deployment therefore explicitly trusts the reviewed
fixed code for its wider read surface. No LSM confinement is claimed.

`internal/runtime/procobserver` validates the separate
`host-process-evidence/v2` contract against exact source/binary/policy/boot/PID
namespace and connecting-caller pins. JSON keys/types are required; null except typed unknown incarnation starts,
duplicate or unknown fields, stale/replayed/partial responses, extra frames,
ancillary FDs and oversized replies refuse. It preserves the helper's earliest
evidence time and original raw counts/digests/errors. Version2 requires typed
kernel absence/retirement witnesses and independently reconciled closing rows;
ordinary read errno never substitutes for kernel proof. Both helper frames and
the exact kernel/filter provenance profile are required for COMPLETE. No provider or ledger access occurs in the helper. The adapter
has no permission-grant, process-start or fallback operation.

`gc pool-admission-probe --per-session --via-controller` selects the fixed
`observe-managed-sessions` operation on the existing controller Unix socket.
The CLI cannot choose policy, construct a provider, call the helper or fall
back to a local scan in this mode. It reads only the fixed root-owned policy
to verify the socket peer through Linux SO_PEERCRED and actual PID/UID/start,
boot and executable hash. Complete JSON provenance must match these pins;
a same-user fake listener or claimed source hash is insufficient. The persistent controller/supervisor uses its
actual current provider and fixed `/etc/gascity-observer/client.json`. A
thread-safe late-ready slot returns UNKNOWN before standalone initialization.
An absent or stale startup policy is retried without writes; after exact caller
verification, the policy is latched for that process lifetime. A restart requires
an approved root rebind of the new PID/start/boot/hash before verification can
succeed. A UID wildcard, child CLI ticket or per-tick policy rewrite is absent.

One process-wide slot covers all supervisor cities; bursts receive UNKNOWN
without queuing. A 25-second context budget and disconnect cancellation reach
helper socket reads. A provider that fails to return keeps the slot occupied,
preventing repeated abandoned scans. Short state locks snapshot and recheck the
provider/config generation. Reload or city shutdown invalidates completeness.
The reply limit is 1 MiB; larger helper/domain output becomes typed UNKNOWN
rather than increasing limits for other controller operations. Strict CLI reply
validation checks source/city, required fields, original interval and caller
provenance. Valid complete and partial daemon bytes pass unchanged; local CLI
build metadata never replaces daemon provenance.

`runtime.ProcessRootTracker` closes the second-scanner problem: tmux reads its
live pane PID; ACP reads the live control socket's `pid` operation; auto merges
positive matches and rejects omitted/changed evidence or conflicting owners.
Both PID and SID must match uniquely. Same-SID old/orphan roots stay untracked.
Provider run epoch/template/token metadata is then compared by the common
observation domain, with provider/roots/ownership checked again after collection.
External evidence never substitutes `tracked=true` for a live owning PID.
The legacy scanners and orphan termination implementations are unchanged.

The persistent caller interface is implemented and fixture-tested. Actual
root-owned deployment binding, capabilities, immutable ownership and restart
readbacks remain separate release gates. The default local probe retains
`controller_binding:not_observed`; only a verified persistent observation can
attest its own source and binary. Independent installed-source acceptance is
still required.

`engdocs/architecture/process-observer-helper.md` specifies the exact JSON fields, fixed limits and errors,
required hard service deadline, privilege/readback proposal and recovery.
Fixture tests run without capabilities and never scan the live host. The Go
Unix codec → redacted roots → live-PID tracker → observation serialization is
checked unchanged against consumer06ca in nine complete/negative scenarios.
These checks are not live capability/namespace, installed-controller or release
acceptance. No service files, permissions or candidate binaries are installed.
After a coordinated new runtime-source release, acceptance starts fresh; the
currently effective 48h/500 ticks must not silently shrink to the documented
minimum24h/200 ticks or reuse old-contract ticks.

### Socket activation credentials

For the fixed systemd Accept=yes unit the client must verify root activation
peer PID1/UID0, not pretend SO_PEERCRED identifies the worker. Linux preserves
the listener creator credentials across inherited descriptors. Before sending
its request the client enables SO_PASSCRED and authenticates every response
byte using kernel SCM_CREDENTIALS: exact dedicated helper UID and one stable
writer PID for the whole frame. Only that ancillary type is allowed; SCM_RIGHTS
received descriptors are closed and rejected, as are missing/wrong credentials,
unknown ancillary data or truncation. Helper write(fd3) automatically receives
kernel credentials at the client, requiring no helper sendmsg/signal syscall.
Root-owned fixed socket/service/executable/binding and actual service UID/cap
readback remain required: activation peer proof is not a helper binary proof
by itself. The controller socket uses ordinary SO_PEERCRED plus actual peer
start/executable/boot verification; it is not an activation endpoint.
