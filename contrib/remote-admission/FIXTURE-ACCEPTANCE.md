# Isolated no-model acceptance fixture — issue208

Preparation only. Root alone publishes immutable code, creates fixture credentials,
mounts, units and daemons after frozen review. No production profile is installed; root may later publish only the separately reviewed real temporary NO-MODEL fixture profiles.
No model, production city/controller, Dolt connection, database copy or global lease
state is part of this fixture. Source seeding is not acceptance.

## Frozen boundaries

- SDK preparation base8170440b974647fb9de43e89a51f5587301c9c38 (includes verified drain readback484c69ea); final executable MUST instead
  include the reviewed actual operator backend/Hold merge before execution.
- Guard helper f1f3d6fdc03c53eba4121afc879bec83fb69f1d7,
  SHA256 d4437c462e72500f5cbd060d420c8bde593de61962f7ea92b4281e113491d716.
  Root profile requires max_consumed_attempts=1. Exactly one benign actor cycle.
- Actual gt-bd b6e5391e86cce449f848b533ea2e3a6eee159f6f,
  SHA256 f4a9bccb3a81e75a2b741d9fdd8ed03fbbf7a938e04cd8a02e099dccb978dbb6.
  Non-executable transport input only: /srv/gc-build/logs/queue-fixture-gt-bd.input.
  Root publishes an exact copy; never a mock lease. Named fixture-only city HOME
  contains .gt-bd/.authority; no global authority marker or VPS lease access.
- City UID990/GID983; transport repairer-transport UID987/GID981; benign actor
  repairer UID988/GID982. Root profiles and immutable helpers remain root-owned.
- Proposed fixture namespace root: /var/lib/gc-repairer-fixtures/queue-208-001.
  Root must verify ABSENT before creation; existing paths refuse, never overwrite.
  ROOT itself stays root-owned0755. Root creates only ROOT/city-state as UID990
  mode0700; the unprivileged seeder creates ABSENT ROOT/city-state/city beneath it.
  Every profile/control City uses this SAME absolute nested path. Never chown ROOT
  or open the fixture database as root.
  This exact path/port and resource values require frozen root manifest review.

## Unprivileged preparation

Build fixtureseed and the FINAL integrated gc only in the approved shared build
unit/lock after root clears the current resource gate. Required normal hooks must
pass before source publication. Never execute a writable checkout as root.

fixtureseed ABSOLUTE-REVIEWED-GC ABSENT-ABSOLUTE-CITY first writes only the fresh
city config, then executes that exact final SDK binary `gc storage migrate
--from-work --fleet-stopped` in the named empty city BEFORE any canonical Create. Failure retains
the named directory and stops: no handmade convergence marker or populated-target
overwrite. Only after successful canonical convergence does it create SQLite infra
via canonical APIs. This minimal city does not import builtin core; its warning
is retained as a fixture limitation, not repaired with doctor --fix. This is not
formula/pack/dispatch acceptance.
It derives the actual component using production sqlite.GraphPath and writes
a workflow root and one plain routed task with gc.root_bead_id and no
gc.kind: the task is graph-class while still subject to the canonical work-record
close gate. It creates an active ephemeral session with private incarnation and
immutable runtime binding. Its PolicyDigest is only an OPAQUE stable fixture
match tag, never the hash of the complete resource policy or admission proof.
Root/helper enforce the full real Profile.digest/resources plus source/manifest;
root verifies that complete authority separately. This fixture does not claim
SDK StartRPC or next wake. A full backend pilot must use a root-owned canonical
policy-derived digest BEFORE first pin; never mutate this fixture pin to pretend
that later policy. It leaves task open/unassigned and current claim empty.
The default query stays unchanged; no fake echo ready result or preclaim.

Its public receipt contains IDs/binding only. fixture-private.json is0600 and
contains a synthetic private instance token/attempt for root control-frame input.
Never cat it, put its token in argv/env/model prompt, or include it in the report.
Partial failed seed state is retained; use another named root, never clean/delete.

## Root-only immutable/namespace preflight

1. Freeze exact release files/hashes, final SDK SHA and binary hash, source manifest,
   parser/driver/seed review, resource budget and this runbook. Publish into a fresh
   root-owned tree with no writable/symlink/hardlink ancestors. No development imports.
