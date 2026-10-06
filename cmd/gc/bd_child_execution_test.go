package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestBDExecutionPositivePrivateCapability(t *testing.T) {
	raw, readers := bdStage0Fixture()
	final := []string{"GC_SESSION_ID=fixture-id", "GC_TEMPLATE=fixture-template", "GC_RUNTIME_EPOCH=2", "GC_CITY_PATH=/fixture-city", "GC_CITY=/fixture-city", "BEADS_ACTOR=fixture-actor", "GC_SIGNAL=fixture-signal", "OTHER=x"}
	before := append([]string(nil), final...)
	spawns := 0
	err := executeBDChild(context.Background(), "linux", raw, final, func(context.Context) ([]string, bdChildReaders, error) {
		return append([]string(nil), raw...), readers, nil
	}, func(_ context.Context, env bdChildEnvironment) error {
		spawns++
		tuple, city, err := parseBDChildRawEnv(env.entries)
		if err != nil || tuple.token != "fixture-only-fence" || city != "/fixture-city" {
			t.Fatal("original capability not restored privately")
		}
		for _, entry := range []string{"BEADS_ACTOR=fixture-actor", "GC_SIGNAL=fixture-signal", "OTHER=x"} {
			found := false
			for _, e := range env.entries {
				if e == entry {
					found = true
				}
			}
			if !found {
				t.Fatal("attribution/city/signal changed")
			}
		}
		return nil
	})
	if err != nil || spawns != 1 || !reflect.DeepEqual(final, before) {
		t.Fatal("positive private child failed or mutated parent")
	}
}

func TestBDExecutionUnknownNeverFallback(t *testing.T) {
	for _, mode := range []string{"partial", "empty", "duplicate", "acquire-error", "raw-mismatch", "projected-mismatch", "final-cancel"} {
		t.Run(mode, func(t *testing.T) {
			raw, r := bdStage0Fixture()
			ambient := append([]string(nil), raw...)
			final := append([]string(nil), raw...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "partial":
				ambient = []string{"GC_SESSION_ID=x"}
			case "empty":
				ambient = bdStage0Replace(ambient, "GC_INSTANCE_TOKEN", "")
			case "duplicate":
				ambient = append(ambient, "GC_INSTANCE_TOKEN=fixture-only-fence")
			case "projected-mismatch":
				final = bdStage0Replace(final, "GC_INSTANCE_TOKEN", "different")
			}
			spawns := 0
			acquire := func(context.Context) ([]string, bdChildReaders, error) {
				if mode == "acquire-error" {
					return nil, bdChildReaders{}, errors.New("fixture denied")
				}
				if mode == "raw-mismatch" {
					raw = bdStage0Replace(raw, "GC_INSTANCE_TOKEN", "different")
				}
				if mode == "final-cancel" {
					cancel()
				}
				return raw, r, nil
			}
			err := executeBDChild(ctx, "linux", ambient, final, acquire, func(context.Context, bdChildEnvironment) error { spawns++; return nil })
			if err == nil || spawns != 0 {
				t.Fatal("UNKNOWN fell through to child spawn")
			}
		})
	}
}

type bdExecutionFixtureResult struct{ message string }

func (e *bdExecutionFixtureResult) Error() string { return e.message }

func TestBDExecutionHumanAndMacCompatibility(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			ambient := []string{"GC_CITY_PATH=/fixture-city", "BEADS_ACTOR=fixture-actor"}
			if platform == "darwin" {
				ambient = append(ambient, "GC_SESSION_ID=legacy-partial")
			}
			final := []string{"GC_CITY_PATH=/fixture-city", "OTHER=x"}
			spawns, acquires := 0, 0
			marker := &bdExecutionFixtureResult{message: "owned spawn result"}
			err := executeBDChild(context.Background(), platform, ambient, final, func(context.Context) ([]string, bdChildReaders, error) {
				acquires++
				return nil, bdChildReaders{}, errBDChildAuthority
			}, func(_ context.Context, e bdChildEnvironment) error {
				spawns++
				if !reflect.DeepEqual(e.entries, final) {
					t.Fatal("existing child ENV changed")
				}
				return marker
			})
			actual := &bdExecutionFixtureResult{}
			exactType := reflect.TypeOf(err) == reflect.TypeOf(marker) && errors.As(err, &actual)
			if !exactType || actual != marker || spawns != 1 || acquires != 0 {
				t.Fatal("human/Mac semantics changed or unnecessary authority reads")
			}
		})
	}
}

func TestBDExecutionBareOwnerNeverHumanFallback(t *testing.T) {
	for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			spawns := 0
			err := executeBDChild(context.Background(), "linux", []string{key}, []string{"OTHER=x"}, nil, func(context.Context, bdChildEnvironment) error { spawns++; return nil })
			if err == nil || spawns != 0 {
				t.Fatal("bare ownership key bypassed authority")
			}
		})
	}
}

func TestBDControllerInfraPhysicalAbsence(t *testing.T) {
	raw, _ := bdStage0Fixture()
	raw = append(raw, "GC_SIGNAL=fixture-signal", "BEADS_OTHER=ambient-secret", "OTHER=x")
	for _, hosted := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "hosted"}[hosted], func(t *testing.T) {
			before := append([]string(nil), raw...)
			result, err := projectBDControllerChild(raw, map[string]string{"GC_CITY_PATH": "/fixture-city", "GC_INSTANCE_TOKEN": "", "BEADS_HOLDER_TOKEN": ""}, hosted)
			if err != nil || !bdOwnerContextAbsent(result) || !reflect.DeepEqual(before, raw) {
				t.Fatal("infra retained ownership keys or changed parent")
			}
			for _, wanted := range []string{"BEADS_ACTOR=fixture-actor", "GC_CITY_PATH=/fixture-city", "GC_SIGNAL=fixture-signal", "OTHER=x"} {
				found := false
				for _, e := range result {
					if e == wanted {
						found = true
					}
				}
				if !found {
					t.Fatal("infra attribution/context missing")
				}
			}
			if hosted {
				for _, e := range result {
					if e == "BEADS_OTHER=ambient-secret" {
						t.Fatal("hosted ambient BEADS secret crossed projection")
					}
				}
			}
		})
	}
}
