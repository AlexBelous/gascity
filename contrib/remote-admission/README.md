# Finite remote admission authority

Issue: AlexBelous/gascity-kb#208. Source-only generic engine; **no production profile,
installed service, sudoers entry, root release, SSH key or model execution is shipped**.
The implementation is Python standard library code. Run its tests unprivileged:

```sh
python3 -B -m unittest discover -v -s contrib/remote-admission/tests
```

Operator-only bounded persistent HOME migration is documented in
[BOUNDED_HOME.md](BOUNDED_HOME.md). `provision_bounded_home.py` defaults to a read-only
plan and requires an accepted root manifest; its separate unprivileged tests are
under `provision_tests`. Never execute mutable development sources as root.

## Authority and execution

The controller calls a fixed root SSH forced-command endpoint with one bounded JSON
object on stdin. `rpc` accepts only prepare, stage_prompt, observe and cancel. A
separate transport UID runs tmux and may sudo only the reviewed fixed entry wrapper
`enter ATTEMPT`. The model UID is a different account. Entry consumes the exact
root-staged attempt before requesting one systemd start; it takes no command,
environment, path, permit, prompt argument or caller stdin. The trusted stage RPC
has already persisted the literal prompt and its hash. No shell interprets payloads.
The instance token and prompt stay in root-private files/control frames, never unit
argv, model environment, audit messages or transport receipt output.

The model is a sibling of the transport, launched by PID1 as `User=model_uid`, never
as root. Fixed privileged Pre/Post hooks check consumed admission, resources,
exact unit cgroup, MainPID/start time, executable inode/hash, InvocationID, UID/GID,
NNP/capabilities and effective cgroup limits. Hooks have exact rootstate write
access inside the sandbox. Root opens private prompt stdin and output append files;
entry streams only model output to its transport. A transport disconnect or tmux
exit does not prove model termination. Remote health and cancellation use this root
authority, not SSH descendant process matching.

The installer must explicitly provision root0600 `ledger.json` containing exactly
`{"schema_version":1,"current":null,"attempts":{}}` only for a proved fresh authority
before activating its root profile. The engine always refuses an absent ledger;
it never silently initializes or recreates lost history. After any prior attempt,
missing ledger requires explicit reconciliation of retained independent evidence.

The finite state machine is prepared -> staged -> consumed -> entered -> exited.
Any ambiguous start/observation retains an unknown occupied attempt. Only one
attempt can occupy the locked durable ledger. Retry never issues another start
for a consumed attempt. Cancellation journals revocation before its effect. There
is no TTL release, background scheduler, automatic retry or cleanup policy.
The required root profile `max_consumed_attempts` is a durable lifetime limit,
separate from concurrent cap1. The pilot must explicitly set it to **1**. Both new
prepare and entry count all retained `ever_consumed` rows under the journal lock,
including exited and unknown attempts. Pre-consumption cancellation does not spend
the budget. Existing prepare is idempotent but cannot grant another entry. History
is never expired or reset; missing or inconsistent consumption markers refuse.
Generic multi-attempt test fixtures explicitly use 2, which is no pilot approval.
Atomic journal publication fsyncs the file and parent directory. Missing/corrupt
state, mismatched profile, reboot, inaccessible proc/cgroup, changed invocation,
partial proof and nonempty scope all deny or retain the seat.

A dedicated operator-created persistent cap1 `.slice` is the release boundary.
The service cgroup can disappear on exit, including with RemainAfterExit=yes.
The root ledger retains its positive entry attestation. Release requires exact
terminal InvocationID history and the same boot and persistent parent device/inode,
with fresh recursive populated=0 and no tasks. Missing parent never means empty.
The slice must be root-owned, nondelegated, persistent StopWhenUnneeded=no, and
exclusive to this authority. No other unit may use it. Do not stop/collect it before
committing the empty receipt. Service units remain loaded for terminal evidence.

## Fixed operator profile

The only CLI profile path is `/etc/gascity/remote-admission/profile.json`; it is
absent by default. It and state files must be root-owned, private, regular, single
linked and opened nofollow. Ancestors must be root-owned and not writable by group
or others. The work mount target must already exist as a root-owned 0700 directory with
root-owned non-writable ancestors; the engine never provisions or repairs it.
Model/transport home ancestors must also be root-owned; the separate
UID-owned homes are private and cannot contain one another or the root state/work
paths. Source tests inject their own unprivileged owner and fake systemd host;
there is no CLI production bypass.

