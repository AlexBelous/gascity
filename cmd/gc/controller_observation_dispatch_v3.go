package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"time"

	"github.com/gastownhall/gascity/internal/runtime/procobserver"
)

// handleControllerObservationV3 serves the existing FULL observation command.
// A missing V3 service returns UNKNOWN; the legacy callback is never invoked.
func handleControllerObservationV3(conn net.Conn, reader *bufio.Reader, city string, options []controllerSocketOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), controllerObservationBudget)
	defer cancel()
	_ = conn.SetDeadline(time.Now().Add(controllerObservationBudget + time.Second))
	unknown := func(reason string) controllerObservationReplyV3 {
		return unknownControllerObservationV3(ctx, city, commit, reason)
	}
	if reader.Buffered() != 0 {
		writeJSONLine(conn, unknown("extra controller observation request data"))
		return
	}
	// Disconnect or extra bytes cancel the same whole attempt, including helper IO.
	go func() { _, _ = reader.ReadByte(); cancel() }()
	reply := unknown("controller v3 observation unsupported")
	if len(options) == 1 && options[0].observeV3 != nil {
		reply = options[0].observeV3(ctx)
	}
	data, err := json.Marshal(reply)
	if err != nil || len(data)+1 > controllerObservationV3Limit {
		data, _ = json.Marshal(unknown("controller v3 observation exceeds reply bound"))
	}
	_, _ = conn.Write(append(data, '\n'))
}

// writeControllerObservationRequestV3 uses the already supported command; the
// deployment policy, never request data, selects the exact V3 helper grammar.
func writeControllerObservationRequestV3(conn net.Conn) error {
	_, err := io.WriteString(conn, controllerObservationCommand+"\n")
	return err
}

// relayControllerObservationV3 preserves producer bytes and both provisional
// frames. It loads the exact V3 selector and never falls back to legacy reads.
func relayControllerObservationV3(ctx context.Context, city, source string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, controllerObservationBudget)
	defer cancel()
	return relayControllerObservationV3At(ctx, city, controllerSocketPath(city), source, stdout,
		func() (procobserver.ReleasePolicyV3, error) {
			return procobserver.LoadPolicyV3(controllerObservationPolicyPath, source)
		}, procobserver.VerifyControllerPeer, writeControllerObservationRequestV3, time.Now)
}
