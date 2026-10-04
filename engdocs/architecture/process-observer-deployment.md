# Process observer: reviewed operator deployment and restart procedure

This is a proposed coordinated #27 installation procedure, requiring explicit
owner approval after immutable source/binary/unit/config review. It has not been
executed. The observer helper alone would receive CAP_SYS_PTRACE and
CAP_DAC_READ_SEARCH. The controller, CLI and ordinary workers receive neither.
Source development and fixture tests do not authorize installation or starts.

## Exact proposed host layout

04 Oct 2026 10:27Z readback: VPS controller account dev UID/GID1000; fixed user
unit gascity-supervisor.service; old MainPID435977; controller PID namespace
pid:[4026531836]. UID/GID62027 and account gascity-observer were absent. The
proposed dedicated static UID/GID62027 must be checked free again before approved
creation. It is not an existing or reserved host identity. Numeric service
User/Group62027 avoid name substitution. Socket group dev is the verified group
for UID1000. Do not reuse an unrelated account, broad UID match or dynamic user.

- Controller release binary: exact installed release path from its reviewed
  manifest; source SHA and binary SHA must match the independently built pair.
- Build gc with the full40hex `COMMIT` override and a clean tree; the default
  short build revision deliberately cannot satisfy the strict deployment pins.
  Build the helper with the same full `SOURCE_REVISION`; no provisional macro.
- Helper: /usr/local/libexec/gc-process-observer-helper root:root0755. No writable
  parent, executable or policy; ELF static Linux x86_64, no fixture macro.
- Fixed root-only deployment manifest: /etc/gascity-observer/release.json,
  root:root0600; exact keys documented by rebind.py, source40hex/binaries64hex,
  dev/UID1000/GID1000, helperUID/GID62027, policy digest and approved namespace.
- /etc/gascity-observer root:root0755. binding.conf root:62027 0640;
  client.json root:dev0640. Both files non-symlink/regular, all ancestors
  root-owned without group/other write. Only root publishes them.
- /run/gascity-observer root:dev0750, created by reviewed tmpfiles entry.
  Socket root:dev0660. Its parent must already have the reviewed group;
  systemd DirectoryMode alone does not establish group ownership.
- Fixed socket/service/tmpfiles bytes are the checked-in *.example proposals;
  publish their final hash-pinned rendered bytes only after approval.
- rebind.py installed root:root0755 below /usr/local/libexec, root-owned parents.
  It has no process-start/restart or signal operation. The only subprocess is
  the fixed runuser/dev systemctl show MainPID of gascity-supervisor.service.

## Backups and release checks before writes

The release owner supplies an immutable artifact manifest containing final
source/archive/patch hashes, Linux gc/helper hashes, policy digest, unit/script
hashes, toolchain receipts, unchanged module hashes and exact compatible Cloud
consumer revision. Earlier a3/2d source custody does not cover this package.

Create a root-owned recovery directory named with the release and UTC time.
Save the actual old controller executable and every PATH shim/symlink target,
controller user unit/drop-ins, Cloud observer argv/config/consumer files,
helper executable/units/tmpfiles/config/binding/client files if present. Record
SHA256, owner/group/mode, absent paths and symlink targets; read each copied file
back and compare. Missing old artifacts are recorded as absent, never invented.
Store backup receipt alongside the manifest. Run schema/store smoke only on an
isolated store copy under VPS-INFRA-03. Obtain exact-source independent review
and the required publication gate decision before installation. Do not bypass
failed repository hooks or change dependency/schema policy for this release.

## Approved one-time install/restart/rebind sequence

1. Recheck host boot, initial PID/user namespace and procfs/LSM visibility,
   static UID/GID availability, binary hashes and all old managed-start holds.
   Create the approved separate system identity only if still absent. Publish
   exact root-owned helper, manifest and proposed socket/service/tmpfiles files.
   Run systemd-tmpfiles for the one approved entry and verify directory ownership.
   Reload system service definitions and enable/start only the observation
   socket after explicit approval. This does not start a managed worker.
2. The authorized external operator installs the approved gc binary through the
   existing release mechanism and restarts the fixed supervisor user unit once.
   Do not stop the fleet as a side effect or remove holds. Initially strict
   observation is UNKNOWN: old PID/start/boot policy does not authorize the new
   persistent instance. CLI cannot rebind itself.
3. Invoke the installed root-owned rebind.py **without --publish**. It reads the
   fixed root manifest, resolves MainPID through the exact dev user unit, reads
   actual PID UID/start/boot/executable hash/namespace, checks helper hash, and
   repeats PID/identity checks. It returns hashes and the proposed caller binding;
   no environment is read, no deployment files or services are changed.
4. Compare this result with the independently reviewed binary/host release
   readback. After the already-approved one-time bind step, invoke rebind.py
   --publish. It atomically replaces binding.conf first and client.json second,
   fsyncs each file and directory, reads exact bytes back, then rechecks the unit
   PID and identity. A mixed-generation interval refuses helper evidence.
   A changed process causes failure, preserving UNKNOWN until reviewed again.
5. The daemon retries absent/stale startup policy and latches it only after
   exact self-PID/UID/start/executable verification. It never writes policy and
   does not reload a verified latch per tick. The relay separately reads the
   fixed root policy to verify kernel socket peer PID/UID and actual start,
   boot/executable hash before/after the reply. Policy/reply pins must agree.
   Thus an already-latched old daemon cannot silently follow a new binding.
6. Verify actual helper capabilities/syscall filter/hard10s service deadline,
   mount/LSM host coverage and exact wire provenance. Two different CLI PIDs
   must obtain complete observations from the same persistent daemon PID,
   unchanged policy hashes and original evidence times. Exercise busy, timeout,
   disconnect, extra bytes, overflow, source/city/pin mismatch and provider swap:
   every case remains UNKNOWN and does not launch/drain/reset/admit work.
7. Only after whole-package readback and backup proof update the compatible
   Cloud observer argv to gc pool-admission-probe --per-session --via-controller.
   Reset the contract epoch. Start a fresh effective48h/500-tick PhaseA; do not
   reuse historical observations or shorten it to the minimum24h/200 ticks.
   PhaseB/manual acceptance and permission for managed starts remain separate.

## Restart and rollback

A later approved restart/reboot changes PID/start/boot and intentionally denies
old binding. Repeat fixed-unit dry-run and the approved one-time root publication;
never refresh root files every tick or authorize all processes of UID1000.

For rollback, the external release operator disables/stops only the observation
socket, restores exact old gc binary/PATH shims and controller user unit from the
verified backup and restarts the same supervisor through the approved procedure.
Restore backed-up Cloud observer argv/config/consumer files and existing helper
files/units/binding/policy with their recorded ownership/modes. Remove only paths
recorded absent before installation. Reload only the relevant definitions. Read
back all restored hashes and the actual supervisor identity. Keep managed-start
holds; absence of the new strict interface remains UNKNOWN, with no local scan
fallback. Privilege/account removal is a separately approved host operation.
No worker process or foreign agent is killed for observation rollback.

This procedure is reviewable source. Production backup, rendered final manifest,
privilege/install acceptance and restart/live readbacks are still unperformed.

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
