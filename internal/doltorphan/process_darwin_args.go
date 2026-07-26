package doltorphan

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// parseDarwinProcArgs decodes the KERN_PROCARGS2 payload returned by macOS.
// The payload starts with argc, then the executable path, NUL padding, argv,
// and finally the environment. Reading exactly argc entries preserves argv
// boundaries and avoids treating environment entries as command arguments.
func parseDarwinProcArgs(raw []byte) ([]string, error) {
	const argcSize = 4
	if len(raw) < argcSize {
		return nil, errors.New("KERN_PROCARGS2 payload is missing argc")
	}
	argc := int(binary.LittleEndian.Uint32(raw[:argcSize]))
	if argc <= 0 {
		return nil, fmt.Errorf("KERN_PROCARGS2 argc is %d", argc)
	}

	payload := raw[argcSize:]
	executableEnd := bytes.IndexByte(payload, 0)
	if executableEnd < 0 {
		return nil, errors.New("KERN_PROCARGS2 payload is missing the executable terminator")
	}
	payload = payload[executableEnd+1:]
	for len(payload) > 0 && payload[0] == 0 {
		payload = payload[1:]
	}
	if argc > len(payload) {
		return nil, fmt.Errorf("KERN_PROCARGS2 argc %d exceeds remaining payload size %d", argc, len(payload))
	}

	argv := make([]string, 0, argc)
	for len(argv) < argc {
		argEnd := bytes.IndexByte(payload, 0)
		if argEnd < 0 {
			return nil, fmt.Errorf("KERN_PROCARGS2 payload ended after %d of %d arguments", len(argv), argc)
		}
		argv = append(argv, string(payload[:argEnd]))
		payload = payload[argEnd+1:]
	}
	return argv, nil
}
