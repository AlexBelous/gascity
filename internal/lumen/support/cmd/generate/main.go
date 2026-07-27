// Command generate rebuilds the checked-in Lumen support matrix from an
// already-present upstream checkout; it never accesses the network.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/lumen/support"
)

const matrixPath = "internal/lumen/support/testdata/matrix.json"

func main() {
	root := flag.String("upstream", "", "path to the pinned formula-language checkout")
	write := flag.Bool("write", false, "replace the checked-in support matrix")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: generate -upstream <checkout> [-write]")
		os.Exit(2)
	}
	data, err := support.Generate(*root)
	data = append(data, '\n')
	if err == nil && *write {
		err = writeMatrix(matrixPath, data)
	} else if err == nil {
		var checkedIn []byte
		checkedIn, err = os.ReadFile(matrixPath)
		if err == nil && !bytes.Equal(checkedIn, data) {
			err = fmt.Errorf("%s is stale; rerun with -write", matrixPath)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeMatrix(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".matrix-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() {
		if removeErr := os.Remove(name); removeErr != nil && !os.IsNotExist(removeErr) {
			fmt.Fprintln(os.Stderr, removeErr)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
