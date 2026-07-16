package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/gastownhall/gascity/internal/convergence"
	"github.com/gastownhall/gascity/internal/nudgequeue"
)

const (
	controllerNudgeBridgeDialTimeout = 2 * time.Second
	controllerNudgeBridgeIOTimeout   = 10 * time.Second
)

type nudgeBridgeDisposition string

const (
	nudgeBridgeDurableAccepted     nudgeBridgeDisposition = "durable_accepted"
	nudgeBridgeLegacyOwnership     nudgeBridgeDisposition = "legacy_ownership"
	nudgeBridgePreEntryUnavailable nudgeBridgeDisposition = "pre_entry_unavailable"
	nudgeBridgeMayHaveEntered      nudgeBridgeDisposition = "may_have_entered"
)

type nudgeBridgeResult struct {
	Disposition nudgeBridgeDisposition
	CommandID   string
	Status      nudgequeue.CommandState
	Created     bool
	Err         error
}

type controllerNudgeDialContext func(context.Context, string, string) (net.Conn, error)
type controllerNudgeTokenReader func(string) (string, error)

func admitNudgeThroughLocalController(ctx context.Context, cityPath string, request nudgequeue.NudgeIngressRequest) nudgeBridgeResult {
	dialer := &net.Dialer{Timeout: controllerNudgeBridgeDialTimeout}
	return admitNudgeThroughLocalControllerWithDeps(ctx, cityPath, request, convergence.ReadToken, dialer.DialContext)
}

func admitNudgeThroughLocalControllerWithDeps(
	ctx context.Context,
	cityPath string,
	request nudgequeue.NudgeIngressRequest,
	readToken controllerNudgeTokenReader,
	dial controllerNudgeDialContext,
) nudgeBridgeResult {
	if ctx == nil || readToken == nil || dial == nil {
		return nudgeBridgeResult{
			Disposition: nudgeBridgeMayHaveEntered,
			Err:         errors.New("durable nudge bridge is not fully configured"),
		}
	}
	if err := ctx.Err(); err != nil {
		return nudgeBridgeResult{Disposition: nudgeBridgePreEntryUnavailable, Err: err}
	}
	token, err := readToken(cityPath)
	if err != nil {
		return nudgeBridgeResult{
			Disposition: nudgeBridgePreEntryUnavailable,
			Err:         fmt.Errorf("reading controller nudge credential: %w", err),
		}
	}
	wire := controllerNudgeAdmissionRequestFromDomain(token, request)
	payload, err := json.Marshal(wire)
	if err != nil {
		return nudgeBridgeResult{
			Disposition: nudgeBridgeMayHaveEntered,
			Err:         fmt.Errorf("encoding durable nudge request: %w", err),
		}
	}
	command := make([]byte, 0, len(controllerNudgeAdmissionCommandPrefix)+len(payload)+1)
	command = append(command, controllerNudgeAdmissionCommandPrefix...)
	command = append(command, payload...)
	command = append(command, '\n')

	first := admitNudgeControllerAttempt(ctx, cityPath, command, dial)
	if first.result.Disposition != nudgeBridgeMayHaveEntered || !first.retryable {
		return first.result
	}
	second := admitNudgeControllerAttempt(ctx, cityPath, command, dial)
	if second.result.Disposition == nudgeBridgeDurableAccepted {
		return second.result
	}
	return nudgeBridgeResult{
		Disposition: nudgeBridgeMayHaveEntered,
		Err: errors.Join(
			first.result.Err,
			second.result.Err,
			errors.New("durable nudge response remained ambiguous after idempotent retry"),
		),
	}
}

func controllerNudgeAdmissionRequestFromDomain(token string, request nudgequeue.NudgeIngressRequest) controllerNudgeAdmissionRequest {
	var deliverAfter *time.Time
	if !request.DeliverAfter.IsZero() {
		value := request.DeliverAfter
		deliverAfter = &value
	}
	var reference *nudgequeue.Reference
	if request.Reference != nil {
		value := *request.Reference
		reference = &value
	}
	return controllerNudgeAdmissionRequest{
		Token:                token,
		RequestID:            request.RequestID,
		Mode:                 request.Mode,
		SessionID:            request.Target.SessionID,
		IntentGeneration:     request.Target.IntentGeneration,
		ContinuationIdentity: request.Target.ContinuationIdentity,
		LaunchIdentity:       request.Target.LaunchIdentity,
		TargetPolicy:         request.Target.Policy,
		Source:               request.Source,
		Message:              request.Message,
		Reference:            reference,
		DeliverAfter:         deliverAfter,
		ExpiresAt:            request.ExpiresAt,
	}
}

type nudgeBridgeAttempt struct {
	result    nudgeBridgeResult
	retryable bool
}

