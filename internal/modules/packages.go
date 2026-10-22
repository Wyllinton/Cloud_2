package modules

import (
	"context"
	"fmt"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

// PackagesManager automates the installation and removal of Debian packages
// through apt. Every invocation is non-interactive.
type PackagesManager struct {
	R runner.Runner
}

// NewPackagesManager builds the manager for a transport.
func NewPackagesManager(r runner.Runner) *PackagesManager { return &PackagesManager{R: r} }

// PackageRequest describes an install or removal request.
type PackageRequest struct {
	Package string
	// Update runs `apt-get update` before installing.
	Update bool
	// Purge removes configuration files as well as the package.
	Purge bool
	// Autoremove removes packages that are no longer needed afterwards.
	Autoremove bool
}

// apt runs apt-get with a non-interactive frontend.
func (m *PackagesManager) apt(ctx context.Context, args ...string) runner.Result {
	full := append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get"}, args...)
	return m.R.Run(ctx, nil, "env", full...)
}

// Status queries dpkg for the state and version of a package.
func (m *PackagesManager) Status(ctx context.Context, name string) (string, error) {
	pkg, err := validate.Package(name)
	if err != nil {
		return "", err
	}
	res := m.R.Run(ctx, nil, "dpkg-query", "-W", "-f", "${db:Status-Abbrev} ${Version}", "--", pkg)
	if !res.Ok() {
		return "no instalado", nil
	}
	line := firstNonEmpty(res.Stdout, "desconocido")
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return line, nil
}

// Install installs a package.
func (m *PackagesManager) Install(ctx context.Context, req PackageRequest) *op.Report {
	pkg, err := validate.Package(req.Package)
	rep := op.New("Instalar paquete " + pkg)
	defer rep.Finish()
	if err != nil {
		return rep.Fail(err)
	}

	rep.Note("apt se ejecuta con DEBIAN_FRONTEND=noninteractive y -y, sin intervención del usuario.")

	if req.Update {
		if _, ok := require(ctx, m.R, rep, "apt-get update", nil, "env",
			"DEBIAN_FRONTEND=noninteractive", "apt-get", "update"); !ok {
			return rep
		}
	}
	if _, ok := require(ctx, m.R, rep, "apt-get install -y "+pkg, nil, "env",
		"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--", pkg); !ok {
		rep.Warn("Revise el detalle de stderr; un error habitual es el paquete inexistente o un lock de dpkg activo.")
		return rep
	}

	res := m.apt(ctx, "list", "--installed", "--", pkg)
	rep.Step("Verificar instalación de "+pkg, res)
	rep.Summary = fmt.Sprintf("El paquete %s se instaló correctamente.", pkg)
	return rep
}

// Remove removes (or purges) a package.
func (m *PackagesManager) Remove(ctx context.Context, req PackageRequest) *op.Report {
	pkg, err := validate.Package(req.Package)
	rep := op.New("Eliminar paquete " + pkg)
	defer rep.Finish()
	if err != nil {
		return rep.Fail(err)
	}

	action := "remove"
	if req.Purge {
		action = "purge"
	}
	if _, ok := require(ctx, m.R, rep, "apt-get "+action+" -y "+pkg, nil, "env",
		"DEBIAN_FRONTEND=noninteractive", "apt-get", action, "-y", "--", pkg); !ok {
		return rep
	}
	if req.Autoremove {
		if _, ok := require(ctx, m.R, rep, "apt-get autoremove -y", nil, "env",
			"DEBIAN_FRONTEND=noninteractive", "apt-get", "autoremove", "-y"); !ok {
			return rep
		}
	}
	rep.Summary = fmt.Sprintf("El paquete %s se eliminó correctamente.", pkg)
	return rep
}

// Installed returns the list of installed packages, used to populate the UI.
func (m *PackagesManager) Installed(ctx context.Context, limit int) ([]string, error) {
	res := m.R.Run(ctx, nil, "dpkg-query", "-W", "-f", "${binary:Package}\\t${Version}\\n")
	if !res.Ok() {
		return nil, fmt.Errorf("no se pudo listar los paquetes instalados: %s", res.ErrorString())
	}
	var out []string
	for _, line := range splitLines(res.Stdout) {
		if line == "" {
			continue
		}
		out = append(out, line)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func trimOr(s, fallback string) string {
	s = firstNonEmpty(s, fallback)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, trimCR(s[start:i]))
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, trimCR(s[start:]))
	}
	return out
}

func trimCR(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
