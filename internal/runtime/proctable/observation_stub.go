//go:build !linux && !darwin

package proctable

import "fmt"

func observeRoots() ([]ObservedRoot, error) {
	return nil, fmt.Errorf("complete process environment coverage unsupported on this platform")
}
