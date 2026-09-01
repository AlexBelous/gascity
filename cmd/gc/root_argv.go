package main

import "strings"

// rootCommandOptions controls side effects performed while constructing the
// Cobra tree. invocationArgs is always the injected run(args) slice and never
// includes argv[0].
type rootCommandOptions struct {
	invocationArgs            []string
	discoverPackCommands      bool
	eagerPackCommandDiscovery bool
}

func rootCommandOptionsForArgs(args []string) rootCommandOptions {
	command, ok := firstRootCommand(args)
	discoverPackCommands := !ok || !rootCommandSkipsPackDiscovery(command)
	return rootCommandOptions{
		invocationArgs:            append([]string(nil), args...),
		discoverPackCommands:      discoverPackCommands,
		eagerPackCommandDiscovery: discoverPackCommands,
	}
}

// rootCommandSkipsPackDiscovery identifies built-in commands that cannot
// resolve to a pack binding. Pack discovery only adds city-config and pack
// loading work; each command still performs its normal scope and config
// resolution when it runs.
func rootCommandSkipsPackDiscovery(command string) bool {
	switch command {
	case "metrics", "bd", "git-credential", "dolt-state", "dolt-config", "bd-store-bridge":
		return true
	default:
		return false
	}
}

// rootInvocationMayNeedPackDiscovery reports whether the injected invocation
// can resolve to a pack-provided root command. Once the built-in Cobra tree is
// available, every built-in name and alias can skip eager city/pack loading;
// packs cannot shadow those names. Unknown or ambiguous argv stays fail-closed
// and keeps discovery enabled so pack commands continue to resolve.
func rootInvocationMayNeedPackDiscovery(rootBuiltins map[string]bool, args []string) bool {
	command, ok := firstRootCommand(args)
	if !ok {
		return true
	}
	return !rootBuiltins[command]
}

// firstRootCommand returns the first command word under the root's narrow
// persistent-scope grammar. Unknown flags fail closed because this pre-scan
// cannot know whether a later token is their value. A separate known value
// flag consumes exactly one following token, including "--", matching pflag.
func firstRootCommand(args []string) (string, bool) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			return "", false
		case isRootPersistentValueFlag(arg):
			if index+1 >= len(args) {
				return "", false
			}
			index++
		case isRootPersistentValueAssignment(arg):
			continue
		case strings.HasPrefix(arg, "-"):
			return "", false
		default:
			return arg, true
		}
	}
	return "", false
}

func isRootPersistentValueFlag(arg string) bool {
	switch arg {
	case "--city", "--rig", "--context", "--city-url", "--city-name":
		return true
	default:
		return false
	}
}

func isRootPersistentValueAssignment(arg string) bool {
	name, _, hasValue := strings.Cut(arg, "=")
	return hasValue && isRootPersistentValueFlag(name)
}
