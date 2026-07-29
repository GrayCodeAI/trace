package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func NewForegroundCommand(_ context.Context, binary string, args ...string) (*exec.Cmd, error) {
	bin, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("%s binary not on PATH: %w", binary, err)
	}
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd, nil
}
