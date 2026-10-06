package main

import (
	"context"
	"os"
	"os/exec"
	goruntime "runtime"
	"time"

	"github.com/gastownhall/gascity/internal/config"
)

func runBDCommandWithChildAuthority(ctx context.Context, cityPath string, cfg *config.City, cmd *exec.Cmd) error {
	if goruntime.GOOS != "linux" {
		return cmd.Run()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return executeBDChild(ctx, "linux", os.Environ(), cmd.Env, func(ctx context.Context) ([]string, bdChildReaders, error) {
		return acquireBDLinuxChildAuthority(ctx, cityPath, cfg)
	}, func(_ context.Context, e bdChildEnvironment) error {
		cmd.Env = e.entries
		return cmd.Run()
	})
}
