# Fixed Linux process evidence helper

This is source for review, not an installation or capability grant. It is a
single-thread static Linux **x86_64** program. It never invokes gc, a provider,
shell, subprocess, CRM, process signal, ptrace, or process-memory operation. Its
only production command is `gc-process-observer-helper` with no arguments. The
system manager supplies one already connected AF_UNIX stream on fd3. It exits
after that connection; provider handles and admission stay in ordinary gc.

## Fixed wire contract

The client writes one uint32 network-order length followed by UTF-8 JSON, then
**shutdown(SHUT_WR)**. Request length is 1..1024 bytes. Exactly these two
unescaped JSON string fields are accepted, in either order:

```json
{"schema":"observe-host-processes/v1","request_nonce":"64-lowercase-hex-digits"}
```

Unknown/duplicate fields, paths, PIDs, filters, malformed JSON, additional frames
and any ancillary control data/SCM_RIGHTS are rejected. Startup/authentication
or request refusal closes the connection with exit2 and no diagnostic payload.
The adapter must turn EOF, deadline or any failure into UNKNOWN, never empty
complete coverage. Output is one length-prefixed JSON `host-process-evidence/v1`.
Its fields are:

- `scope`: `host_procfs`; the distinct test build emits `fixture_procfs` and must
  never be accepted by the production adapter.
- `request_nonce`, `helper_source_revision` (build-time exact40hex source pin),
  `helper_binary_sha256` (self hash checked against immutable binding),
  `policy_digest` (SHA256 of the literal `POLICY` in observer.c).
- `boot_id`, `pid_namespace_identity`; `started_at`, `finished_at` (RFC3339Nano
  UTC); `duration_ms` from monotonic clock. Startup/request time counts too.
- `complete`; `enumerated_count_before/after`,
  `enumeration_digest_before/after`. A digest covers sorted PID, PPID, PGID,
  start_ticks, epoch, validity, comm, selected GC identity and token SHA256.
- `caller_binding`: `pid`, `uid`, `start_ticks` (decimal string), `boot_id`,
  `controller_source_revision`, `controller_binary_sha256`.
- `roots`, sorted by PID: `pid`, `ppid`, `pgid`, `start_ticks` (decimal string),
  `session_id`, `city`, `template`, `epoch`, `instance_token_sha256`, `name`,
  `parent_is_provider_infrastructure`, `parent_name`.
- `errors`: bounded128 rows `{reason,operation,pid,errno}` with no contents or
  caller-provided path. `errors_total` and `errors_truncated` preserve coverage
  failures beyond the output limit. Error reasons include process_unavailable,
  malformed_environment, malformed_identity, incomplete_identity,
  parent_unavailable, incarnation_changed, coverage_changed,
  caller_binding_changed, boot_changed, clock_reversal, enumeration_failed,
  malformed_directory, duplicate_pid, deadline, limit_reached,
  allocation_failed, sandbox_unavailable, response_limit.

All roots are host-wide, including other cities. No UID or role exclusion.
Exact tmux infrastructure is distinguished; same-session descendants of an
ordinary parent are not second roots. The adapter performs the actual
template/epoch/token/provider PID join, including the second scanner boundary;
these roots alone do not constitute managed-session-observation completeness.

## Immutable binding and startup

Production opens only `/etc/gascity-observer/binding.conf`, fixed startup host
identity and `/proc/self/exe` before its syscall filter. `/`, `/etc`, and
`/etc/gascity-observer` must be root-owned directories not writable by group or
other. Binding must be root-owned regular file, not writable by group/other and
not a symlink. Executable must likewise be root-owned regular file without
group/other write. The binary trusts reviewed root-owned source/deployment;
owner write is intentional and replacing it requires new pins and review.

Binding max4096 bytes, exactly these unique `key=value` lines, no optional keys:

```text
boot_id=<exact lowercase UUID from approved host readback>
controller_pid=<exact SO_PEERCRED PID>
controller_uid=<exact SO_PEERCRED UID>
controller_start_ticks=<exact kernel field22>
controller_source_revision=<exact40hex release source>
controller_binary_sha256=<exact64hex release binary>
pid_namespace_identity=pid:[<approved host inode>]
helper_binary_sha256=<exact64hex released helper>
```

The helper checks kernel SO_PEERCRED PID/UID and proc start_ticks before and
after observation, boot ID before/after, its binary hash, expected PID namespace,
equality of PID1/helper PID namespaces, procfs type, and visibility mount options.
Controller executable/hash is a deployment readback pin: SO_PEERCRED cannot
prove executable identity or defeat a compromised pinned controller transferring
its connected fd. No wildcard same-UID readers or stale-controller binding.
Controller restart requires a reviewed binding refresh. Host initial user/PID
namespace, mount/LSM/options, current controller binary hash and actual capability
sets must be independently proven before installation. Equal PID1/self namespace
alone is not proof that a container is the host; immutable expected host pin is
required. No global hidepid, Yama or LSM changes are proposed.

