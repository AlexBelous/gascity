# Bounded persistent HOME: operator source plan (issue 208)

This is a deployment preparation asset, not authorization to run development code
as root. Publish and independently review an immutable root-owned script release
first. Default action is dry-run; no apply was performed by the author.

Accepted operator paths for this pilot:

- Current/mounted home: `/srv/gc-repairer/worker` (model UID988/GID982, private0700).
- New fully allocated 2GiB ext4 image: `/srv/gc-repairer/volumes/worker-home-2g.ext4`.
- Root-only volumes directory: `/srv/gc-repairer/volumes` (0700).
- Retained original home: `/srv/gc-repairer/Archive/worker-home-<UTC>` under root0700 Archive.
- Temporary mount target: `/srv/gc-repairer/home-staging-<UTC>` under root-controlled ancestors.

The manifest must pin a root0600 operator freeze receipt and its SHA256, continuously
held through final profile acceptance. Its exact fields are schema_version=1,
model_uid, boot_id (32hex), controller_hold_reference, all_uid_launchers_held=true.
This is an explicit operator attestation after live readback, not independent
machine proof against an adversarial root actor. Root must verify all Native,
transport and manual launch paths are held, no model SSH keys/sudo/cron/linger/
user-manager/model units/runtime profile and no model UID processes. The engine
rechecks receipt identity/bytes/current boot and UID processes at every boundary;
profile absence alone does not establish continuous freeze. No receipt is invented
by this source author.

The manifest must explicitly pin these paths, UID/GID, exact image size, current
home and auth dev/inode, UTC generation, script source/file pins and reserve bytes.
The escaped mount unit name is derived with systemd-escape and must equal the
manifest; existing unit/drop-ins/image/staging/archive/partial receipt are blockers.
CI images and units are outside this procedure and remain untouched.

1. Freeze remote admission; the production authority profile must be absent or
   separately held. Prove no process has model UID in any real/effective/saved/fs
   credential, no active model user manager/linger, and account cannot SSH login.
   Inspect current home and regular single-linked private0600 `.codex/auth.json`
   metadata only. Do not print, hash or export authentication. Dry-run writes nothing.
2. Apply only an exact accepted manifest. Under a root-private flock persist and
   fsync an intent before any mutation. Require a fresh transaction: any previous
   receipt or unexplained partial object refuses, retaining all evidence. Provision
   new root0700 directories only; never chmod or repair unexpected existing paths.
3. Create image O_EXCL/root0600, fully allocate exactly2147483648 bytes, verify its
   dev/inode/size/allocated blocks, format that exact fresh file ext4, attach/mount
   with loop,nodev,nosuid at the unique staging path. Preserve all original HOME
   files/metadata, refuse special files/unsafe links or concurrent changes. Fsync
   copied files/directories. Compare auth bytes internally to source, no hash/value
   output. Recheck no model UID processes.
4. Before cutover record a durable intent. Same-filesystem rename original home to
   the unique retained archive (no overwrite); its private root0700 parent denies
   model access. Create an empty final mount target. Install a NEW exact .mount unit
   and wants link without overwriting existing units/drop-ins/links. Mount this
   exact image at the final home and verify mounted ext4 source/loop/backing-file
   identity, byte bounds, UID/GID/mode and internally equal native auth bytes.
5. Persist final root-private receipt before enabling future admission. Output only
   public filesystem/profile fields, never authentication bytes or hashes. Retain
   original home, image, unit, journal and any staging evidence. Any unknown/failure
   stops for explicit reconciliation; do not recreate, delete or blindly roll back.

The mount unit persists across reboot. Runtime root profile must pin fresh loop
major/minor plus root-owned backing dev/inode/size; boot and cgroup evidence must be
reconciled after reboot. A persistent mount is not automatic acceptance of stale
loop/boot pins. Device OAuth remains mutable in its original logical HOME path;
readonly auth binds and per-attempt copied snapshots are unsuitable.

The source helper additionally checks full backing allocation (no sparse image),
exact mountpoint/source/ext4/device, kernel loop backing path and filesystem maximum
bytes. It never creates/mounts/repairs a HOME itself. Runtime guard/unit fixtures and
final profile acceptance are separate from provisioning and run no model.

## Exact manifest fields and invocation

The generic engine takes only a root-owned0600 JSON manifest; model callers never
supply it. `base_dir` is the explicitly reviewed `/srv/<name>` root (pilot value
`/srv/gc-repairer`). CI roots and `/srv/gc-build` are rejected. `model_uid` and
`model_gid` are required nonzero integers; model_uid must differ from explicit
`transport_uid` and `build_uid` (pilot988/982 vs987/990). No other account is changed.

Required exact keys are:

```text
schema_version=1, base_dir, model_uid, model_gid, transport_uid, build_uid,
home, image, image_bytes=2147483648, archive, staging, state_dir, unit_name,
auth_relative=".codex/auth.json", home_device, home_inode, auth_device, auth_inode,
reserve_bytes, source_sha, script_sha256, freeze_receipt, freeze_receipt_sha256
```

Archive/staging/state paths share one UTC generation suffix. `unit_name` must equal
`systemd-escape --path --suffix=mount /srv/gc-repairer/worker`. Home/auth dev/inode
come from private read-only metadata inventory. Reserve bytes are an explicit
operator storage policy, not the latest free-space snapshot. source_sha/script_sha256
pin the independently reviewed immutable published script; never hash auth.
freeze_receipt points to `<base_dir>/operator-freeze.json` with exact root-approved
operator attestation fields above. Do not manufacture a receipt without live inventory.

After publication/review, root invokes the immutable file with isolated Python:

```text
/usr/bin/python3 -I -B <immutable-script> --manifest <root0600-manifest>
/usr/bin/python3 -I -B <immutable-script> --manifest <same-manifest> --apply
```

The first invocation is mandatory read-only plan. The second is a separate explicit
operator decision after plan acceptance. Root provisioning must run in the host
mount namespace, with privilege to create its exact loop filesystem and NEW mount
unit; do not apply a build-job PrivateTmp/ProtectSystem namespace that would hide
staging mounts. No root execution of gc-build-owned source. No models run during
provisioning. The script checks image allocation again after mkfs, after copy and
unmount, and after final mount. `mkfs.ext4 -E nodiscard,lazy_itable_init=0,lazy_journal_init=0`
preserves full backing allocation; operator trim/backup policies must preserve it.
Any unknown result retains all objects and its root-private receipt for manual
reconciliation. No automatic retry, expiry, rollback, image recreation or cleanup.
