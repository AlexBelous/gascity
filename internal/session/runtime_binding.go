package session

import (
	"encoding/json"
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

const runtimeBindingMetadataKey = "runtime_binding_v1"

// RuntimeBinding reads the immutable execution binding for an exact SID.
func (s *Store) RuntimeBinding(id string) (runtime.RuntimeBinding, bool, error) {
	b, err := s.validatedBead(id)
	if err != nil {
		return runtime.RuntimeBinding{}, false, err
	}
	return decodeRuntimeBinding(id, b.Metadata[runtimeBindingMetadataKey])
}

func decodeRuntimeBinding(id, raw string) (runtime.RuntimeBinding, bool, error) {
	if raw == "" {
		return runtime.RuntimeBinding{}, false, nil
	}
	var binding runtime.RuntimeBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return binding, false, fmt.Errorf("session %q: invalid runtime binding: %w", id, err)
	}
	if err := validateRuntimeBinding(binding); err != nil {
		return binding, false, err
	}
	return binding, true, nil
}

func validateRuntimeBinding(b runtime.RuntimeBinding) error {
	if b.Selector == "" || b.ResourceDomain == "" || b.PolicyDigest == "" {
		return fmt.Errorf("incomplete runtime binding")
	}
	return nil
}

// PinRuntimeBinding writes once using an exact revision fence, under the
// session mutation lock. A changed template or policy can never redirect an
// existing session to another host. The first pin requires conditional writes;
// unavailable/off capability refuses instead of degrading to an unsafe write.
func (s *Store) PinRuntimeBinding(id string, binding runtime.RuntimeBinding) error {
	if err := validateRuntimeBinding(binding); err != nil {
		return err
	}
	return WithSessionMutationLock(id, func() error {
		row, err := s.validatedBead(id)
		if err != nil {
			return err
		}
		current, ok, err := decodeRuntimeBinding(id, row.Metadata[runtimeBindingMetadataKey])
		if err != nil {
			return err
		}
		if ok {
			if current != binding {
				return fmt.Errorf("session %q: runtime binding is immutable", id)
			}
			return nil
		}
		raw, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		writer, _, err := beads.ResolveConditionalWriter(s.store)
		if err != nil {
			return err
		}
		if writer == nil {
			return fmt.Errorf("session %q: runtime binding requires conditional session writes", id)
		}
		if err = writer.UpdateIfMatch(id, row.Revision, beads.UpdateOpts{Metadata: map[string]string{runtimeBindingMetadataKey: string(raw)}}); err != nil {
			return fmt.Errorf("pinning session %q runtime: %w", id, err)
		}
		return nil
	})
}

// RuntimeRoute resolves one exact provider session name. A missing record is
// distinguishable from a store failure, and duplicate names are refused.
func (s *Store) RuntimeRoute(name string) (id, template string, binding runtime.RuntimeBinding, found bool, err error) {
	rows, err := s.store.List(beads.ListQuery{Metadata: map[string]string{"session_name": name}, IncludeClosed: true, Live: true})
	if err != nil {
		return "", "", binding, false, err
	}
	if len(rows) == 0 {
		return "", "", binding, false, nil
	}
	if len(rows) != 1 {
		// Closed local history commonly reuses a name. Preserve the unique open
		// legacy route, but never choose between an old remote pin and another SID.
		open := -1
		for i, row := range rows {
			if row.Metadata[runtimeBindingMetadataKey] != "" {
				return "", "", binding, false, fmt.Errorf("ambiguous pinned runtime session %q", name)
			}
			if row.Status != "closed" {
				if open != -1 {
					return "", "", binding, false, fmt.Errorf("ambiguous runtime session %q", name)
				}
				open = i
			}
		}
		if open >= 0 {
			rows = rows[open : open+1]
		} else {
			rows = rows[:1]
		}
	}
	row := rows[0]
	if row.ID == "" || !IsSessionBeadOrRepairable(row) {
		return "", "", binding, false, ErrSessionNotFound
	}
	b, ok, err := decodeRuntimeBinding(row.ID, row.Metadata[runtimeBindingMetadataKey])
	return row.ID, row.Metadata["template"], b, ok, err
}

// RuntimeBindings lists pins independently of currently configured templates.
func (s *Store) RuntimeBindings() ([]runtime.RuntimeBinding, error) {
	rows, err := s.listAllBeads(ListAllOptions{IncludeClosed: true, Live: true})
	if err != nil {
		return nil, err
	}
	var out []runtime.RuntimeBinding
	for _, row := range rows {
		b, ok, err := decodeRuntimeBinding(row.ID, row.Metadata[runtimeBindingMetadataKey])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, b)
		}
	}
	return out, nil
}
