package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/reconciletest/effectinventory"
	"github.com/gastownhall/gascity/internal/worker"
	"golang.org/x/tools/go/packages"
)

func TestProductionNudgeProducerManifestPinsEveryCanonicalRoute(t *testing.T) {
	want := [nudgeProducerRouteCount]nudgeProducerManifestEntry{
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
	got := productionNudgeProducerManifest()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("producer manifest = %#v, want %#v", got, want)
	}
	if productionNudgeProducersCovered() {
		t.Fatal("partial producer manifest claimed complete coverage")
	}
}

func TestProductionNudgeEffectContractsReconcileTypedDiscoveryBidirectionally(t *testing.T) {
	discovery := discoverProductionNudgeEffects(t)
	contracts := productionNudgeEffectContracts()
	manifest := productionNudgeProducerManifest()

	contractBySite := make(map[nudgeEffectSite]nudgeEffectContract, len(contracts))
	routeEvidence := make(map[nudgeProducerRoute][]nudgeEffectContract, len(manifest))
	for _, contract := range contracts {
		site := contract.site()
		if previous, duplicate := contractBySite[site]; duplicate {
			t.Fatalf("duplicate nudge effect contract for %s: %#v and %#v", site, previous, contract)
		}
		contractBySite[site] = contract
		switch contract.class {
		case nudgeEffectCommandProducer:
			if !contract.hasRoute || contract.route >= nudgeProducerRouteCount {
				t.Fatalf("command-producer contract %s has no valid producer route", site)
			}
			routeEvidence[contract.route] = append(routeEvidence[contract.route], contract)
		case nudgeEffectLegacyQueueExecutor, nudgeEffectKeyedAuthorizedExecutor, nudgeEffectAdapterPassthrough:
			if contract.hasRoute {
				t.Fatalf("executor/exclusion contract %s silently counts as producer route %d", site, contract.route)
			}
		case nudgeEffectDistinctInteraction:
			if contract.kind != nudgeEffectWorkerRespond || contract.hasRoute {
				t.Fatalf("distinct interaction contract %s can silently count toward command coverage", site)
			}
		default:
			t.Fatalf("contract %s has unknown class %d", site, contract.class)
		}
	}

	for site := range discovery.effects {
		if _, classified := contractBySite[site]; !classified {
			t.Errorf("discovered nudge-like effect site has no producer/executor/exclusion contract: %s", site)
		}
	}
	for site := range contractBySite {
		if _, exists := discovery.effects[site]; !exists {
			t.Errorf("nudge effect contract has no discovered production evidence: %s", site)
		}
	}
	for _, entry := range manifest {
		owner := nudgeEffectOwner{
			ownerFile:     entry.ownerFile,
			ownerReceiver: entry.ownerReceiver,
			ownerFunction: entry.ownerFunction,
		}
		if !discovery.owners[owner] {
			t.Errorf("producer route %d names missing production owner %s", entry.route, owner)
		}
		evidence := routeEvidence[entry.route]
		if len(evidence) == 0 {
			t.Errorf("producer route %d (%s.%s) has no discovered command-producer evidence", entry.route, entry.ownerFile, entry.ownerFunction)
			continue
		}
		ownerEvidence := false
		typedDurableIngress := false
		for _, contract := range evidence {
			if contract.owner() != owner {
				continue
			}
			ownerEvidence = true
			if contract.kind == nudgeEffectLocalIngress || contract.kind == nudgeEffectTypedAPIAdmission {
				typedDurableIngress = true
			}
		}
		if !ownerEvidence {
			t.Errorf("producer route %d owner %s is not its discovered command-producing boundary", entry.route, owner)
		}
		if entry.state == nudgeProducerBridged && !typedDurableIngress {
			t.Errorf("bridged producer route %d owner %s lacks a go/types-proven durable ingress call", entry.route, owner)
		}
	}
}

