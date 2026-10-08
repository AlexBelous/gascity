//go:build !linux && !darwin

package proctable

import "context"

func readCallerPID(context.Context, int) (CallerIncarnation, error) {
	return CallerIncarnation{}, errCallerUnknown
}
