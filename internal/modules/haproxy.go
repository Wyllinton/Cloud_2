package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

// HAProxyManager adds and removes back-end servers in a HAProxy configuration
// file, validating the file before reloading the service.
type HAProxyManager struct {
	R          runner.Runner
	ConfigPath string
	Service    string
}

// NewHAProxyManager builds the manager for a transport.
func NewHAProxyManager(r runner.Runner, configPath, service string) *HAProxyManager {
	if strings.TrimSpace(configPath) == "" {
		configPath = "/etc/haproxy/haproxy.cfg"
	}
	if strings.TrimSpace(service) == "" {
		service = "haproxy"
	}
	return &HAProxyManager{R: r, ConfigPath: configPath, Service: service}
}

// BackendServer is a server line inside a backend block.
type BackendServer struct {
	Name    string
	Address string
	Options string
}

// Backend is a backend block with its servers.
type Backend struct {
	Name    string
	Servers []BackendServer
}

// AddServerRequest describes a server to add.
type AddServerRequest struct {
	BackendName   string
	ServerName    string
	Address       string
	Port          string
	Check         bool
	Backup        bool
	Weight        string
	CreateBackend bool
	Mode          string
	Balance       string
}

// RemoveServerRequest describes a server to remove.
type RemoveServerRequest struct {
	BackendName string
	ServerName  string
}

// CurrentContent reads the configured HAProxy file.
func (m *HAProxyManager) CurrentContent(ctx context.Context) (string, error) {
	content, found, err := readFile(ctx, m.R, m.ConfigPath)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no existe el archivo %s", m.ConfigPath)
	}
	return string(content), nil
}

// Backends lists the backend blocks found in the configuration.
func (m *HAProxyManager) Backends(cfg string) []Backend { return ParseBackends(cfg) }

// ParseBackends extracts every backend block and its servers. It is a pure
// function and is unit tested.
func ParseBackends(cfg string) []Backend {
	lines := configLines(cfg)
	var out []Backend
	idx := -1
	for _, line := range lines {
		keyword, name, ok := sectionHeader(line)
		if ok {
			if keyword == "backend" {
				out = append(out, Backend{Name: name})
				idx = len(out) - 1
			} else {
				idx = -1
			}
			continue
		}
		if idx >= 0 {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == "server" {
				out[idx].Servers = append(out[idx].Servers, BackendServer{
					Name:    fields[1],
					Address: fields[2],
					Options: strings.Join(fields[3:], " "),
				})
			}
		}
	}
	return out
}

// AddServer returns the configuration with the server added to the backend.
func AddServer(cfg string, req AddServerRequest) (string, error) {
	line := buildServerLine(req)
	lines := configLines(cfg)

	start, end, ok := findBackend(lines, req.BackendName)
	if !ok {
		if !req.CreateBackend {
			return "", fmt.Errorf("el backend %q no existe en %s", req.BackendName, req.BackendName)
		}
		return appendBackend(cfg, req, line), nil
	}

	for _, l := range lines[start+1 : end] {
		fields := strings.Fields(l)
		if len(fields) >= 2 && fields[0] == "server" && fields[1] == req.ServerName {
			return "", fmt.Errorf("el servidor %q ya existe en el backend %q", req.ServerName, req.BackendName)
		}
	}

	// Insert after the last non-empty line of the block.
	insertAt := end
	for insertAt > start+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	newLines := make([]string, 0, len(lines)+1)
	newLines = append(newLines, lines[:insertAt]...)
	newLines = append(newLines, line)
	newLines = append(newLines, lines[insertAt:]...)
	return joinConfig(newLines), nil
}

// RemoveServer returns the configuration with the server removed.
func RemoveServer(cfg string, req RemoveServerRequest) (string, error) {
	lines := configLines(cfg)
	start, end, ok := findBackend(lines, req.BackendName)
	if !ok {
		return "", fmt.Errorf("el backend %q no existe", req.BackendName)
	}
	for i := start + 1; i < end; i++ {
		fields := strings.Fields(lines[i])
		if len(fields) >= 2 && fields[0] == "server" && fields[1] == req.ServerName {
			remaining := append(append([]string{}, lines[:i]...), lines[i+1:]...)
			return joinConfig(remaining), nil
		}
	}
	return "", fmt.Errorf("el servidor %q no existe en el backend %q", req.ServerName, req.BackendName)
}

// Add applies an AddServerRequest to the live configuration.
func (m *HAProxyManager) Add(ctx context.Context, req AddServerRequest) *op.Report {
	rep := op.New("Agregar servidor al backend de HAProxy")
	defer rep.Finish()

	if err := m.normalizeAdd(&req); err != nil {
		return rep.Fail(err)
	}
	rep.Action = fmt.Sprintf("Agregar %s a %s", req.ServerName, req.BackendName)

	cfg, err := m.CurrentContent(ctx)
	if err != nil {
		return rep.Fail(err)
	}
	updated, err := AddServer(cfg, req)
	if err != nil {
		return rep.Fail(err)
	}
	return m.applyAndReload(ctx, rep, updated, "El servidor "+req.ServerName+" se agregó al backend "+req.BackendName+".")
}

// Remove applies a RemoveServerRequest to the live configuration.
func (m *HAProxyManager) Remove(ctx context.Context, req RemoveServerRequest) *op.Report {
	rep := op.New("Eliminar servidor del backend de HAProxy")
	defer rep.Finish()

	backend, err := validate.Identifier("el nombre del backend", req.BackendName)
	if err != nil {
		return rep.Fail(err)
	}
	server, err := validate.Identifier("el nombre del servidor", req.ServerName)
	if err != nil {
		return rep.Fail(err)
	}
	req.BackendName, req.ServerName = backend, server
	rep.Action = fmt.Sprintf("Eliminar %s de %s", server, backend)

	cfg, err := m.CurrentContent(ctx)
	if err != nil {
		return rep.Fail(err)
	}
	updated, err := RemoveServer(cfg, req)
	if err != nil {
		return rep.Fail(err)
	}
	return m.applyAndReload(ctx, rep, updated, "El servidor "+server+" se eliminó del backend "+backend+".")
}