2. Reuse the ALREADY provisioned/attested bounded actor HOME
   /srv/gc-repairer/worker (current2GiB loop4 image); independently recheck actual
   loop source/device/inode/allocation/size. No auth migration/copy, new HOME image
   or second provisioning transaction. The benign immutable Python actor and
   queue_client never invoke Codex or read OAuth. An exact-attempt model unit
   drop-in MUST set InaccessiblePaths=/srv/gc-repairer/worker/.codex. The '+'
   prefix grants hook privileges, not a blanket filesystem-namespace exemption.
   Actual +ExecStartPre sees the bounded model-owned work tmpfs; the trusted
   unit-hook validates it separately from the root-owned host target.
   Attest actual namespace views; do not infer Post-hook behavior from this probe.
   Prove actual UID988 model namespace denial with os.access/stat ONLY, never
   auth bytes/hash; no assumption
   from unit text, no fallback when denial is unproved.
   Prepare separate named root state/work paths and explicitly fresh ledger
   schema1/currentnull/empty attempts. Persistent exclusive cap1 parent remains
   loaded until exact terminal/same-parent empty proof.
3. Create only fixture SSH host/client keys through the authorized root credential
   workflow, private0600 outside git/logs; record public fingerprints only. Never use
   production keys or change host sshd/authorized_keys. Control credential is
   required by backend config but no root/control login is enabled in this smoke.
4. Confirm127.0.0.1:22208 unoccupied. A dedicated root sshd listens there only,
   uses its own host key/config/authorized_keys, AllowUsers repairer-transport,
   PasswordAuthentication no, KbdInteractiveAuthentication no, UsePAM yes,
   PermitRootLogin no, PermitUserEnvironment no, PermitUserRC no, DisableForwarding
   yes, PermitTTY no, X11Forwarding no. No inherited SetEnv or user RC.
   Existing transport account is password-locked but not expired; do not unlock
   or alter host account policy. Root-approved alternative is UsePAM yes ONLY in
   this own sshd unit with readonly private bind of immutable fixture_sshd.pam at
   /etc/pam.d/sshd. Its complete stack is auth pam_deny, account pam_unix,
   session pam_permit, password pam_deny; no include, pam_env/systemd/motd or host
   PAM mutation. Before authentication root proves real sshd/auth-child namespace
   sees exact private file inode/content; host PAM hash unchanged. The -f config
   has NO Include and authentication is publickey ONLY (AuthenticationMethods
   publickey plus all other authentication disabled). Account expiry remains
   enforced. pam_deny setcred behavior must pass a bounded actual auth test on
   this OpenSSH/PAM version; no assumed login PASS. Any read/PAM/expired/session
   failure => STOP, never authpermit/hostconfig/account relaxation. Afterward
   transport user-manager remains inactive and account metadata unchanged.
5. Inside only the sshd unit filesystem namespace, bind a fresh empty transport HOME over the
   existing account HOME, so shell startup cannot read production .bashrc. The
   authorized key fixed ForceCommand is versioned Python -I -B plus immutable
   fixture_metadata.py plus exact private tmux.sock, rootprotected stage root and
   immutable fixture-metadata-carrier.json. The trace is generated by REAL Go
   Provider.SetMeta and tested for drift; no hand-written alternate protocol.
   The interpreter compares the ENTIRE sh-on-stdin script as DATA, allowing only
   native random heredoc tags and3typed drain values. It never executes shell,
   find, mktemp, rm or a tmux source/config file. The native source-file request
   names only a validated fixed slot; both files are checked and the known single
   assignment becomes actual tmux metadata argv on EXACTSOCKET. Direct writes
   outside this native staged carrier refuse. No generic shell/metadata/source-file,
   socket/session selection or forwarding. Root creates only that tmux session asUID987 with a benign bounded
   process. Before enabling the SSH key, root pins EVERY socket ancestor to root
   ownership/no group-or-other write; socket parent root:9810750 permits transport
   traversal without rename. The socket leaf remains UID987-owned. The forced
   parser refuses ANY UID987-owned ancestor; no auto-restart/repair if socket is lost.
   No common/default socket is touched.
