package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func bdStage0Fixture() ([]string, bdChildReaders) {
	tuple := bdChildTuple{"fixture-id", "fixture-template", "2", "fixture-only-fence"}
	env := []string{
		"GC_SESSION_ID=" + tuple.sid, "GC_TEMPLATE=" + tuple.template, "GC_RUNTIME_EPOCH=" + tuple.epoch,
		"GC_INSTANCE_TOKEN=" + tuple.token, "BEADS_HOLDER_TOKEN=" + tuple.token, "GC_CITY_PATH=/fixture-city", "BEADS_ACTOR=fixture-actor",
	}
	uid := [4]uint32{1000, 1000, 1000, 1000}
	record := bdChildRecord{tuple: tuple, city: "/fixture-city", provider: "fixture-provider", handle: "fixture-handle", revision: "revision-1", state: "active"}
	provider := bdChildProvider{tuple: tuple, city: record.city, provider: record.provider, handle: record.handle, namespace: "fixture-ns", rootStart: "100", rootPID: 100, uid: uid}
	caller := bdChildCaller{processes: []bdChildProcess{
		{pid: 101, ppid: 100, start: "101", namespace: provider.namespace, city: record.city, uid: uid, env: append([]string(nil), env...)},
		{pid: 100, ppid: 1, start: "100", namespace: provider.namespace, city: record.city, uid: uid, env: append([]string(nil), env...)},
	}}
	readers := bdChildReaders{
		record: func(context.Context, string) (bdChildRecord, error) { return record, nil },
		provider: func(context.Context, bdChildRecord) ([]bdChildProvider, error) {
			return []bdChildProvider{provider}, nil
		},
		caller: func(context.Context) (bdChildCaller, error) { return caller, nil },
	}
	return env, readers
}

type bdStage0Harness struct {
	raw, final                                      []string
	record                                          bdChildRecord
	providers                                       []bdChildProvider
	caller                                          bdChildCaller
	recordErr, providerErr, callerErr               error
	recordCalls, providerCalls, callerCalls, spawns int
	onRecord, onProvider, onCaller                  func(*bdStage0Harness)
}

func newBDStage0Harness() *bdStage0Harness {
	raw, readers := bdStage0Fixture()
	ctx := context.Background()
	record, _ := readers.record(ctx, "fixture-id")
	providers, _ := readers.provider(ctx, record)
	caller, _ := readers.caller(ctx)
	return &bdStage0Harness{raw: raw, final: append([]string(nil), raw...), record: record, providers: providers, caller: caller}
}

func (h *bdStage0Harness) readers() bdChildReaders {
	return bdChildReaders{
		record: func(context.Context, string) (bdChildRecord, error) {
			h.recordCalls++
			if h.onRecord != nil {
				h.onRecord(h)
			}
			return h.record, h.recordErr
		},
		provider: func(context.Context, bdChildRecord) ([]bdChildProvider, error) {
			h.providerCalls++
			if h.onProvider != nil {
				h.onProvider(h)
			}
			return h.providers, h.providerErr
		},
		caller: func(context.Context) (bdChildCaller, error) {
			h.callerCalls++
			if h.onCaller != nil {
				h.onCaller(h)
			}
			return h.caller, h.callerErr
		},
	}
}

func (h *bdStage0Harness) run() error {
	return runBDSessionChild(context.Background(), bdChildSessionTool, h.raw, h.final, h.readers(), func(context.Context, bdChildEnvironment) error { h.spawns++; return nil })
}

func TestBDSessionChildNonSessionOperationDenied(t *testing.T) {
	for _, op := range []bdChildOperation{bdChildUnknown, bdChildInfrastructure, bdChildOperation(99)} {
		t.Run(fmt.Sprint(op), func(t *testing.T) {
			raw, readers := bdStage0Fixture()
			reads, spawns := 0, 0
			readRecord := readers.record
			readers.record = func(ctx context.Context, sid string) (bdChildRecord, error) {
				reads++
				return readRecord(ctx, sid)
			}
			err := runBDSessionChild(context.Background(), op, raw, raw, readers, func(context.Context, bdChildEnvironment) error {
				spawns++
				return nil
			})
			if err == nil || reads != 0 || spawns != 0 {
				t.Fatal("non-session operation entered managed authority or spawned")
			}
		})
	}
}

