package rollout

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestResolveDaemonSessionStartReconciler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		want       Mode
		wantOrigin Origin
		wantErr    bool
	}{
		{name: "omitted", want: Off, wantOrigin: OriginBuiltin},
		{name: "off", raw: "off", want: Off, wantOrigin: OriginConfig},
		{name: "auto", raw: "auto", want: Auto, wantOrigin: OriginConfig},
		{name: "require", raw: "require", want: Require, wantOrigin: OriginConfig},
		{name: "invalid", raw: "enabled", wantErr: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			flags, err := Resolve(
				&config.City{Daemon: config.DaemonConfig{SessionStartReconciler: test.raw}},
				ResolveOptions{LookupEnv: func(string) (string, bool) { return "", false }},
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("Resolve error = %v, wantErr=%v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if got := flags.SessionStartReconciler(); got != test.want {
				t.Fatalf("SessionStartReconciler = %q, want %q", got, test.want)
			}
			if got := flags.OriginOf(KeyDaemonSessionStartReconciler); got != test.wantOrigin {
				t.Fatalf("OriginOf = %q, want %q", got, test.wantOrigin)
			}
			if got := flags.ValueOf(KeyDaemonSessionStartReconciler); got != string(test.want) {
				t.Fatalf("ValueOf = %q, want %q", got, test.want)
			}
		})
	}
}

func TestForTestSessionStartReconcilerIsInstanceLocal(t *testing.T) {
	t.Parallel()

	if got := ForTest().SessionStartReconciler(); got != Off {
		t.Fatalf("ForTest default = %q, want off", got)
	}
	auto := ForTest(WithSessionStartReconciler(Auto))
	require := ForTest(WithSessionStartReconciler(Require))
	off := ForTest(WithSessionStartReconciler(Off))
	if auto.SessionStartReconciler() != Auto ||
		require.SessionStartReconciler() != Require ||
		off.SessionStartReconciler() != Off {
		t.Fatalf(
			"ForTest modes = %q/%q/%q, want auto/require/off",
			auto.SessionStartReconciler(),
			require.SessionStartReconciler(),
			off.SessionStartReconciler(),
		)
	}
}

func TestZeroFlagsKeepsSessionStartReconcilerLegacy(t *testing.T) {
	t.Parallel()

	var flags Flags
	if got := flags.SessionStartReconciler(); got != ModeUnset {
		t.Fatalf("zero Flags session-start reconciler = %q, want ModeUnset", got)
	}
	if got := flags.OriginOf(KeyDaemonSessionStartReconciler); got != "" {
		t.Fatalf("zero Flags origin = %q, want empty", got)
	}
}

func TestSessionStartReconcilerRegistryBinding(t *testing.T) {
	t.Parallel()

	for _, spec := range Specs() {
		if spec.Key != KeyDaemonSessionStartReconciler {
			continue
		}
		if spec.ConfigPath != "daemon.session_start_reconciler" {
			t.Fatalf("ConfigPath = %q", spec.ConfigPath)
		}
		if spec.Default.Mode == nil || *spec.Default.Mode != Off {
			t.Fatalf("Default = %#v, want mode off", spec.Default)
		}
		if spec.EnvOverride != "" {
			t.Fatalf("EnvOverride = %q, want none for boot-latched ownership", spec.EnvOverride)
		}
		if spec.Owner.Bead != "ga-f7v2ft" {
			t.Fatalf("Owner.Bead = %q, want ga-f7v2ft", spec.Owner.Bead)
		}
		return
	}
	t.Fatalf("registry missing %s", KeyDaemonSessionStartReconciler)
}

func TestSessionStartReconcilerDefaultsDoNotDrift(t *testing.T) {
	t.Parallel()

	if got := defaultFlags().SessionStartReconciler(); got != Off {
		t.Fatalf("defaultFlags session-start reconciler = %q, want off", got)
	}
	if got := (config.DaemonConfig{}).SessionStartReconcilerMode(); got != string(Off) {
		t.Fatalf("config accessor default = %q, want %q", got, Off)
	}
}
