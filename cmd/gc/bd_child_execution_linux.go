//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

func acquireBDLinuxChildAuthority(ctx context.Context, cityPath string, cfg *config.City) ([]string, bdChildReaders, error) {
	if ctx == nil || ctx.Err() != nil || cfg == nil {
		return nil, bdChildReaders{}, bdChildFailure(ctx, bdDiagAcquire, nil)
	}
	own, err := proctable.ReadCallerIncarnation(ctx, os.Getpid())
	if err != nil || ctx.Err() != nil {
		return nil, bdChildReaders{}, bdChildFailure(ctx, bdDiagAcquire, err)
	}
	raw := own.Environment.Entries()
	tuple, city, err := parseBDChildRawEnv(raw)
	if err != nil || city != cityPath {
		return nil, bdChildReaders{}, bdChildFailure(ctx, bdDiagAcquire, err)
	}
	// Use the relocation-aware, persisted-only front door. Never Manager.Get
	// enrichment/healing, hook handled=false, or the silent provider loader.
	store, err := openCityStoreAtWithConfig(cityPath, cfg)
	if err != nil || ctx.Err() != nil {
		return nil, bdChildReaders{}, bdChildFailure(ctx, bdDiagStore, err)
	}
	front := cliSessionFrontDoor(store, cfg, cityPath)
	providerContext := sessionProviderContextForCity(cfg, cityPath, "")
	providerIdentity := providerContext.providerName
	if providerIdentity == "" {
		providerIdentity = "tmux"
	} // existing configured-default registry factory
	if !validBDChildIdentityText(providerIdentity) {
		return nil, bdChildReaders{}, bdChildFailure(ctx, bdDiagStore, err)
	}
	var anchor proctable.CallerAnchor
	readers := bdChildReaders{
		record: func(ctx context.Context, sid string) (bdChildRecord, error) {
			if ctx.Err() != nil {
				return bdChildRecord{}, bdChildFailure(ctx, bdDiagRecordRead, err)
			}
			info, persisted, err := front.GetPersistedResponse(sid)
			if err != nil || ctx.Err() != nil || info.ID != sid {
				return bdChildRecord{}, bdChildFailure(ctx, bdDiagRecordRead, err)
			}
			// Full persisted fingerprint detects row/generation/reset/handle drift on
			// final re-read without exposing metadata or credentials in diagnostics.
			encoded, err := json.Marshal(struct {
				Info      any
				Persisted any
			}{info, persisted})
			if err != nil {
				return bdChildRecord{}, bdChildFailure(ctx, bdDiagRecordRead, err)
			}
			digest := sha256.Sum256(encoded)
			for i := range encoded {
				encoded[i] = 0
			}
			r := bdChildRecord{tuple: bdChildTuple{info.ID, info.Template, info.Generation, info.InstanceToken}, city: cityPath, provider: providerIdentity, handle: info.SessionName, revision: fmt.Sprintf("%x", digest), state: info.MetadataState, closed: info.Closed || persisted.Status == "closed", resetPending: info.ContinuationResetPending != ""}
			if ctx.Err() != nil {
				return bdChildRecord{}, bdChildFailure(ctx, bdDiagRecordRead, err)
			}
			return r, nil
		},
		provider: func(ctx context.Context, r bdChildRecord) ([]bdChildProvider, error) {
			if ctx.Err() != nil {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			infos, err := front.ListLabeledSessionInfosUnfiltered()
			if err != nil || ctx.Err() != nil {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			sp, err := newSessionProviderFromContext(providerContext, newSessionBeadSnapshotFromInfos(infos))
			if err != nil || ctx.Err() != nil {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			strict, ok := sp.(interface {
				ReadSessionAuthorityTarget(context.Context, string, string) (runtime.SessionAuthorityHandle, error)
			})
			if !ok {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			selected, err := strict.ReadSessionAuthorityTarget(ctx, r.handle, r.tuple.sid)
			if err != nil || ctx.Err() != nil {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			t, c, e := parseBDChildRawEnv(selected.Environment)
			if e != nil || selected.PID <= 1 || !validBDChildIdentityText(selected.Name) ||
				t != r.tuple || c != cityPath || selected.Name != r.handle {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			// Independent provider PID precedes OS capture; ambient SID does not pick
			// a PID. Only the selected root is read; no process table/global census.
			root, err := proctable.ReadCallerIncarnation(ctx, selected.PID)
			if err != nil || ctx.Err() != nil || !root.UIDs.HasFileSystem {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			rt, rc, err := parseBDChildRawEnv(root.Environment.Entries())
			if err != nil || rt != r.tuple || rc != cityPath {
				return nil, bdChildFailure(ctx, bdDiagProviderRead, err)
			}
			anchor = proctable.CallerAnchor{PID: root.PID, Start: root.Start, Domain: root.Domain, Platform: root.Platform, UIDs: root.UIDs}
			u := root.UIDs
			return []bdChildProvider{{tuple: rt, city: cityPath, provider: r.provider, handle: selected.Name, namespace: root.Domain, rootStart: root.Start, rootPID: root.PID, uid: [4]uint32{u.Real, u.Effective, u.Saved, u.FileSystem}}}, nil
		},
		caller: func(ctx context.Context) (bdChildCaller, error) {
			chain, err := proctable.ReadCallerIncarnations(ctx, os.Getpid(), anchor)
			if err != nil || ctx.Err() != nil {
				return bdChildCaller{}, bdChildFailure(ctx, bdDiagCallerRead, err)
			}
			out := bdChildCaller{}
			for _, p := range chain {
				if !p.Environment.Complete() || !p.UIDs.HasFileSystem {
					return bdChildCaller{}, bdChildFailure(ctx, bdDiagCallerRead, err)
				}
				entries := p.Environment.Entries()
				_, city, err := parseBDChildRawEnv(entries)
				if err != nil {
					return bdChildCaller{}, bdChildFailure(ctx, bdDiagCallerRead, err)
				}
				u := p.UIDs
				out.processes = append(out.processes, bdChildProcess{pid: p.PID, ppid: p.PPID, start: p.Start, namespace: p.Domain, city: city, uid: [4]uint32{u.Real, u.Effective, u.Saved, u.FileSystem}, env: entries})
			}
			if ctx.Err() != nil {
				return bdChildCaller{}, bdChildFailure(ctx, bdDiagCallerRead, err)
			}
			return out, nil
		},
	}
	if tuple.sid == "" || ctx.Err() != nil {
		return nil, bdChildReaders{}, bdChildFailure(ctx, bdDiagAcquire, err)
	}
	return raw, readers, nil
}
