package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/nudgequeue"
)

const (
	controllerNudgeAdmissionCommandPrefix = "nudge-admit-v1:"

	controllerNudgeLocalPrincipal = "gascity-local-command-producer"
	controllerNudgeLocalEvidence  = "controller-token-authenticated-v1"
)

type controllerNudgeAdmissionOutcome string

const (
	controllerNudgeAdmissionAccepted controllerNudgeAdmissionOutcome = "accepted"
	controllerNudgeAdmissionLegacy   controllerNudgeAdmissionOutcome = "legacy"
	controllerNudgeAdmissionRejected controllerNudgeAdmissionOutcome = "rejected"
)

type controllerNudgeAdmissionCode string

const (
	controllerNudgeAdmissionCodeUnauthorized controllerNudgeAdmissionCode = "unauthorized"
	controllerNudgeAdmissionCodeInvalid      controllerNudgeAdmissionCode = "invalid"
	controllerNudgeAdmissionCodeDenied       controllerNudgeAdmissionCode = "denied"
	controllerNudgeAdmissionCodeConflict     controllerNudgeAdmissionCode = "conflict"
	controllerNudgeAdmissionCodeUnavailable  controllerNudgeAdmissionCode = "unavailable"
	controllerNudgeAdmissionCodeInternal     controllerNudgeAdmissionCode = "internal"
)

// controllerSocketConfig contains process-owned controller capabilities. It is
// never populated from socket input.
type controllerSocketConfig struct {
	nudgeToken     string
	nudgeAuthority productionSessionNudgeAuthority
}

// controllerNudgeAdmissionRequest is the local socket's typed caller-owned
// intent. Requester, tenant, credential, and policy authority are deliberately
// absent; the authenticated controller endpoint stamps them after token proof.
type controllerNudgeAdmissionRequest struct {
	Token                string                   `json:"token"`
	RequestID            string                   `json:"request_id"`
	Mode                 nudgequeue.DeliveryMode  `json:"mode"`
	SessionID            string                   `json:"session_id"`
	IntentGeneration     uint64                   `json:"intent_generation"`
	ContinuationIdentity string                   `json:"continuation_identity,omitempty"`
	LaunchIdentity       string                   `json:"launch_identity,omitempty"`
	TargetPolicy         nudgequeue.TargetPolicy  `json:"target_policy"`
	Source               nudgequeue.CommandSource `json:"source"`
	Message              string                   `json:"message"`
	Reference            *nudgequeue.Reference    `json:"reference,omitempty"`
	DeliverAfter         *time.Time               `json:"deliver_after,omitempty"`
	ExpiresAt            time.Time                `json:"expires_at"`
}

func (r controllerNudgeAdmissionRequest) domainRequest() nudgequeue.NudgeIngressRequest {
	deliverAfter := time.Time{}
	if r.DeliverAfter != nil {
		deliverAfter = *r.DeliverAfter
	}
	return nudgequeue.NudgeIngressRequest{
		RequestID: r.RequestID,
		Mode:      r.Mode,
		Target: nudgequeue.CommandTarget{
			SessionID:            r.SessionID,
			IntentGeneration:     r.IntentGeneration,
			ContinuationIdentity: r.ContinuationIdentity,
			LaunchIdentity:       r.LaunchIdentity,
			Policy:               r.TargetPolicy,
		},
		Source:       r.Source,
		Message:      r.Message,
		Reference:    r.Reference,
		DeliverAfter: deliverAfter,
		ExpiresAt:    r.ExpiresAt,
	}
}

type controllerNudgeAdmissionReply struct {
	Outcome   controllerNudgeAdmissionOutcome `json:"outcome"`
	Code      controllerNudgeAdmissionCode    `json:"code,omitempty"`
	CommandID string                          `json:"command_id,omitempty"`
	Status    nudgequeue.CommandState         `json:"status,omitempty"`
	Created   bool                            `json:"created,omitempty"`
	Error     string                          `json:"error,omitempty"`
}

