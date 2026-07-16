package main

// nudgeProducerRoute is the closed inventory of production paths that can
// cause nudge-like provider input. Adding a route requires adding one manifest
// row; the fixed-size array and tests fail closed on omissions or duplicates.
type nudgeProducerRoute uint8

const (
	nudgeProducerCLIImmediate nudgeProducerRoute = iota
	nudgeProducerCLIDeferred
	nudgeProducerManagedWake
	nudgeProducerMail
	nudgeProducerSling
	nudgeProducerWait
	nudgeProducerIdleClaim
	nudgeProducerTypedAPI
	nudgeProducerExternalMessage
	nudgeProducerRouteCount
)

type nudgeProducerState uint8

const (
	nudgeProducerUncovered nudgeProducerState = iota
	nudgeProducerBridged
)

type nudgeProducerManifestEntry struct {
	route         nudgeProducerRoute
	ownerFile     string
	ownerReceiver string
	ownerFunction string
	state         nudgeProducerState
}

// productionNudgeProducerManifest returns a value copy so startup evidence
// cannot be changed through a mutable package-level slice or binding field.
func productionNudgeProducerManifest() [nudgeProducerRouteCount]nudgeProducerManifestEntry {
	return [nudgeProducerRouteCount]nudgeProducerManifestEntry{
		{route: nudgeProducerCLIImmediate, ownerFile: "cmd/gc/cmd_nudge.go", ownerFunction: "deliverSessionNudgeWithIngress", state: nudgeProducerBridged},
		{route: nudgeProducerCLIDeferred, ownerFile: "cmd/gc/cmd_nudge.go", ownerFunction: "deliverSessionNudgeWithWorker", state: nudgeProducerUncovered},
		{route: nudgeProducerManagedWake, ownerFile: "cmd/gc/cmd_nudge.go", ownerFunction: "queueManagedSessionNudgeWake", state: nudgeProducerUncovered},
		{route: nudgeProducerMail, ownerFile: "cmd/gc/cmd_nudge.go", ownerFunction: "sendMailNotifyWithWorker", state: nudgeProducerUncovered},
		{route: nudgeProducerSling, ownerFile: "cmd/gc/cmd_sling.go", ownerFunction: "deliverSlingNudge", state: nudgeProducerUncovered},
		{route: nudgeProducerWait, ownerFile: "cmd/gc/cmd_wait.go", ownerFunction: "dispatchReadyWaitNudgesWithSnapshot", state: nudgeProducerUncovered},
		{route: nudgeProducerIdleClaim, ownerFile: "cmd/gc/idle_nudge.go", ownerFunction: "nudgeStalledPoolClaims", state: nudgeProducerUncovered},
		{route: nudgeProducerTypedAPI, ownerFile: "internal/api/handler_session_nudge_admission.go", ownerReceiver: "Server", ownerFunction: "humaHandleSessionNudgeAdmission", state: nudgeProducerUncovered},
		{route: nudgeProducerExternalMessage, ownerFile: "internal/api/session_resolution.go", ownerReceiver: "Server", ownerFunction: "sendBackgroundMessageToSession", state: nudgeProducerUncovered},
	}
}

// nudgeEffectKind is the closed vocabulary discovered at production
// nudge-command, legacy-queue, provider, and interaction boundaries. It is
// intentionally narrower than every provider-native implementation: the
// canonical reconciler effect inventory owns those physical leaves.
type nudgeEffectKind uint8

const (
	nudgeEffectLocalIngress nudgeEffectKind = iota + 1
	nudgeEffectWorkerNudge
	nudgeEffectProviderNudge
	nudgeEffectLegacyManagedEnqueue
	nudgeEffectLegacyQueueEnqueue
	nudgeEffectLegacyQueueStoreEnqueue
	nudgeEffectExternalMail
	nudgeEffectTypedAPIAdmission
	nudgeEffectWorkerRespond
)

// nudgeEffectClass says why one discovered site either contributes producer
// coverage or is explicitly excluded from it. Executor and interaction sites
// never count as command producers.
type nudgeEffectClass uint8

const (
	nudgeEffectCommandProducer nudgeEffectClass = iota + 1
	nudgeEffectLegacyQueueExecutor
	nudgeEffectKeyedAuthorizedExecutor
	nudgeEffectAdapterPassthrough
	nudgeEffectDistinctInteraction
)

type nudgeEffectSite struct {
	ownerFile     string
	ownerReceiver string
	ownerFunction string
	kind          nudgeEffectKind
	ordinal       uint8
}

type nudgeEffectOwner struct {
	ownerFile     string
	ownerReceiver string
	ownerFunction string
}

type nudgeEffectContract struct {
	ownerFile     string
	ownerReceiver string
	ownerFunction string
	kind          nudgeEffectKind
	ordinal       uint8
	class         nudgeEffectClass
	route         nudgeProducerRoute
	hasRoute      bool
}

func (c nudgeEffectContract) site() nudgeEffectSite {
	return nudgeEffectSite{
		ownerFile:     c.ownerFile,
		ownerReceiver: c.ownerReceiver,
		ownerFunction: c.ownerFunction,
		kind:          c.kind,
		ordinal:       c.ordinal,
	}
}

func (c nudgeEffectContract) owner() nudgeEffectOwner {
	return nudgeEffectOwner{
		ownerFile:     c.ownerFile,
		ownerReceiver: c.ownerReceiver,
		ownerFunction: c.ownerFunction,
	}
}