func bdStage0Replace(raw []string, key, value string) []string {
	out := append([]string(nil), raw...)
	for i, e := range out {
		if strings.HasPrefix(e, key+"=") {
			out[i] = key + "=" + value
		}
	}
	return out
}

func TestBDStage0RawMissingEmptyDuplicate(t *testing.T) {
	keys := []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN"}
	for _, key := range keys {
		for _, kind := range []string{"missing", "empty", "duplicate"} {
			t.Run(kind+"/"+key, func(t *testing.T) {
				h := newBDStage0Harness()
				before := append([]string(nil), h.raw...)
				switch kind {
				case "missing":
					h.raw = nil
					for _, e := range before {
						if !strings.HasPrefix(e, key+"=") {
							h.raw = append(h.raw, e)
						}
					}
				case "empty":
					h.raw = bdStage0Replace(h.raw, key, "")
				case "duplicate":
					for _, e := range before {
						if strings.HasPrefix(e, key+"=") {
							h.raw = append(h.raw, e)
							break
						}
					}
				}
				rawBefore := append([]string(nil), h.raw...)
				if h.run() == nil || h.spawns != 0 || h.recordCalls != 0 {
					t.Fatal("invalid raw ENV reached a reader or spawn")
				}
				if !reflect.DeepEqual(rawBefore, h.raw) {
					t.Fatal("raw entries mutated")
				}
			})
		}
	}
}

