package main

import (
	"strings"
	"testing"
)

func TestWorkQueryProbeOwnShellAndNestedShellHaveRoutingWithoutOwnership(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin:/bin",
		"GC_SESSION_ID=fixture-sid",
		"GC_TEMPLATE=fixture-template",
		"GC_RUNTIME_EPOCH=2",
		"GC_INSTANCE_TOKEN=fixture-token",
		"BEADS_HOLDER_TOKEN=fixture-token",
		"GC_WORK_QUERY_SESSION_ID=stale-alias",
	}
	probe, err := workQueryProbeEnv(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range probe {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN":
			t.Fatalf("owner key %s reached work-query child", key)
		case "GC_WORK_QUERY_SESSION_ID":
			if entry != "GC_WORK_QUERY_SESSION_ID=fixture-sid" {
				t.Fatal("routing alias was not rebuilt from current SID")
			}
		}
	}
	// The command reports only key presence and whether the routing alias is
	// correct; it never prints an environment value or token.
	command := `sh -c 'if env | grep -Eq "^(GC_(SESSION_ID|TEMPLATE|RUNTIME_EPOCH|INSTANCE_TOKEN)|BEADS_HOLDER_TOKEN)="; then printf owner; else printf clean; fi; if [ "$GC_WORK_QUERY_SESSION_ID" = fixture-sid ]; then printf route; else printf noroute; fi'`
	out, err := shellWorkQueryWithEnv(command, "", parent)
	if err != nil {
		t.Fatal(err)
	}
	if out != "cleanroute" {
		t.Fatalf("nested work-query child flags = %q", out)
	}
}

func TestWorkQueryProbeRejectsLegacyCustomOwnerVariable(t *testing.T) {
	_, err := shellWorkQueryWithEnv(`printf '%s' "$GC_SESSION_ID"`, "", []string{"PATH=/usr/bin:/bin", "GC_SESSION_ID=fixture-sid"})
	if err == nil || err.Error() != "work_query references GC_SESSION_ID; use GC_WORK_QUERY_SESSION_ID for routing" {
		t.Fatalf("legacy owner variable diagnostic = %v", err)
	}
}

func TestWorkQueryProbeAcceptsGeneratedSIDFallbackWithoutOwnerEnv(t *testing.T) {
	command := `sh -c 'printf "%s" "${GC_WORK_QUERY_SESSION_ID:-$GC_SESSION_ID}"'`
	out, err := shellWorkQueryWithEnv(command, "", []string{
		"PATH=/usr/bin:/bin", "GC_SESSION_ID=fixture-sid", "GC_TEMPLATE=worker",
		"GC_RUNTIME_EPOCH=fixture-epoch", "GC_INSTANCE_TOKEN=fixture-token",
		"BEADS_HOLDER_TOKEN=fixture-holder",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "fixture-sid" {
		t.Fatalf("nested generated fallback = %q, want routed SID", out)
	}
}

func TestWorkQueryProbeRejectsAmbiguousOrInvalidSID(t *testing.T) {
	for _, env := range [][]string{
		{"GC_SESSION_ID=one", "GC_SESSION_ID=two"},
		{"GC_SESSION_ID=bad\nline"},
	} {
		if _, err := workQueryProbeEnv(env); err == nil {
			t.Fatal("ambiguous or invalid SID accepted")
		}
	}
}
