package main

import (
	"strings"
	"testing"
)

func TestRemoteOperatorProfileStrictJSON(t *testing.T) {
	for _, input := range []string{`{"version":1,"version":1,"profiles":[]}`, `{"version":1,"profiles":[],"unexpected":true}`, `{"version":1,"profiles":[]} {}`} {
		if _, err := decodeRemoteOperatorProfiles(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted ambiguous profile %s", input)
		}
	}
	if _, err := decodeRemoteOperatorProfiles(strings.NewReader(`{"version":1,"profiles":[]}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteOperatorProfileRejectsIncomplete(t *testing.T) {
	if err := validateRemoteOperatorProfile(remoteOperatorProfile{}); err == nil {
		t.Fatal("accepted empty profile")
	}
}
