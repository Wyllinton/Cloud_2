package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
)

// step runs a command and records it in the report. It returns the raw result.
func step(ctx context.Context, r runner.Runner, rep *op.Report, title string, input []byte, name string, args ...string) runner.Result {
	res := r.Run(ctx, input, name, args...)
	rep.Step(title, res)
	return res
}

// require runs a command, records it and aborts the operation when it fails.
// The returned bool is false when the caller must stop.
func require(ctx context.Context, r runner.Runner, rep *op.Report, title string, input []byte, name string, args ...string) (runner.Result, bool) {
	res := step(ctx, r, rep, title, input, name, args...)
	if res.Ok() {
		return res, true
	}
	rep.Failf("%s: %s", title, firstNonEmpty(res.ErrorString(), "el comando terminó con código "+strconv.Itoa(res.ExitCode)))
	return res, false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// exists reports whether path exists on the target. It distinguishes "the file
// does not exist" (false, nil) from "the transport could not be reached"
// (false, error), so callers do not mistake an unreachable host for a missing
// file.
func exists(ctx context.Context, r runner.Runner, path string) (bool, error) {
	res := r.Run(ctx, nil, "test", "-e", path)
	if res.Ok() {
		return true, nil
	}
	if res.TransportFailed() {
		return false, fmt.Errorf("no se pudo comprobar %s: %s", path, firstNonEmpty(res.ErrorString(), "transporte no disponible"))
	}
	if res.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("no se pudo comprobar %s: %s", path, firstNonEmpty(res.ErrorString(), "el comando terminó con código "+strconv.Itoa(res.ExitCode)))
}

// executableFile reports whether path is a regular file with execute
// permission. It distinguishes "the path is missing or not executable"
// (false, nil) from "the transport could not be reached" (false, error).
func executableFile(ctx context.Context, r runner.Runner, path string) (bool, error) {
	for _, flag := range []string{"-f", "-x"} {
		res := r.Run(ctx, nil, "test", flag, path)
		if res.Ok() {
			continue
		}
		if res.TransportFailed() {
			return false, fmt.Errorf("no se pudo comprobar %s: %s", path, firstNonEmpty(res.ErrorString(), "transporte no disponible"))
		}
		if res.ExitCode == 1 {
			return false, nil
		}
		return false, fmt.Errorf("no se pudo comprobar %s: %s", path, firstNonEmpty(res.ErrorString(), "el comando terminó con código "+strconv.Itoa(res.ExitCode)))
	}
	return true, nil
}

// readFile reads a target file. Transports that implement FileTransfer (the
// Guest Additions channel) use their own copy mechanism; the rest read through
// cat.
func readFile(ctx context.Context, r runner.Runner, path string) ([]byte, bool, error) {
	found, err := exists(ctx, r, path)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	if ft, ok := r.(runner.FileTransfer); ok {
		data, _, err := ft.ReadFile(ctx, path)
		if err != nil {
			return nil, true, fmt.Errorf("no se pudo leer %s: %w", path, err)
		}
		return data, true, nil
	}

	res := r.Run(ctx, nil, "cat", "--", path)
	if !res.Ok() {
		return nil, true, fmt.Errorf("no se pudo leer %s: %s", path, res.ErrorString())
	}
	return []byte(res.Stdout), true, nil
}

// writeFile writes data to path with the given permissions, recording each
// command in the report. Transports that implement FileTransfer use their own
// copy mechanism.
func writeFile(ctx context.Context, r runner.Runner, rep *op.Report, path string, data []byte, mode uint32) error {
	if ft, ok := r.(runner.FileTransfer); ok {
		steps, err := ft.WriteFile(ctx, path, data, mode)
		for _, s := range steps {
			rep.Step("Transferir "+path, s)
		}
		if err != nil {
			return err
		}
		return nil
	}

	if _, ok := require(ctx, r, rep, "Escribir "+path, data, "tee", "--", path); !ok {
		return fmt.Errorf("escritura de %s", path)
	}
	if mode != 0 {
		if _, ok := require(ctx, r, rep, "Ajustar permisos de "+path, nil, "chmod", octal(mode), "--", path); !ok {
			return fmt.Errorf("permisos de %s", path)
		}
	}
	return nil
}

func octal(mode uint32) string { return "0" + strconv.FormatUint(uint64(mode), 8) }
