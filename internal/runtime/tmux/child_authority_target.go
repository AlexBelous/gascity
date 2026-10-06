package tmux

import (
	"context"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
)

const authorityTargetRosterFormat = "#{session_name}\t#{GC_SESSION_ID}\t#{pane_pid}"

// readAuthorityTarget pins the independently persisted handle and SID to a
// single tmux roster read before acquiring its private environment. The roster
// checks all session names, nonempty SIDs, and pane PIDs for ambiguity, while
// an unrelated session's missing ENV or subsequent exit cannot deny the target.
func readAuthorityTarget(ctx context.Context, targetName, targetSID string, run authorityRawCommand) (runtime.SessionAuthorityHandle, error) {
	var zero runtime.SessionAuthorityHandle
	if ctx == nil || ctx.Err() != nil || run == nil || targetName == "" || targetSID == "" ||
		strings.ContainsAny(targetName, "\x00\r\n\t") || strings.ContainsAny(targetSID, "\x00\r\n\t") {
		return zero, errSessionAuthoritySnapshot
	}
	raw, err := run(ctx, "list-sessions", "-F", authorityTargetRosterFormat)
	if err != nil || ctx.Err() != nil || len(raw) == 0 || len(raw) > 65536 || raw[len(raw)-1] != '\n' {
		return zero, errSessionAuthoritySnapshot
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	if len(lines) > 4096 {
		return zero, errSessionAuthoritySnapshot
	}
	seenName := map[string]bool{}
	seenSID := map[string]bool{}
	seenPID := map[int]bool{}
	targetPID := 0
	for _, line := range lines {
		parts := strings.Split(line, "\t")
		if ctx.Err() != nil || len(parts) != 3 || parts[0] == "" || len(parts[0]) > 4096 ||
			len(parts[1]) > 4096 || strings.ContainsAny(line, "\x00\r") || seenName[parts[0]] {
			return zero, errSessionAuthoritySnapshot
		}
		pid, err := strconv.ParseInt(parts[2], 10, 32)
		if err != nil || pid <= 1 || strconv.FormatInt(pid, 10) != parts[2] || seenPID[int(pid)] {
			return zero, errSessionAuthoritySnapshot
		}
		seenName[parts[0]] = true
		seenPID[int(pid)] = true
		if parts[1] != "" {
			if seenSID[parts[1]] {
				return zero, errSessionAuthoritySnapshot
			}
			seenSID[parts[1]] = true
		}
		if parts[0] == targetName {
			if parts[1] != targetSID {
				return zero, errSessionAuthoritySnapshot
			}
			targetPID = int(pid)
		}
	}
	if targetPID == 0 {
		return zero, errSessionAuthoritySnapshot
	}
	args := []string{}
	for i, key := range authorityProviderKeys {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "show-environment", "-t", "="+targetName, key)
	}
	fields, err := run(ctx, args...)
	if err != nil || ctx.Err() != nil {
		return zero, errSessionAuthoritySnapshot
	}
	env, err := decodeAuthorityFields(fields, authorityProviderKeys)
	for i := range fields {
		fields[i] = 0
	}
	if err != nil || strings.TrimPrefix(env[0], "GC_SESSION_ID=") != targetSID {
		return zero, errSessionAuthoritySnapshot
	}
	rawPID, err := run(ctx, "display-message", "-t", "="+targetName+":^.0", "-p", "#{pane_pid}")
	if err != nil || ctx.Err() != nil || len(rawPID) < 2 || rawPID[len(rawPID)-1] != '\n' {
		return zero, errSessionAuthoritySnapshot
	}
	text := string(rawPID[:len(rawPID)-1])
	pid, err := strconv.ParseInt(text, 10, 32)
	if err != nil || pid != int64(targetPID) || strconv.FormatInt(pid, 10) != text {
		return zero, errSessionAuthoritySnapshot
	}
	return runtime.SessionAuthorityHandle{Name: targetName, PID: targetPID, Environment: env}, nil
}
