# Remote work queue relay (source contract)

This is an explicit delegated-authority path, not a transparent remote `gc bd` shell. It is **inactive without reviewed immutable installation, root policies, pinned helpers/keys, resource admission and independent acceptance**. Ordinary `gc bd` local process authority is unchanged. No new city or copied database is needed.

## Components and trust

- VM `queue_proxy.py` runs as root, owns one Unix socket, authenticates SO_PEERCRED plus fresh PID/start/UID/cgroup from `/proc` and rootguard `queue-observe`. The model has only group connect permission, no SSH key or profile/state write access. Root-owned parent is0750 with model group; every ancestor is root-owned, non-writable by group/others and traversed without symlinks. Socket mode changes pin the socket inode before effects; socket0660 root:modelgroup. Existing socket/path refuses startup, never blindly unlinks it.
- Controller `controller_relay.py` is a pinned root-owned forced-command intake. It independently asks VM rootguard for fresh exact attempt/peer evidence, journals intent, and launches one pinned SDK receiver after dropping supplementary groups/GID/UID to the existing nonroot city user. It never opens a city DB as root.
- `gc remote-work --proof-fd 3` is a hidden Linux-only receiver. It accepts only an anonymous root-owned read pipe, an actual root PPID with exact pinned-helper argv/hash and a fixed root-owned public policy. CLI args, environment, arbitrary files and same-UID nonroot parents cannot create this context. The trusted root operator is the TCB; protection against malicious root is not claimed. No root `/proc/<pid>/exe` or private-fd visibility is assumed.
- Native hook/claim/current and existing by-ID handlers operate in canonical controller stores. Session/store/route/current-claim/assignee/incarnation are checked. Manual result/close writes use a stable verified principal with native gt-bd lease, revision CAS and readback. Close requires the atomic metadata-and-close capability and existing enforced work-record gate. The first profile allowlist is authoritative graph-class SQLite work with the existing sessions store. Ordinary Dolt work close is unsupported in this release. Unsupported stores refuse; no human/raw-bd fallback.

## Rootguard Observe binding

The fixed helper operation is `<pinned-absolute-python> -I <pinned-helper.py> queue-observe`, using the rootguard's fixed installed profile. Executable policy rejects symlinks: on the selected VM use the actual versioned `/usr/bin/python3.12`, not the `/usr/bin/python3` symlink or `/bin` alias. Exact `parent_argv` must match the actual interpreter invocation. The proxy sends `{schema_version:1,profile_id,peer:{pid,uid,gid,start,cgroup}}` derived from its actual kernel peer. Intake sends those fields plus `expected_identity`. Rootguard derives its single active attempt from its private journal; it does not select a session from model input or accept a claimed healthy boolean.

Private output has `schema_version:1,profile_id,identity:{sid,template,runtime_handle,generation,instance_token,attempt},observe:{receipt_id,revision,boot_id,unit,cgroup,invocation_id,model_uid,pid,start,phase,source_sha,manifest_sha,observed_at}`. Phase must be entered, identities/source pins exact, UTC RFC3339 evidence fresh (≤20seconds), cgroup exact in the first release. The controller queries again before delegation. Instance token travels only on root-private transport/FD3; it never enters argv, prompts, model JSON or logs.

Observe and relay commands are fixed argument arrays in root-private config, not caller commands. Pin the executable and every script/module via `command_pins`/`code_pins`. Dedicated root600 SSH keys and pinned known_hosts use forced commands, BatchMode, IdentitiesOnly, strict host checking, bounded connect time, no agent/port forwarding and no arbitrary remote command. No credentials live in these source files.

## Configuration surfaces (values supplied outside the SDK)

SDK fixed public path: `/etc/gascity/remote-work-policy.json`. Root owner, readable by city UID, no group/other write, no symlink/hardlink. Fields: schema_version=1, city_uid, helper_path, helper_sha256, exact parent_argv (Python interpreter/isolated flag/intake path/fixed config args), journal_dir, lease_command, lease_sha256 and profiles[]. Each profile has id, city_path, template, model_uid, runtime_provider, runtime_target, resource_domain, policy_digest, source_sha40 and manifest_sha64. No secrets in this public file. Journal directory is city-owned0700; file records0600.

