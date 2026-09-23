package main

import (
	"io"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

type (
	infraConvergenceCheck  func(string, *config.City, string, io.Writer) infraMigrationReport
	infraContainmentReader func(string, infraBindingTarget, map[string]bool) (infraContainmentGap, error)
)

// primeReadyStorageRoutes resolves the existing route memo with ready's already
// opened CITY work store. The synchronous check borrows that exact live handle;
// the memo retains only the resulting routes, never the handle or callback.
// Genesis, born-split and operator migration paths keep their normal openers.
func primeReadyStorageRoutes(cityPath string, work beads.Store) {
	if cityPath == "" || work == nil {
		return
	}
	entry := cliStorageRoutesEntryFor(filepath.Clean(cityPath))
	entry.once.Do(func() {
		check := func(path string, cfg *config.City, prefix string, stderr io.Writer) infraMigrationReport {
			read := func(_ string, target infraBindingTarget, proven map[string]bool) (infraContainmentGap, error) {
				return classifyInfraContainmentGapFromSource(work, target, proven)
			}
			return checkInfraClassConvergenceWithReader(path, cfg, prefix, stderr, read)
		}
		entry.routes = resolveCLIStorageRoutesWithCheck(cityPath, check)
	})
}