const productionNudgeEffectContractCount = 21

// productionNudgeEffectContracts classifies every typed nudge-like entry in
// production cmd/gc and the typed API front door. Its owning test discovers
// these calls independently with go/types and reconciles both directions, so
// adding or deleting an effect cannot silently preserve this promise.
func productionNudgeEffectContracts() [productionNudgeEffectContractCount]nudgeEffectContract {
	producer := func(file, receiver, function string, kind nudgeEffectKind, route nudgeProducerRoute) nudgeEffectContract {
		return nudgeEffectContract{ownerFile: file, ownerReceiver: receiver, ownerFunction: function, kind: kind, ordinal: 1, class: nudgeEffectCommandProducer, route: route, hasRoute: true}
	}
	excluded := func(file, receiver, function string, kind nudgeEffectKind, class nudgeEffectClass) nudgeEffectContract {
		return nudgeEffectContract{ownerFile: file, ownerReceiver: receiver, ownerFunction: function, kind: kind, ordinal: 1, class: class}
	}
	return [productionNudgeEffectContractCount]nudgeEffectContract{
		producer("cmd/gc/cmd_nudge.go", "", "deliverSessionNudgeWithIngress", nudgeEffectLocalIngress, nudgeProducerCLIImmediate),
		producer("cmd/gc/cmd_nudge.go", "", "deliverSessionNudgeWithWorker", nudgeEffectWorkerNudge, nudgeProducerCLIDeferred),
		producer("cmd/gc/cmd_nudge.go", "", "sendMailNotifyWithWorker", nudgeEffectWorkerNudge, nudgeProducerMail),
		excluded("cmd/gc/cmd_nudge.go", "", "tryDeliverQueuedNudgesByPoller", nudgeEffectWorkerNudge, nudgeEffectLegacyQueueExecutor),
		producer("cmd/gc/cmd_sling.go", "", "deliverSlingNudge", nudgeEffectWorkerNudge, nudgeProducerSling),
		excluded("cmd/gc/nudge_key_effect_owner.go", "nudgeKeyEffectOwner", "reconcile", nudgeEffectWorkerNudge, nudgeEffectKeyedAuthorizedExecutor),
		excluded("cmd/gc/status_provider.go", "statusProvider", "Nudge", nudgeEffectProviderNudge, nudgeEffectAdapterPassthrough),
		producer("cmd/gc/idle_nudge.go", "", "nudgeStalledPoolClaims", nudgeEffectProviderNudge, nudgeProducerIdleClaim),
		producer("cmd/gc/cmd_nudge.go", "", "queueManagedSessionNudgeWake", nudgeEffectLegacyManagedEnqueue, nudgeProducerManagedWake),
		producer("cmd/gc/cmd_nudge.go", "", "sendMailNotifyWithWorker", nudgeEffectLegacyManagedEnqueue, nudgeProducerMail),
		producer("cmd/gc/cmd_nudge.go", "", "queueSessionNudgeWithWorker", nudgeEffectLegacyQueueEnqueue, nudgeProducerCLIDeferred),
		producer("cmd/gc/cmd_nudge.go", "", "sendMailNotifyWithWorker", nudgeEffectLegacyQueueEnqueue, nudgeProducerMail),
		excluded("cmd/gc/cmd_nudge.go", "", "enqueueManagedNudgeThenWake", nudgeEffectLegacyQueueStoreEnqueue, nudgeEffectLegacyQueueExecutor),
		excluded("cmd/gc/cmd_nudge.go", "", "enqueueQueuedNudge", nudgeEffectLegacyQueueStoreEnqueue, nudgeEffectLegacyQueueExecutor),
		producer("cmd/gc/cmd_sling.go", "", "deliverSlingNudge", nudgeEffectLegacyQueueStoreEnqueue, nudgeProducerSling),
		producer("cmd/gc/cmd_wait.go", "", "dispatchReadyWaitNudgesWithSnapshot", nudgeEffectLegacyQueueStoreEnqueue, nudgeProducerWait),
		producer("cmd/gc/cmd_mail.go", "", "newMailNudgeFunc", nudgeEffectExternalMail, nudgeProducerMail),
		producer("internal/api/session_resolution.go", "Server", "sendBackgroundMessageToSession", nudgeEffectWorkerNudge, nudgeProducerExternalMessage),
		producer("internal/api/handler_session_nudge_admission.go", "Server", "humaHandleSessionNudgeAdmission", nudgeEffectTypedAPIAdmission, nudgeProducerTypedAPI),
		excluded("cmd/gc/worker_handle.go", "", "workerRespondSessionTargetWithConfig", nudgeEffectWorkerRespond, nudgeEffectDistinctInteraction),
		excluded("internal/api/handler_session_interaction.go", "Server", "handleSessionRespond", nudgeEffectWorkerRespond, nudgeEffectDistinctInteraction),
	}
}

func productionNudgeProducersCovered() bool {
	manifest := productionNudgeProducerManifest()
	var seen [nudgeProducerRouteCount]bool
	for _, entry := range manifest {
		if entry.route >= nudgeProducerRouteCount || seen[entry.route] || entry.ownerFile == "" || entry.ownerFunction == "" {
			return false
		}
		seen[entry.route] = true
		switch entry.state {
		case nudgeProducerBridged:
		case nudgeProducerUncovered:
			return false
		default:
			return false
		}
	}
	for _, present := range seen {
		if !present {
			return false
		}
	}
	return true
}