Every field is required; no numeric example is an approved resource policy:

| Fields | Meaning |
| --- | --- |
| schema_version, profile_id, city, template | Version 1 and exact operator scope |
| max_consumed_attempts | Explicit positive integer lifetime consumed-attempt budget; pilot 1 |
| binding | selector, endpoint, resource_domain, policy_digest; exact request match |
| source_sha, manifest_sha256 | Git commit 40 lowercase hex and manifest 64 lowercase hex |
| transport_uid, model_uid, model_gid | Explicit separate nonzero UIDs and model GID |
| unit_prefix, cgroup_parent | Unit `<prefix>-<32hex attempt>.service`, direct child of dedicated slice |
| state_dir, helper_path, helper_sha256 | Root-only journal/prompt/output path and immutable reviewed release pin |
| model_argv, model_exe_sha256 | Fixed absolute executable and full argv, separate binary SHA256 |
| model_home, transport_home, working_directory | Separate absolute private paths; model HOME is persistent, work ephemeral |
| environment, environment_keys | Fixed HOME/PATH/CODEX_HOME/LANG/LC_ALL/SHELL only; staged overrides limited to LANG/LC_ALL |
| resources | cpu_quota_percent, memory_max_bytes, memory_swap_max_bytes, tasks_max, runtime_max_seconds, output_max_bytes, tmp_max_bytes, work_max_bytes |
| thresholds | min_mem_available_bytes, max_memory_psi_full_avg10, max_io_psi_full_avg10, min_disk_available_bytes, min_free_inodes |
| disk_path | Filesystem (must match rootstate/output device) whose free byte/inode reserve is checked; byte reserve additionally covers one maximum output |
| home_mount | source, device_major, device_minor, max_bytes, backing_file, backing_device, backing_inode |

Persistent mutable OAuth state stays in the model HOME. The operator must prepare
a dedicated bounded ext4 loop filesystem, preserving actual authentication through
its own reviewed migration; the helper neither copies nor reads auth. It verifies
exact mounted source/device, root-owned 0600 fully allocated backing-file identity/size, kernel
loop backing path and filesystem maximum bytes. Sparse backing files are refused;
preallocation and the backing filesystem allocation guarantees are operator gates. HOME is writable inside the unit,
but cannot grow its backing image. PrivateDevices=yes and PrivateIPC=yes isolate host devices and IPC; /dev/shm is
a separate size-capped tmpfs. Its device and IPC namespace must differ from PID1.
Work, /tmp and /var/tmp are separate size-capped
tmpfs mounts charged to the model cgroup. LimitFSIZE bounds each output/file; no
model-writable unbounded persistent path is allowed. Rootstate is private 0700,
so its explicit sandbox write allowance serves only the privileged hooks. Retained
outputs/journal require an operator retention policy; the engine never deletes.

Before acceptance the operator must prove the model UID has no SSH login, sudo,
user manager/linger, transport socket/home/key access or cgroup delegation, and no
other execution channel escaping this service. ProtectSystem=strict, ProtectHome=yes,
Delegate=no, NNP and zero capabilities are mandatory. Place HOME outside hidden
/home, /root and /run/user. Install only a separately reviewed immutable root-owned
release and wrapper; never execute development sources as root. Install no generic
sudo COMMAND, caller environment preservation or dynamic profile argument.

## Wire protocol

Strict UTF-8 JSON, maximum 262144 bytes, one object, no duplicate/unknown keys or
nonfinite values. No shell framing. Control request:

```json
{"schema_version":1,"op":"prepare","request":{"city":"city","session_id":"sid","template":"role","runtime_handle":"runtime","generation":"1","instance_token":"private-incarnation","attempt":"11111111111111111111111111111111","binding":{"selector":"ssh","endpoint":"operator-endpoint","resource_domain":"operator-domain","policy_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
```

`stage_prompt` adds exactly `prompt` (literal string) and `environment` (allowlisted
map). It returns the same prepared phase with a newer durable revision; it does not
claim VM entry. `observe` and `cancel` use the same exact request tuple. Generation
is canonical decimal text. The instance token is private control data, not bearer
command authority. All responses have exactly:

```text
{schema_version:1, receipt:{request,authority_receipt_id,journal_revision,phase,
 identity:{boot_id,unit,invocation_id,cgroup_path,cgroup_device,cgroup_inode,
 source_sha,manifest_sha256,model_uid,transport_uid}},
 scope_empty:boolean,never_entered:boolean,scope_identity:{boot_id,device,inode,cgroup_path}}
```

Boot and Invocation IDs are 32 lowercase hex. Prepared identity has observed boot
and profile pins with empty/zero unit fields. It is reservation evidence only.
Entered identity requires complete fresh kernel proof. `scope_empty` is explicit;
never infer it from phase. Pre-entry cancel can have no-start proof and blank unit
identity; explicit never_entered=true is allowed only after durable revocation
before any consumption and fresh exact parent empty proof. Consumed/unknown attempts
never qualify. The SDK validates this separate no-start branch; it must not
masquerade as a full entered/exited child kernel receipt.
Response loss, startup races or unknown remain occupied until root reconciliation.
Errors are content-free `{schema_version:1,error:"refused"}` with nonzero exit.

Early startup observation may read an empty parent before systemd reports
activating/start-pre. The pending path reconciles that earlier view with its
existing second systemd/child-cgroup read and fresh recursive parent scope. It
returns only the final nonempty scope, with unchanged boot, parent, invocation,
and child kernel pins. Final-empty or changed snapshots refuse without polling
or granting entry. A pending seat remains consumed and unattested (public phase
unknown); queue access still requires the complete entered proof. This does not
change the existing handling of previously unknown rows or release any retained
fixture authority. These reads are sequential, not an atomic OS snapshot: the
cached diagnostic timestamp belongs to the systemd read, while scope_empty may
reflect the later final scope read.

`queue-observe` is a separate fixed root-only read API. Input is exactly
`{schema_version:1,profile_id,peer:{pid,uid,gid,start,cgroup}}`, optionally plus
`expected_identity`. It derives the current attempt from its single root ledger,
never caller-selected SID. Fresh entered proof, same profile digest and exact
kernel peer PID/start/UID/GID/cgroup are required. The first proxy call has no
expected identity; the controller includes the previously obtained exact identity.
Output is the private relay envelope `{schema_version,profile_id,identity,observe}`;
identity includes sid/template/runtime_handle/generation/instance_token/attempt.
Observe includes receipt_id/revision/boot_id/unit/cgroup/invocation_id/model_uid,
revision is canonical positive decimal text (control journal_revision is integer).
It also includes peer pid/start, phase=entered, source_sha/manifest_sha and RFC3339 UTC observed_at.
It travels only through the root-private relay/FD protocol, never model env/logs.

## Evidence and deployment boundary

Tests use fake systemd/process evidence and real unprivileged private-file locking.
Root separately tested harmless immutable Python/id fixtures on vm328699: service
cgroup removal despite RemainAfterExit, persistent slice inode and populated 0->1->0,
root600 stdin, UID988/GID982, privileged root Pre/Post hooks, strict sandbox,
NNP/caps0, bounded tmpfs and file limit. Root-private receipts are under
`/var/lib/gc-repairer-fixtures/{systemd-20261020T185416Z,slice-20261020T185618Z,sandbox-20261020T190707Z}/receipt.json`.
These prove OS mechanics, not acceptance of a production profile, home mount,
model invocation, queue RPC or installed release. Those remain separate gates.


## Private refusal diagnostics

After a profile and its private state directory validate, the helper may create
three derived evidence files there: `diagnostic-unit-pre.json`,
`diagnostic-unit-post.json` and `diagnostic-first-unknown.json`. Each is at most
1024 bytes, root0600 in production, created once with no-follow/exclusive semantics;
existing or partial evidence is retained without overwrite, rotation or fallback.
Only static refusal codes, generic exception categories, monotonic ordering,
process ID and sanitized already-read native state enums/booleans are recorded.
No request, attempt/unit name, token, prompt, environment or exception text is logged.

The first unknown-transition record is attempted only after the original ledger
save. Diagnostic failure cannot change admission, replace its original error or
relax an occupied hold. Public RPC/schema and generic refused output are unchanged;
profile-load/state-validation failures have no trusted diagnostic destination.
These files do not backfill historical failures (including fixture003) or prove
entry, termination, release or queue acceptance.
