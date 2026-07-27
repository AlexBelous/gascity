package ir

import (
	"encoding/json"
	"fmt"
)

var requiredTypeFields = map[TypeKind][]string{
	TypeAtomic:  {"name"},
	TypeAlias:   {"name", "target"},
	TypeLiteral: {"value"},
	TypeUnion:   {"of"},
	TypeArray:   {"element"},
	TypeRecord:  {"fields"},
	TypeChannel: {"payload", "stream", "capability"},
	TypeHandle:  {"name"},
}

func validateInputTypes(raw json.RawMessage, label string) error {
	if len(raw) == 0 {
		return nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil {
		return fmt.Errorf("%s is invalid: %w", label, err)
	}
	rawFields, ok := input["fields"]
	if !ok {
		return nil
	}
	var fields []json.RawMessage
	if err := json.Unmarshal(rawFields, &fields); err != nil {
		return fmt.Errorf("%s.fields is invalid: %w", label, err)
	}
	for i, rawField := range fields {
		var field map[string]json.RawMessage
		if err := json.Unmarshal(rawField, &field); err != nil {
			return fmt.Errorf("%s.fields[%d] is invalid: %w", label, i, err)
		}
		rawType, ok := field["type"]
		if !ok {
			continue
		}
		if err := validateType(rawType, fmt.Sprintf("%s.fields[%d].type", label, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateType(raw json.RawMessage, label string) error {
	var typ map[string]json.RawMessage
	if err := json.Unmarshal(raw, &typ); err != nil {
		return fmt.Errorf("%s is invalid: %w", label, err)
	}
	var kind TypeKind
	rawKind, ok := typ["kind"]
	if !ok {
		return fmt.Errorf("%s missing required field %q", label, "kind")
	}
	if err := json.Unmarshal(rawKind, &kind); err != nil {
		return fmt.Errorf("%s has invalid kind: %w", label, err)
	}
	if !KnownTypeKinds[kind] {
		return fmt.Errorf("%s has unknown type kind %q", label, kind)
	}
	for _, field := range requiredTypeFields[kind] {
		if _, ok := typ[field]; !ok {
			return fmt.Errorf("%s (%s) missing required field %q", label, kind, field)
		}
	}

	if rawCapability, ok := typ["capability"]; ok {
		var capability ChannelCapability
		if err := json.Unmarshal(rawCapability, &capability); err != nil {
			return fmt.Errorf("%s has invalid capability: %w", label, err)
		}
		switch capability {
		case CapSource, CapSink, CapAll:
		default:
			return fmt.Errorf("%s has unknown channel capability %q", label, capability)
		}
	}

	for _, field := range []string{"target", "element", "additionalFields", "payload"} {
		if child, ok := typ[field]; ok {
			if err := validateType(child, label+"."+field); err != nil {
				return err
			}
		}
	}
	if rawOf, ok := typ["of"]; ok {
		var members []json.RawMessage
		if err := json.Unmarshal(rawOf, &members); err != nil {
			return fmt.Errorf("%s.of is invalid: %w", label, err)
		}
		for i, member := range members {
			if err := validateType(member, fmt.Sprintf("%s.of[%d]", label, i)); err != nil {
				return err
			}
		}
	}
	if rawFields, ok := typ["fields"]; ok {
		var fields []json.RawMessage
		if err := json.Unmarshal(rawFields, &fields); err != nil {
			return fmt.Errorf("%s.fields is invalid: %w", label, err)
		}
		for i, rawField := range fields {
			var field map[string]json.RawMessage
			if err := json.Unmarshal(rawField, &field); err != nil {
				return fmt.Errorf("%s.fields[%d] is invalid: %w", label, i, err)
			}
			rawFieldType, ok := field["type"]
			if !ok {
				return fmt.Errorf("%s.fields[%d] missing required field %q", label, i, "type")
			}
			if err := validateType(rawFieldType, fmt.Sprintf("%s.fields[%d].type", label, i)); err != nil {
				return err
			}
		}
	}
	return nil
}