func (m *HAProxyManager) normalizeAdd(req *AddServerRequest) error {
	backend, err := validate.Identifier("el nombre del backend", req.BackendName)
	if err != nil {
		return err
	}
	server, err := validate.Identifier("el nombre del servidor", req.ServerName)
	if err != nil {
		return err
	}
	addr, err := validate.Address(req.Address)
	if err != nil {
		return err
	}
	port, err := validate.Port(req.Port)
	if err != nil {
		return err
	}
	if _, err := validate.OptionalWeight(req.Weight); err != nil {
		return err
	}
	if req.Mode != "" {
		mode := strings.ToLower(strings.TrimSpace(req.Mode))
		if mode != "http" && mode != "tcp" {
			return fmt.Errorf("el modo %q no es válido: use http o tcp", req.Mode)
		}
		req.Mode = mode
	}
	if req.Balance != "" {
		balance, err := validate.Identifier("el algoritmo de balanceo", req.Balance)
		if err != nil {
			return err
		}
		req.Balance = balance
	}
	req.BackendName, req.ServerName, req.Address, req.Port = backend, server, addr, strconv.Itoa(port)
	return nil
}

// buildServerLine renders a `server` directive.
func buildServerLine(req AddServerRequest) string {
	var b strings.Builder
	b.WriteString("    server ")
	b.WriteString(req.ServerName)
	b.WriteString(" ")
	b.WriteString(req.Address)
	b.WriteString(":")
	b.WriteString(req.Port)
	if req.Check {
		b.WriteString(" check")
	}
	if w, err := strconv.Atoi(strings.TrimSpace(req.Weight)); err == nil && w > 0 {
		b.WriteString(" weight " + strconv.Itoa(w))
	}
	if req.Backup {
		b.WriteString(" backup")
	}
	return b.String()
}

func appendBackend(cfg string, req AddServerRequest, serverLine string) string {
	lines := configLines(cfg)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	mode := req.Mode
	if mode == "" {
		mode = "tcp"
	}
	balance := req.Balance
	if balance == "" {
		balance = "roundrobin"
	}
	lines = append(lines,
		"",
		"backend "+req.BackendName,
		"    mode "+mode,
		"    balance "+balance,
		serverLine,
	)
	return joinConfig(lines)
}

// applyAndReload backs up the file, writes the new content, validates it with
// `haproxy -c` and reloads the service. On a validation error the backup is
// restored so the load balancer keeps working.
func (m *HAProxyManager) applyAndReload(ctx context.Context, rep *op.Report, content, summary string) *op.Report {
	backup := m.ConfigPath + ".np2bak"
	if _, ok := require(ctx, m.R, rep, "Respaldo de "+m.ConfigPath, nil, "cp", "-a", "--", m.ConfigPath, backup); !ok {
		return rep
	}
	if err := writeFile(ctx, m.R, rep, m.ConfigPath, []byte(content), 0644); err != nil {
		return rep.Fail(err)
	}

	check := m.R.Run(ctx, nil, "haproxy", "-c", "-f", m.ConfigPath)
	rep.Step("Validar configuración (haproxy -c)", check)
	if !check.Ok() {
		restore := m.R.Run(ctx, nil, "cp", "-a", "--", backup, m.ConfigPath)
		rep.Step("Restaurar respaldo tras error de validación", restore)
		return rep.Fail(fmt.Errorf("haproxy rechazó la configuración; se restauró el respaldo %s", backup))
	}

	reload := m.R.Run(ctx, nil, "systemctl", "reload", "--", m.Service)
	rep.Step("systemctl reload "+m.Service, reload)
	if !reload.Ok() {
		rep.Warn("La configuración es válida pero el servicio no se recargó; ejecute `systemctl reload " + m.Service + "` manualmente.")
		return rep.Fail(fmt.Errorf("no se pudo recargar el servicio %s", m.Service))
	}

	rep.Note("Respaldo de la configuración anterior en " + backup + ".")
	rep.Summary = summary
	return rep
}

// --- helpers -----------------------------------------------------------------

func configLines(cfg string) []string {
	cfg = strings.ReplaceAll(cfg, "\r\n", "\n")
	return strings.Split(strings.TrimRight(cfg, "\n"), "\n")
}

func joinConfig(lines []string) string { return strings.Join(lines, "\n") + "\n" }

func sectionHeader(line string) (keyword, name string, ok bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
		return "", "", false
	}
	fields := strings.Fields(line)
	switch fields[0] {
	case "backend", "frontend", "listen", "global", "defaults", "peers",
		"resolvers", "userlist", "cache", "program", "ring", "mailers",
		"log-forward", "http-errors", "fcgi-app", "crt-store", "traces":
		name = ""
		if len(fields) > 1 {
			name = fields[1]
		}
		return fields[0], name, true
	}
	return "", "", false
}

func findBackend(lines []string, name string) (start, end int, ok bool) {
	start = -1
	for i, line := range lines {
		keyword, sectionName, isHeader := sectionHeader(line)
		if isHeader {
			if start >= 0 {
				return start, i, true
			}
			if keyword == "backend" && sectionName == name {
				start = i
			}
			continue
		}
	}
	if start >= 0 {
		return start, len(lines), true
	}
	return 0, 0, false
}
