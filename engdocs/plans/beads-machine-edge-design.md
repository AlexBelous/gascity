# `beads-machine-edge` — machine ingress for hosted beads-web

Tracked as `ga-bplkj.31`. **Slice 0 done; Slice 1 written and landed as a PR
(gascity/infra#1475) but NOT deployed** — the identity cluster was unreachable
from the authoring session. Slices 2-7 are still design only.

## Thesis

The machine leg for beads is already built — on the wrong side of the wall. beads-web verifies
`aud=beads` EIAs offline in production today (`BEADS_EIA_ISSUER` active, `eia-signing-beads-v1`
JWKS mounted), its `/api/v1` matrix already admits `machine_bearer` on the three bdproxy rows, and
it ships a `-machine-origin` flag whose own doc comment names the missing piece: *"a path-scoped
route straight to beads-web"* bypassing the shell-BFF cookie leg.

What does not exist is any ingress that delivers `X-Gc-Identity` to it.

**Premise correction.** Crucible and manifold machine-edge Deployments do **not** carry
`-machine-origin`. It exists in one binary — `cmd/beads-web/main.go:407` — and is unset everywhere.
It is a URL-emission knob (which base URL appears in connection recipes), not a trust knob.
*Positive control:* the sweep returning zero `machine-origin` hits in `/data/projects/infra` finds
`crucible-api`, `BEADS_EIA_ISSUER` and `machine-edge` throughout.

## The wall

`works.gascity.com/beads/api/*` → shell-bff NodePort 30092. The BFF requires a sealed apex session
cookie and returns `401 no session` before anything else — emitter at
`gasworks-platform/internal/shellbff/productproxy.go:197`. It then *strips* `X-Gc-Identity` as
anti-forgery (`gcTrustedHeaders`, `productproxy.go:31-40, 104-108`) and stamps its own
cookie-derived headers.

So a valid machine EIA dies at step one **for the same reason garbage does** — the cookie check
runs first. Every auth experiment against this surface returns one answer for every reason. That is
the diagnostic trap, and it is why this took a day to locate.

## Shape: passthrough verifier, not minting edge

Two patterns exist in the fleet. The discriminator is what credential the caller arrives with.

| | manifold / forge | crucible | **beads** |
|---|---|---|---|
| Caller holds | long-lived product key (`mn_live_`) | self-minted EIA | self-minted EIA |
| Edge must | **mint** (OpenBao transit + Accounts resolver, SA token) | **verify + forward** | **verify + forward** |
| Signing authority at edge | yes | **none** | **none** |

`bd getToken beads --json` already yields a live `kid=eia-signing-beads-v1`, `aud=["beads"]` EIA via
the STS machine leg. beads-web's own `fastMachineRecipe` (`apijson.go:659-711`) documents exactly
this flow — *"present the minted short-lived EIA as X-Gc-Identity; the raw key never reaches
beads."* A minting edge would duplicate the STS; a passthrough completes the designed system.

Reuse `gasworks-platform` `cmd/eia-machine-proxy` + `internal/eiaproxy` unchanged. The edge parses
no bodies and writes no JSON, so the typed-wire invariant is untouched — all beads wire shapes stay
Huma-generated in beads-team-server.

## The one constraint beads does not share

`-machine-origin` **must be a bare https host**. `validAbsoluteOrigin` (`apijson.go:630-635`)
rejects any path component, boot-fatally (`main.go:591-605`), and every emitted URL is
`origin + /beads/api/...` (`fastBaseURL`, `apijson.go:617-628`).

So beads **cannot** use the `works.gascity.com/<product>-api/*` shape crucible and manifold use — a
works.gascity.com origin would emit URLs routing straight back into the shell-bff cookie leg, i.e.
back into this bug.

- **Stage 1:** `beads-api.ops.gascity.com` as an identity-Caddy private route → `127.0.0.1:30187`
  (the `trust.ops` / `hyperdx.ops` precedent).
- **Stage 2:** promote the same hostname public under Cloudflare origin-pull. A route move, not a
  rebuild.

## Configuration

| Knob | Value |
|---|---|
| `EIA_PROXY_AUDIENCE` | `beads` (matches `BEADS_EIA_AUDIENCE`) |
| `EIA_PROXY_ISSUER` | `https://edge.gascity.internal` (matches `BEADS_EIA_ISSUER`) |
| `EIA_PROXY_JWKS_FILE` | new `beads-eia-jwks` ConfigMap in `identity-edge`, byte-identical to corp-public's — **rotation updates BOTH** |
| `EIA_PROXY_UPSTREAM_URL` / `_HOST` | `https://beads.ops.gascity.com`, Host + TLS SNI pinned — resolves to **100.109.51.65** (`corp-public-ha-1`) |
| `EIA_PROXY_STRIP_PREFIX` | *(empty)* — caller-facing paths are beads-web's real paths |
| `EIA_PROXY_ALLOWED_PATH_PREFIXES` | `/beads/api/v1/beads/projects` — exactly the three frozen bdproxy rows |
| Caller auth (initial) | `ALLOWED_ORGS=<pilot org>` + `REQUIRE_ALLOWED_ORGS=true` |
| **Caller key provisioning** | the machine caller's `beads_live_` SP key must carry **`role_beads_member`** (granular `/api/v1` scopes incl. `beads:issues.read`), **not** the controller orchestrator ceiling `[beads:read, beads:write]` — that ceiling is provisioned for the MySQL-wire gateway and 403s on the bdproxy rows |
| Rate / size | RPS 20 / burst 40 / global 200 / 1 MiB |
| Netpol | default-deny egress; kube-dns (ns-wide `egress-baseline`) + **`namespaceSelector: tailscale-egress` × `podSelector: app=ts-egress-manifold` on :443** — ~~`100.109.51.65/32:443`~~ |
| SA token | `automountServiceAccountToken: false` — no mint, nothing to talk to |
| NodePort | ~~propose 30187~~ **30187 is TAKEN (`gasworks-observer-edge`). Slice 2 takes 30190.** |

**Netpol correction (Slice 1).** A `/32` for the upstream is *wrong on this
cluster*, not merely redundant. In-cluster, `beads.ops.gascity.com` resolves via
the HA `coredns-custom` overlay to the tailscale-egress proxy Service
(`manifold-egress`, `10.43.233.167`), which DNATs :443 to `corp-public-ha-1` and
preserves SNI. Under Cilium socketLB the ClusterIP is translated to the proxy
**pod** before egress policy is evaluated, so an ipBlock `/32` for either the
ClusterIP or the tailnet address is *shadowed* and drops under enforce — the same
trap `crucible-machine-edge` and `crucible-edge` each needed a cluster-level
patch to escape. `beads-machine-edge` is HA-only, so it carries the
identity-aware peer directly, pod-scoped so it gets the corp-public conduit and
never the `ts-egress-bao` secret conduit. Ingress needs nothing new:
`tailscale-egress-ingress` already admits namespace `identity-edge` on :443.

**NodePort correction (Slice 1, resolves Unverified #6).** Rendered from the live
overlay, identity-ha holds 27 NodePorts and **30187 is `gasworks-observer-edge`**.
The allocations doc was stale twice over — headed `identity-v0` (whose flux tree
now renders zero NodePorts) and missing 30186/30187/30188/30189 entirely, which is
what made 30187 look free. Corrected in `docs/standards/nodeport-allocations.md`,
re-headed `identity-ha`, with a regeneration one-liner so it cannot silently rot
again. **Slice 2 takes 30190.**

**Cannot be confused with the human leg, by construction:** different host, different NodePort
(30092 vs 30190), different pod, disjoint credential surfaces. The BFF accepts only the sealed
cookie and strips `X-Gc-Identity`; the edge accepts only `X-Gc-Identity` and forwards no cookies
(the header allowlist drops `Cookie` entirely). No request both legs would accept.

**Two independent verifications, one credential, no re-minting:** the edge verifies offline and
forwards the same validated token; beads-web verifies it again through its own `eia.Verifier` and
binds it per-operation.

## Security properties

| Property | Enforced at | Fails |
|---|---|---|
| Audience pinning | edge (`eia.go:402-404`) **and** app per-row (`apiauthz/identity.go:167-172`) | closed |
| Caller is a machine | edge: `subject_type=service` (`proxy.go:105-108`) | closed |
| Scope enforcement | app only, deliberately: authn → **exact single scope** → tenancy; no wildcards, no read floor implying writes (`authorize.go:92,108,136`) | closed |
| Org/tenant re-check | app: verified `org_id` must equal the tenant resolved from the **control plane**, never from path or header (`identity.go:176-186`) | closed |
| Anti-probing | app: unknown project, unauthorized project, and no-backend are one **timing-padded** 404 (`bdproxy/http.go:82-86, 371-380`) | closed |
| Unknown kid / malformed / wrong alg | edge: typed errors → uniform 401, token value never logged | closed |
| Ambiguous credentials | app: two offered credentials = rejection, not precedence (`identity.go:88-131`) | closed |
| Open-relay misconfig | edge **refuses to boot** with empty allowlists absent explicit `ALLOW_ANY_SERVICE_ORG` (`config.go:149-156`) | closed (CrashLoop) |
| Header/cookie forgery | edge rebuilds outbound from a two-header allowlist | closed |
| Lateral movement from edge | no SA token, no signing key, egress to one IP:port | contained |
| **Replay** | **not enforced for bearer EIAs** — stateless verification, no jti cache | **open within TTL** |

Replay is bounded, not solved: ~90s TTL, TLS-only channels, header scrub — a token can replay only
as itself, to the same product, for seconds. The one machine *mutation* is replay-safe at the
semantic layer via idempotent claim (`bdproxy/http.go:280-313`).

**Honest asymmetry:** the human leg has an extra layer the machine leg will not — shell-bff's
`ResolveHuman` product-entitlement gate. On the machine leg the EIA's scopes *are* the entitlement
and beads-web's matrix is the sole authority. Same posture crucible runs, but a beads-web authz bug
has no second net here. That is what the Slice 6 drill exists to test.

## Where beads genuinely differs

1. **The caller population is every hosted tenant.** Crucible admits a handful of platform-internal
   orgs. A static `ALLOWED_ORGS` for beads is an onboarding treadmill — the exact
   "fail-open-by-omission footgun" crucible red-teamed away before going org-agnostic
   (`943c9438`/#1159). End state is the same flip, safe *only because* beads-web's tenancy binding
   is the real boundary. **That inverts the burden of proof:** for crucible the edge allowlist was
   the belt and the app the suspenders; for beads the app is the belt.

2. **A machine caller can address a per-project `bd serve` backend.** Three consequences no other
   edge faced:
   - The edge's path allowlist **cannot enumerate authorization targets** — `{project}` is a path
     variable spanning all tenants. Prefix allowlisting is shape-control only; all tenant-sensitive
     authorization is downstream. (Crucible's `/v0/sandboxes` prefix really *was* the authorization
     surface.)
   - The caller's EIA must **never** reach `bd serve`, which authenticates one machine token and has
     no tenancy. beads-web already swaps credentials at the boundary. The edge must not grow a
     "forward to project backend" shortcut, ever.
   - Probing pressure concentrates here. The padded uniform 404 is the control, and the edge does
     not undercut it — edge 404s depend only on path *shape*, never on tenant data.

3. **The bare-host origin rule** (above) dictates a dedicated hostname. Payoff: the recipe system
   lights up for free, and it **deploys dark** — with `-machine-origin` unset every payload is
   byte-identical to today.

## Slices

Each independently landable, verifiable, rollback-able. Every acceptance test names the *distinct*
failure it must produce — the anti-"one 401 for every reason" discipline.

**Slice 0 — truth anchor. DONE 2026-08-12.** Drove beads-web directly at `beads.ops.gascity.com`,
bypassing identity-plane ingress. Artifacts: `/var/tmp/slice0-20260812T104700Z-1532964/`.

| # | Input | Status | Content-Type | Body |
|---|---|---|---|---|
| 1 | valid EIA → `getContext` on the pilot | **200** | `application/json` | `dolt_mode:"proxied-server"`, `database:"bd_prj_75f11276a6deb657"`, `repo_root:"/workspace"` |
| 2 | no `X-Gc-Identity` | 401 | `application/problem+json` | `unauthenticated` |
| 3 | wrong-audience EIA | 401 | `application/problem+json` | **identical to row 2** |
| 4 | valid EIA + bogus project | 404 | `application/problem+json` | `not_found` (padded) |
| 5 | valid EIA + bogus path | 404 | `text/plain` | Go router `404 page not found` |
| 6 | `X-Gc-Identity: garbage` | 401 | `application/problem+json` | **identical to row 2** |
| 7 | valid EIA → `listProjects` | 403 | `application/problem+json` | `forbidden` — authn succeeded, authz denied |

**No row returned `no session`** — the wall is shell-bff's alone, and the private Caddy route
forwards `X-Gc-Identity` verbatim (resolves Unverified #2).

**Row 1 was proxied, by causation not correlation:** a repeat probe moved the pilot serve log's
`getContext` count 308 → 310 with both entries stamped at the probe second; serve-edge nginx shows
`sni="bdserve-prj-…" upstream=10.53.177.81:8080 status=200`. The last prior `getContext` was
08:21:16Z, before the 09:23:21Z flip — **these are the first proxied served requests under the flag.**

**Padding confirmed empirically:** bogus-but-well-formed project 0.4931/0.4937/0.4914s vs malformed
project id 0.4883/0.4893/0.4915s — a ±3ms cluster across two different denial reasons, against a
real 200 at 0.587–0.845s. The denial path leaks neither existence nor id well-formedness.

**Finding — rows 2/3/6 collapse.** Byte-identical after normalizing `request_id`, including headers.
No credential, valid-but-wrong-audience, and garbage are indistinguishable to the caller. Defensible
as the intended anti-oracle posture, and it does *not* reproduce shell-bff's pathology (401 / typed
404 / router 404 separate cleanly). But **auth failures are not self-diagnosing** — the only
discriminator is `x-request-id` → server-side logs.

**Finding — 2× upstream amplification.** One API `getContext` produces exactly two upstream
`getContext` calls to `bd serve` (reproduced). Benign for reads; pin it before Slice 4's
`claimIssue` replay work, where double-dispatch interacts with the idempotency assertion.

**Slice 1 — deploy cluster-internal only. WRITTEN 2026-08-12, gascity/infra#1475.
NOT DEPLOYED** — identity-ha was unreachable from the authoring session (local
kubeconfig points at `cherry`; identity-ha SSH wants an interactive Tailscale
reauth). ClusterIP Service, no NodePort, no Caddy route. Rollback: delete the
kustomization entry.

*Blocker cleared:* `ga-bplkj.32` is resolved via option (a) — a true
service-subject EIA is mintable for beads through the STS machine leg. The
`subject_type=service` gate stands unmodified.

*Acceptance — MEASURED, and both prior statements were partly wrong.* The
original "six inputs, five distinct answers" and Slice 0's revision ("cannot be
satisfied from response bytes") were each read off the wrong surface. Running the
shipped config against the real handler and real verifier gives **five
wire-distinguishable classes at the edge** plus **two collapses**, and the
collapsed pair is *not* the one Slice 0 found:

| Input | Edge answer | Log line |
|---|---|---|
| `/healthz` | 200 `ok` | — |
| no `X-Gc-Identity` | 401 `missing identity` | — |
| garbage / wrong-aud / expired | 401 `invalid identity` | `eia verification failed` · `err` separates them |
| human-subject EIA | 403 `forbidden` | `caller rejected` · `subject_type=user` |
| service EIA, wrong org | 403 `forbidden` | `caller rejected` · `subject_type=service` + rejected `org_id` |
| admitted, off-allowlist path | 404 `not found` | — |
| admitted, allowed path | upstream's answer, headers verbatim | — |

At beads-web, no-credential / wrong-audience / garbage are byte-identical (Slice
0). **At the edge they are not** — `missing identity` ≠ `invalid identity`. The
genuine edge collapses are (a) the three verify failures and (b) human-subject vs
wrong-org, both separated log-side keyed by `x-request-id`. A log assertion
written against Slice 0's pair would have asserted the wrong thing.

Two ordering properties fall out and are load-bearing later: the path allowlist
runs **after** authentication, so the edge 404 is unreachable without an admitted
service EIA (the edge leaks no path shape to an unauthenticated prober, and Slice
2's SPA-catch-all control needs a valid EIA to run); and the validated token is
forwarded **verbatim** with `X-Crucible-Tenant` carrying the verified `org_id`,
with no token value in any log line.

*One out-of-app dependency Slice 1 surfaced:* the new edge lifts the identity-edge
simultaneous-roll peak to exactly the namespace `pods` quota (46 of 46). The
preflight passes at equality while leaving zero room for one more surge pod —
the infra #348 silent-deadlock condition, and it can wedge a *sibling* edge's
roll, not only this one's. Raised in a separate, droppable commit.

**Slice 2 — tailnet-private hostname.** NodePort **30190** (not 30187) + private Caddy route, org allowlist pinned.
**SPA-catch-all control:** `/anything/healthz` must be an edge 404 text, never 200 text/html —
proving this host cannot lie the way `works.gascity.com` does.

**Slice 3 — flip `-machine-origin`.** One env on beads-web. The dark-deploy property is itself the
test: before the flip, byte-identical absence. Rollback: unset.

**Slice 4 — end-to-end with the real caller.** All three rows; `claimIssue` replayed twice asserting
one upstream claim and a stored-response replay. Cross-check an admitted project vs a 501-sentinel
project — both must answer differently and correctly.

**Slice 5 — public exposure** under Cloudflare origin-pull. Rate-limit control (burst past 40 →
429); bogus-path control by content-type *and* status.

**Slice 6 — org-agnostic flip.** Precondition drill run and archived first: org-A EIA against an
org-B project → padded 404 inside the denial timing budget; read-scope EIA attempting `claimIssue` →
403 insufficient-scope. Then mirror crucible `943c9438` exactly.

**Slice 7 (optional) — the Fast surface.** Needs a mid-path wildcard the prefix allowlist cannot
express. Keep out of the critical path.

## Blast radius

A second internet-facing door to a multi-tenant service holding every hosted customer's work graph,
plus a routed path toward per-project backends.

- *Path allowlist too broad* → machine-reachable shape includes provisioning/connection rows, whose
  payloads disclose gateway coordinates and database names. Authorization still gates them, but the
  exposure class changes. Control: one env line, reviewed per slice, pinned by Slice 1's test.
- *Org-agnostic flip while beads-web tenancy has a bug* → cross-tenant read for anyone with any
  org's service EIA. **Worst case, and the reason the flip is last**, gated on the archived drill.
- *Wrong upstream pin* → requests land on another ops vhost on the same Caddy IP. Control: Host+SNI
  pinned; Slice 1 checks response identity.
- *Edge compromise* → no key, no SA token, egress of one IP:port, ~90s bearer EIAs in flight. The
  long-lived key never crosses this wire.

**Rollback is uniform:** the edge is stateless. Delete the Caddy handle and the flux entry; unset
`-machine-origin` and emissions revert byte-identically. No migrations, no rotations. Clients that
adopted the recipe lose the leg — the intended kill switch.

**Before it carries traffic:** JWKS byte-parity both sides; netpol applied and a deny-all probe
verified failing; org allowlist non-empty with `REQUIRE_ALLOWED_ORGS=true`; the Slice 0/1 answer
matrix archived; reject-path log lines visible; security review recorded.

## Unverified

1. Which infra branch is live — `/data/projects/infra` is on `deploy/recall-backup-deadman` with a
   modified tree; the machine-edge tree moved `identity-v0` → `identity-ha-v0` (`4356471c`) and the
   org-agnostic manifest (`943c9438`) is on branches this checkout lacks. *Settles it:*
   `flux get kustomizations` on the identity cluster.
2. ~~Whether the corp-public private route forwards `X-Gc-Identity` verbatim.~~ **RESOLVED YES** by
   Slice 0.
3. ~~Tailnet ACL reachability of `beads.ops.gascity.com`.~~ **RESOLVED** — reachable, and it
   resolves to **100.109.51.65** (`corp-public-ha-1`), *not* the `100.119.244.94` this design
   originally named. That IP is `corp-public-v0-1` and has been **offline 42 days**. Control proving
   death rather than ACL: `tailscale ping 100.119.244.94` → timeout, `100.77.42.113` → pong 109ms.
4. ~~Exact issuer on STS-minted beads EIAs.~~ **RESOLVED** — `https://edge.gascity.internal`,
   `kid=eia-signing-beads-v1`, `aud=["beads"]`, TTL exactly 90s.
5. ~~Whether the deployed beads-web image carries the bdproxy rows live.~~ **RESOLVED** — row 1
   returned 200 `proxied-server`, not the 501 sentinel.
6. ~~NodePort 30187 availability.~~ **RESOLVED — 30187 is TAKEN**
   (`gasworks-observer-edge`). Rendered from `identity-ha-v0/flux/clusters/identity-ha`,
   identity-ha holds 27 NodePorts; 30186/30188/30189 are also allocated. **Slice 2
   takes 30190**, and the stale allocations doc is fixed.
7. **NEW — the machine leg is still unexercised end to end.** Slice 0 exercised the *human-bearer*
   leg over a machine-shaped transport (`ga-bplkj.32`). Whether a true service-subject EIA can be
   minted for beads is open.
8. **NEW — cross-tenant denial** (org-A EIA vs org-B project) was not run; needs another org's
   project id. That is Slice 6's drill.
