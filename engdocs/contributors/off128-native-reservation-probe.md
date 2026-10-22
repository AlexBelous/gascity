# Manual pinned native reservation probe — issue128

Journal: https://github.com/AlexBelous/gascity-kb/issues/128

This manual evidence harness is excluded from ordinary Go ./... discovery. It
changes no production code or configuration and is not a new CLI or installer.
Run only in an isolated build-server fixture, never in a city checkout/database.
The fixed fixture paths intentionally refuse initial setup when its repo exists.
For an independent fresh replay use a new isolated workspace and consistently
substitute its root in all three files; preserve the original store/receipts.

Source under test: SDK8084c6f032b650a08d19c07f37e91f99d5a8c791 (PR50 tree),
actual native ELF from gastown-vps /usr/local/bin/bd, version1.3.0/source
`da08f2739`, SHA256
`9371d74153170dcd5046edb61dbce8924221ef623b8739f5b0c4cc1b885fac97`.
The installed PATH shim was not used or changed. Only this ELF was copied to
`/srv/gc-build/inputs/off128-bd-da08f2739-20261022`; no credentials or production
state were copied. Embedded Dolt requires no external server or dolt executable.

## Execution boundary

The observed fixture is
`/srv/gc-build/workspaces/off128-pinned-claim-20261022/{repo,home,bin}`.
Its bin/bd symlink resolves to the exact input ELF. Separate git repository,
explicit fixture actor, empty environment except listed tool settings, no remote.
All probes ran on vm328699 as UID990/GID983 under gc-build-208.slice, build.lock,
PrivateNetwork=yes, NoNewPrivileges=yes, ProtectHome=yes, ProtectSystem=strict;
only their own workspaces/cache were writable. Runtime240s/CPU100%/Memory2GiB/
Tasks128 for Python; Go wrapper Runtime300s/CPU200%/Memory4GiB/Tasks256. No Mac
build, test, lint or code-generation command was run.

Run sequence within those bounded systemd units:

1. `python3 prepare.py` creates the fresh fixture and runs native open/blocked/
   deferred create and exact-ID claim controls. All raw commands/results persist
   in fixture receipt.json. Every bd call is a separate process/reopen.
2. `python3 recovery.py` reuses only that fixture. It checks blocked reservation
   after route/assignee mutation, native ready positive/negative controls, removes
   exactly its own dependency while retaining a foreign one, measures subsequent
   claim behavior and probes --if-revision support. recovery-receipt.json persists.
3. `go run ./testdata/off128-native-probe` in this SDK checkout uses the real
   BdStore/ExecCommandRunnerWithExactEnvContext, through bin/bd into native Dolt.
   It asserts native status directly before/after Claim and includes a positive
   open control with the same hold label/metadata as reserved cases.

The Go parent PATH must include the fixture bin as well as the explicit child
PATH: exec.LookPath resolves the program before applying Cmd.Env. Use the declared Go1.26.6,
GOMAXPROCS2, GOFLAGS=-p=2, CGO_ENABLED=0, GOTOOLCHAIN=local, GOPROXY=off,
GOSUMDB=off and retained local module/cache paths. GOTMPDIR must point to the
private writable .gotmp in the SDK evidence worktree; /tmp remains read-only.
Two setup attempts are retained: readonly /tmp refusal, then absent bd in parent
PATH. Neither mutated a business system or weakened sandbox restrictions.

## Observed results and limits

* Native open -> claim succeeds. Native blocked/deferred -> exact-ID claim
  exits1, status unchanged and unassigned. Blocked remains refused after a new
  process, explicit same-actor assignee and gc.routed_to=deal-executor. Ready
  excludes it while finding a positive open control.
* SDK8084 Create forwards --status to the actual native create. Native blocked/
  deferred rows remain unclaimable. Existing SDK read normalization still reports
  those states as open; that SDK projection is not a native reservation receipt.
* Labels/metadata alone do not fence an open bead: the SDK open control claims.
* Removing only the own dependency preserves the unrelated dependency. However,
  after guarded blocked->open, direct native claim SUCCEEDS despite that remaining
  blocked dependency. Fixture carrier off128-0px became in_progress while native
  show still listed blocked dependency off128-6lf. This is a proven limitation of
  using dependency/hold as a final-claim fence, not a new authorization to ignore it.
* Installed bd rejects --if-revision as unknown flag; the control remains open.
  --if-status/--if-assignee cannot be combined with --claim. A pre-claim read or
  status-only guarded update cannot be advertised as full package/registry fencing.

Evidence is default durable storage only, embedded Dolt only, these exact source
bytes. It does not prove no-history/ephemeral/proxied-server variants, gc-ready or
installed gc manifests, arbitrary concurrent dependency mutations, authenticated
reviewer/result submission, full enrollment or production safety. There is no
recovery sender or business effect here. Production ingress OFF; CP0/CP1 unpassed.

A supported final-claim mechanism must evaluate actual target membership, current
accepted versions and all persistent holds at the mutation boundary, fail closed
on UNKNOWN, and prevent exact-ID/legacy entry points bypassing the check. Adding
only --if-revision would not by itself make an external registry lookup atomic.
Do not ship a precheck+unconditional-claim approximation as that mechanism.

## Primary evidence retained on vm328699

* prepare: unit off128-pinned-claim-20261022, invocation
  f6f9ae5a8f0a422483fa4ac20ce0119f, exit0. Log SHA256
  1d3283cc0d41611864c1f318100f76f787b2486e74ecdf2559344d789fa7a9a7;
  fixture receipt SHA256
  2f8e450aa842e9636f1fc617ffd2ad9cef2bb9f973b7d9aee008de9abcc1b2c1.
* recovery: unit off128-pinned-recovery-20261022, invocation
  79f709653461472a858d6ee47ff130e8, exit0. Log SHA256
  ae4b4f26b10a880abdaecba200a88b06e600478958f4273aa8a4531899bd1a9d;
  fixture recovery-receipt SHA256
  9f491d3d78490db86c935a6d0c1bb07b1267514a80bc2fd73a35ce6076d2ba3f.
* SDK chain: unit off128-native-sdk4-20261022, invocation
  a407cabab63f421ba594ddc5a74c3bab, exit0. Log SHA256
  af51065d40849dfe251af88504cb680d5478b1fa1642a1a6d359c17365ba7829;
  tested main.go SHA256
  4783807ce46348ea245ee278363e8116885ad8686eedde3e2202968e0d4a879b.

PID1 journal records successful terminal outcomes for each named invocation.
Logs are /srv/gc-build/logs/<unit>.log. Source commit and receipt hashes permit
independent inspection; no independent rerun is claimed by the author.

The earlier pre-format SDK3/Go1.27.1 result remains retained and was independently
reviewed with the native/recovery evidence in issue128 comment6349739860. Final
SDK4 reruns the formatter-adjusted same logic on the declared Go1.26.6. The
manual description lives under engdocs/contributors so the existing documentation
coverage check covers it; no coverage exclusions were added.
