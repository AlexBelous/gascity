package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAuthorityTargetUnrelatedChurn(t *testing.T) {
	for _, mode := range []string{"unrelated-incomplete", "unrelated-disappears"} {
		t.Run(mode, func(t *testing.T) {
			targetEnv, targetPID, unrelatedReads := 0, 0, 0
			run := func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "list-sessions":
					return []byte("target\tfixture\t777\nunrelated\tother\t888\n"), nil
				case "show-environment":
					if args[2] != "=target" {
						unrelatedReads++
						if mode == "unrelated-incomplete" {
							return []byte("GC_SESSION_ID=other\n"), nil
						}
						return nil, errors.New("unrelated session disappeared")
					}
					targetEnv++
					return authorityEnvFixture(), nil
				case "display-message":
					if args[2] != "=target:^.0" {
						unrelatedReads++
						return nil, errors.New("unrelated session disappeared")
					}
					targetPID++
					return []byte("777\n"), nil
				}
				return nil, errors.New("unexpected command")
			}
			h, err := readAuthorityTarget(context.Background(), "target", "fixture", run)
			if err != nil || h.Name != "target" || h.PID != 777 || len(h.Environment) != 6 || targetEnv != 1 || targetPID != 1 || unrelatedReads != 0 {
				t.Fatalf("healthy target denied or unrelated session queried: err=%v name=%q pid=%d fields=%d targetEnv=%d targetPID=%d unrelatedReads=%d", err, h.Name, h.PID, len(h.Environment), targetEnv, targetPID, unrelatedReads)
			}
		})
	}
}

func TestAuthorityTargetAmbiguityAndLossFailClosed(t *testing.T) {
	for _, mode := range []string{"duplicate-sid", "duplicate-name", "duplicate-pid", "forged-target-sid", "forged-target-env", "forged-target-pid", "target-lost", "malformed-roster"} {
		t.Run(mode, func(t *testing.T) {
			targetReads := 0
			run := func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "list-sessions":
					switch mode {
					case "duplicate-sid":
						return []byte("target\tfixture\t777\nunrelated\tfixture\t888\n"), nil
					case "duplicate-name":
						return []byte("target\tfixture\t777\ntarget\tother\t888\n"), nil
					case "duplicate-pid":
						return []byte("target\tfixture\t777\nunrelated\tother\t777\n"), nil
					case "forged-target-sid":
						return []byte("target\tother\t777\n"), nil
					case "malformed-roster":
						return []byte("target\tfixture\t777\nunrelated\tbad\t888\textra\n"), nil
					}
					return []byte("target\tfixture\t777\n"), nil
				case "show-environment":
					targetReads++
					if mode == "target-lost" {
						return nil, errors.New("target session vanished")
					}
					if mode == "forged-target-env" {
						return []byte(strings.Replace(string(authorityEnvFixture()), "GC_SESSION_ID=fixture", "GC_SESSION_ID=other", 1)), nil
					}
					return authorityEnvFixture(), nil
				case "display-message":
					targetReads++
					if mode == "forged-target-pid" {
						return []byte("999\n"), nil
					}
					return []byte("777\n"), nil
				}
				return nil, errors.New("unexpected command")
			}
			if _, err := readAuthorityTarget(context.Background(), "target", "fixture", run); err == nil {
				t.Fatal("ambiguous, forged, or lost target gained authority")
			}
			if mode == "duplicate-sid" || mode == "duplicate-name" || mode == "duplicate-pid" || mode == "forged-target-sid" || mode == "malformed-roster" {
				if targetReads != 0 {
					t.Fatal("invalid roster reached target secret read")
				}
			}
		})
	}
}