Private proxy config fields: schema_version, profile_id, model_uid, model_gid, source_sha, manifest_sha, observe_command, relay_command, command_pins, code_pins, socket_path, ssh_key_path. Private intake config additionally uses observe_key_path, journal_dir(root700), max_requests(1..256), sdk_path, sdk_sha256, city_uid, city_gid, city_path, city_path_env and city_home, replacing socket/model_gid/relay/key fields. City PATH must consist only of reviewed read-only tool directories containing the accepted gc/bd tools; do not inherit caller env. Both private configs are root600 and all code/policy parents are protected. The selected helper/profile/binary must be immutable exact-source release, not a writable checkout.

Rootparent FD3 remap occurs only in the forked child: parent fd3 may hold the request journal lock. Supplementary groups are cleared and GID/UID dropped before SDK exec. The actual cloud process is a delegated intake child, not a fabricated remote PID. VM admission/service receipt remains an independent required authority.

## Model-side executable and hooks

Install reviewed root-owned `queue_client.py` and sibling `queue_proxy.py` read-only. A root-owned `gc` entrypoint can execute `/usr/bin/python3.12 -I -B <release>/queue_client.py gc "$@"`; an optional `bd` entrypoint uses `bd` instead. Add this immutable tool directory first in the VM model profile PATH. This directory contains no queue key or root-private state. The model's fixed public client config `/etc/gascity/remote-work-client.json` contains only schema_version=1, socket_path and timeout_seconds(1..60). Socket parent/owner and server SO_PEERCRED root are checked.

Exact supported command subset:

```sh
gc hook --claim --json
gc hook --claim --json --drain-ack
gc hook current --id-only
gc bd show <owned-bead-id> --json
gc remote-work result <owned-bead-id> --outcome no-op --text '<before/after, evidence and natural cycle>' --evidence-sha <sha256>
gc bd close <owned-bead-id> --reason '<verified result>' --evidence-sha <same-sha256>
gc runtime drain-ack
```

Result requires a canonical work outcome (`shipped|no-op|blocked|abandoned`); shipped still needs the existing canonical commit/branch record. There are no arbitrary update/metadata/assignee/ready/list/session/shell or store/city/profile selectors. `gc prime`, discovery-only hook, `--include-comments`, arbitrary file flags and ordinary development formulas are outside this subset. Deliver literal prompt/SOP and the specific supported hook profile; the normal provider hook suite is **not** automatically compatible. Callback-lane claim is refused. First model canary must prove the actual delivered PATH/entrypoint and prompt/hook calls, not a hand-run socket request.

The client generates a UUID request ID and never retries. An uncertain response prints only the request ID and preservation instruction. To reconcile, repeat the exact command with `gc --request-id <same-id> ...`; changed payload for an ID is refused. Combined `--drain-ack` performs explicit self-ack only after a proven idle result. A failed claim never becomes idle or drain success. Queue receipts are not proof of the business repair: the role still requires real control operation and next natural cycle.

## Failure and durability

All input is bounded64KiB, rejects duplicate/unknown fields, invalid numbers/UTF-8 and excessive depth. Socket frame reading has one monotonic5second deadline; command output/time is bounded. Timeout terminates only the newly created child process group. No other city/session is killed.

Root and SDK journals persist intent before effect, fsync files/parents, serialize per exact session/attempt scope and store request digest plus typed operation/ID. An uncertain or malformed completion holds **all new request IDs in that scope**; only the pending exact request can reconcile. A lost response cannot cause a new claim or second repair. Cached receipts require fresh authority. Root receipt validation occurs before completion is written. An unresolvable claim/drain intent stays hold for reviewed recovery, never auto replay or TTL release.

The first release has bounded retained request count and refuses when full; no automatic journal deletion. Close metadata and terminal state are one conditional transaction or refused. Existing P0/human gates, native locks, target repair lease, secret/retention rules and CI lifecycle remain required. Editor lease, target lease, resource permit, work claim and session heartbeat protect different things.

## Acceptance still required

Unit tests use fake process/root ports, real isolated journal files and in-memory canonical stores. They do not prove deployed root parent/FIFO inheritance, systemd/cgroup truth, key restrictions, published source pins, model hooks or real store lifecycle. Source delivery is not activation.

After independent review and immutable publication, a separately bounded offline process fixture must prove real root pipe→city receiver denial/acceptance, UID drop, unchanged parent lock and no ordinary caller authority. Then an isolated real SQLite-backed graph plus session fixture must exercise the actual queue client claim/result/canonical close/drain before any live profile. It must use fresh fixture work, not a deferred production bead. Then a named queue fixture must prove actual model startup→claim→read→result→atomic close→self drain→next wake in the existing stores, including partial/network/unknown and wrong-identity refusal. No production disk/service breakage, mutable key tests or arbitrary cleanup is part of source acceptance.
