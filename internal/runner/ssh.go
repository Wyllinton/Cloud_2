package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHOptions configures a remote runner.
type SSHOptions struct {
	Host                  string
	Port                  int
	User                  string
	PrivateKey            []byte
	PrivateKeyPassphrase  string
	KnownHostsFile        string
	InsecureIgnoreHostKey bool
	Sudo                  bool
	Timeout               time.Duration
}

// SSH executes commands on a remote host over an SSH connection authenticated
// with a private key, as required once the guest starts accepting key-only
// access.
type SSH struct {
	client *ssh.Client
	sudo   bool
	target string
}

// NewSSH dials the remote host and returns a runner bound to that connection.
func NewSSH(opts SSHOptions) (*SSH, error) {
	signer, err := parsePrivateKey(opts.PrivateKey, opts.PrivateKeyPassphrase)
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}

	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	if opts.KnownHostsFile != "" {
		cb, err := knownhosts.New(opts.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts file: %w", err)
		}
		hostKeyCallback = cb
	} else if !opts.InsecureIgnoreHostKey {
		return nil, errors.New("no known_hosts file supplied: pass --ssh-known-hosts or accept the risk with --ssh-insecure-ignore-host-key")
	}

	port := opts.Port
	if port == 0 {
		port = 22
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}

	cfg := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	addr := net.JoinHostPort(opts.Host, strconv.Itoa(port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return &SSH{client: client, sudo: opts.Sudo, target: opts.User + "@" + addr}, nil
}

func parsePrivateKey(pemBytes []byte, passphrase string) (ssh.Signer, error) {
	if len(pemBytes) == 0 {
		return nil, errors.New("empty private key")
	}
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase(pemBytes, []byte(passphrase))
	}
	return ssh.ParsePrivateKey(pemBytes)
}

func (s *SSH) Kind() string   { return "ssh" }
func (s *SSH) Target() string { return s.target }
func (s *SSH) UsesSudo() bool { return s.sudo }

// Close terminates the underlying SSH connection.
func (s *SSH) Close() error { return s.client.Close() }

// Run implements Runner. The argv is joined into a single, correctly quoted
// command string because the SSH "exec" request carries a string, not an argv.
func (s *SSH) Run(ctx context.Context, input []byte, name string, args ...string) Result {
	argv := buildArgv(s.sudo, name, args...)
	res := Result{Argv: argv, Transport: "ssh"}

	session, err := s.client.NewSession()
	if err != nil {
		res.Err = fmt.Errorf("open ssh session: %w", err)
		res.ExitCode = -1
		return res
	}
	defer session.Close()

	if input != nil {
		session.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- session.Run(ShellQuote(argv)) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		res.Err = ctx.Err()
		res.ExitCode = -1
	case e := <-done:
		res.Err = e
		if e != nil {
			var ee *ssh.ExitError
			if errors.As(e, &ee) {
				res.ExitCode = ee.ExitStatus()
			} else {
				res.ExitCode = -1
			}
		}
	}
	res.Duration = time.Since(start)
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	return res
}