6. Root journals the exact frozen named NO-MODEL transaction BEFORE publication.
   Require all fixed profile destinations ABSENT; publish new root-owned files with
   O_EXCL/no symlink/no hardlink/no overwrite:
   /etc/gascity/remote-runtime-profiles.json,
   /etc/gascity/remote-work-policy.json,
   /etc/gascity/remote-work-client.json,
   /etc/gascity/remote-admission/profile.json.
   These are REAL temporary host fixture profiles, readable by real production
   helper Pre/Post hooks. Exact host-profile publication remains the required
   authority contract; the earlier private-mount workaround remains rejected.
   Verify actual hook access to those exact host-published profiles rather than
   assuming that '+' exempts filesystem namespaces.
   No namespace/nsenter/profile-alternate flag substitutes for real authority.
7. Freeze every UID988 launcher for the entire transaction. Keep normal controller
   admissions held; no keys/sudo/normal role activation beyond reviewed fixture-only
   loopback metadata key. Exact fixture SID/template/real SSH binding/source/manifest
   and benign Python argv+binary/script pins are the only authority. No Codex,
   model executable, OAuth read, arbitrary shell/metadata or broad root control.
   Profile max_consumed_attempts=1, fresh exact root ledger/state, retained exclusive
   cap1 parent, bounded private fixture HOME/work and strict guard sandbox.
8. Unknown/occupied state retains ALL real fixture profiles and private history for
   reconciliation while normal admissions stay held. After exact terminal identity,
   same-parent recursive empty proof and consumed count1, root may archive ONLY
   the recorded profile inodes to dated named archive (no overwrites/deletion).
   Retain ledger/output/stages; no reset, TTL release or inferred model permission.


## Fixed staging slots and actual sudo entry

Root preallocates3 root:9810750 directories beneath a rootprotected stage root:
gc-session-ack, gc-session-source, gc-session-digest. Each contains exactly
env.sh and session.tmux, empty regular single-linked UID9870600. Root also creates
one stage.lock UID9870600 under the root-owned stage root. No actor can rename a
slot/path; only the3typed writes happen, once per slot under the lock. Files are
bounded4096bytes each; a partial write stays occupied and is never overwritten.
There is no dynamic temp directory, orphan sweep or retention cleanup.

The native rm-rf cleanup request is interpreted only as a denied retention
request for an EXACT known slot, NEVER executed. Provider cleanup is best-effort;
its nonzero result is not represented as successful deletion. Root retains
all slots until terminal+sameparentempty+noUID987writers, then archives only
the recorded exact slot inodes with an explicit source/destination map.

Root publishes a reviewed immutable entry wrapper with exact argv shape
enter FIXTURE32HEXATTEMPT. It invokes only the immutable real helper, preserving
the sudo-provided validated SUDO_UID=987; no generic Python/shell/environment
preservation. Root validates a new single exact sudoers command using visudo before
O_EXCL publication: transport987 -> root -> FIXEDENTRY enter EXACTATTEMPT.
No wildcard/argument variant/SETENV/generic sudo. UID988 receives no sudo.

Root actually invokes the UID987 -> sudo -n -> fixedentry chain. Do not substitute
a root-spoofed SUDO_UID or claim that manual root RPC proves this boundary.
After terminal+sameparentempty, archive the EXACT recorded sudoers inode FIRST,
then real fixture profile inodes. Unknown state retains policy/ledger for reviewed
reconciliation while normal admissions remain held. Root alone performs effects.

## Root relay and real store preflight

Public work policy exact parent_argv:
 /usr/bin/python3.12 -I IMMUTABLERELEASE/controller_relay.py --config FIXTURE/intake.json
Pin that script/hash, real gc binary and actual gt-bd. Anonymous FD3 is opened by
real root intake; intake drops groups/GID/UID to990 before SDK exec. Readability of
actual parent status/stat/cmdline and private root pipe must be proven on this VM;
never weaken checks for hidepid or substitute a file/fake parent.

