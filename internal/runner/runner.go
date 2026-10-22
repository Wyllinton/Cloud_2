// Package runner provides a uniform, injection-safe command execution
// abstraction over a local host, a remote host reached through SSH, or a
// VirtualBox guest reached through the Guest Additions (VBoxManage).
//
// Every command is expressed as an explicit argument vector (argv). The
// package never builds a command by concatenating user input into a shell
// string; the only place where quoting happens is the SSH transport, which
// requires a single command string, and there every argument is POSIX
// single-quoted before being sent.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Result captures everything needed to diagnose a single command execution.
// It is intentionally rich: the application must be able to show the user
// what ran, what it printed and how it ended.
type Result struct {
	Argv        []string
	Stdout      string
	Stderr      string
	ExitCode    int
	Err         error
	Duration    time.Duration
	Title       string
	Transport   string
	HostCommand string
}

// Ok reports whether the command completed with exit status 0.
func (r Result) Ok() bool { return r.Err == nil && r.ExitCode == 0 }

// CommandLine renders the argv as a human readable, shell-quoted string. It is
// used for display and logging only and is never executed.
func (r Result) CommandLine() string { return ShellQuote(r.Argv) }

// ErrorString returns the most useful diagnostic message available.
func (r Result) ErrorString() string {
	stderr := strings.TrimSpace(r.Stderr)
	if r.Err != nil {
		if stderr != "" {
			return fmt.Sprintf("%v: %s", r.Err, firstLine(stderr))
		}
		return r.Err.Error()
	}
	if r.ExitCode != 0 && stderr != "" {
		return firstLine(stderr)
	}
	return ""
}

// TransportFailed reports whether the command could not be executed at all
// because the transport is unavailable (for example, the SSH connection dropped
// or VBoxManage could not reach the guest). A guest process that simply exited
// with a non-zero status is not a transport failure.
func (r Result) TransportFailed() bool {
	if r.Err == nil {
		return false
	}
	var se *ssh.ExitError
	if errors.As(r.Err, &se) {
		return false
	}
	var ee *exec.ExitError
	if errors.As(r.Err, &ee) {
		return false
	}
	return true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// FileTransfer is implemented by transports that cannot stream a file through
// stdin/stdout and therefore need a dedicated transfer mechanism. The Guest
// Additions transport uses VBoxManage copyto/copyfrom.
type FileTransfer interface {
	// ReadFile returns the content of path. The Result describes the transfer
	// for diagnostic purposes.
	ReadFile(ctx context.Context, path string) ([]byte, Result, error)
	// WriteFile writes data to path with the given permission bits and returns
	// one Result per underlying transfer step.
	WriteFile(ctx context.Context, path string, data []byte, mode uint32) ([]Result, error)
}

// Runner executes commands on a target system.
type Runner interface {
	// Run executes name with args. When input is non-nil it is written to the
	// command's standard input. The command is never routed through a shell.
	Run(ctx context.Context, input []byte, name string, args ...string) Result
	// Kind returns a short transport label, such as "local", "ssh" or
	// "guestcontrol".
	Kind() string
	// Target describes the destination, for display purposes.
	Target() string
	// UsesSudo reports whether the runner prefixes commands with sudo.
	UsesSudo() bool
	// Close releases any resource held by the runner.
	Close() error
}

// buildArgv assembles the final argv, adding a non-interactive sudo prefix when
// required.
func buildArgv(sudo bool, name string, args ...string) []string {
	if sudo {
		argv := make([]string, 0, len(args)+3)
		argv = append(argv, "sudo", "-n", name)
		argv = append(argv, args...)
		return argv
	}
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, name)
	argv = append(argv, args...)
	return argv
}

// ShellQuote renders an argv as a single POSIX shell command string. Each
// element is single-quoted so the remote shell can only ever see literal
// arguments, never command separators supplied by a user.
func ShellQuote(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(a string) string {
	if a == "" {
		return "''"
	}
	safe := true
	for _, r := range a {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == '/', r == ':', r == '=', r == '@', r == '%', r == '+', r == ',':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}
