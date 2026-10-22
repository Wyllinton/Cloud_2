package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Local runs commands on the machine where the web application is running.
//
// This is the transport used while the virtual machine lacks SSH: the binary is
// started inside the guest through the Guest Additions channel. It also keeps
// the application testable without a remote host.
type Local struct {
	sudo bool
}

// NewLocal returns a runner that executes commands locally. When sudo is true
// every command is prefixed with `sudo -n`, which fails instead of prompting.
func NewLocal(sudo bool) *Local { return &Local{sudo: sudo} }

func (l *Local) Kind() string   { return "local" }
func (l *Local) Target() string { return "localhost" }
func (l *Local) UsesSudo() bool { return l.sudo }
func (l *Local) Close() error   { return nil }

// Run implements Runner.
func (l *Local) Run(ctx context.Context, input []byte, name string, args ...string) Result {
	argv := buildArgv(l.sudo, name, args...)
	res := Result{Argv: argv, Transport: "local"}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	res.Duration = time.Since(start)
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	if err != nil {
		res.Err = err
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	return res
}
