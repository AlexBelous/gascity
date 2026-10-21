//go:build !linux

package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newRemoteWorkCmd(_ io.Writer, stderr io.Writer) *cobra.Command {
	return &cobra.Command{Use: "remote-work", Hidden: true, Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		_, _ = fmt.Fprintln(stderr, "gc remote-work: Linux root intake required")
		return errExit
	}}
}
