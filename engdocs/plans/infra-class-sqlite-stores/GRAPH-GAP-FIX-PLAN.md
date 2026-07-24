# Graph gap fix plan — execution checklist

Source inventories: GRAPH-READ-GAP-ANALYSIS.md (G00-G38) +
GRAPH-READ-GAP-ANALYSIS-ADDENDUM.md (N00-N24). Status legend:
DONE / IN-PROGRESS / TODO / DESCOPED(reason).

## Done this pass (commits on feat/infra-class-sqlite-stores)
- [x] G00-G04 + G07/G10/G20 reconciler assigned-work frame (reachable
      fan-outs + collectAssignedWork graph leg + retirement family)
- [x] G08/G13 crash-recovery in_progress union (runner env identities)
- [x] G23 release-if-current routed arm
- [x] G06(part) fail-closed bd read backstop for unfederated gcg shapes

## Priority order for the remainder
1.  [x] N07/N10/N11 retention: DISABLE the 4h terminal sweeper on the
        graph class store (closed steps must outlive running workflows;
        workflow GC owns tree cleanup) + test
2.  [x] N08 legacy-id routing: widen graphHookClaimStore + the doBd
        mutation arm + show-fed reserved arm gating from gcg-prefix to
        "routed AND graph store owns id" (Get probe) + test
3.  [x] N00/N09 migration+residue must iterate RIG bd stores too
        (openGraphClassMigrationStores plural, orders pattern) + test
4.  [x] N02/N22 wait-dep graph leg (newWaitDependencyStoreSet +
        loadWaitDependencyBead) + FailWait only after graph consulted
5.  [x] N01/N03 convergence adapter routed arm (newConvergenceScope)
6.  [x] G18 order dispatch: wisp-root label stamp + order-run evidence
        + stale-wisp sweep routed (by-id graph mutation arm on policy
        store Update/SetMetadata* or explicit routing at the 3 sites)
7.  [x] G22/N04/N13 sling: deps.GraphStore threading (cmd_sling + api
        handler_sling) + sourceWorkflowStores graph arm (re-sling
        double-pour; cook --attach idempotency)
8.  [x] G19 control-dispatcher pack-custom queries: wrap shell fallback
        in graphFederatedWorkQueryRunner (dispatch_runtime.go ~883)
9.  [x] G12 drain MemberStores work-store tail (+retry/retry-eval/ralph)
10. [x] G25 retry-eval required-artifact source read (port ebeba2a55)
11. [x] G35 findBeadAcrossStores gcg arm: fall through to city/rig scan
        on ErrNotFound (manual recovery of misplaced control beads)
12. [x] G14/G15/G21/G30 API GET /beads list + ready graph legs
        (fail-loud 503 shape from integ)
13. [x] G31 memberStoreComplement for /beads/graph cross-class members
14. [x] G28 /status work counts graph leg; [x] G36 type=molecule augment (graph leg)
15. [x] G16 bead.* emission — COMPLETE on both sides: the controller
        CachingStore wrapper is now inherited by EVERY routed-store seam
        (resolveGraphStoreRouted / graphStoreForID / routedGraphStoreOrWarn /
        appendRoutedGraphStore / policy dispatch), and one-shot CLI writes
        (gc bd close/update on gcg ids, gc hook --claim) append bead.*
        to the city log directly via emitGraphBeadLifecycle.
    [x] G17 ADDRESSED AT THE ROOT rather than in one consumer: the BFF's
        run-detail fold was empty because graph lifecycle emitted NO
        events; with emission closed on both the controller and CLI sides
        the existing fold sees graph transitions. The b36 loopback
        fetchRunGraph remains a possible belt-and-braces follow-up (it
        would also cover a lost/rotated event log) but is no longer the
        fix for the reported symptom. b36 db9d6302c still does not apply
        (RunDetailOptions refactor diverges from local runproj).
16. [x] N05/N19/N21 convoy: gc convoy status synthetic-convoy graph
        arm; convoys list lane; controller-down fallback candidate
17. [x] N06 CAS fencing on graph plane: decide implement-ConditionalWriter
        vs loud-fail (control epochs / drain reservations / attach fences)
18. [x] N14 prime/nudge wisp-step injection graph arm
19. [x] N15 API ActiveBead fan-out graph leg
20. [x] N16 non-claim gc hook runner federation (same wrapper as claim)
21. [x] N17 DESCOPED (already covered: controller orphan release G10 +
        worker self-re-adoption G13 both landed — the shells are a
        redundant third path); [ ] N23 spawn-storm reset-loop discovery
22. [x] N18 wisp retention: graph-store closed-wisp purge path (wisp-
        compact.sh/reaper are bd-only); depends on decision in item 1
23. [ ] G05/G24/G27 gc bd mol current/progress federation
24. [x] G29 gc graph reserved-id arm; G37 autoclose graph-store arms
25. [x] G38/N20 backlog-depth graph leg (fail-loud); N24 BatchDeleter
        IMPLEMENTED on SQLiteStore + comments corrected. Remaining doctor
        checks DESCOPED with evidence: hold-label/custom-types read config
        not graph beads; order-tracking-retention is orders-class; the
        routed_to/session-model legs are follow-ups (specs in /tmp not
        committed — re-derive from the spec workflow if wanted).
26. [ ] G32 substring show resolution (low); G33 bd v1.1.x ephemeral
        probe repair (low); G34 per-store-ref partial gating (low)