func TestBDStage0AuthorityNegatives(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bdStage0Harness)
	}{
		{"storeUNKNOWN", func(h *bdStage0Harness) { h.recordErr = errors.New("private fixture failure") }},
		{"providerUNKNOWN", func(h *bdStage0Harness) { h.providerErr = errors.New("private fixture failure") }},
		{"callerUNKNOWN", func(h *bdStage0Harness) { h.callerErr = errors.New("private fixture failure") }},
		{"missingRecord", func(h *bdStage0Harness) { h.record = bdChildRecord{} }},
		{"closed", func(h *bdStage0Harness) { h.record.closed = true }},
		{"resetPending", func(h *bdStage0Harness) { h.record.resetPending = true }},
		{"missingRevision", func(h *bdStage0Harness) { h.record.revision = "" }},
		{"recordSID", func(h *bdStage0Harness) { h.record.tuple.sid = "different" }},
		{"recordTemplate", func(h *bdStage0Harness) { h.record.tuple.template = "different" }},
		{"recordEpoch", func(h *bdStage0Harness) { h.record.tuple.epoch = "3" }},
		{"recordToken", func(h *bdStage0Harness) { h.record.tuple.token = "different" }},
		{"recordCity", func(h *bdStage0Harness) { h.record.city = "/different" }},
		{"recordHandleEmpty", func(h *bdStage0Harness) { h.record.handle = "" }},
		{"providerRouteEmpty", func(h *bdStage0Harness) { h.record.provider = "" }},
		{"noProvider", func(h *bdStage0Harness) { h.providers = nil }},
		{"providerTokenMissing", func(h *bdStage0Harness) { h.providers[0].tuple.token = "" }},
		{"providerTokenMismatch", func(h *bdStage0Harness) { h.providers[0].tuple.token = "different" }},
		{"providerEpochMismatch", func(h *bdStage0Harness) { h.providers[0].tuple.epoch = "3" }},
		{"providerTemplateMismatch", func(h *bdStage0Harness) { h.providers[0].tuple.template = "different" }},
		{"providerCityMismatch", func(h *bdStage0Harness) { h.providers[0].city = "/different" }},
		{"providerNameMismatch", func(h *bdStage0Harness) { h.providers[0].provider = "different" }},
		{"providerHandleMismatch", func(h *bdStage0Harness) { h.providers[0].handle = "different" }},
		{"providerNamespaceMissing", func(h *bdStage0Harness) { h.providers[0].namespace = "" }},
		{"providerRootStartMissing", func(h *bdStage0Harness) { h.providers[0].rootStart = "" }},
		{"providerRootMissing", func(h *bdStage0Harness) { h.providers[0].rootPID = 0 }},
		{"providerDuplicateSID", func(h *bdStage0Harness) {
			p := h.providers[0]
			p.rootPID++
			p.handle = "other"
			h.providers = append(h.providers, p)
		}},
		{"providerDuplicatePID", func(h *bdStage0Harness) {
			p := h.providers[0]
			p.tuple.sid = "other"
			p.handle = "other"
			h.providers = append(h.providers, p)
		}},
		{"providerDuplicateHandle", func(h *bdStage0Harness) {
			p := h.providers[0]
			p.tuple.sid = "other"
			p.rootPID++
			h.providers = append(h.providers, p)
		}},
		{"callerMissing", func(h *bdStage0Harness) { h.caller.processes = nil }},
		{"callerUID", func(h *bdStage0Harness) { h.caller.processes[0].uid[1]++ }},
		{"callerNamespace", func(h *bdStage0Harness) { h.caller.processes[0].namespace = "different" }},
		{"callerCity", func(h *bdStage0Harness) { h.caller.processes[0].city = "/different" }},
		{"callerStartMissing", func(h *bdStage0Harness) { h.caller.processes[0].start = "" }},
		{"callerNotAncestor", func(h *bdStage0Harness) { h.caller.processes[0].ppid = 999 }},
		{"callerCycle", func(h *bdStage0Harness) { h.caller.processes[1].pid = h.caller.processes[0].pid }},
		{"callerWrongRoot", func(h *bdStage0Harness) { h.caller.processes[1].pid = 999; h.caller.processes[0].ppid = 999 }},
		{"callerWrongRootStart", func(h *bdStage0Harness) { h.caller.processes[1].start = "101" }},
		{"callerTokenMismatch", func(h *bdStage0Harness) {
			h.caller.processes[0].env = bdStage0Replace(h.caller.processes[0].env, "GC_INSTANCE_TOKEN", "different")
		}},
		{"callerHolderDuplicate", func(h *bdStage0Harness) {
			h.caller.processes[0].env = append(h.caller.processes[0].env, "BEADS_HOLDER_TOKEN=duplicate")
		}},
		{"callerEpochMismatch", func(h *bdStage0Harness) {
			h.caller.processes[0].env = bdStage0Replace(h.caller.processes[0].env, "GC_RUNTIME_EPOCH", "3")
		}},
		{"parentTupleMismatch", func(h *bdStage0Harness) {
			h.caller.processes[1].env = bdStage0Replace(h.caller.processes[1].env, "GC_TEMPLATE", "different")
		}},
		{"finalEmptyToken", func(h *bdStage0Harness) { h.final = bdStage0Replace(h.final, "GC_INSTANCE_TOKEN", "") }},
		{"finalOtherToken", func(h *bdStage0Harness) { h.final = bdStage0Replace(h.final, "GC_INSTANCE_TOKEN", "different") }},
		{"finalSplitHolder", func(h *bdStage0Harness) { h.final = bdStage0Replace(h.final, "BEADS_HOLDER_TOKEN", "different") }},
		{"finalDuplicateToken", func(h *bdStage0Harness) { h.final = append(h.final, "GC_INSTANCE_TOKEN=fixture-only-fence") }},
		{"finalTemplateChanged", func(h *bdStage0Harness) { h.final = bdStage0Replace(h.final, "GC_TEMPLATE", "different") }},
		{"finalCityChanged", func(h *bdStage0Harness) { h.final = bdStage0Replace(h.final, "GC_CITY_PATH", "/different") }},
		{"rawHolderMismatch", func(h *bdStage0Harness) { h.raw = bdStage0Replace(h.raw, "BEADS_HOLDER_TOKEN", "different") }},
		{"rawEpochZero", func(h *bdStage0Harness) { h.raw = bdStage0Replace(h.raw, "GC_RUNTIME_EPOCH", "0") }},
		{"rawEpochMalformed", func(h *bdStage0Harness) { h.raw = bdStage0Replace(h.raw, "GC_RUNTIME_EPOCH", "x") }},
		{"rawEpochNonCanonical", func(h *bdStage0Harness) { h.raw = bdStage0Replace(h.raw, "GC_RUNTIME_EPOCH", "02") }},
		{"rawUnterminatedEntry", func(h *bdStage0Harness) { h.raw = append(h.raw, "BROKEN") }},
		{"rawNul", func(h *bdStage0Harness) { h.raw = append(h.raw, "ANY=bad\x00value") }},
		{"rawDuplicateCity", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY_PATH=/fixture-city") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newBDStage0Harness()
			c.mutate(h)
			if h.run() == nil || h.spawns != 0 {
				t.Fatal("negative context reached spawn")
			}
		})
	}
	for _, state := range []string{"", "asleep", "suspended", "draining", "quarantined", "failed-create"} {
		t.Run("state/"+state, func(t *testing.T) {
			h := newBDStage0Harness()
			h.record.state = state
			if h.run() == nil || h.spawns != 0 {
				t.Fatal("unapproved state reached spawn")
			}
		})
	}
}

