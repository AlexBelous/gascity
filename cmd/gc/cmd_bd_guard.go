package main

// Fail-closed `gc bd` write guard (engdocs/design/infra-class-sqlite-stores.md,
// "The bd / raw-prompt story" item 3): doBd refuses any write that targets an
// infra-class bead — a mutation of a reserved-prefix id, or a create carrying
// an infra type/label — with a message naming the `gc` replacement. Reserved
// class prefixes (gco/gcn/gcm/gcs/gcg) are minted only by the embedded class
// stores and never exist in bd, and beadPolicyStore.createTarget is identity
// on the CLI path, so without this guard a stray prompt could mint a
// divergent infra bead in the work db that no routed reader ever sees.
// Reads (`gc bd show`, `list`) stay unguarded; by-id show federates instead.

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/bdflags"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
)

// infraClassBdReplacement names the gc surface that owns each relocated
// class, for the refusal message.
var infraClassBdReplacement = map[string]string{
	config.BeadClassGraph:     "gc mol (graph beads are controller-managed)",
	config.BeadClassMessaging: "gc mail",
	config.BeadClassSessions:  "gc session",
	config.BeadClassOrders:    "gc order (order tracking is dispatcher-managed)",
	config.BeadClassNudges:    "gc nudge",
}

// reservedClassForBeadID returns the relocated class whose reserved prefix
// owns id's namespace, if any.
func reservedClassForBeadID(id string) (string, bool) {
	for class, prefix := range config.ReservedClassPrefixes() {
		if strings.HasPrefix(id, prefix+"-") {
			return class, true
		}
	}
	return "", false
}

// bdInfraWriteRefusal reports whether bdArgs is a bd write targeting an
// infra-class bead, and the operator-facing refusal message. Mutations
// (update/close/reopen/delete, plus the gc-only release-if-current) are
// judged by their positional ids' reserved prefixes; creates are judged by
// classifying the declared type/labels. Anything else — including an
// ambiguous arg scan, which the exact-ID guard already fails closed — is
// not this guard's business.
func bdInfraWriteRefusal(bdArgs []string) (string, bool) {
	if len(bdArgs) == 0 {
		return "", false
	}
	if bdArgs[0] == "create" {
		return bdCreateInfraRefusal(bdArgs)
	}
	var ids []string
	if bdArgs[0] == "release-if-current" && len(bdArgs) >= 2 {
		ids = bdArgs[1:2]
	} else if mutIDs, ok, ambiguous := bdMutationWriteIDs(bdArgs); ok && !ambiguous {
		ids = mutIDs
	}
	for _, id := range ids {
		if class, found := reservedClassForBeadID(id); found {
			return fmt.Sprintf("refusing %s of %s: %s-class beads live in the embedded class store (.gc/store), not bd; use %s",
				bdArgs[0], id, class, infraClassBdReplacement[class]), true
		}
	}
	return "", false
}

// bdCreateInfraRefusal classifies a `bd create` from its declared --type and
// --labels values (plus --wisp-type, which declares a wisp) and refuses any
// create that classifies off ClassWork. The walk mirrors bdMutationWriteIDs'
// value-flag handling; an unrecognized flag falls through unguarded — a
// create is not id-targeted, and bd's own parser owns malformed input.
func bdCreateInfraRefusal(bdArgs []string) (string, bool) {
	valueFlags := bdflags.ValueFlags("create")
	var beadType string
	var labels []string
	positional := false
	takeValue := func(flag string, i int) (string, int) {
		if eq := strings.IndexByte(flag, '='); eq >= 0 {
			return flag[eq+1:], i
		}
		if i+1 < len(bdArgs) {
			return bdArgs[i+1], i + 1
		}
		return "", i
	}
	for i := 1; i < len(bdArgs); i++ {
		arg := bdArgs[i]
		if positional || !strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "--" {
			positional = true
			continue
		}
		name := arg
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		switch name {
		case "--type", "-t":
			beadType, i = takeValue(arg, i)
		case "--labels", "-l":
			var raw string
			raw, i = takeValue(arg, i)
			for _, l := range strings.Split(raw, ",") {
				if l = strings.TrimSpace(l); l != "" {
					labels = append(labels, l)
				}
			}
		case "--wisp-type":
			return fmt.Sprintf("refusing create of a wisp via bd: graph-class beads live in the embedded class store (.gc/store), not bd; use %s",
				infraClassBdReplacement[config.BeadClassGraph]), true
		default:
			if !strings.Contains(arg, "=") && valueFlags[name] {
				i++ // skip the flag's value
			}
		}
	}
	class := coordclass.Classify(beads.Bead{Type: beadType, Labels: labels})
	if class == coordclass.ClassWork {
		return "", false
	}
	name := class.String()
	return fmt.Sprintf("refusing create of a %s-class bead via bd (type=%q labels=%v): it lives in the embedded class store (.gc/store), not bd; use %s",
		name, beadType, labels, infraClassBdReplacement[name]), true
}
