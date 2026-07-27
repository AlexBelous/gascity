// Package canon provides the deterministic JSON representation used by graph
// records. It has no storage or domain-policy dependencies.
package canon

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Canonicalize parses JSON and emits its deterministic representation. Object
// keys are lexically ordered, whitespace is removed, duplicate keys and
// trailing data are rejected, and every zero number is represented as 0.
func Canonicalize(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("canonical JSON: invalid UTF-8")
	}
	if err := validateUTF16Surrogates(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON: decode: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("canonical JSON: trailing data")
	}
	var output bytes.Buffer
	if err := writeValue(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// validateUTF16Surrogates rejects the malformed JSON escapes encoding/json
// would otherwise replace with U+FFFD. Accepting them would make invalid input
// canonicalize to the same bytes as a literal replacement character.
func validateUTF16Surrogates(raw []byte) error {
	inString := false
	for index := 0; index < len(raw); {
		if !inString {
			if raw[index] == '"' {
				inString = true
			}
			index++
			continue
		}
		switch raw[index] {
		case '"':
			inString = false
			index++
		case '\\':
			if index+1 >= len(raw) || raw[index+1] != 'u' {
				index += 2
				continue
			}
			unit, valid := escapedUTF16(raw, index)
			if !valid {
				index += 2
				continue
			}
			switch {
			case unit >= 0xd800 && unit <= 0xdbff:
				low, valid := escapedUTF16(raw, index+6)
				if !valid || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("canonical JSON: lone high UTF-16 surrogate")
				}
				index += 12
			case unit >= 0xdc00 && unit <= 0xdfff:
				return fmt.Errorf("canonical JSON: lone low UTF-16 surrogate")
			default:
				index += 6
			}
		default:
			index++
		}
	}
	return nil
}

func escapedUTF16(raw []byte, index int) (uint16, bool) {
	if index+6 > len(raw) || raw[index] != '\\' || raw[index+1] != 'u' {
		return 0, false
	}
	var unit uint16
	for _, byteValue := range raw[index+2 : index+6] {
		digit, ok := hexDigit(byteValue)
		if !ok {
			return 0, false
		}
		unit = unit<<4 | uint16(digit)
	}
	return unit, true
}

func hexDigit(byteValue byte) (byte, bool) {
	switch {
	case byteValue >= '0' && byteValue <= '9':
		return byteValue - '0', true
	case byteValue >= 'a' && byteValue <= 'f':
		return byteValue - 'a' + 10, true
	case byteValue >= 'A' && byteValue <= 'F':
		return byteValue - 'A' + 10, true
	default:
		return 0, false
	}
}

// Hash returns the SHA-256 hash of canonical bytes. It deliberately does not
// canonicalize its input, so callers choose when validation occurs.
func Hash(payload []byte) [32]byte {
	return sha256.Sum256(payload)
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("canonical JSON: non-string object key %v", keyToken)
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("canonical JSON: duplicate object key %q", key)
			}
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("canonical JSON: unexpected delimiter %q", delimiter)
	}
}

func writeValue(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case json.Number:
		number, err := canonicalNumber(typed)
		if err != nil {
			return err
		}
		output.WriteString(number)
	case string:
		writeString(output, typed)
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeValue(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			writeString(output, key)
			output.WriteByte(':')
			if err := writeValue(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON: unsupported value %T", value)
	}
	return nil
}

func canonicalNumber(number json.Number) (string, error) {
	literal := number.String()
	if !strings.ContainsAny(literal, ".eE") {
		if literal == "-0" {
			return "0", nil
		}
		return literal, nil
	}
	value, err := strconv.ParseFloat(literal, 64)
	if err != nil {
		return "", fmt.Errorf("canonical JSON: parse number %q: %w", literal, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("canonical JSON: non-finite number %q", literal)
	}
	if value == 0 {
		return "0", nil
	}
	return strconv.FormatFloat(value, 'g', -1, 64), nil
}

func writeString(output *bytes.Buffer, value string) {
	output.WriteByte('"')
	for index := 0; index < len(value); {
		byteValue := value[index]
		if byteValue < utf8.RuneSelf {
			switch byteValue {
			case '"':
				output.WriteString(`\"`)
			case '\\':
				output.WriteString(`\\`)
			case '\n':
				output.WriteString(`\n`)
			case '\r':
				output.WriteString(`\r`)
			case '\t':
				output.WriteString(`\t`)
			case '\b':
				output.WriteString(`\b`)
			case '\f':
				output.WriteString(`\f`)
			default:
				if byteValue < 0x20 {
					fmt.Fprintf(output, `\u%04x`, byteValue)
				} else {
					output.WriteByte(byteValue)
				}
			}
			index++
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		if runeValue == utf8.RuneError && size == 1 {
			output.WriteRune(utf8.RuneError)
			index++
			continue
		}
		output.WriteString(value[index : index+size])
		index += size
	}
	output.WriteByte('"')
}
