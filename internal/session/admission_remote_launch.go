package session

import (
	"errors"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
)

// RemoteLaunchDescriptor fixes the reviewed guarded entry prefix and workdir.
// The SDK appends only its public lower-hex attempt identifier, never a prompt
// or opaque permit. The root-owned backend validates the fixed entry prefix.
type RemoteLaunchDescriptor struct{ Command, WorkDir string }

func validRemoteLaunchDescriptor(d RemoteLaunchDescriptor) bool {
	return d.Command != "" && strings.TrimSpace(d.Command) == d.Command && !strings.ContainsAny(d.Command, "\x00\r\n") && path.IsAbs(d.WorkDir) && path.Clean(d.WorkDir) == d.WorkDir && !strings.ContainsAny(d.WorkDir, "\x00\r\n")
}

func guardedRemoteLaunchConfig(d RemoteLaunchDescriptor, r RemoteAdmissionRequest, original runtime.Config) (runtime.Config, error) {
	if !validRemoteLaunchDescriptor(d) || !remoteHex(r.Attempt, 32) {
		return runtime.Config{}, errors.New("remote_admission_launch_descriptor_invalid")
	}
	env := make(map[string]string, 6)
	for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_SESSION_NAME", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN"} {
		env[key] = original.Env[key]
	}
	if value, ok := original.Env["GC_CITY"]; ok {
		env["GC_CITY"] = value
	}
	return runtime.Config{Command: d.Command + " " + r.Attempt, WorkDir: d.WorkDir, Env: env, Lifecycle: runtime.LifecycleOneShot, ProcessNames: []string{"sudo"}}, nil
}

// cloneRemoteLaunchInput gives the callback ownership of every mutable Config
// branch while preserving exact input order, values, and nil/empty semantics.
func cloneRemoteLaunchInput(cfg runtime.Config) runtime.Config {
	cfg.Env = maps.Clone(cfg.Env)
	cfg.OperatorEnv = maps.Clone(cfg.OperatorEnv)
	cfg.FingerprintExtra = maps.Clone(cfg.FingerprintExtra)
	cfg.MCPServers = slices.Clone(cfg.MCPServers)
	for i := range cfg.MCPServers {
		server := &cfg.MCPServers[i]
		server.Args = slices.Clone(server.Args)
		server.Env = maps.Clone(server.Env)
		server.Headers = maps.Clone(server.Headers)
	}
	cfg.StartupEnvelope = slices.Clone(cfg.StartupEnvelope)
	cfg.ProcessNames = slices.Clone(cfg.ProcessNames)
	cfg.PreStart = slices.Clone(cfg.PreStart)
	cfg.SessionSetup = slices.Clone(cfg.SessionSetup)
	cfg.SessionLive = slices.Clone(cfg.SessionLive)
	cfg.InstallAgentHooks = slices.Clone(cfg.InstallAgentHooks)
	cfg.PackOverlayDirs = slices.Clone(cfg.PackOverlayDirs)
	cfg.CopyFiles = slices.Clone(cfg.CopyFiles)
	if cfg.AcceptStartupDialogs != nil {
		accepted := *cfg.AcceptStartupDialogs
		cfg.AcceptStartupDialogs = &accepted
	}
	if cfg.CommandScope != nil {
		scope := *cfg.CommandScope
		scope.EntryCommand = slices.Clone(scope.EntryCommand)
		cfg.CommandScope = &scope
	}
	return cfg
}

func validateRemoteLaunchConfig(expected, rewritten runtime.Config) error {
	// An allowlist by whole-value equality also rejects future startup knobs
	// until their safety under the fixed guard is explicitly reviewed.
	if !reflect.DeepEqual(expected, rewritten) {
		return errors.New("remote_admission_launch_config_invalid")
	}
	return nil
}