func TestBDStage0FinalRecheckDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bdStage0Harness)
	}{
		{"recordGeneration", func(h *bdStage0Harness) {
			h.onRecord = func(h *bdStage0Harness) {
				if h.recordCalls == 2 {
					h.record.tuple.epoch = "3"
				}
			}
		}},
		{"recordToken", func(h *bdStage0Harness) {
			h.onRecord = func(h *bdStage0Harness) {
				if h.recordCalls == 2 {
					h.record.tuple.token = "different"
				}
			}
		}},
		{"recordRevision", func(h *bdStage0Harness) {
			h.onRecord = func(h *bdStage0Harness) {
				if h.recordCalls == 2 {
					h.record.revision = "revision-2"
				}
			}
		}},
		{"recordClosed", func(h *bdStage0Harness) {
			h.onRecord = func(h *bdStage0Harness) {
				if h.recordCalls == 2 {
					h.record.closed = true
				}
			}
		}},
		{"recordUnavailable", func(h *bdStage0Harness) {
			h.onRecord = func(h *bdStage0Harness) {
				if h.recordCalls == 2 {
					h.recordErr = errors.New("private fixture failure")
				}
			}
		}},
		{"providerGeneration", func(h *bdStage0Harness) {
			h.onProvider = func(h *bdStage0Harness) {
				if h.providerCalls == 2 {
					h.providers[0].tuple.epoch = "3"
				}
			}
		}},
		{"providerUnavailable", func(h *bdStage0Harness) {
			h.onProvider = func(h *bdStage0Harness) {
				if h.providerCalls == 2 {
					h.providerErr = errors.New("private fixture failure")
				}
			}
		}},
		{"providerRootChanged", func(h *bdStage0Harness) {
			h.onProvider = func(h *bdStage0Harness) {
				if h.providerCalls == 2 {
					h.providers[0].rootStart = "102"
					h.caller.processes[1].start = "102"
				}
			}
		}},
		{"callerStart", func(h *bdStage0Harness) {
			h.onCaller = func(h *bdStage0Harness) {
				if h.callerCalls == 2 {
					h.caller.processes[0].start = "102"
				}
			}
		}},
		{"callerUnavailable", func(h *bdStage0Harness) {
			h.onCaller = func(h *bdStage0Harness) {
				if h.callerCalls == 2 {
					h.callerErr = errors.New("private fixture failure")
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newBDStage0Harness()
			c.mutate(h)
			if h.run() == nil || h.spawns != 0 {
				t.Fatal("final drift reached spawn")
			}
		})
	}
}

func TestBDStage0StartupKnownAndPreregisrationUnknown(t *testing.T) {
	for _, state := range []string{"creating", "start-pending", "active", "awake"} {
		t.Run(state, func(t *testing.T) {
			h := newBDStage0Harness()
			h.record.state = state
			if h.run() != nil || h.spawns != 1 {
				t.Fatal("known startup facts refused")
			}
			h = newBDStage0Harness()
			h.record.state = state
			h.providers = nil
			if h.run() == nil || h.spawns != 0 {
				t.Fatal("unregistered startup gained authority")
			}
		})
	}
}

func TestBDStage0PrivateFormattingNoCapability(t *testing.T) {
	env, readers := bdStage0Fixture()
	ticket, err := authorizeBDSessionChild(context.Background(), bdChildSessionTool, env, readers)
	if err != nil {
		t.Fatal("fixture authorization failed")
	}
	for _, v := range []any{ticket, ticket.record.tuple, ticket.record, ticket.provider, ticket.caller, ticket.caller.processes[0], bdChildEnvironment{entries: env}} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, v), "fixture-only-fence") {
				t.Fatal("credential entered formatting")
			}
		}
	}
	readers.record = func(context.Context, string) (bdChildRecord, error) {
		return bdChildRecord{}, errors.New("fixture-only-fence")
	}
	err = runBDSessionChild(context.Background(), bdChildSessionTool, env, env, readers, func(context.Context, bdChildEnvironment) error { t.Fatal("unexpected spawn"); return nil })
	if err == nil || strings.Contains(err.Error(), "fixture-only-fence") {
		t.Fatal("reader credential entered error")
	}
}