Proxy invokes actual immutable helper queue-observe; local fixed argv invokes
actual immutable intake, which queries helper again. There is no SSH queue relay
in this smoke; only runtime metadata uses real loopback strictSSH. Existing key
path checks for these local root-to-root calls use explicit root0600 inert fixture
sentinel files, never accepted SSH credentials. No model caller chooses SID,
attempt, store, city, profile, command or key.

Socket parent root0750:modelGID982; socket0660 root:982. Root journals0700; SDK
journal cityUID9900700. Client fixed public config contains only socket/timeout.
Actor PATH resolves gc to immutable queue_client shim. Intake city PATH resolves
gc to real SDK plus jq; no shim recursion. Native lease HOME/state is fixture-only.

Before actor entry run real gc ready --metadata-field gc.routed_to=queue-fixture-worker
--unassigned --json as cityUID990 and verify exactly the seeded child, and no claim.
No controller starts. Verify direct nonroot remote-work with ordinary input/FD3
and wrongUID socket requests refuse before mutation. Preserve parent root request
lock throughout UID-drop/FD remap and verify descriptor identity afterward.

## One real cycle

Root alone submits actual helper prepare then stage_prompt with the exact literal:
 "isolated queue fixture: execute exactly one no-op lifecycle\n"
and empty staged env, then invokes actual transport987 sudo entry for its public attempt. No model argv/shell/prompt
suffix is supplied by caller. Root model_argv uses actual Python3.12 -I -B plus
immutable fixture_driver.py and the fixed immutable model gc shim path.

The driver first blocks SIGUSR1 and waits at most30seconds before any queue
request. Root waits for the real positive entered receipt, verifies the attested
MainPID/start/UID/cgroup and SIGUSR1 bit in SigBlk, opens a pidfd, rechecks identity,
then uses pidfd_send_signal(SIGUSR1) on only this own fixture process. The driver
requires sender UID0. No arbitrary process signal, ready-status file, timing sleep
or queue retry substitutes for entry proof. Missing/foreign signal makes fixture
fail and retain journals.

The UID988 Python actor invokes queue_client, without retries:
 claim -> current -> by-ID read -> no-op result -> atomic close -> idle -> drain.
Each operation has a stable named request ID; driver refuses ambiguous failure.
The driver JSON is public and limited. It is NOT enough to accept the fixture.

Before any queue request, fixture_driver asserts all R/W/X access checks on the
fixed .codex directory fail and stat of its known auth.json raises PermissionError.
No open/read/hash occurs. Missing/unknown denial fails closed before entry wait or
queue. Successful driver receipt includes oauth_denied=true. This does not grant
model execution or credentials access.

Root independently verifies:
- real SO_PEERCRED UID/PID/start/cgroup and matching actual entered guard receipt;
- real root parent+anonymous FD3, UID/GID/group drop, unchanged root lock;
- SQLite canonical current claim, assignee, route, revision and result/close receipt
  in one conditional close; no work in the empty file ledger and no Dolt process;
- native lease claim/status/release against actual isolated gt-bd;
- strictSSH server authentication/pinned host and exact tmux drain metadata readback:
  GC_DRAIN_ACK=1, GC_DRAIN_ACK_SOURCE=agent, requester digest equals SHA256 of the
  private incarnation (compare privately, report only boolean).
  Native SSH SetMeta historically ignores transport errors, so CLI success alone
  is NOT metadata proof. An SSH or parser refusal makes this gate fail.
- driver exited, exact service terminal identity, persistent slice recursively empty,
  actual guard observe reports scope_empty, and budget consumed count remains1.
- only journaled fixture profile inodes created/archived; hostsshd/common tmux/other services unchanged.

Wrong identity/changed request replay and uncertain-response scenarios must use
separately named bounded fixtures after this single cycle; do not consume a second
attempt or claim full fault acceptance here. No controller StartRPC, remote-city
activation, real inference, business repair or next wake is proved by this smoke.

## Retention

Keep every fixture DB/private journal/output for reviewed evidence, with private
permissions. Reports contain public IDs/hashes/boolean gates only. No automatic
cleanup, recursive deletion, stop-all, tmux kill-server, TTL release or key reuse.
Root may stop only exact fixture unit/session/sshd after terminal/empty proof.
