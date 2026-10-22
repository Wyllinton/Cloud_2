package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GuestControlOptions configures the Guest Additions transport.
type GuestControlOptions struct {
	// VBoxManage is the path (or name) of the VBoxManage executable on the host.
	VBoxManage string
	// VM is the VirtualBox machine name or UUID.
	VM string
	// User is an existing account on the guest operating system.
	User string
	// Password is the guest account password (may be empty if the guest allows
	// empty passwords).
	Password string
	// PasswordFile is an optional --passwordfile value. VirtualBox documents it
	// as a path on the guest, so use it only if you know what you are doing.
	PasswordFile string
	// Domain is the Windows domain, when applicable.
	Domain string
	// Sudo prefixes guest commands with `sudo -n`.
	Sudo bool
	// Timeout bounds each guest command; zero means no explicit limit.
	Timeout time.Duration
}

// GuestControl executes commands on a VirtualBox guest through the Guest
// Additions, using `VBoxManage guestcontrol` on the host.
//
// The transport is intended for the bootstrap phase, before SSH is available.
// File writes and reads use the copyto/copyfrom subcommands because VBoxManage
// does not offer a reliable stdin/stdout stream for arbitrary files.
type GuestControl struct {
	opts GuestControlOptions
}

// NewGuestControl builds the Guest Additions transport.
func NewGuestControl(opts GuestControlOptions) *GuestControl {
	if strings.TrimSpace(opts.VBoxManage) == "" {
		opts.VBoxManage = "VBoxManage"
	}
	if strings.TrimSpace(opts.User) == "" {
		opts.User = "root"
	}
	return &GuestControl{opts: opts}
}

func (g *GuestControl) Kind() string   { return "guestcontrol" }
func (g *GuestControl) UsesSudo() bool { return g.opts.Sudo }
func (g *GuestControl) Close() error   { return nil }

func (g *GuestControl) Target() string {
	return fmt.Sprintf("%s (invitado: %s)", g.opts.VM, g.opts.User)
}

// Ping verifies that the Guest Additions channel is usable by running `true` in
// the guest.
func (g *GuestControl) Ping(ctx context.Context) error {
	res := g.Run(ctx, nil, "true")
	if res.Ok() {
		return nil
	}
	if res.Err != nil {
		return res.Err
	}
	return fmt.Errorf("el proceso invitado terminó con código %d", res.ExitCode)
}

// Run executes a command in the guest. Standard input is not supported by this
// transport; use WriteFile instead.
func (g *GuestControl) Run(ctx context.Context, input []byte, name string, args ...string) Result {
	argv := buildArgv(g.opts.Sudo, name, args...)
	if input != nil {
		return Result{
			Argv:        argv,
			Transport:   "guestcontrol",
			ExitCode:    -1,
			Title:       "Entrada estándar no soportada",
			HostCommand: "",
			Err:         errors.New("el transporte Guest Additions no admite entrada estándar; use la transferencia de archivos"),
		}
	}
	return g.runGuest(ctx, argv)
}

// runGuest wraps the command in a fixed shell script that records the guest
// exit code in a machine-readable trailer. The user-provided argv is passed as
// positional parameters, never interpolated into the script, so no injection is
// possible.
func (g *GuestControl) runGuest(ctx context.Context, argv []string) Result {
	token := randomToken()
	vboxArgs := g.runArgs(ctx, token, argv)

	host := g.execHost(ctx, vboxArgs)
	res := Result{
		Argv:        argv,
		Transport:   "guestcontrol",
		Duration:    host.Duration,
		HostCommand: ShellQuote(redactArgs(vboxArgs)),
	}

	cleaned, code, found := parseGuestExit(host.Stdout, token)
	if found {
		res.Stdout = cleaned
		res.Stderr = host.Stderr
		res.ExitCode = code
		return res
	}

	// No trailer means the guest process never ran to completion: VM powered
	// off, wrong credentials, Guest Additions unavailable, or a timeout.
	res.Stdout = host.Stdout
	res.Stderr = host.Stderr
	res.ExitCode = -1
	res.Err = fmt.Errorf("VBoxManage guestcontrol falló (código %d)", host.ExitCode)
	return res
}

// runArgs builds the full VBoxManage argv for `guestcontrol run`. It is a pure
// function so the argument layout can be unit tested.
func (g *GuestControl) runArgs(ctx context.Context, token string, argv []string) []string {
	// Single-line, fixed script: the user argv arrives as positional
	// parameters, so it is never interpolated into shell code. The trailer
	// carries the guest exit code back to the host in a machine-readable way.
	script := fmt.Sprintf(`command "$@"; rc=$?; printf '\nNP2RC:%s:%%d\n' "$rc"; exit 0`, token)

	guestArgs := make([]string, 0, len(argv)+3)
	guestArgs = append(guestArgs, "-c", script, "sh")
	guestArgs = append(guestArgs, argv...)

	vboxArgs := g.baseArgs("run")
	vboxArgs = append(vboxArgs,
		"--exe=/bin/sh",
		"--wait-stdout",
		"--wait-stderr",
		"--unquoted-args",
	)
	if ms := g.timeoutMS(ctx); ms > 0 {
		vboxArgs = append(vboxArgs, "--timeout="+strconv.FormatInt(ms, 10))
	}
	vboxArgs = append(vboxArgs, "--")
	vboxArgs = append(vboxArgs, guestArgs...)
	return vboxArgs
}