func TestCanonicalNudgePhysicalInventoryBacksTypedEffectCensus(t *testing.T) {
	registry, err := effectinventory.CanonicalRegistry()
	if err != nil {
		t.Fatalf("CanonicalRegistry: %v", err)
	}

	canonicalDirect := make(map[string]bool)
	workerExecutor := make(map[string]bool)
	for _, registration := range registry.Registrations {
		if registration.BoundaryID != "runtime.provider.Nudge" &&
			registration.BoundaryID != "runtime.immediate-nudge.NudgeNow" {
			continue
		}
		if !registrationHasNudgeRoute(registration) {
			continue
		}
		fn := registration.Matcher.Enclosing
		key := strings.Join([]string{fn.File, fn.Object.Receiver, fn.Object.Name}, "|")
		for _, profileCase := range registration.Cases {
			for _, route := range profileCase.Routes {
				if route.ActionFamily != effectinventory.FamilyNudge || route.AccessPath != effectinventory.AccessProviderBypass {
					continue
				}
				if fn.Object.Package != "github.com/gastownhall/gascity/cmd/gc" {
					t.Errorf("canonical provider-bypass nudge can hide outside the typed producer census: %s:%s", registration.BoundaryID, key)
				}
			}
		}
		switch fn.Object.Package {
		case "github.com/gastownhall/gascity/cmd/gc":
			canonicalDirect[key] = true
		case "github.com/gastownhall/gascity/internal/worker":
			workerExecutor[key] = true
		}
	}

	wantDirect := map[string]bool{
		"cmd/gc/idle_nudge.go||nudgeStalledPoolClaims":   false,
		"cmd/gc/status_provider.go|statusProvider|Nudge": false,
	}
	for key := range canonicalDirect {
		if _, expected := wantDirect[key]; !expected {
			t.Errorf("canonical cmd/gc provider-nudge site lacks typed producer/exclusion ownership: %s", key)
			continue
		}
		wantDirect[key] = true
	}
	for key, found := range wantDirect {
		if !found {
			t.Errorf("typed provider-nudge census is not backed by canonical physical inventory: %s", key)
		}
	}
	for _, key := range []string{
		"internal/worker/runtime_handle.go|RuntimeHandle|Nudge",
		"internal/worker/runtime_handle.go|RuntimeHandle|nudgeNow",
		"internal/worker/runtime_handle.go|RuntimeHandle|nudgeWaitIdle",
	} {
		if !workerExecutor[key] {
			t.Errorf("canonical inventory lacks shared worker executor %s", key)
		}
	}
}

func TestInteractionRespondIsTypedAndCannotSatisfyNudgeCommandCoverage(t *testing.T) {
	contracts := productionNudgeEffectContracts()
	var interactions []nudgeEffectContract
	for _, contract := range contracts {
		if contract.class == nudgeEffectDistinctInteraction {
			interactions = append(interactions, contract)
		}
	}
	if len(interactions) != 2 {
		t.Fatalf("distinct interaction contracts = %d, want exactly 2", len(interactions))
	}
	wantInteractionSites := map[nudgeEffectSite]bool{
		{ownerFile: "cmd/gc/worker_handle.go", ownerFunction: "workerRespondSessionTargetWithConfig", kind: nudgeEffectWorkerRespond, ordinal: 1}:                              false,
		{ownerFile: "internal/api/handler_session_interaction.go", ownerReceiver: "Server", ownerFunction: "handleSessionRespond", kind: nudgeEffectWorkerRespond, ordinal: 1}: false,
	}
	for _, contract := range interactions {
		if contract.kind != nudgeEffectWorkerRespond || contract.hasRoute {
			t.Fatalf("interaction contract = %#v, want typed worker Respond exclusion", contract)
		}
		if _, ok := wantInteractionSites[contract.site()]; !ok {
			t.Fatalf("unexpected interaction contract = %#v", contract)
		}
		wantInteractionSites[contract.site()] = true
	}
	for site, found := range wantInteractionSites {
		if !found {
			t.Errorf("missing typed interaction exclusion %s", site)
		}
	}

	respondType := reflect.TypeOf(workerRespondSessionTargetWithConfig)
	wantResponseType := reflect.TypeOf(worker.InteractionResponse{})
	if respondType.NumIn() != 6 || respondType.In(5) != wantResponseType {
		t.Fatalf("workerRespondSessionTargetWithConfig type = %v, want final worker.InteractionResponse parameter", respondType)
	}
	requestID, ok := wantResponseType.FieldByName("RequestID")
	if !ok || requestID.Type.Kind() != reflect.String {
		t.Fatalf("worker.InteractionResponse does not carry the pending request identity: %v", wantResponseType)
	}
}

