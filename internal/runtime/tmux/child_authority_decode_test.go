package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func authorityEnvFixture() []byte {
	return []byte("GC_SESSION_ID=fixture\nGC_TEMPLATE=template\nGC_RUNTIME_EPOCH=2\nGC_INSTANCE_TOKEN=fixture-only\nBEADS_HOLDER_TOKEN=fixture-only\nGC_CITY_PATH=/fixture\n")
}

func TestAuthorityRawFieldsExact(t *testing.T) {
	raw := authorityEnvFixture()
	raw = []byte(strings.Replace(string(raw), "fixture-only\n", " fixture-only \n", 1))
	env, err := decodeAuthorityFields(raw, authorityProviderKeys)
	if err != nil || env[3] != "GC_INSTANCE_TOKEN= fixture-only " {
		t.Fatal("provider ENV normalized or rejected exact whitespace")
	}
	for i, data := range [][]byte{nil, []byte("GC_SESSION_ID=x"), append(authorityEnvFixture(), []byte("GC_INSTANCE_TOKEN=other\n")...), []byte(strings.Replace(string(authorityEnvFixture()), "GC_TEMPLATE=template", "GC_SESSION_ID=template", 1)), []byte(strings.Replace(string(authorityEnvFixture()), "fixture-only", "value\nGC_SESSION_ID=spoof", 1)), []byte(strings.Replace(string(authorityEnvFixture()), "template\n", "template\r\n", 1))} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := decodeAuthorityFields(data, authorityProviderKeys); err == nil {
				t.Fatal("malformed raw provider frame accepted")
			}
		})
	}
}

func TestAuthoritySnapshotInjected(t *testing.T) {
	for _, mode := range []string{"valid", "read-error", "empty", "duplicate-name", "duplicate-pid", "duplicate-sid", "bad-pid", "cancel-final"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			run := func(_ context.Context, args ...string) ([]byte, error) {
				calls++
				switch args[0] {
				case "list-sessions":
					if mode == "read-error" {
						return nil, errors.New("fixture denied")
					}
					if mode == "empty" {
						return nil, nil
					}
					if mode == "duplicate-name" {
						return []byte("fixture-handle\nfixture-handle\n"), nil
					}
					if mode == "duplicate-pid" || mode == "duplicate-sid" {
						return []byte("fixture-handle\nother-handle\n"), nil
					}
					return []byte("fixture-handle\n"), nil
				case "show-environment":
					b := authorityEnvFixture()
					if mode == "duplicate-pid" && calls > 3 {
						b = []byte(strings.Replace(string(b), "GC_SESSION_ID=fixture", "GC_SESSION_ID=other", 1))
					}
					return b, nil
				case "display-message":
					if mode == "cancel-final" {
						cancel()
					}
					if mode == "bad-pid" {
						return []byte("+777\n"), nil
					}
					return []byte("777\n"), nil
				}
				return nil, errors.New("unexpected fixture command")
			}
			rows, err := readAuthoritySnapshot(ctx, run)
			if (err == nil) != (mode == "valid") {
				t.Fatal("provider UNKNOWN promoted to snapshot")
			}
			if mode == "valid" && (len(rows) != 1 || rows[0].PID != 777 || len(rows[0].Environment) != 6) {
				t.Fatal("provider snapshot incomplete")
			}
		})
	}
}