func handleControllerNudgeAdmissionPayload(ctx context.Context, w io.Writer, payload string, config controllerSocketConfig) {
	if isNilProductionSessionNudgeAuthority(config.nudgeAuthority) {
		writeControllerNudgeAdmissionReply(w, controllerNudgeAdmissionReply{Outcome: controllerNudgeAdmissionLegacy})
		return
	}
	var wire controllerNudgeAdmissionRequest
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		writeControllerNudgeAdmissionReply(w, rejectedControllerNudgeAdmissionReply(
			controllerNudgeAdmissionCodeInvalid,
			"invalid durable nudge request",
		))
		return
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeControllerNudgeAdmissionReply(w, rejectedControllerNudgeAdmissionReply(
			controllerNudgeAdmissionCodeInvalid,
			"invalid durable nudge request",
		))
		return
	}
	if !controllerNudgeTokenMatches(config.nudgeToken, wire.Token) {
		writeControllerNudgeAdmissionReply(w, rejectedControllerNudgeAdmissionReply(
			controllerNudgeAdmissionCodeUnauthorized,
			"controller authentication failed",
		))
		return
	}

	request := wire.domainRequest()
	tenantScope, cityScope, credentialClass := config.nudgeAuthority.RequesterScope()
	if tenantScope == "" || cityScope == "" || credentialClass == "" {
		_, err := config.nudgeAuthority.Admit(ctx, request)
		if errors.Is(err, errNudgeLegacyEffectOwnership) {
			writeControllerNudgeAdmissionReply(w, controllerNudgeAdmissionReply{Outcome: controllerNudgeAdmissionLegacy})
			return
		}
		writeControllerNudgeAdmissionReply(w, rejectedControllerNudgeAdmissionReply(
			controllerNudgeAdmissionCodeUnavailable,
			"durable nudge authority is unavailable",
		))
		return
	}
	authenticated := nudgequeue.AuthenticatedNudgeRequester{
		PrincipalID:     controllerNudgeLocalPrincipal,
		TenantScope:     tenantScope,
		CityScope:       cityScope,
		CredentialClass: credentialClass,
		EvidenceID:      controllerNudgeLocalEvidence,
	}
	result, err := config.nudgeAuthority.Admit(
		nudgequeue.WithAuthenticatedNudgeRequester(ctx, authenticated),
		request,
	)
	if err != nil {
		writeControllerNudgeAdmissionReply(w, classifyControllerNudgeAdmissionError(err))
		return
	}
	if result.Entry.Command == nil || result.Entry.Command.ID == "" {
		writeControllerNudgeAdmissionReply(w, rejectedControllerNudgeAdmissionReply(
			controllerNudgeAdmissionCodeInternal,
			"durable nudge admission returned no command",
		))
		return
	}
	writeControllerNudgeAdmissionReply(w, controllerNudgeAdmissionReply{
		Outcome:   controllerNudgeAdmissionAccepted,
		CommandID: result.Entry.Command.ID,
		Status:    result.Entry.Command.State,
		Created:   result.Created,
	})
}

func controllerNudgeTokenMatches(expected, supplied string) bool {
	if expected == "" || supplied == "" {
		return false
	}
	expectedDigest := sha256.Sum256([]byte(expected))
	suppliedDigest := sha256.Sum256([]byte(supplied))
	return subtle.ConstantTimeCompare(expectedDigest[:], suppliedDigest[:]) == 1
}

func classifyControllerNudgeAdmissionError(err error) controllerNudgeAdmissionReply {
	switch {
	case errors.Is(err, errNudgeLegacyEffectOwnership):
		return controllerNudgeAdmissionReply{Outcome: controllerNudgeAdmissionLegacy}
	case errors.Is(err, nudgequeue.ErrNudgeAuthorizationDenied):
		return rejectedControllerNudgeAdmissionReply(controllerNudgeAdmissionCodeDenied, "durable nudge admission was denied")
	case errors.Is(err, nudgequeue.ErrNudgeAuthorizationInvalid),
		errors.Is(err, nudgequeue.ErrCommandRepositoryInvalidRequest):
		return rejectedControllerNudgeAdmissionReply(controllerNudgeAdmissionCodeInvalid, "durable nudge request is invalid")
	case errors.Is(err, nudgequeue.ErrCommandRepositoryIdempotencyConflict),
		errors.Is(err, nudgequeue.ErrLocalNudgeAuthorityConflict):
		return rejectedControllerNudgeAdmissionReply(controllerNudgeAdmissionCodeConflict, "durable nudge request conflicts with existing authority")
	case errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, nudgequeue.ErrNudgeAuthorizationUnknown),
		errors.Is(err, nudgequeue.ErrLocalNudgeAuthorityUnavailable):
		return rejectedControllerNudgeAdmissionReply(controllerNudgeAdmissionCodeUnavailable, "durable nudge authority is unavailable")
	default:
		return rejectedControllerNudgeAdmissionReply(controllerNudgeAdmissionCodeInternal, "durable nudge admission failed")
	}
}

func rejectedControllerNudgeAdmissionReply(code controllerNudgeAdmissionCode, message string) controllerNudgeAdmissionReply {
	return controllerNudgeAdmissionReply{Outcome: controllerNudgeAdmissionRejected, Code: code, Error: message}
}

func writeControllerNudgeAdmissionReply(w io.Writer, reply controllerNudgeAdmissionReply) {
	if w == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(reply); err != nil {
		log.Printf("controller nudge admission reply: %v", err)
	}
}
