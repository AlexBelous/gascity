package main

import (
	"strings"
	"testing"
)

// TestBdInfraWriteRefusalMutations pins the reserved-prefix arm: every bd
// mutation verb targeting a reserved class id is refused with a message
// naming the class and the gc replacement; work-prefixed ids pass.
func TestBdInfraWriteRefusalMutations(t *testing.T) {
	cases := []struct {
		args    []string
		refuse  bool
		mention string
	}{
		{[]string{"update", "gco-5", "--status", "closed"}, true, "gc order"},
		{[]string{"close", "gcs-12", "--reason", "done for the day today"}, true, "gc session"},
		{[]string{"delete", "gcn-3", "--force"}, true, "gc nudge"},
		{[]string{"reopen", "gcm-9"}, true, "gc mail"},
		{[]string{"update", "gcg-wisp-abc", "--claim"}, true, "gc mol"},
		{[]string{"release-if-current", "gcs-4", "worker-1"}, true, "gc session"},
		{[]string{"close", "gc-abc123"}, false, ""},
		{[]string{"update", "mc-77", "--priority", "1"}, false, ""},
		{[]string{"update", "gcodex-1"}, false, ""}, // prefix match is on "gco-", not "gco"
		{[]string{"show", "gco-5"}, false, ""},      // reads stay unguarded
		{[]string{"list", "--json"}, false, ""},
	}
	for _, tc := range cases {
		msg, refuse := bdInfraWriteRefusal(tc.args)
		if refuse != tc.refuse {
			t.Errorf("bdInfraWriteRefusal(%v) = (%q, %v), want refuse=%v", tc.args, msg, refuse, tc.refuse)
			continue
		}
		if refuse && !strings.Contains(msg, tc.mention) {
			t.Errorf("bdInfraWriteRefusal(%v) message %q does not name %q", tc.args, msg, tc.mention)
		}
	}
}

// TestBdInfraWriteRefusalCreate pins the create arm: a create whose declared
// type/labels classify off ClassWork is refused; plain work creates pass.
func TestBdInfraWriteRefusalCreate(t *testing.T) {
	cases := []struct {
		args    []string
		refuse  bool
		mention string
	}{
		{[]string{"create", "hello", "-t", "message"}, true, "gc mail"},
		{[]string{"create", "hello", "--type=session"}, true, "gc session"},
		{[]string{"create", "x", "--labels", "order-tracking,extra"}, true, "gc order"},
		{[]string{"create", "x", "-l", "gc:nudge"}, true, "gc nudge"},
		{[]string{"create", "x", "-l", "gc:session"}, true, "gc session"},
		{[]string{"create", "x", "-l", "gc:wait"}, true, "gc session"},
		{[]string{"create", "x", "--labels=gc:extmsg-binding"}, true, "gc mail"},
		{[]string{"create", "w", "--wisp-type", "patrol"}, true, "gc mol"},
		{[]string{"create", "x", "-t", "convergence"}, true, "gc mol"},
		{[]string{"create", "normal task", "-t", "task", "-l", "sprint-1", "-p", "1"}, false, ""},
		{[]string{"create", "titled message", "-t", "task", "-d", "type message in prose"}, false, ""},
		// A value flag whose value looks like an infra label must not confuse
		// the walk: --assignee consumes its value.
		{[]string{"create", "x", "--assignee", "gc:nudge", "-t", "task"}, false, ""},
	}
	for _, tc := range cases {
		msg, refuse := bdInfraWriteRefusal(tc.args)
		if refuse != tc.refuse {
			t.Errorf("bdInfraWriteRefusal(%v) = (%q, %v), want refuse=%v", tc.args, msg, refuse, tc.refuse)
			continue
		}
		if refuse && !strings.Contains(msg, tc.mention) {
			t.Errorf("bdInfraWriteRefusal(%v) message %q does not name %q", tc.args, msg, tc.mention)
		}
	}
}
