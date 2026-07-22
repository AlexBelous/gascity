package extmsg

import (
	"context"
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

type deliveryContextService struct {
	backend    fabricBackend
	locks      *bindingLockPool
	transcript bindingMembershipEnsurer
}

type deliveryCleaner struct {
	backend fabricBackend
	locks   *bindingLockPool
}

func newDeliveryContextService(backend fabricBackend, locks *bindingLockPool, transcript bindingMembershipEnsurer) DeliveryContextService {
	return &deliveryContextService{backend: backend, locks: locks, transcript: transcript}
}

func (s *deliveryContextService) Record(ctx context.Context, caller Caller, input DeliveryContextRecord) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	ref, err := validateConversationRef(input.Conversation)
	if err != nil {
		return err
	}
	if err := authorizeMutation(caller, ref); err != nil {
		return err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return fmt.Errorf("%w: session_id required", ErrInvalidInput)
	}
	if input.BindingGeneration <= 0 {
		return fmt.Errorf("%w: binding_generation required", ErrInvalidInput)
	}
	fields := DeliveryFields{
		Ref:               ref,
		SessionID:         sessionID,
		BindingGeneration: input.BindingGeneration,
		LastPublishedAt:   input.LastPublishedAt,
		LastMessageID:     input.LastMessageID,
		SourceSessionID:   input.SourceSessionID,
		Meta:              input.Metadata,
	}
	label := deliveryRouteLabel(ref, sessionID)
	return withBindingLock(s.locks, ref, func() error {
		activeBinding, err := resolveActiveBindingLocked(ctx, s.backend, deliveryCleaner{s.backend, s.locks}, s.transcript, ref, timeNow())
		if err != nil {
			return err
		}
		if activeBinding == nil || activeBinding.SessionID != sessionID || activeBinding.BindingGeneration != input.BindingGeneration {
			return ErrBindingMismatch
		}
		return withLockKey(s.locks, label, func() error {
			records, err := s.backend.OpenDeliveryContexts(ref, sessionID)
			if err != nil {
				return err
			}
			if len(records) > 0 {
				if err := checkContext(ctx); err != nil {
					return err
				}
				return s.backend.UpdateDeliveryContext(records[0].ID, fields)
			}
			return s.backend.CreateDeliveryContext(fields)
		})
	})
}

func (s *deliveryContextService) Resolve(ctx context.Context, sessionID string, ref ConversationRef) (*DeliveryContextRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	label := deliveryRouteLabel(ref, sessionID)
	var out *DeliveryContextRecord
	err = withBindingLock(s.locks, ref, func() error {
		activeBinding, err := resolveActiveBindingLocked(ctx, s.backend, deliveryCleaner{s.backend, s.locks}, s.transcript, ref, timeNow())
		if err != nil {
			return err
		}
		return withLockKey(s.locks, label, func() error {
			records, err := s.backend.OpenDeliveryContexts(ref, sessionID)
			if err != nil {
				return err
			}
			for _, record := range records {
				if err := checkContext(ctx); err != nil {
					return err
				}
				if activeBinding != nil &&
					activeBinding.SessionID == sessionID &&
					activeBinding.BindingGeneration == record.BindingGeneration {
					if out == nil {
						rec := record
						out = &rec
						continue
					}
					if err := s.backend.CloseDeliveryContext(record.ID); err != nil {
						return fmt.Errorf("close duplicate delivery context %s: %w", record.ID, err)
					}
					continue
				}
				if err := s.backend.CloseDeliveryContext(record.ID); err != nil {
					return fmt.Errorf("close stale delivery context %s: %w", record.ID, err)
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *deliveryContextService) ClearForConversation(ctx context.Context, sessionID string, ref ConversationRef) error {
	return deliveryCleaner{s.backend, s.locks}.ClearForConversation(ctx, sessionID, ref)
}

func decodeDeliveryBead(b beads.Bead) (DeliveryContextRecord, error) {
	ref, err := conversationRefFromMetadata(b.Metadata)
	if err != nil {
		return DeliveryContextRecord{}, err
	}
	lastPublishedAt, err := parseTime(b.Metadata, "last_published_at")
	if err != nil {
		return DeliveryContextRecord{}, err
	}
	return DeliveryContextRecord{
		ID:                b.ID,
		SchemaVersion:     parseInt(b.Metadata, "schema_version"),
		SessionID:         strings.TrimSpace(b.Metadata["session_id"]),
		Conversation:      ref,
		BindingGeneration: parseInt64(b.Metadata, "binding_generation"),
		LastPublishedAt:   lastPublishedAt,
		LastMessageID:     strings.TrimSpace(b.Metadata["last_message_id"]),
		SourceSessionID:   strings.TrimSpace(b.Metadata["source_session_id"]),
		Metadata:          decodePrefixedMetadata(b.Metadata),
	}, nil
}

func (c deliveryCleaner) ClearForConversation(ctx context.Context, sessionID string, ref ConversationRef) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	ref, err := validateConversationRef(ref)
	if err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	label := deliveryRouteLabel(ref, sessionID)
	return withLockKey(c.locks, label, func() error {
		records, err := c.backend.OpenDeliveryContexts(ref, sessionID)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := checkContext(ctx); err != nil {
				return err
			}
			if err := c.backend.CloseDeliveryContext(record.ID); err != nil {
				return fmt.Errorf("close delivery context %s: %w", record.ID, err)
			}
		}
		return nil
	})
}
