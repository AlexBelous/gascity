package session

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

type remoteLaunchCaptureProvider struct {
	*remoteAdmissionTestProvider
	configs     []runtime.Config
	beforeStart func()
}

func (p *remoteLaunchCaptureProvider) Start(ctx context.Context, name string, cfg runtime.Config) error {
	if p.beforeStart != nil {
		p.beforeStart()
	}
	p.configs = append(p.configs, cfg)
	return p.remoteAdmissionTestProvider.Start(ctx, name, cfg)
}

func remoteLaunchFixtureProfile(a *remoteAdmissionTestAuthority) {
	a.profile.Launch = RemoteLaunchDescriptor{Command: "/usr/bin/sudo -n /usr/local/libexec/gascity-remote-entry enter", WorkDir: "/srv/fixture/transport"}
}

func remoteLaunchFixtureConfig(original runtime.Config, p RemoteAdmissionProfile, r RemoteAdmissionReceipt) runtime.Config {
	env := map[string]string{}
	for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_SESSION_NAME", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "GC_CITY"} {
		if value, ok := original.Env[key]; ok {
			env[key] = value
		}
	}
	return runtime.Config{Command: p.Launch.Command + " " + r.Request.Attempt, WorkDir: p.Launch.WorkDir, Env: env, Lifecycle: runtime.LifecycleOneShot, ProcessNames: []string{"sudo"}}
}

func TestRemoteLaunchStagesLiteralAndUsesFixedBridge(t *testing.T) {
	city, store, row, base, a := remoteAdmissionFixture(t)
	remoteLaunchFixtureProfile(a)
	provider := &remoteLaunchCaptureProvider{remoteAdmissionTestProvider: base}
	original := remoteAdmissionConfig(row)
	literal := "literal\n$(do-not-execute) ' ` ;"
	original.PromptText = runtime.PromptTextFromLiteral(literal)
	original.PromptSuffix = "untrusted quoted suffix"
	original.PreStart = []string{"must not execute"}
	original.Env["GC_CITY"] = city
	original.Env["OPERATOR_VALUE"] = "must not forward"
	staged := ""
	a.profile.Begin = func(_ context.Context, r RemoteAdmissionReceipt, cfg runtime.Config) (RemoteBeginReceipt, runtime.Config, error) {
		staged = cfg.PromptText.Literal()
		return RemoteBeginReceipt{Reservation: r, IntentID: "intent"}, remoteLaunchFixtureConfig(original, a.profile, r), nil
	}
	ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", row.ID, time.Now(), provider, store)
	if !ok {
		t.Fatal(reason)
	}
	manager := NewManagerWithOptions(store, provider, WithCityPath(city))
	if err := manager.startRuntime(ctx, row.Metadata["session_name"], original); err != nil {
		t.Fatal(err)
	}
	if staged != literal || len(provider.configs) != 1 || !reflect.DeepEqual(provider.configs[0], remoteLaunchFixtureConfig(original, a.profile, a.prepared)) {
		t.Fatal("literal or fixed launch was not delivered exactly")
	}
}

