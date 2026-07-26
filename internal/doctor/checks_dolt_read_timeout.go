package doctor

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/config"
)

// doltReadTimeoutRiskCeilingMillis is the managed Dolt listener
// read_timeout_millis above which DoltReadTimeoutRiskCheck advises review.
//
// read_timeout is the reaper for orphaned per-call Sleep sockets. Managed
// multi-agent cities open a short-lived bd/dolt-sql client connection per
// operation (bd subprocess, gc hook claim, pool probe, nudge tick); a client
// killed by its `timeout N` wrapper exits without a clean COM_QUIT, so the
// server pins the socket in Sleep until read_timeout fires. The tuned managed
// default is config.DefaultDoltReadTimeoutMillis (15s), reached after the
// 5min -> 30s -> 15s lineage documented on the config constant. A city
// override that widens the reap window (e.g. the 60s seen in the gc-2h7b
// saturation incident) lets those orphaned sockets accumulate toward
// max_connections under churn, which is what saturated the store.
//
// The ceiling is 2x the managed default: cities with slower legitimate live
// operations may raise read_timeout, but beyond 2x the accumulation risk is
// worth an operator's attention. The check is advisory only — a wide
// read_timeout is a supported setting, not a broken state — so it never gates
// automation.
const doltReadTimeoutRiskCeilingMillis = 2 * config.DefaultDoltReadTimeoutMillis

// DoltReadTimeoutRiskCheck warns when a city's effective managed Dolt
// read_timeout_millis is wide enough to let orphaned per-call Sleep sockets
// accumulate toward max_connections under multi-agent connection churn.
//
// It is the safety complement to DoltConfigCheck: DoltConfigCheck asserts the
// generated dolt-config.yaml matches the city's configured values (drift), and
// so passes for any override as long as the file is faithful — including a
// read_timeout high enough to have caused the gc-2h7b saturation incident.
// This check reads the configured value itself and flags the accumulation risk
// that drift detection cannot see.
type DoltReadTimeoutRiskCheck struct {
	cityPath        string
	skip            bool
	applicableKnown bool
	applicable      bool
	doltConfig      config.DoltConfig
}

// NewDoltReadTimeoutRiskCheck creates a managed Dolt read_timeout risk check
// that resolves applicability from the city path at run time.
func NewDoltReadTimeoutRiskCheck(cityPath string, skip bool) *DoltReadTimeoutRiskCheck {
	return &DoltReadTimeoutRiskCheck{cityPath: cityPath, skip: skip}
}

// NewDoltReadTimeoutRiskCheckForConfig creates the check using preloaded city
// config, mirroring NewDoltConfigCheckForConfig so registration does not
// reparse city.toml.
func NewDoltReadTimeoutRiskCheckForConfig(cityPath string, skip bool, cfg *config.City, cfgErr error) *DoltReadTimeoutRiskCheck {
	var doltConfig config.DoltConfig
	if cfg != nil {
		doltConfig = cfg.Dolt
	}
	return &DoltReadTimeoutRiskCheck{
		cityPath:        cityPath,
		skip:            skip,
		applicableKnown: true,
		applicable:      ManagedLocalDoltChecksApplicableForConfig(cityPath, cfg, cfgErr),
		doltConfig:      doltConfig,
	}
}

func (c *DoltReadTimeoutRiskCheck) managedApplicable() bool {
	if c.applicableKnown {
		return c.applicable
	}
	return managedLocalDoltChecksApplicable(c.cityPath)
}

// Name returns the check identifier.
func (c *DoltReadTimeoutRiskCheck) Name() string { return "dolt-read-timeout-risk" }

// WarmupEligible returns false: this is a steady-state advisory, not a
// fail-fast gate that should block gc start.
func (c *DoltReadTimeoutRiskCheck) WarmupEligible() bool { return false }

// CanFix returns false: the safe value depends on a city's slowest legitimate
// live operation, which is operator policy, not a mechanical fix.
func (c *DoltReadTimeoutRiskCheck) CanFix() bool { return false }

// Fix is a no-op; the check is report-only.
func (c *DoltReadTimeoutRiskCheck) Fix(_ *CheckContext) error { return nil }

// Run compares the effective managed read_timeout against the risk ceiling and
// warns (advisory) when it is exceeded.
func (c *DoltReadTimeoutRiskCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	if c.skip || !c.managedApplicable() {
		r.Status = StatusOK
		r.Message = "skipped (file backend, external dolt endpoint, or GC_DOLT=skip)"
		return r
	}

	effective := c.doltConfig.EffectiveReadTimeoutMillis()
	if effective <= doltReadTimeoutRiskCeilingMillis {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("managed dolt read_timeout_millis=%d within safe range (<= %d)",
			effective, doltReadTimeoutRiskCeilingMillis)
		return r
	}

	r.Status = StatusWarning
	r.Severity = SeverityAdvisory
	r.Message = fmt.Sprintf(
		"managed dolt read_timeout_millis=%d exceeds the risk ceiling %d (managed default %d): "+
			"under multi-agent connection churn a wide read_timeout lets orphaned per-call Sleep "+
			"sockets accumulate toward max_connections before they are reaped",
		effective, doltReadTimeoutRiskCeilingMillis, config.DefaultDoltReadTimeoutMillis)
	r.FixHint = fmt.Sprintf(
		"lower [dolt] read_timeout_millis in city.toml toward the %d ms managed default so "+
			"orphaned Sleep sockets are reaped promptly; raise it only as far as your slowest "+
			"legitimate live operation needs, then gc dolt restart to regenerate the managed config",
		config.DefaultDoltReadTimeoutMillis)
	return r
}
