package proctable

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

func callerRawFixture(args, env []string) []byte {
	out := make([]byte, 4)
	binary.NativeEndian.PutUint32(out, uint32(len(args)))
	out = append(out, []byte("/fixture/executable\x00\x00\x00")...)
	for _, a := range args {
		out = append(out, []byte(a)...)
		out = append(out, 0)
	}
	for _, e := range env {
		out = append(out, []byte(e)...)
		out = append(out, 0)
	}
	return out
}

func TestCallerStrictNUL(t *testing.T) {
	valid := []byte("GC_SESSION_ID=\x00GC_INSTANCE_TOKEN=fixture-only\x00BEADS_HOLDER_TOKEN=fixture-only\x00OTHER=with spaces=equals\x00")
	e, err := callerStrictNUL(valid)
	if err != nil || len(e.Entries()) != 4 {
		t.Fatal("valid counted environment refused")
	}
	if e.Entries()[0] != "GC_SESSION_ID=" {
		t.Fatal("present-empty entry lost")
	}
	entriesCopy := e.Entries()
	entriesCopy[0] = "changed"
	if e.Entries()[0] != "GC_SESSION_ID=" {
		t.Fatal("environment aliases caller data")
	}
	if strings.Contains(fmt.Sprintf("%#v", e), "fixture-only") {
		t.Fatal("environment entered formatting")
	}
	cases := [][]byte{nil, []byte("\x00"), []byte("A=unterminated"), []byte("BROKEN\x00"), []byte("=bad\x00"), []byte("GC_INSTANCE_TOKEN=x\x00GC_INSTANCE_TOKEN=x\x00"), []byte("BEADS_HOLDER_TOKEN=x\x00BEADS_HOLDER_TOKEN=x\x00"), []byte("A=x\x00\x00B=y\x00")}
	for i, data := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := callerStrictNUL(data); err == nil {
				t.Fatal("ambiguous/malformed/omitted environment accepted")
			}
		})
	}
}

func TestCallerDarwinArgvNeverEnvironment(t *testing.T) {
	data := callerRawFixture([]string{"fixture", "GC_INSTANCE_TOKEN=argv-spoof", "", "GC_SESSION_ID=argv-spoof"}, []string{"GC_SESSION_ID=real-fixture", "GC_INSTANCE_TOKEN=fixture-only"})
	e, err := callerDarwinProcargs(data)
	if err != nil || len(e.Entries()) != 2 || strings.Contains(strings.Join(e.Entries(), "|"), "argv-spoof") {
		t.Fatal("argv was confused with ENV")
	}
	if _, err := callerDarwinProcargs(callerRawFixture([]string{"fixture", "GC_SESSION_ID=argv-spoof"}, nil)); err == nil {
		t.Fatal("omitted ENV accepted")
	}
}

func TestCallerRawMalformedKeyControls(t *testing.T) {
	for i, data := range [][]byte{[]byte("GC_SESSION_ID\n=x\x00"), []byte("GC_INSTANCE_TOKEN\r=x\x00"), []byte("GC_TEMPLATE\t=x\x00"), []byte("GC_\x7f=x\x00")} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if _, err := callerStrictNUL(data); err == nil {
				t.Fatal("control byte in key accepted")
			}
		})
	}
}

func TestCallerRawLimitsAndDuplicates(t *testing.T) {
	for _, key := range []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_CITY_PATH", "GC_CITY", "GC_OTHER", "BEADS_ACTOR"} {
		t.Run(key, func(t *testing.T) {
			if _, err := callerStrictNUL([]byte(key + "=a\x00" + key + "=a\x00")); err == nil {
				t.Fatal("duplicate authority key accepted")
			}
		})
	}
	if _, err := callerStrictNUL([]byte(strings.Repeat("A=x\x00", callerMaxEntries+1))); err == nil {
		t.Fatal("entry limit ignored")
	}
	if _, err := callerStrictNUL([]byte("A=" + strings.Repeat("x", callerMaxBytes) + "\x00")); err == nil {
		t.Fatal("byte limit ignored")
	}
	e, err := callerStrictNUL([]byte("OTHER=x\x00OTHER=y\x00GC_SESSION_ID=\x00"))
	if err != nil || len(e.Entries()) != 3 {
		t.Fatal("unrelated duplicate or present-empty information lost")
	}
}

func TestCallerDarwinUnframedTailUnknown(t *testing.T) {
	b := callerRawFixture([]string{"fixture"}, []string{"GC_SESSION_ID=fixture"})
	b = append(b, []byte("\x00\x00stack-opaque-data\x00")...)
	if _, err := callerDarwinProcargs(b); err == nil {
		t.Fatal("unbounded Darwin tail silently trimmed")
	}
}