func TestRemoteLaunchRejectsEveryUnreviewedStartupEffect(t *testing.T) {
	cases := map[string]func(*runtime.Config){
		"command": func(c *runtime.Config) { c.Command += " ; unexpected" }, "workdir": func(c *runtime.Config) { c.WorkDir = "/other" },
		"identity": func(c *runtime.Config) { c.Env["GC_SESSION_ID"] = "other" }, "extraenv": func(c *runtime.Config) { c.Env["EXTRA"] = "value" },
		"scope": func(c *runtime.Config) { c.CommandScope = &runtime.CommandScope{} }, "prestart": func(c *runtime.Config) { c.PreStart = []string{"effect"} },
		"setup": func(c *runtime.Config) { c.SessionSetup = []string{"effect"} }, "live": func(c *runtime.Config) { c.SessionLive = []string{"effect"} },
		"nudge": func(c *runtime.Config) { c.Nudge = "effect" }, "quotedprompt": func(c *runtime.Config) { c.PromptSuffix = "effect" },
		"literal": func(c *runtime.Config) { c.PromptText = runtime.PromptTextFromLiteral("effect") }, "operatorenv": func(c *runtime.Config) { c.OperatorEnv = map[string]string{"KEY": "value"} },
		"lifecycle": func(c *runtime.Config) { c.Lifecycle = "" }, "process": func(c *runtime.Config) { c.ProcessNames = []string{"other"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			city, store, row, base, a := remoteAdmissionFixture(t)
			remoteLaunchFixtureProfile(a)
			provider := &remoteLaunchCaptureProvider{remoteAdmissionTestProvider: base}
			original := remoteAdmissionConfig(row)
			a.profile.Begin = func(_ context.Context, r RemoteAdmissionReceipt, cfg runtime.Config) (RemoteBeginReceipt, runtime.Config, error) {
				rewritten := remoteLaunchFixtureConfig(cfg, a.profile, r)
				mutate(&rewritten)
				return RemoteBeginReceipt{Reservation: r, IntentID: "intent"}, rewritten, nil
			}
			ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", row.ID, time.Now(), provider, store)
			if !ok {
				t.Fatal(reason)
			}
			manager := NewManagerWithOptions(store, provider, WithCityPath(city))
			err := manager.startRuntime(ctx, row.Metadata["session_name"], original)
			if err == nil || !IsRemoteAdmissionHeld(err) || !errors.Is(err, ErrCapacityAdmission) || len(provider.configs) != 0 || !a.occupied {
				t.Fatal("unreviewed config reached provider or released source")
			}
		})
	}
}

func TestRemoteLaunchCallbackCannotMutateCallerIdentityEnv(t *testing.T) {
	city, store, row, base, a := remoteAdmissionFixture(t)
	remoteLaunchFixtureProfile(a)
	provider := &remoteLaunchCaptureProvider{remoteAdmissionTestProvider: base}
	original := remoteAdmissionConfig(row)
	original.OperatorEnv = map[string]string{"KEY": "original"}
	a.profile.Begin = func(_ context.Context, r RemoteAdmissionReceipt, cfg runtime.Config) (RemoteBeginReceipt, runtime.Config, error) {
		rewritten := remoteLaunchFixtureConfig(cfg, a.profile, r)
		cfg.Env["GC_SESSION_ID"] = "mutated"
		cfg.OperatorEnv["KEY"] = "mutated"
		return RemoteBeginReceipt{Reservation: r, IntentID: "intent"}, rewritten, nil
	}
	ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", row.ID, time.Now(), provider, store)
	if !ok {
		t.Fatal(reason)
	}
	manager := NewManagerWithOptions(store, provider, WithCityPath(city))
	if err := manager.startRuntime(ctx, row.Metadata["session_name"], original); err != nil {
		t.Fatal(err)
	}
	if original.Env["GC_SESSION_ID"] != row.ID || original.OperatorEnv["KEY"] != "original" {
		t.Fatal("callback changed caller identity map")
	}
}

func TestRemoteLaunchIncompleteDescriptorCannotRegister(t *testing.T) {
	for _, descriptor := range []RemoteLaunchDescriptor{{}, {Command: "fixed-prefix"}, {Command: "fixed-prefix", WorkDir: "relative"}, {Command: "fixed-prefix\n", WorkDir: "/owned"}, {Command: "fixed-prefix", WorkDir: "/owned/../other"}} {
		_, _, _, _, a := remoteAdmissionFixture(t)
		a.profile.Launch = descriptor
		if _, err := WithRemoteAdmissionProfiles(context.Background(), a.profile); err == nil || a.prepares != 0 {
			t.Fatal("incomplete launch profile reached authority")
		}
	}
}

func remoteLaunchReferenceConfig() runtime.Config {
	accepted := true
	return runtime.Config{
		Env: map[string]string{"k": "original"}, OperatorEnv: map[string]string{"k": "original"}, FingerprintExtra: map[string]string{"k": "original"},
		MCPServers:      []runtime.MCPServerConfig{{Name: "original", Args: []string{"original"}, Env: map[string]string{"k": "original"}, Headers: map[string]string{"k": "original"}}},
		StartupEnvelope: json.RawMessage(`{"value":"original"}`), ProcessNames: []string{"original"}, AcceptStartupDialogs: &accepted,
		PreStart: []string{"original"}, SessionSetup: []string{"original"}, SessionLive: []string{"original"}, InstallAgentHooks: []string{"original"}, PackOverlayDirs: []string{"original"}, CopyFiles: []runtime.CopyEntry{{Src: "original", RelDst: "original"}},
		CommandScope: &runtime.CommandScope{Launcher: "original", EntryCommand: []string{"original"}}, PromptText: runtime.PromptTextFromLiteral("original"),
	}
}

