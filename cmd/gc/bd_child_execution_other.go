//go:build !linux

package main

import (
	"context"

	"github.com/gastownhall/gascity/internal/config"
)

// The real command wrapper preserves the existing non-Linux branch and never
// invokes this acquisition seam. No Darwin UNKNOWN stub is wired as a denial.
func acquireBDLinuxChildAuthority(context.Context, string, *config.City) ([]string, bdChildReaders, error) {
	return nil, bdChildReaders{}, errBDChildAuthority
}
