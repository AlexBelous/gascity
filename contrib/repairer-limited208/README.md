# Limited issue208 native890 coordinator

Source only. No installation, queue dispatch, actor, model or activation is
represented by this source delivery. Issue: AlexBelous/gascity-kb#208.

The native890 cloud command-agent owns the canonical fixture. Its model and fixed
root-authorized fixture action execute only on vm328699. No new Go SDK or
controller replacement is required. Sessions remain in the live infra store;
work stays in canonical default work/Dolt. Do not seed session IDs or invent a
remote RuntimeBinding. Do not activate the old local repairer-pilot.

## Installation contract

Install the reviewed Python source at cloud
`tools/repairer-limited208/coordinator.py`; install the separate suspended agent
`repairer-coordinator` at city-root agents/repairer-coordinator/agent.toml (never under an imported core pack). Inspect resolved configuration: native
default work query, no inherited idle/max-age timeout, actual python3 process
hint, max active1, native capacity admission. There is no custom work_query.
Create the exact fixture deferred (not Ready), install the immutable accepted
profile pinned to that ID, then publish its native private route and create/wake
the native session. Root reads accepted and publishes the VM witness during the
120s pending-profile wait. The accepted activation publishes exactly one private
fixture route and no business work to this template. Root is the sole publisher for this new role;
preflight proves its own/assigned/routed default-query tiers contain exactly that
fixture. After close, no further Ready work may exist for the role. Concurrent
publishing to the role invalidates this finite activation contract. A native claim for any other ID is held.

The real owner comes from native890 session creation/pool dispatch. For manual
creation the existing command is `gc session new repairer-coordinator --alias
repairer-coordinator --title "Repairer VM limited pilot" --no-attach --json`.
Creation while suspended is not runtime acceptance. Do not create a second
manual owner alongside demand-driven dispatch.

Root/coordinator installer supplies a private mode0600 JSON profile at
`.gc/runtime/repairer-limited208/profile.json`, protected non-symlink ancestors,
and private mode0700 precreated state directory of that same name. Required keys:

- schema_version:1; fixture_id: exact newly created fixture ID.
- gc_binary: exact installed890 path pinned in coordinator.py.
- state_dir: exact `.gc/runtime/repairer-limited208` absolute path.
- artifact: {commit,branch,work_dir}; commit is the genuine reviewed source
  artifact that fulfilled this acceptance bead; branch must already contain it
  in the cloud work_dir repository. Root delivers that ref before closure.
- accepted_base: {executor_host,target_host,resource,scope,source_sha,sop_pins,
  next_checkpoint}. executor_host is vm328699; source_sha equals artifact.commit;
  sop_pins maps the two canonical infrastructure SOPs and role file absolute
  cloud paths to SHA256. These three files are read and checked before work.

The installer verifies profile/source/SOP digests, private ancestor ownership,
exact city stores and resolved template before activation. Runtime must inherit
real GC_SESSION_ID/GC_INSTANCE_TOKEN/GC_TEMPLATE/GC_CITY_PATH. A conflicting
CLAUDE_SESSION_ID or alternate gt-bd authority environment is refused. Native
session generation is real metadata, not work-bead metadata or attempt count.
No native token, cloud key or native environment is sent to the VM/model.

## One finite sequence

Immediately before RUN, fresh SOP pins, canonical work/session/incarnation,
exact accepted comment and hash metadata, and actual editor lease are rechecked.
Drift during readiness holds with no RUN. READY received at/after its fixed
monotonic deadline is refused.

The local durable O_EXCL/fsync once.json is consumed BEFORE claim. Existing,
partial or symlink receipt means hold forever; do not delete it to retry.
Read-only native `gc hook --json` discovery must yield exactly the configured
fixture before the first claim. Native hook claim/current must identify the configured fixture and canonical
assignee/gc.session_id. Actual gt-bd status must prove same nonempty SID, same
host, same lease ID and nonstale lease before every write; exit0 alone is not
proof. Claims and writes are separate subprocesses. Normal close performs its
ordinary hook-post release; this program never releases somebody else's lease.

Accepted is exact canonical JSON comment plus SHA metadata/readback, containing
actual native generation and incarnation hash with generation_source=native_session.
Root independently reads this canonical receipt and publishes the private VM
witness before ready. The model never writes its own accepted witness.

SSH uses only the frozen original commands `repairer-limited208 status` and
`repairer-limited208 run` with empty stdin, fixed transport user/key path,
strict ed25519 host pin, no agent/forwarding/multiplexed connection. Pending
profile may be polled read-only for120s. Exactly one RUN is issued; lost reply
allows only STATUS reconciliation for240s (root decision: model120s +
fixture natural90s +30s finite margin). Readiness remains120s; model runtime
and lifetime cap remain unchanged. RUN is never retried.