func TestRemoteLaunchCloneOwnsAllMutableBranches(t *testing.T) {
	original := remoteLaunchReferenceConfig()
	cloned := cloneRemoteLaunchInput(original)
	if !reflect.DeepEqual(cloned, original) {
		t.Fatal("clone changed config values")
	}
	cloned.Env["k"] = "changed"
	cloned.OperatorEnv["k"] = "changed"
	cloned.FingerprintExtra["k"] = "changed"
	cloned.MCPServers[0].Name = "changed"
	cloned.MCPServers[0].Args[0] = "changed"
	cloned.MCPServers[0].Env["k"] = "changed"
	cloned.MCPServers[0].Headers["k"] = "changed"
	cloned.StartupEnvelope[0] = '!'
	cloned.ProcessNames[0] = "changed"
	*cloned.AcceptStartupDialogs = false
	cloned.PreStart[0] = "changed"
	cloned.SessionSetup[0] = "changed"
	cloned.SessionLive[0] = "changed"
	cloned.InstallAgentHooks[0] = "changed"
	cloned.PackOverlayDirs[0] = "changed"
	cloned.CopyFiles[0].Src = "changed"
	cloned.CommandScope.Launcher = "changed"
	cloned.CommandScope.EntryCommand[0] = "changed"
	if !reflect.DeepEqual(original, remoteLaunchReferenceConfig()) {
		t.Fatal("callback config aliases caller mutable data")
	}
}

func TestRemoteLaunchCloneAccountsForConfigReferenceFields(t *testing.T) {
	covered := map[string]bool{"Env": true, "OperatorEnv": true, "FingerprintExtra": true, "MCPServers": true, "StartupEnvelope": true, "ProcessNames": true, "AcceptStartupDialogs": true, "PreStart": true, "SessionSetup": true, "SessionLive": true, "InstallAgentHooks": true, "PackOverlayDirs": true, "CopyFiles": true, "CommandScope": true}
	typ := reflect.TypeOf(runtime.Config{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		switch field.Type.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface, reflect.Func, reflect.Chan:
			if !covered[field.Name] {
				t.Fatalf("Config.%s needs clone ownership and alias coverage", field.Name)
			}
		}
	}
}

func TestRemoteLaunchOwnsValidatedOutputBeforeTransport(t *testing.T) {
	city, store, row, base, a := remoteAdmissionFixture(t)
	remoteLaunchFixtureProfile(a)
	provider := &remoteLaunchCaptureProvider{remoteAdmissionTestProvider: base}
	original := remoteAdmissionConfig(row)
	var returned runtime.Config
	a.profile.Begin = func(_ context.Context, r RemoteAdmissionReceipt, cfg runtime.Config) (RemoteBeginReceipt, runtime.Config, error) {
		returned = remoteLaunchFixtureConfig(cfg, a.profile, r)
		return RemoteBeginReceipt{Reservation: r, IntentID: "intent"}, returned, nil
	}
	provider.beforeStart = func() {
		returned.Env["GC_SESSION_ID"] = "mutated after validation"
		returned.ProcessNames[0] = "mutated"
	}
	ctx, ok, reason := AdmitCapacityStart(remoteAdmissionContext(t, a), city, "worker", row.ID, time.Now(), provider, store)
	if !ok {
		t.Fatal(reason)
	}
	manager := NewManagerWithOptions(store, provider, WithCityPath(city))
	if err := manager.startRuntime(ctx, row.Metadata["session_name"], original); err != nil {
		t.Fatal(err)
	}
	if len(provider.configs) != 1 || provider.configs[0].Env["GC_SESSION_ID"] != row.ID || provider.configs[0].ProcessNames[0] != "sudo" {
		t.Fatal("callback retained mutable ownership of validated transport config")
	}
}
