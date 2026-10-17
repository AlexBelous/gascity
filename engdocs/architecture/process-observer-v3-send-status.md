# V3 helper send budget and fatal-stage diagnostics

This Source change belongs to [gascity-kb #142](https://github.com/AlexBelous/gascity-kb/issues/142).
It is not a deployment or admission grant. The separate V2 entry retains its
original sender and exit2 behavior.

## Original wall budget

The V3 entry enables `GC_HELPER_V3_SEND_BUDGET` before including `observer.c`.
Its nonblocking sender uses the remaining part of the original ten-second
monotonic wall budget, including bootstrap, collection and encoding. It does
not establish a new deadline. Readiness waits and EINTR/EAGAIN continue the
same unsent frame suffix; they do not repeat collection or re-encode output.
Permanent errors and original-budget expiry still fail. The client deadline,
system-manager RuntimeMaxSec, wire, caller binding, census completeness and
both fresh provider joins remain unchanged.

An owned socket test establishes an availability defect in the previous
100ms poll: a reader withheld for 250 ms caused a sender failure at 101 ms while
its original budget remained. This does **not** establish the cause of the
historical service 2327 or 2330 exit2 failures. A later ordinary sample was
COMPLETE; no claim that every UNKNOWN is repaired follows from this patch.

## Closed V3 fatal-stage statuses

Complete remains 0; typed incomplete remains 1. Fatal statuses 64–84 identify
only the following closed stages, with no errno, path, environment, process
content or secret output. The machine-readable table is
[`V3-FATAL-STAGE-CODES.json`](../../tools/observation/proc-helper/V3-FATAL-STAGE-CODES.json).
Use this table rather than a system manager's conventional status name.

| Status | Stage |
| --- | --- |
|64|invocation_source|
|65|clock_start|
|66|sigpipe_setup|
|67|bootstrap|
|68|binding_file|
|69|self_hash|
|70|boot_identity|
|71|namespace_identity|
|72|proc_mount|
|73|caller_initial|
|74|request|
|75|dumpable|
|76|kernel_provenance|
|77|fd_get|
|78|fd_nonblock|
|79|seccomp|
|80|global_alloc|
|81|ledger_alloc|
|82|envelope_alloc|
|83|envelope_finalize|
|84|send|

The socket client does not receive the helper process's exit status. Numeric
status alone is not a client admission gate. A send-stage 84 may follow partial
or complete bytes already written; it is not proof that no bytes reached the
client. Existing framed-response, EOF, deadline, authenticated-writer and
pinned-response validation independently determine whether evidence is usable.
No diagnostic status manufactures COMPLETE or permits admission.

The existing service has no SuccessExitStatus exception and Restart=no;
nonzero fatal statuses remain service failures. No unit/profile change is
part of this Source change. Ordinary incomplete census continues to emit its
typed provisional envelope with status 1. Startup, auth and request ordering,
short-circuit behavior and all denial predicates are preserved.

## Owning tests and release boundary

`make -C tools/observation/proc-helper send-test v3-fatal-test` runs owned
portable tests. The sender test compiles the actual production function and
uses a private socketpair with a child reader; it covers backpressure,
EINTR/EAGAIN, original-budget expiry, permanent failure and V2 preservation.
The fatal-stage tests compile the actual main against finite owned stubs and
compare call order with the pinned666 baseline. They cover 26 injected fatal
outcomes plus complete/incomplete/final fences and cleanup. Stubbed send
failure is not a proof about actual socket byte delivery.

These tests invoke no Native/helper RPC and supply no live host coverage or
caller authority. A delivered candidate still requires independent review,
normal gates and a coherent same-Source gc/helper Linux PAIR, followed by the
separate Root deployment protocol. The wall budget 10s, global whole budget 25s,
maximum two whole attempts, cadence 600s, freshness 60s, global immutable
retention 16, cap 3 and Phase A shadow/admission/owner holds remain unchanged.
