package main

import (
	"sort"
	"strings"
)

func projectBDControllerChild(base []string, overrides map[string]string, hosted bool) ([]string, error) {
	input := append([]string(nil), base...)
	if hosted {
		// Preserve original tool attribution while retaining the hosted runner's
		// existing removal of every other ambient BEADS_* input.
		retained := []string{}
		for _, e := range input {
			k, _, _ := strings.Cut(e, "=")
			if !strings.HasPrefix(k, "BEADS_") || k == "BEADS_ACTOR" {
				retained = append(retained, e)
			}
		}
		input = retained
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		retained := []string{}
		for _, e := range input {
			k, _, _ := strings.Cut(e, "=")
			if k != key {
				retained = append(retained, e)
			}
		}
		input = retained
		input = append(input, key+"="+overrides[key])
	}
	return projectBDInfrastructureChild(bdChildInfrastructure, input)
}
