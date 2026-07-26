# Release gate: composable RouteLabel/RouteLabelAny narrowing

- Deploy bead: `ga-fmvtha`
- Source bead: `ga-0av489`
- Design: `ga-i4lcx1`
- Exact-SHA reviewed source: `d81595564a4e34338d01a8c4a20d4f002d6e4852`
- Rebased feature tip: `cbab66071eb4fdc95a25aa67cd0b7715e16d729a`
- Deploy tip before this evidence update: `b73d590cc7a257097e5079061ba3e01788866b9b`
- Base evaluated: `origin/main@9efcfb5411be1056e2a824bacebc65421a98f982`
- Evaluated: 2026-07-26
- Outcome: **PASS**

`docs/PROJECT_MANIFEST.md` is not present in this checkout. This checklist
applies the seven release criteria from the active deployer contract, the
acceptance criteria in `ga-i4lcx1`, and the repository test policy in
`TESTING.md`.

## Release criteria

| # | Criterion | Result | Evidence |
|---|---|---|---|
| 1 | Review PASS present | **PASS** | The `ga-fmvtha` notes contain a fresh reviewer pass tied to exact candidate `d81595564`: `verdict: pass`, with no style, security, spec, or coverage findings. After `origin/main` advanced, `git range-diff 2c4c43b27..d81595564 9efcfb541..cbab66071` marked all five feature commits `=` (patch-identical). |
| 2 | Acceptance criteria met | **PASS** | FR-01–FR-07 and NFR-01–NFR-04 were checked against the source and tests; detailed evidence is below. |
| 3 | Tests pass | **PASS** | After the bounded self-rebase, acceptance-focused Go tests passed. `make test-fast-parallel` passed all 9 jobs on the first explicit post-rebase attempt. `go build ./...`, `go vet ./...`, changed-file `gofmt -l`, `make spec-ci`, and `make dashboard-check` passed. A Vite preview served the built dashboard at `127.0.0.1:4179` with HTTP 200 and the expected root/module entrypoint. These checks ran at rebased deploy tip `b73d590cc`. |
| 4 | No high-severity review findings open | **PASS** | Unresolved HIGH findings: **0**. The exact-SHA review records no style or security findings and no uncovered requirements. |
| 5 | Final branch is clean | **PASS** | `git status --porcelain=v1` was empty on the reviewed source, after the bounded self-rebase, and after all post-rebase generated-artifact checks. The branch is rechecked again after committing this evidence update. |
| 6 | Branch diverges cleanly from main | **PASS** | This criterion passed initially, then `origin/main` advanced during the first push. The canonical bounded helper completed a clean replay and lease-protected push (`37693b779` → `b73d590cc`, rc 0). `origin/main@9efcfb541` is an ancestor of the deploy branch, the feature range-diff is patch-identical, and the base was refreshed unchanged after post-rebase testing. |
| 7 | Single feature theme | **PASS** | The five source commits implement one configuration behavior: composable label narrowing shared by the routed claim and pool-demand predicates. Config copying, migration serialization, generated schemas/client, and dashboard assets are supporting surfaces of that same feature. |

## Acceptance evidence

| Requirement | Result | Evidence |
|---|---|---|
| FR-01 | **PASS** | `Agent.RouteLabel` and `Agent.RouteLabelAny` feed a shared `routeLabelFilter` in `buildWorkQuery`, `buildRoutedPoolQuery`, and `buildPoolDemandQuery`; `TestRouteLabelPredicateSharedWithWorkQuery` and `TestRouteLabelNarrowsWorkQueryAndPoolDemandTogether` passed. |
| FR-02 | **PASS** | `route_label` renders `bd ready --label` with AND semantics and `route_label_any` renders `--label-any` with OR semantics. Label-only, label-any-only, and combined cases passed. |
| FR-03 | **PASS** | `ValidateAgents` rejects either structured label field combined with raw `work_query` or `scale_check`; `TestValidateAgentsRouteLabelExclusiveWithRawOverride` passed. |
| FR-04 | **PASS** | The same filter is threaded through both canonical and migration `bd ready` probes and through the work-query, routed-pool, and demand-count builders. Structural and execution-backed parity tests passed. |
| FR-05 | **PASS** | The diff leaves assigned-work tiers, `buildOnBoot`, `buildOnDeath`, and the legacy-ephemeral probe label-neutral. Label filtering is confined to routed Tier 3 and its demand counterpart. |
| FR-06 | **PASS** | The new TOML fields, generated config reference/schema, patch/override plumbing, pool cloning, and migration serialization provide the required migration surface. Pack conversion remains the separately tracked follow-on. |
| FR-07 | **PASS** | Raw redirect-target and compound-query overrides remain supported; this change does not reinterpret or remove raw `work_query` behavior. `TestRawWorkQueryOverrideCollapsesAllDiscoveryTiers` passed. |
| NFR-01 | **PASS** | `routeLabelFilter.shellArgs` uses `internal/shellquote`; `TestRouteLabelQuotesShellMetacharacters` executed the generated shell and confirmed the canary command was not run. |
| NFR-02 | **PASS** | Flag rendering exists once, in `routeLabelFilter.shellArgs`, and both routed-demand probes call it. |
| NFR-03 | **PASS** | An empty filter renders an empty fragment; existing work-query golden/parity coverage passed in the first-attempt fast suite. |
| NFR-04 | **PASS** | `TestAgentFieldSync`, `TestApplyAgentPatchCoversAllFields`, `TestApplyAgentOverrideCoversAllFields`, `TestAgentCloneIsDeep`, `TestAgentConfigFromAgentCoversPersistedFields`, and `TestDeepCopyAgentCoversAllFields` all passed. |

## Commands

```text
go test -count=1 ./internal/config ./internal/migrate ./cmd/gc -run '<RouteLabel and field-sync acceptance set>' -v
make test-fast-parallel
git diff --name-only -z origin/main...HEAD -- '*.go' | xargs -0 -r gofmt -l
go build ./...
go vet ./...
make spec-ci
make dashboard-check
npm --workspace gas-city-dashboard-frontend run preview -- --host 127.0.0.1 --port 4179
curl --fail http://127.0.0.1:4179/
```
