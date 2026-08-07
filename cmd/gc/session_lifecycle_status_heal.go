package main

import (
	"fmt"
	"time"

	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/session"
)

// sessionLifecycleStatusHealContext carries the site-specific runtime evidence
// the legacy status-heal writer needs.
type sessionLifecycleStatusHealContext struct {
	RuntimeObserved   bool
	RuntimeAlive      bool
	RollbackAvailable bool
}

// applySessionLifecycleStatusHeal keeps the legacy status write and its
// successful same-tick fold on one synchronous path.
func applySessionLifecycleStatusHeal(
	tick *reconcileTick,
	sessionID string,
	healContext sessionLifecycleStatusHealContext,
	sessFront *session.Store,
	clk clock.Clock,
	startupTimeout time.Duration,
) (map[string]string, error) {
	info, ok := tick.infoByID[sessionID]
	if !ok {
		return nil, fmt.Errorf("applying session lifecycle status heal: session %q missing from reconcile tick", sessionID)
	}
	if info.ID != sessionID {
		return nil, fmt.Errorf("applying session lifecycle status heal: requested session ID %q, tick info ID %q", sessionID, info.ID)
	}
	patch, err := healStateWithRollbackInfo(info, healContext.RuntimeAlive, sessFront, clk, startupTimeout, healContext.RollbackAvailable)
	if err != nil {
		return nil, err
	}
	tick.apply(sessionID, patch)
	return patch, nil
}