func TestProductionBindingCannotMutablyPromiseProducerCoverage(t *testing.T) {
	if _, ok := reflect.TypeOf(productionNudgeAuthorityBinding{}).FieldByName("commandProducersCovered"); ok {
		t.Fatal("production binding retains mutable commandProducersCovered promise")
	}
}

func TestCLIImmediateDurableIngressTerminatesBeforeLegacyProviderFallback(t *testing.T) {
	_, file := parseGCTestSource(t, "cmd_nudge.go")
	fn := findGCFunction(t, file, "deliverSessionNudgeWithIngress")

	var immediate *ast.IfStmt
	for _, statement := range fn.Body.List {
		candidate, ok := statement.(*ast.IfStmt)
		if !ok || !astExprCallsOrNames(candidate.Cond, "nudgeDeliveryImmediate") {
			continue
		}
		immediate = candidate
		break
	}
	if immediate == nil {
		t.Fatal("deliverSessionNudgeWithIngress lacks the immediate durable-admission branch")
	}

	var disposition *ast.SwitchStmt
	ast.Inspect(immediate.Body, func(node ast.Node) bool {
		candidate, ok := node.(*ast.SwitchStmt)
		if !ok || !astExprCallsOrNames(candidate.Tag, "Disposition") {
			return true
		}
		disposition = candidate
		return false
	})
	if disposition == nil {
		t.Fatal("immediate durable-admission branch lacks a result disposition switch")
	}

	wantCases := map[string]bool{
		"nudgeBridgeDurableAccepted":     false,
		"nudgeBridgeLegacyOwnership":     false,
		"nudgeBridgePreEntryUnavailable": false,
		"nudgeBridgeMayHaveEntered":      false,
		"default":                        false,
	}
	for _, clauseNode := range disposition.Body.List {
		clause, ok := clauseNode.(*ast.CaseClause)
		if !ok {
			continue
		}
		labels := make([]string, 0, len(clause.List))
		if clause.List == nil {
			labels = append(labels, "default")
		}
		for _, label := range clause.List {
			ident, ok := label.(*ast.Ident)
			if !ok {
				t.Fatalf("disposition case label has type %T, want identifier", label)
			}
			labels = append(labels, ident.Name)
		}
		for _, label := range labels {
			if _, known := wantCases[label]; !known {
				t.Fatalf("unclassified durable-ingress disposition case %q", label)
			}
			wantCases[label] = true
		}

		mustTerminate := false
		for _, label := range labels {
			switch label {
			case "nudgeBridgeDurableAccepted", "nudgeBridgeMayHaveEntered", "default":
				mustTerminate = true
			}
		}
		if mustTerminate {
			if len(clause.Body) == 0 {
				t.Fatalf("durable-ingress disposition %v has an empty fallthrough body", labels)
			}
			if _, ok := clause.Body[len(clause.Body)-1].(*ast.ReturnStmt); !ok {
				t.Fatalf("durable-ingress disposition %v does not terminate before provider fallback", labels)
			}
		}
		for _, label := range labels {
			if label != "nudgeBridgeDurableAccepted" {
				continue
			}
			if len(clause.Body) != 1 || !astExprCallsOrNames(clause.Body[0], "writeAcceptedSessionNudgeResult") {
				t.Fatalf("durable acceptance body = %#v, want one accepted-result return", clause.Body)
			}
		}
	}
	for label, found := range wantCases {
		if !found {
			t.Fatalf("durable-ingress disposition switch lacks %q", label)
		}
	}

	providerFallbacks := 0
	var fallbackAfterImmediate bool
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || calledFunction(call) != "deliverSessionNudgeWithWorker" {
			return true
		}
		providerFallbacks++
		if call.Pos() > immediate.End() {
			fallbackAfterImmediate = true
		}
		return true
	})
	if providerFallbacks != 1 || !fallbackAfterImmediate {
		t.Fatalf("legacy provider fallbacks = %d after-immediate=%t, want one fallback after terminating durable cases", providerFallbacks, fallbackAfterImmediate)
	}
}

