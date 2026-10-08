package tmux

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
)

var (
	errSessionAuthoritySnapshot = errors.New("provider session authority unavailable")
	authorityProviderKeys       = []string{"GC_SESSION_ID", "GC_TEMPLATE", "GC_RUNTIME_EPOCH", "GC_INSTANCE_TOKEN", "BEADS_HOLDER_TOKEN", "GC_CITY_PATH"}
)

type authorityRawCommand func(context.Context, ...string) ([]byte, error)

func decodeAuthorityFields(raw []byte, keys []string) ([]string, error) {
	if len(raw) == 0 || len(raw) > 32768 || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\r') >= 0 {
		return nil, errSessionAuthoritySnapshot
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	if len(lines) != len(keys) {
		return nil, errSessionAuthoritySnapshot
	}
	for i, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok || k != keys[i] || len(v) > 4096 {
			return nil, errSessionAuthoritySnapshot
		}
	}
	return lines, nil
}

func readAuthoritySnapshot(ctx context.Context, run authorityRawCommand) ([]runtime.SessionAuthorityHandle, error) {
	if ctx == nil || ctx.Err() != nil || run == nil {
		return nil, errSessionAuthoritySnapshot
	}
	raw, err := run(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil || ctx.Err() != nil || len(raw) == 0 || len(raw) > 65536 || raw[len(raw)-1] != '\n' {
		return nil, errSessionAuthoritySnapshot
	}
	names := strings.Split(string(raw[:len(raw)-1]), "\n")
	if len(names) > 4096 {
		return nil, errSessionAuthoritySnapshot
	}
	seen := map[string]bool{}
	seenPID := map[int]bool{}
	seenSID := map[string]bool{}
	out := make([]runtime.SessionAuthorityHandle, 0, len(names))
	for _, name := range names {
		if ctx.Err() != nil || name == "" || len(name) > 4096 || strings.ContainsAny(name, "\x00\r\n\t") || seen[name] {
			return nil, errSessionAuthoritySnapshot
		}
		seen[name] = true
		args := []string{}
		for i, k := range authorityProviderKeys {
			if i > 0 {
				args = append(args, ";")
			}
			args = append(args, "show-environment", "-t", "="+name, k)
		}
		fields, err := run(ctx, args...)
		if err != nil || ctx.Err() != nil {
			return nil, errSessionAuthoritySnapshot
		}
		env, err := decodeAuthorityFields(fields, authorityProviderKeys)
		for i := range fields {
			fields[i] = 0
		}
		if err != nil {
			return nil, errSessionAuthoritySnapshot
		}
		sid := strings.TrimPrefix(env[0], "GC_SESSION_ID=")
		if sid == "" || seenSID[sid] {
			return nil, errSessionAuthoritySnapshot
		}
		seenSID[sid] = true
		rawPID, err := run(ctx, "display-message", "-t", "="+name+":^.0", "-p", "#{pane_pid}")
		if err != nil || ctx.Err() != nil || len(rawPID) < 2 || rawPID[len(rawPID)-1] != '\n' {
			return nil, errSessionAuthoritySnapshot
		}
		text := string(rawPID[:len(rawPID)-1])
		pid, err := strconv.ParseInt(text, 10, 32)
		if err != nil || pid <= 1 || strconv.FormatInt(pid, 10) != text || seenPID[int(pid)] {
			return nil, errSessionAuthoritySnapshot
		}
		seenPID[int(pid)] = true
		out = append(out, runtime.SessionAuthorityHandle{Name: name, PID: int(pid), Environment: env})
	}
	if ctx.Err() != nil {
		return nil, errSessionAuthoritySnapshot
	}
	return out, nil
}
