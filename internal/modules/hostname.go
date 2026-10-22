package modules

import (
	"context"
	"fmt"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

// HostnameManager sets the system host name and keeps /etc/hosts in sync.
type HostnameManager struct {
	R runner.Runner
}

// NewHostnameManager builds the manager for a transport.
func NewHostnameManager(r runner.Runner) *HostnameManager { return &HostnameManager{R: r} }

// HostnameRequest carries the requested host name.
type HostnameRequest struct {
	Name string
}

const hostsPath = "/etc/hosts"
const hostnamePath = "/etc/hostname"

// Current reports the running host name and the content of the host files.
func (m *HostnameManager) Current(ctx context.Context) (name, hosts string, err error) {
	res := m.R.Run(ctx, nil, "hostname")
	if !res.Ok() {
		return "", "", fmt.Errorf("no se pudo consultar el nombre de host: %s", res.ErrorString())
	}
	name = strings.TrimSpace(res.Stdout)
	if content, found, rerr := readFile(ctx, m.R, hostsPath); rerr == nil && found {
		hosts = string(content)
	}
	return name, hosts, nil
}

// UpdateHosts returns a new /etc/hosts body that resolves the given host name to
// 127.0.1.1, removing previous entries for other names on that address. It is a
// pure function and is covered by unit tests.
func UpdateHosts(content, name string) string {
	lines := splitLines(content)
	var out []string
	placed := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == "127.0.1.1" {
			// Rebuild the 127.0.1.1 entry so that it only maps the new name.
			out = append(out, "127.0.1.1\t"+name)
			placed = true
			continue
		}
		out = append(out, line)
	}

	if !placed {
		out = append(out, "127.0.1.1\t"+name)
	}
	return strings.Join(out, "\n") + "\n"
}

// Set validates the name, applies it with hostnamectl and updates /etc/hosts.
func (m *HostnameManager) Set(ctx context.Context, req HostnameRequest) *op.Report {
	rep := op.New("Asignar nombre de host")
	defer rep.Finish()

	name, err := validate.Hostname(req.Name)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Note("Nombre de host solicitado: " + name)

	if _, ok := require(ctx, m.R, rep, "hostnamectl set-hostname "+name, nil, "hostnamectl", "set-hostname", "--", name); !ok {
		return rep
	}
	if _, ok := require(ctx, m.R, rep, "Actualizar "+hostnamePath, nil, "hostname", "--", name); !ok {
		rep.Warn("hostnamectl ya actualizó /etc/hostname; el comando hostname devolvió un error no crítico.")
	}

	current, found, err := readFile(ctx, m.R, hostsPath)
	if err != nil {
		return rep.Fail(err)
	}
	if !found {
		return rep.Fail(fmt.Errorf("no existe %s", hostsPath))
	}
	updated := UpdateHosts(string(current), name)
	if err := writeFile(ctx, m.R, rep, hostsPath, []byte(updated), 0644); err != nil {
		return rep.Fail(err)
	}

	res := m.R.Run(ctx, nil, "cat", "--", hostsPath)
	rep.Step("Verificar "+hostsPath, res)
	rep.Summary = "Nombre de host actualizado a " + name + " y " + hostsPath + " sincronizado."
	return rep
}
