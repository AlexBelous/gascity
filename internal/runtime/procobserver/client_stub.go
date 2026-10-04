//go:build !linux

package procobserver

import (
	"fmt"
	"net"
)

func verifyActivationPeer(*net.UnixConn) error {
	return fmt.Errorf("linux observer activation unsupported")
}

func newWriterAuthenticatedReader(*net.UnixConn, uint32) (func([]byte) (int, error), error) {
	return nil, fmt.Errorf("linux observer writer credentials unsupported")
}

func trustedPath(string, bool) error   { return fmt.Errorf("linux observer policy unsupported") }
func verifyCaller(CallerBinding) error { return fmt.Errorf("linux observer caller unsupported") }

// VerifyControllerPeer requires Linux kernel peer and process proof.
func VerifyControllerPeer(net.Conn, CallerBinding) error {
	return fmt.Errorf("linux controller peer unsupported")
}