func astExprCallsOrNames(node ast.Node, name string) bool {
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.Ident:
			found = found || typed.Name == name
		case *ast.CallExpr:
			found = found || calledFunction(typed) == name
		}
		return !found
	})
	return found
}

type productionNudgeDiscovery struct {
	effects map[nudgeEffectSite]bool
	owners  map[nudgeEffectOwner]bool
}

func discoverProductionNudgeEffects(t *testing.T) productionNudgeDiscovery {
	t.Helper()
	discovery := productionNudgeDiscovery{
		effects: make(map[nudgeEffectSite]bool),
		owners:  make(map[nudgeEffectOwner]bool),
	}
	for _, pkg := range loadProductionPackages(t, "./...") {
		for index, file := range pkg.Syntax {
			filename := productionPackageFilename(t, pkg, index)
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				owner := nudgeEffectOwner{
					ownerFile:     filename,
					ownerReceiver: gcReceiverName(fn.Recv),
					ownerFunction: fn.Name.Name,
				}
				if discovery.owners[owner] {
					t.Fatalf("typed discovery produced duplicate production owner %s", owner)
				}
				discovery.owners[owner] = true
				ordinals := make(map[nudgeEffectKind]uint8)
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					kind, relevant := productionNudgeCallKind(pkg.PkgPath, pkg.TypesInfo, call)
					if !relevant {
						return true
					}
					ordinals[kind]++
					site := nudgeEffectSite{
						ownerFile:     filename,
						ownerReceiver: owner.ownerReceiver,
						ownerFunction: fn.Name.Name,
						kind:          kind,
						ordinal:       ordinals[kind],
					}
					if discovery.effects[site] {
						t.Fatalf("typed discovery produced duplicate nudge effect site %s", site)
					}
					discovery.effects[site] = true
					return true
				})
			}
		}
	}
	return discovery
}

func loadProductionPackages(t *testing.T, patterns ...string) []*packages.Package {
	t.Helper()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	loaded, err := packages.Load(&packages.Config{
		Dir: repoRoot,
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedDeps,
		Fset: token.NewFileSet(),
	}, patterns...)
	if err != nil {
		t.Fatalf("loading production packages %v: %v", patterns, err)
	}
	if packages.PrintErrors(loaded) != 0 {
		t.Fatalf("loading production packages %v reported errors", patterns)
	}
	if len(loaded) == 0 {
		t.Fatalf("loading production packages %v returned no packages", patterns)
	}
	return loaded
}

func productionPackageFilename(t *testing.T, pkg *packages.Package, index int) string {
	t.Helper()
	if index >= len(pkg.CompiledGoFiles) {
		t.Fatalf("package %s syntax/file mismatch: syntax index %d, compiled files %d", pkg.PkgPath, index, len(pkg.CompiledGoFiles))
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	relative, err := filepath.Rel(repoRoot, pkg.CompiledGoFiles[index])
	if err != nil {
		t.Fatalf("relativizing %s: %v", pkg.CompiledGoFiles[index], err)
	}
	return filepath.ToSlash(relative)
}

func gcReceiverName(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) != 1 {
		return ""
	}
	return gcReceiverTypeName(fields.List[0].Type)
}

