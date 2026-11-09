package sysproxy

import (
	"context"
	"os/exec"
	"runtime"
)

// Default returns the Reader of this machine: the real commands and
// registry, and getenv for the environment (the one PostSonde started
// with). Only the app uses it; tests build a Reader with fixtures.
func Default(getenv func(string) string) Reader {
	return Reader{
		GOOS:     runtime.GOOS,
		Run:      run,
		LookPath: exec.LookPath,
		Getenv:   getenv,
		Registry: ReadRegistry,
	}
}

// run runs name with args, without a shell, and returns its standard
// output.
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
