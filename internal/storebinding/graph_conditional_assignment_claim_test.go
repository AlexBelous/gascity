package storebinding

import (
	"reflect"
	"testing"
)

// TestGraphStoreDeclaresConditionalAssignmentClaim keeps the atomic transfer
// on the closed class contract. A method present only on one concrete provider
// is unusable to hook claim routing, whose only handle is GraphStore.
func TestGraphStoreDeclaresConditionalAssignmentClaim(t *testing.T) {
	contract := reflect.TypeOf((*GraphStore)(nil)).Elem()
	method, ok := contract.MethodByName("ClaimIfCurrent")
	if !ok {
		t.Fatal("GraphStore does not declare ClaimIfCurrent; class-bound pool work cannot use the atomic transfer")
	}
	if got, want := method.Type.NumIn(), 3; got != want {
		t.Fatalf("GraphStore.ClaimIfCurrent arguments = %d, want %d (id, expected assignee, concrete assignee)", got, want)
	}
	if got, want := method.Type.NumOut(), 3; got != want {
		t.Fatalf("GraphStore.ClaimIfCurrent results = %d, want %d (bead, won, error)", got, want)
	}
}