The public wire has exactly nine fields: schema_version,state,incident_id,
owner_session,generation,incarnation_sha256,accepted_comment_sha256,
evidence_sha256,summary. pending_profile has null identity/evidence and exact
incident ID. Other states must match actual accepted identity; only resolved
carries a SHA256 evidence digest. Unknown/malformed/mismatched/hold never closes.
Root supplies a fixed safe summary, never raw model output.

Resolved produces a verified result comment and genuine artifact metadata
`gc.work_outcome=shipped`, gc.work_commit, gc.work_branch and gc.work_dir.
GC_WORK_RECORD_ENFORCE=1 is passed to native890; no-op is not used to describe
fixture restart. Ordinary `gc bd close --reason-file -` enforces reachability.
After close the exact closed row/assignee/session stamp is checked without
requiring hook-current to survive closure; native drain/no_work or drain_pending hook and drain-ack
complete the local lifecycle, with a fresh same-incarnation check after idle.
Only post-close checks permit draining; stale_session remains hold. A Hold keeps the actual cloud coordinator alive;
operator reconciliation precedes stopping it. Root target/model fencing remains
independent of local editor and queue ownership.

## Factual compatibility references

Installed890 help supports gt-bd claim/status/check-lease, comment --stdin,
show --include-comments, update --set-metadata, close --reason-file -, native
hook/current and runtime drain-ack. Installed gt-bd lease_json emits bd/lease_id;
status leases emit bd/lease_id/session_id/holder_host/stale. No diagnostic
--session-id override is used. No gc bd work-record command exists.

Git object890 source:

- internal/session/lifecycle.go:35-48 emits actual GC_SESSION_ID/runtime epoch/token;
  internal/citylayout/runtime.go:193 emits GC_CITY_PATH;
  cmd/gc/template_resolve.go:303-310 emits GC_TEMPLATE.
- internal/config/agent_discovery.go:35-49 preserves root directory agent name;
  cmd/gc/agent_build_params.go:303-308 preserves ordinary template identity.
- cmd/gc/session_identity.go:55-60 stamps generation and instance_token.
- cmd/gc/cmd_session.go:342-422 native SID/store/reconciler creation.
- cmd/gc/cmd_hook_claim.go:949-958 emits work/claimed/bead_id/assignee;
  1240-1246 stamps work and session identity;1614-1638 verifies gc.session_id.
- cmd/gc/cmd_hook_current.go:91-106 reads raw session claim stamp.
- cmd/gc/session_affinity_metadata.go:37-58 and session_beads.go release/retire
  paths can clear that stamp; closed readback does not require it.
- internal/workrecord/gate.go:93-118 requires shipped commit/branch/reachability;
  GC_WORK_RECORD_ENFORCE is the existing enforcement switch.

## Verification

Pure tests use real temporary files, fake native CLI/transport and benign Python
child processes. No actual SSH/native queue/model calls. Tests run only on VM as
UID990 under approved V2 private /tmp,/var/tmp namespace and python-tests.lock:
CPU50%, Memory128M, Swap0, Tasks64, gc-build-208.slice. No Mac development/tests.

Initial RED evidence: coordinator-red-7d43d30d7c0a4a3880c3610acc919033.log.
V1 27 GREEN evidence: coordinator-freeze-09bd37fa084c4458bc86496603c3d5d6.log,
under `/srv/gc-build/logs/`; corresponding JSON records exact approved argv.
Three independent code/security/test reviews and actual native lifecycle/model/
fixture acceptance are still required. This test result is not activation.

Native890 non-claim discovery is not a typed no_work equivalent: cmd_hook.go
1113-1164 returns1 with normalized empty output for no work and also returns1
for query errors. It is used only for positive exact-fixture preflight. Terminal
--claim/no_work retains the explicit sole-publisher/empty-role installation gate;
unexpected work is held and never causes a second VM invocation.

R2 verification:36 tests GREEN0.407s and AST/JSON/TOML parse PASS in the same
approved lane. Evidence: `/srv/gc-build/logs/coordinator-r2-green-efdb5eafee8a4da59a20f2fece46cb52.log`.
R2 RED reproduced8 expected failures before fixes: coordinator-r2-red-0012ff8cd6c4450785221f2d20aa63ad.log.
Findings and dispositions: `/srv/gc-build/logs/repairer-limited208-coordinator-findings-v2.json`.

R3 root timing correction: synchronous RUN SSH timeout60s covers helper.start15s
plus entry observation15s and finite prechecks/SSH overhead. STATUS stays15s
(or the smaller remaining readiness budget). Overall reconciliation is still240s
measured before RUN; readiness120s, model120s, lifetime cap1 and no reRUN remain
unchanged. This does not introduce an asynchronous RUN protocol.
