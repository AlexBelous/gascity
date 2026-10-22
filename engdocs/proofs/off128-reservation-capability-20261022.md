# #128 first reservation proof: negative capability result

Source baseline: `890c59dc0a1cd9be4149113b32a63f34eca7a189` (tree `fabe3c90218846d18c0a821b3a815c611f070a22`). Worktree: `/Users/ba/Projects/.worktrees/gascity-src/off128-reservation-proof-20261022`, branch `strategy/off128-reservation-proof-20261022`. This is an isolated Mac development result. No production city, store, CRM, observer, admission, or canary was exercised. CP0/CP1 are **not passed**.

## Result

The current Gas City store abstraction does not supply the required reserve-before-bind primitive across its supported paths. `Bead.Status="blocked"` is persisted by `SQLiteStore.Create` and passed through `NativeDoltStore.Create`, but `BdStore.CreateWithStorage` omits the status argument when it invokes `bd create`. The pinned `bd` has a supported `create --status` flag; the gap is the Gas City adapter, not the upstream CLI. A reservation label or metadata marker cannot substitute: `SQLiteStore.Claim` accepts an open row by exact ID without checking either field. An initially blocked SQLite row refuses the same exact-ID claim.

The attached test at `internal/beads/off128_capability_probe_test.go` executes the adapter with its existing `CommandRunner` fake and executes create/claim against fresh `t.TempDir()` SQLite stores. Its fake checks the *actual* `BdStore.Create` argument construction; it does not simulate a binder or an alternate dispatch implementation. The isolated SQLite checks prove the direct claim boundary. They do not prove the pinned `bd` runtime, `gc-ready`, NativeDolt, or installed binaries. An isolated end-to-end reserve→bind→dispatch proof cannot be honestly marked green on this baseline.

## Reproduction

From this worktree:

```sh
CGO_CPPFLAGS=-I/opt/homebrew/opt/icu4c/include CGO_LDFLAGS=-L/opt/homebrew/opt/icu4c/lib \
  go test ./internal/beads -run '^TestOff128CurrentReservationGaps$' -count=1 -v
```

Observed 2026-10-22: exit 0, three passing subtests. Key output:

```text
requested_status=blocked argv="create --json reserved -t task --labels hold:external" returned_status="open"
created_id=gc-1 created_status=open claimed=true claimed_status=in_progress
created_id=gc-1 created_status=blocked claimed=false
```

The first raw `go test` attempt exited 1 during compilation before any assertion because `github.com/dolthub/go-icu-regex` could not find `unicode/regex.h`. The command above uses this repository's documented Homebrew ICU include/library paths. The test uses no production state or inherited `GC_CITY_PATH` fallback.

## Exact source and dependency inputs

| File at baseline, except attached new test | SHA256 |
| --- | --- |
| `go.mod` | `54fa5cb08163aef8e11e5b07eaf97dc9c52b6c86fcb056777c397f508bef4e9d` |
| `go.sum` | `b9ec0f1ad5b98d2c8db8b5d264059cebb4aca619c3d72e5f10681c196efc3d9f` |
| `internal/beads/beads.go` | `42146106033a7d2d9425408c980a65fb6a68a3418af3e631cd4c4665f901480f` |
| `internal/beads/bdstore.go` | `202a6780b6531dbd60ffdf65a48e8ae193fc15d0beac78324be51e6adaf5d1eb` |
| `internal/beads/sqlite_store.go` | `98a797edc2e6f668d5ecb7fa2eac91a91e7ef91f8025f4583ecfc6a1517cc075` |
| `internal/beads/sqlite_store_claim.go` | `b27482a5591e04e6545b94138146726527e4bfad17efda4fb6f6266054c0158a` |
| `internal/beads/native_dolt_store.go` | `aaf8d7c8e3d93522c4a479e7dcd2b081e78bfd02c0d4014030fcc19d2d15468f` |
| `cmd/gc/cmd_bd_by_id.go` | `cdad4c5898e72a3fb41572dd14937163fd2aa2f6d2afa90510eeb1ea8ef526f0` |
| `cmd/gc/cmd_hook_claim.go` | `19f55a16fd7bf17e2ecbec1ba3f63e3c0f67b26d3401682ee06f0ca2d7bfe0aa` |
| `internal/beads/off128_capability_probe_test.go` (new) | `c5e4fea3d31e652ddb0fd4af09761b2aeaee3a989a814fd2450f9450d71a216a` |

`go.mod` replaces `github.com/steveyegge/beads` with `github.com/AlexBelous/beads v1.1.1-0.20260928222722-da08f27390f1`; `go.sum` records module hash `h1:umGl7pHBKmG3hdBNxdEayW8KV0LX+YJeBIf7JLgJuOc=`. The pinned dependency's `cmd/bd/create.go` accepts `--status` and validates built-in `blocked`; `internal/storage/issueops/claim.go` limits claimable source statuses to `open` and configured active statuses. This source evidence is narrower than an installed-binary receipt.

## Smallest safe next engineering choice

1. Add a status-preserving `BdStore.CreateWithStorage` call and shared create-time status conformance for the actual bound providers. Preserve the blocked status and a durable reservation fence before exposing the minted carrier ID. Do not use a finite defer, label, metadata key, or absence of route as the sole guard.
2. Introduce authoritative operation/stage/task membership and enrollment in the existing workflow store. Validate the exact carrier ID, current version, package digest, and enrollment at every publication/discovery boundary and the final claim mutation. Refuse missing/unknown registry state. The current direct-ID `gc bd update --claim` and `gc hook --claim` paths need explicit coverage; adding a `Ready` filter alone cannot close the direct-ID path.
3. Release only the reservation's own fence after verified bind, then persist a dispatch intent/receipt with recovery for an attached carrier lacking a sling receipt. Keep the same carrier on retries and ambiguous outcomes. Test concurrent reserve/dispatch and crashes before mapping, after mapping, after bind, and before dispatch against disposable stores.

These are proposed changes, not implemented capabilities. The first slice cannot be accepted until they are executable and independently reviewed. Due-queue creation, task-contract publication, bootstrap finalize, role/Clerk readers, pool demand, result enrollment, installed `gc` and `gc-ready`, and unrelated holds remain outside this proof. Production ingress stays off.
