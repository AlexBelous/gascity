package main

import "testing"

// TestFormatDeadAssigneeReopenedMessage guards ga-x3ofe0's BLOCKER finding:
// the message must not call a route name masquerading as an assignee a "dead
// session" — that assignee was never a session identity at all, it was the
// bare route/template name left in place by an ephemeral pool dispatch that
// no live session ever claimed. Genuine dead-session wording (a real session
// identity that has since closed) is preserved unchanged.
func TestFormatDeadAssigneeReopenedMessage(t *testing.T) {
	tests := []struct {
		name         string
		beadID       string
		deadAssignee string
		routedTo     string
		want         string
	}{
		{
			name:         "genuine dead session identity keeps dead-session wording",
			beadID:       "ga-abc123",
			deadAssignee: "worker-mc-dead",
			routedTo:     "worker",
			want:         "reopened routed work ga-abc123 assigned to dead session worker-mc-dead (route worker); assignee cleared so the pool can reclaim it",
		},
		{
			name:         "assignee is the bare route name — never a real session",
			beadID:       "ga-def456",
			deadAssignee: "worker",
			routedTo:     "worker",
			want:         "reopened routed work ga-def456 routed to worker with no live session claiming it; assignee cleared so the pool can reclaim it",
		},
		{
			name:         "unknown assignee falls back to dead-session wording unchanged",
			beadID:       "ga-ghi789",
			deadAssignee: "",
			routedTo:     "worker",
			want:         "reopened routed work ga-ghi789 assigned to dead session <unknown> (route worker); assignee cleared so the pool can reclaim it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDeadAssigneeReopenedMessage(tt.beadID, tt.deadAssignee, tt.routedTo)
			if got != tt.want {
				t.Fatalf("formatDeadAssigneeReopenedMessage(%q, %q, %q) = %q, want %q", tt.beadID, tt.deadAssignee, tt.routedTo, got, tt.want)
			}
		})
	}
}