func gcReceiverTypeName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return gcReceiverTypeName(typed.X)
	case *ast.IndexExpr:
		return gcReceiverTypeName(typed.X)
	case *ast.IndexListExpr:
		return gcReceiverTypeName(typed.X)
	default:
		return ""
	}
}

func productionNudgeCallKind(ownerPackage string, info *types.Info, call *ast.CallExpr) (nudgeEffectKind, bool) {
	if ident, ok := call.Fun.(*ast.Ident); ok {
		if variable, ok := info.Uses[ident].(*types.Var); ok && namedType(variable.Type(), "github.com/gastownhall/gascity/cmd/gc", "localNudgeIngressAdmitter") {
			return nudgeEffectLocalIngress, true
		}
	}
	object := calledTypesObject(info, call.Fun)
	fn, ok := object.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return 0, false
	}
	packagePath := fn.Pkg().Path()
	switch {
	case packagePath == "github.com/gastownhall/gascity/internal/worker" && fn.Name() == "Nudge":
		return nudgeEffectWorkerNudge, true
	case productionNudgeSurfacePackage(ownerPackage) && packagePath == "github.com/gastownhall/gascity/internal/runtime" && fn.Name() == "Nudge":
		return nudgeEffectProviderNudge, true
	case packagePath == "github.com/gastownhall/gascity/cmd/gc" && fn.Name() == "enqueueManagedNudgeThenWake":
		return nudgeEffectLegacyManagedEnqueue, true
	case packagePath == "github.com/gastownhall/gascity/cmd/gc" && fn.Name() == "enqueueQueuedNudge":
		return nudgeEffectLegacyQueueEnqueue, true
	case packagePath == "github.com/gastownhall/gascity/cmd/gc" && fn.Name() == "enqueueQueuedNudgeWithStore":
		return nudgeEffectLegacyQueueStoreEnqueue, true
	case packagePath == "github.com/gastownhall/gascity/cmd/gc" && fn.Name() == "sendMailNotify":
		return nudgeEffectExternalMail, true
	case packagePath == "github.com/gastownhall/gascity/internal/api" && fn.Name() == "AdmitSessionNudge":
		return nudgeEffectTypedAPIAdmission, true
	case packagePath == "github.com/gastownhall/gascity/internal/worker" && fn.Name() == "Respond":
		return nudgeEffectWorkerRespond, true
	default:
		return 0, false
	}
}

func productionNudgeSurfacePackage(packagePath string) bool {
	return packagePath == "github.com/gastownhall/gascity/cmd/gc" ||
		packagePath == "github.com/gastownhall/gascity/internal/api"
}

func calledTypesObject(info *types.Info, expression ast.Expr) types.Object {
	switch typed := expression.(type) {
	case *ast.Ident:
		return info.Uses[typed]
	case *ast.SelectorExpr:
		if selection := info.Selections[typed]; selection != nil {
			return selection.Obj()
		}
		return info.Uses[typed.Sel]
	default:
		return nil
	}
}

func namedType(value types.Type, packagePath, name string) bool {
	named, ok := value.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == packagePath && named.Obj().Name() == name
}

func registrationHasNudgeRoute(registration effectinventory.SiteRegistration) bool {
	for _, profileCase := range registration.Cases {
		for _, route := range profileCase.Routes {
			if route.ActionFamily == effectinventory.FamilyNudge {
				return true
			}
		}
	}
	return false
}

func (s nudgeEffectSite) String() string {
	owner := s.ownerFunction
	if s.ownerReceiver != "" {
		owner = s.ownerReceiver + "." + owner
	}
	return fmt.Sprintf("%s:%s:%d:%d", s.ownerFile, owner, s.kind, s.ordinal)
}

func (o nudgeEffectOwner) String() string {
	owner := o.ownerFunction
	if o.ownerReceiver != "" {
		owner = o.ownerReceiver + "." + owner
	}
	return o.ownerFile + ":" + owner
}