func TestBDStage0NoUnknownOperationOrZeroTicket(t *testing.T) {
	env, readers := bdStage0Fixture()
	if _, err := authorizeBDSessionChild(context.Background(), bdChildUnknown, env, readers); err == nil {
		t.Fatal("unknown operation gained authority")
	}
	if recheckBDSessionChild(context.Background(), bdChildTicket{}, env, readers) == nil {
		t.Fatal("zero ticket gained authority")
	}
	if _, err := projectBDInfrastructureChild(bdChildSessionTool, env); err == nil {
		t.Fatal("managed context silently became infrastructure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if runBDSessionChild(ctx, bdChildSessionTool, env, env, readers, func(context.Context, bdChildEnvironment) error { calls++; return nil }) == nil || calls != 0 {
		t.Fatal("canceled context spawned")
	}
}

func TestBDStage0InfraPreservesDuplicatesAndSignalAndParent(t *testing.T) {
	raw := []string{
		"GC_INSTANCE_TOKEN=fixture", "GC_SESSION_ID=id", "GC_TEMPLATE=t", "GC_RUNTIME_EPOCH=2", "BEADS_HOLDER_TOKEN=fixture",
		"BEADS_ACTOR=actor", "GC_CITY_PATH=/city", "GC_SUPERVISOR_PRESERVE_SESSIONS_ON_SIGNAL=1", "UNRELATED=one", "UNRELATED=two",
	}
	before := append([]string(nil), raw...)
	out, err := projectBDInfrastructureChild(bdChildInfrastructure, raw)
	if err != nil || !reflect.DeepEqual(out, raw[5:]) || !reflect.DeepEqual(raw, before) {
		t.Fatal("infra altered attribution/signal/multiplicity/parent")
	}
	out[0] = "BEADS_ACTOR=changed"
	if !reflect.DeepEqual(raw, before) {
		t.Fatal("infra projection aliased parent")
	}
}

func TestBDStage0SpawnErrorNoCapability(t *testing.T) {
	env, readers := bdStage0Fixture()
	calls := 0
	err := runBDSessionChild(context.Background(), bdChildSessionTool, env, env, readers, func(context.Context, bdChildEnvironment) error { calls++; return errors.New("fixture-only-fence") })
	if calls != 1 || err == nil || strings.Contains(err.Error(), "fixture-only-fence") {
		t.Fatal("spawn error leaked a capability or bypassed the recorder")
	}
}

func TestBDStage0TicketDeepCopyAndSecondProviderRoot(t *testing.T) {
	h := newBDStage0Harness()
	ticket, err := authorizeBDSessionChild(context.Background(), bdChildSessionTool, h.raw, h.readers())
	if err != nil {
		t.Fatal("fixture authorization failed")
	}
	h.caller.processes[0].env = bdStage0Replace(h.caller.processes[0].env, "GC_INSTANCE_TOKEN", "different")
	if recheckBDSessionChild(context.Background(), ticket, h.final, h.readers()) == nil {
		t.Fatal("ticket aliased a mutated reader snapshot")
	}
	h = newBDStage0Harness()
	other := h.providers[0]
	other.tuple.sid = "other-id"
	other.handle = "other-handle"
	other.rootPID = 200
	h.providers = append(h.providers, other)
	if h.run() != nil || h.spawns != 1 {
		t.Fatal("unrelated known unique provider handle should not change matching authority")
	}
}

func TestBDStage0CityFinalAliasCounterexample(t *testing.T) {
	h := newBDStage0Harness()
	h.final = append(h.final, "GC_CITY=/different-city")
	if h.run() == nil || h.spawns != 0 {
		t.Fatal("conflicting final GC_CITY reached spawn despite a valid GC_CITY_PATH")
	}
}

func TestBDStage0CityRawPairIsSealed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*bdStage0Harness)
	}{
		{"rawConflict", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY=/different") }},
		{"finalConflict", func(h *bdStage0Harness) { h.final = append(h.final, "GC_CITY=/different") }},
		{"finalAliasCRLF", func(h *bdStage0Harness) { h.final = append(h.final, "GC_CITY=bad\r\nalias") }},
		{"finalAliasEmpty", func(h *bdStage0Harness) { h.final = append(h.final, "GC_CITY=") }},
		{"rawAliasCRLF", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY=bad\nalias") }},
		{"rawAliasEmpty", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY=") }},
		{"finalSameAliasAddedStillDrift", func(h *bdStage0Harness) { h.final = append(h.final, "GC_CITY=/fixture-city") }},
		{"finalAliasRemoved", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY=/fixture-city") }},
		{"finalOnlyAliasChanged", func(h *bdStage0Harness) {
			h.raw = append(h.raw, "GC_CITY=/fixture-city")
			h.final = append(h.final, "GC_CITY=/different")
		}},
		{"finalPathChangedWithAlias", func(h *bdStage0Harness) {
			h.raw = append(h.raw, "GC_CITY=/fixture-city")
			h.final = append(bdStage0Replace(h.final, "GC_CITY_PATH", "/different"), "GC_CITY=/fixture-city")
		}},
		{"duplicateAlias", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY=/fixture-city", "GC_CITY=/fixture-city") }},
		{"finalDuplicateAlias", func(h *bdStage0Harness) { h.final = append(h.final, "GC_CITY=/fixture-city", "GC_CITY=/fixture-city") }},
		{"duplicatePath", func(h *bdStage0Harness) { h.raw = append(h.raw, "GC_CITY_PATH=/fixture-city") }},
		{"finalDuplicatePath", func(h *bdStage0Harness) { h.final = append(h.final, "GC_CITY_PATH=/fixture-city") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newBDStage0Harness()
			c.mutate(h)
			if h.run() == nil || h.spawns != 0 {
				t.Fatal("invalid or changed raw city pair reached spawn")
			}
		})
	}
	for _, kind := range []string{"bothSame", "onlyAlias", "onlyPath"} {
		t.Run(kind, func(t *testing.T) {
			h := newBDStage0Harness()
			if kind == "bothSame" {
				h.raw = append(h.raw, "GC_CITY=/fixture-city")
				h.final = append(h.final, "GC_CITY=/fixture-city")
			}
			if kind == "onlyAlias" {
				h.raw = nil
				for _, entry := range h.final {
					if !strings.HasPrefix(entry, "GC_CITY_PATH=") {
						h.raw = append(h.raw, entry)
					}
				}
				h.raw = append(h.raw, "GC_CITY=/fixture-city")
				h.final = append([]string(nil), h.raw...)
			}
			if h.run() != nil || h.spawns != 1 {
				t.Fatal("known unchanged valid raw city pair refused")
			}
		})
	}
}

func TestBDStage0KnownContextSpawnsOnce(t *testing.T) {
	env, readers := bdStage0Fixture()
	parent := append([]string(nil), env...)
	calls := 0
	err := runBDSessionChild(context.Background(), bdChildSessionTool, env, env, readers, func(_ context.Context, child bdChildEnvironment) error {
		calls++
		if !reflect.DeepEqual(child.entries, env) {
			t.Fatal("private environment changed")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatal("known injected context must spawn exactly once")
	}
	if !reflect.DeepEqual(env, parent) {
		t.Fatal("parent environment mutated")
	}
}

func TestBDStage0MissingTokenDeniesBeforeSpawn(t *testing.T) {
	env, readers := bdStage0Fixture()
	raw := append([]string(nil), env[:3]...)
	raw = append(raw, env[4:]...)
	calls := 0
	err := runBDSessionChild(context.Background(), bdChildSessionTool, raw, raw, readers, func(context.Context, bdChildEnvironment) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("missing capability must deny before spawn")
	}
}

func TestBDStage0InfrastructureKeepsAttribution(t *testing.T) {
	env, _ := bdStage0Fixture()
	out, err := projectBDInfrastructureChild(bdChildInfrastructure, env)
	if err != nil || !reflect.DeepEqual(out, env[5:]) {
		t.Fatal("explicit infrastructure must preserve city and tool actor")
	}
}
