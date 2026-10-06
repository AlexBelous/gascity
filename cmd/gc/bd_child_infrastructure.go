package main

import (
	"context"
	goruntime "runtime"

	"github.com/gastownhall/gascity/internal/beads"
)

var beadsExecCommandRunnerWithExactEntriesContext = beads.ExecCommandRunnerWithExactEntriesContext

func beadsInfrastructureRunnerForHostedCity(cityPath string, env map[string]string) (beads.CommandRunner, error) {
	if goruntime.GOOS != "linux" {
		return beadsCommandRunnerForHostedCity(cityPath, env)
	}
	hosted, err := citySelectsHostedBeadsCredentialProvider(cityPath)
	if err != nil {
		return nil, err
	}
	projected, err := projectBDControllerChild(processEnvSnapshotExcludingNativeDoltOpen(), env, hosted)
	if err != nil {
		return nil, err
	}
	return beadsExecCommandRunnerWithExactEntriesContext(context.Background(), projected), nil
}
