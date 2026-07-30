// Package nudgeshadow resolves the nudge-shadow configuration selection.
package nudgeshadow

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/config"
)

// Mode is the resolved nudge-shadow mode.
type Mode string

const (
	// Off preserves the existing behavior without requiring nudge shadowing.
	Off Mode = "off"
	// Required requires the nudge-shadow path.
	Required Mode = "required"
)

// Provenance identifies whether a selection was built in or configured.
type Provenance string

const (
	// Builtin identifies the default selection used when nudge_shadow is omitted.
	Builtin Provenance = "builtin"
	// Config identifies a selection explicitly set in city configuration.
	Config Provenance = "config"
)

// Selection is the resolved nudge-shadow mode and its provenance.
type Selection struct {
	Mode       Mode
	Provenance Provenance
}

// Required reports whether the selection requires nudge shadowing.
func (s Selection) Required() bool {
	return s.Mode == Required
}

// Resolve resolves the explicit nudge-shadow selection from city configuration.
func Resolve(city *config.City) (Selection, error) {
	if city == nil {
		return Selection{}, fmt.Errorf("resolving nudge shadow: nil city config")
	}

	if city.Daemon.NudgeShadow == nil {
		return Selection{Mode: Off, Provenance: Builtin}, nil
	}

	value := *city.Daemon.NudgeShadow
	switch value {
	case string(Off):
		return Selection{Mode: Off, Provenance: Config}, nil
	case string(Required):
		return Selection{Mode: Required, Provenance: Config}, nil
	default:
		return Selection{}, fmt.Errorf("resolving nudge shadow: invalid nudge_shadow %q (want off or required)", value)
	}
}
