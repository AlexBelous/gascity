package main

import "os"

// stdinIsRealTerminal reports whether stdin is an interactive terminal.
var stdinIsRealTerminal = func() bool { return true }

// resolveImplicitCWD resolves the implicit target directory used when a
// command is given no explicit path argument.
func resolveImplicitCWD() (string, error) {
	return os.Getwd()
}
