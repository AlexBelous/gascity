//go:build !unix

package main

import "os"

// Unsupported platforms cannot authenticate a root-owned operator policy.
func remoteFileOwnership(_ os.FileInfo) (uint32, uint64, bool) { return 0, 0, false }