// ReadFile copies a guest file to a host temporary directory and returns it.
func (g *GuestControl) ReadFile(ctx context.Context, guestPath string) ([]byte, Result, error) {
	dir, err := os.MkdirTemp("", "np2-gc-read-*")
	if err != nil {
		return nil, Result{}, fmt.Errorf("no se pudo crear el directorio temporal: %w", err)
	}
	defer os.RemoveAll(dir)

	args := g.baseArgs("copyfrom")
	args = append(args, "--target-directory="+dir, guestPath)
	res := g.execHost(ctx, args)
	res.Argv = redactArgs(res.Argv)
	res.Transport = "guestcontrol"
	res.Title = "Guest Additions: leer " + guestPath
	if !res.Ok() {
		return nil, res, fmt.Errorf("copyfrom %s: %s", guestPath, res.ErrorString())
	}

	data, err := os.ReadFile(filepath.Join(dir, path.Base(guestPath)))
	if err != nil {
		return nil, res, fmt.Errorf("no se pudo leer el archivo copiado desde el invitado: %w", err)
	}
	return data, res, nil
}

// WriteFile copies data to a host temporary file, transfers it into the guest
// and moves it to its final destination, setting the requested permissions.
func (g *GuestControl) WriteFile(ctx context.Context, target string, data []byte, mode uint32) ([]Result, error) {
	dir, err := os.MkdirTemp("", "np2-gc-write-*")
	if err != nil {
		return nil, fmt.Errorf("no se pudo crear el directorio temporal: %w", err)
	}
	defer os.RemoveAll(dir)

	base := "np2-" + randomToken()
	hostFile := filepath.Join(dir, base)
	if err := os.WriteFile(hostFile, data, 0600); err != nil {
		return nil, fmt.Errorf("no se pudo escribir el archivo temporal: %w", err)
	}
	guestTmp := path.Join("/tmp", base)

	var steps []Result

	copyArgs := g.baseArgs("copyto")
	copyArgs = append(copyArgs, "--target-directory=/tmp", hostFile)
	copyRes := g.execHost(ctx, copyArgs)
	copyRes.Argv = redactArgs(copyRes.Argv)
	copyRes.Transport = "guestcontrol"
	copyRes.Title = "Guest Additions: copiar archivo a " + target
	steps = append(steps, copyRes)
	if !copyRes.Ok() {
		return steps, fmt.Errorf("copyto %s: %s", target, copyRes.ErrorString())
	}

	mv := g.runGuest(ctx, buildArgv(g.opts.Sudo, "mv", "-f", "--", guestTmp, target))
	mv.Title = "Guest Additions: mover a " + target
	steps = append(steps, mv)
	if !mv.Ok() {
		return steps, fmt.Errorf("mv %s: %s", target, mv.ErrorString())
	}

	if mode != 0 {
		chmod := g.runGuest(ctx, buildArgv(g.opts.Sudo, "chmod", "0"+strconv.FormatUint(uint64(mode), 8), "--", target))
		chmod.Title = "Guest Additions: permisos de " + target
		steps = append(steps, chmod)
		if !chmod.Ok() {
			return steps, fmt.Errorf("chmod %s: %s", target, chmod.ErrorString())
		}
	}
	return steps, nil
}

// --- helpers -----------------------------------------------------------------

func (g *GuestControl) baseArgs(sub string) []string {
	args := []string{"--nologo", "guestcontrol", g.opts.VM, sub}
	if g.opts.User != "" {
		args = append(args, "--username="+g.opts.User)
	}
	switch {
	case g.opts.PasswordFile != "":
		args = append(args, "--passwordfile="+g.opts.PasswordFile)
	case g.opts.Password != "":
		args = append(args, "--password="+g.opts.Password)
	}
	if g.opts.Domain != "" {
		args = append(args, "--domain="+g.opts.Domain)
	}
	return args
}

func (g *GuestControl) execHost(ctx context.Context, args []string) Result {
	argv := append([]string{g.opts.VBoxManage}, args...)
	res := Result{Argv: argv, Transport: "guestcontrol"}

	cmd := exec.CommandContext(ctx, g.opts.VBoxManage, args...)
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

func (g *GuestControl) timeoutMS(ctx context.Context) int64 {
	d := g.opts.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 1
		}
		if d <= 0 || remaining < d {
			d = remaining
		}
	}
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// parseGuestExit removes the exit-code trailer from stdout and returns the
// guest exit code. It is a pure function and is unit tested.
func parseGuestExit(stdout, token string) (cleaned string, code int, found bool) {
	marker := "NP2RC:" + token + ":"
	idx := strings.LastIndex(stdout, marker)
	if idx < 0 {
		return stdout, 0, false
	}
	rest := stdout[idx+len(marker):]
	end := strings.IndexByte(rest, '\n')
	numStr := rest
	if end >= 0 {
		numStr = rest[:end]
	}
	code, err := strconv.Atoi(strings.TrimSpace(numStr))
	if err != nil {
		return stdout, 0, false
	}

	start := idx
	if start > 0 && stdout[start-1] == '\n' {
		start--
	}
	stop := idx + len(marker) + len(numStr)
	if stop < len(stdout) && stdout[stop] == '\n' {
		stop++
	}
	return stdout[:start] + stdout[stop:], code, true
}

func redactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, a := range out {
		if strings.HasPrefix(a, "--password=") {
			out[i] = "--password=***"
		}
	}
	return out
}

func randomToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}