func admitNudgeControllerAttempt(
	ctx context.Context,
	cityPath string,
	command []byte,
	dial controllerNudgeDialContext,
) nudgeBridgeAttempt {
	conn, err := dial(ctx, "unix", controllerSocketPath(cityPath))
	if err != nil {
		return nudgeBridgeAttempt{result: nudgeBridgeResult{
			Disposition: nudgeBridgePreEntryUnavailable,
			Err:         fmt.Errorf("connecting to controller nudge ingress: %w", err),
		}}
	}
	defer conn.Close() //nolint:errcheck // transport outcome is decided before cleanup
	deadline := time.Now().Add(controllerNudgeBridgeIOTimeout)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return nudgeBridgeAttempt{result: nudgeBridgeResult{
			Disposition: nudgeBridgePreEntryUnavailable,
			Err:         fmt.Errorf("setting controller nudge write deadline: %w", err),
		}}
	}
	entered := false
	for remaining := command; len(remaining) > 0; {
		written, writeErr := conn.Write(remaining)
		if written > 0 {
			entered = true
			remaining = remaining[written:]
		}
		if writeErr != nil {
			disposition := nudgeBridgePreEntryUnavailable
			if entered {
				disposition = nudgeBridgeMayHaveEntered
			}
			return nudgeBridgeAttempt{
				result: nudgeBridgeResult{
					Disposition: disposition,
					Err:         fmt.Errorf("writing controller nudge request: %w", writeErr),
				},
				retryable: entered,
			}
		}
		if written == 0 {
			disposition := nudgeBridgePreEntryUnavailable
			if entered {
				disposition = nudgeBridgeMayHaveEntered
			}
			return nudgeBridgeAttempt{
				result:    nudgeBridgeResult{Disposition: disposition, Err: io.ErrNoProgress},
				retryable: entered,
			}
		}
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nudgeBridgeAttempt{
			result: nudgeBridgeResult{
				Disposition: nudgeBridgeMayHaveEntered,
				Err:         fmt.Errorf("setting controller nudge read deadline: %w", err),
			},
			retryable: true,
		}
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4*1024), 64*1024)
	if !scanner.Scan() {
		readErr := scanner.Err()
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
		}
		return nudgeBridgeAttempt{
			result: nudgeBridgeResult{
				Disposition: nudgeBridgeMayHaveEntered,
				Err:         fmt.Errorf("reading controller nudge response: %w", readErr),
			},
			retryable: true,
		}
	}
	var reply controllerNudgeAdmissionReply
	decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return nudgeBridgeAttempt{
			result:    nudgeBridgeResult{Disposition: nudgeBridgeMayHaveEntered, Err: fmt.Errorf("decoding controller nudge response: %w", err)},
			retryable: true,
		}
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nudgeBridgeAttempt{
			result:    nudgeBridgeResult{Disposition: nudgeBridgeMayHaveEntered, Err: errors.New("decoding controller nudge response: trailing content")},
			retryable: true,
		}
	}
	switch reply.Outcome {
	case controllerNudgeAdmissionAccepted:
		if reply.CommandID == "" || !knownNudgeBridgeCommandState(reply.Status) {
			return nudgeBridgeAttempt{
				result:    nudgeBridgeResult{Disposition: nudgeBridgeMayHaveEntered, Err: errors.New("controller returned incomplete durable nudge acceptance")},
				retryable: true,
			}
		}
		return nudgeBridgeAttempt{result: nudgeBridgeResult{
			Disposition: nudgeBridgeDurableAccepted,
			CommandID:   reply.CommandID,
			Status:      reply.Status,
			Created:     reply.Created,
		}}
	case controllerNudgeAdmissionLegacy:
		return nudgeBridgeAttempt{result: nudgeBridgeResult{Disposition: nudgeBridgeLegacyOwnership}}
	case controllerNudgeAdmissionRejected:
		return nudgeBridgeAttempt{result: nudgeBridgeResult{
			Disposition: nudgeBridgeMayHaveEntered,
			Err:         fmt.Errorf("controller rejected durable nudge (%s): %s", reply.Code, reply.Error),
		}}
	default:
		return nudgeBridgeAttempt{
			result:    nudgeBridgeResult{Disposition: nudgeBridgeMayHaveEntered, Err: fmt.Errorf("controller returned unknown durable nudge outcome %q", reply.Outcome)},
			retryable: true,
		}
	}
}

func knownNudgeBridgeCommandState(state nudgequeue.CommandState) bool {
	switch state {
	case nudgequeue.CommandStatePending,
		nudgequeue.CommandStateInFlight,
		nudgequeue.CommandStateDelivered,
		nudgequeue.CommandStateInjectedUnconfirmed,
		nudgequeue.CommandStateDeliveryUnknown,
		nudgequeue.CommandStateExpired,
		nudgequeue.CommandStateSuperseded,
		nudgequeue.CommandStateDeadLettered,
		nudgequeue.CommandStateUpgradeRequired:
		return true
	default:
		return false
	}
}