## Bounds and sandbox limits

Two passes over numeric proc entries; each process uses stat-before → environ →
comm → stat-after. Every process is read, even with no GC identity or an
infrastructure comm. Malformed/missing/inaccessible/changed data means incomplete.
No disappearing PID is silently reclassified as unrelated. Duplicate GC keys
are refused; only selected identity strings and token hashes are retained. Raw
environment/token buffers are cleared before release and never emitted/logged.
PR_SET_DUMPABLE=0 is set before collection; deployment must also disable cores.

Fixed bounds: ten-second monotonic budget, **65536 PID attainment is incomplete**,
16MiB environment per process, 16MiB retained identity per pass, selected string
max4096 bytes, 16MiB output and128 errors. Output overflow yields complete=false
with empty roots and response_limit, never complete empty. The loop checks time
between reads, but synchronous kernel proc reads can themselves block: **the
system manager's hard RuntimeMaxSec10s is mandatory**, and client deadline must
close/UNKNOWN if no typed response arrives. No promise that a blocked syscall
can be cancelled internally by this single-thread helper.

The helper ignores SIGPIPE on its own process during startup; a closed peer
produces transport failure/UNKNOWN with normal exit2. Nonblocking I/O bounds
writes and does not itself suppress SIGPIPE. No process signals are sent;
post-startup signal syscalls remain denied.

After startup and bounded request read, seccomp default denies the x86_64 ABI
(compat/x32 cannot match). Only read, close, openat2, getdents64, fstat/newfstatat,
clock_gettime, non-executable memory allocation operations, getsockopt, poll and
exit are allowed. Write is restricted to fd3. No ptrace, process_vm*, signals,
exec/fork/clone, namespace/mount mutation, socket creation, ancillary FD passing,
open_by_handle_at, ioctl, io_uring, ordinary open/openat, or file-mutating syscall.
Required openat2 support has no weaker fallback; opens are fixed O_RDONLY with
RESOLVE_BENEATH|NO_SYMLINKS|NO_MAGICLINKS beneath the preopened proc fd.

**Residual read/source trust:** seccomp cannot inspect openat2's pointer flags or
path strings. It is not an enforcing filesystem path boundary; reviewed fixed
code is responsible for its read-only flags and fixed paths. CAP_SYS_PTRACE and
CAP_DAC_READ_SEARCH are broader than this operation. They are proposed only for
this dedicated helper service, not controller/gc, and need separate owner
approval. Read-only service filesystem restrictions reduce mutation surface;
an enforcing reviewed LSM policy is an optional additional read boundary. No
LSM protection is claimed or installed here.

## Reproducible source/fixture checks

On a disposable Linux x86_64 source directory, with no capabilities:

```sh
make SOURCE_REVISION=<exact committed source SHA> OUT=/absolute/disposable/helper build
make SOURCE_REVISION=<exact committed source SHA> OUT=/absolute/disposable/helper test
```

`build` creates only the production binary; it does not execute it. `test` builds
distinct `GC_HELPER_TEST` binaries accepting **synthetic** proc/binding paths.
They emit fixture_procfs. The production binary has no test path, binding,
limit, syscall-probe or arbitrary PID flags. Test build's selftest probes are
performed solely in its disposable child; denied operations do not target
business processes. Fixture tests exercise positive parsing/redaction/hash,
permissions and missing reads, exact/substring infrastructure, parent coverage,
environment overflow/malformed/duplicates, bounded errors/output, symlink
escape, strict frame/ancillary-FD refusal, exact-limit and limit+1 chunk-end,
caller boot/PID/UID/start/namespace mismatches, JSON injection, and actual syscall
denials. They do not prove production capabilities or live cross-UID coverage.

Do not install/apply the illustrative systemd files without owner approval,
exact helper/controller/policy/binding binary pins, independent review,
deployment SOP, live rights/scope readback and complete provider join proof.
Disable the adapter/socket to roll back; restore prior exact source/pins and
preserve UNKNOWN/managed-start hold. Stop only this helper service, not agents.
New runtime contract starts a new observation window; effective48h/500ticks must
not silently shrink to the SOP minimum24h/200ticks.

## Persistent caller and restart

The fixed controller socket verb `observe-managed-sessions` calls the helper
from the existing persistent controller/supervisor, using its current provider.
`gc pool-admission-probe --per-session --via-controller` only relays that reply.
An unavailable controller, policy/binding disagreement, busy reader, cancelled
request, generation change, partial scan or reply above 1 MiB stays UNKNOWN.
The service retries loading an unverified startup policy but latches it after
exact local PID/UID/start/executable verification. Reboot/restart therefore
requires one approved root binding publication for the new persistent process;
ordinary ticks and CLI processes never write policy. Existing latched pins
cannot silently follow a replacement binary or caller. See the reviewed
restart/deployment runbook for the separate privileged operator step.

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
