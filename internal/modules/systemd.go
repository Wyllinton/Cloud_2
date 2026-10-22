package modules

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

// SystemdManager deploys applications as systemd units and controls them.
type SystemdManager struct {
	R runner.Runner
}

// NewSystemdManager builds the manager for a transport.
func NewSystemdManager(r runner.Runner) *SystemdManager { return &SystemdManager{R: r} }

const systemdUnitDir = "/etc/systemd/system"

// FileUpload is a file transferred to the managed machine as part of a deploy.
type FileUpload struct {
	Name       string
	Data       []byte
	Executable bool
}

// DeployRequest configures an application as a systemd service.
type DeployRequest struct {
	ServiceName string
	Description string
	WorkingDir  string
	ExecCommand string
	ExecArgs    string
	RunAs       string
	Environment []string // each item is "KEY=VALUE"
	Files       []FileUpload
	Enable      bool
	Start       bool
	Restart     bool
}

// Service is a systemd unit as shown in the UI.
type Service struct {
	Unit        string
	Load        string
	Active      string
	Sub         string
	Description string
}

// List returns the known service units.
func (m *SystemdManager) List(ctx context.Context) ([]Service, error) {
	res := m.R.Run(ctx, nil, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager", "--plain")
	if !res.Ok() {
		return nil, fmt.Errorf("no se pudo listar las unidades: %s", res.ErrorString())
	}
	var out []Service
	for _, line := range splitLines(res.Stdout) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		out = append(out, Service{Unit: fields[0], Load: fields[1]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Unit < out[j].Unit })
	return out, nil
}

// unitName normalises the user supplied name into a .service unit name.
func unitName(raw string) (string, error) {
	name, err := validate.ServiceName(raw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(name, ".") {
		name += ".service"
	}
	return name, nil
}

// Action runs a control command against a unit.
func (m *SystemdManager) Action(ctx context.Context, rawName, action string) *op.Report {
	unit, err := unitName(rawName)
	rep := op.New(fmt.Sprintf("Acción %q sobre %s", action, unit))
	defer rep.Finish()
	if err != nil {
		return rep.Fail(err)
	}

	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return rep.Fail(fmt.Errorf("acción %q no soportada", action))
	}

	if _, ok := require(ctx, m.R, rep, "systemctl "+action+" "+unit, nil, "systemctl", action, "--", unit); !ok {
		return rep
	}
	// Report the resulting state so the user can confirm it.
	m.appendState(ctx, rep, unit)
	rep.Summary = fmt.Sprintf("systemctl %s %s se ejecutó correctamente.", action, unit)
	return rep
}

// Status reports the current state of a unit.
func (m *SystemdManager) Status(ctx context.Context, rawName string) *op.Report {
	unit, err := unitName(rawName)
	rep := op.New("Estado de " + unit)
	defer rep.Finish()
	if err != nil {
		return rep.Fail(err)
	}

	res := m.R.Run(ctx, nil, "systemctl", "status", "--no-pager", "-l", "--", unit)
	rep.Step("systemctl status "+unit, res)
	m.appendState(ctx, rep, unit)
	if !res.Ok() {
		rep.Note("systemctl status devuelve un código distinto de cero cuando la unidad está inactiva o no existe; revise la salida anterior.")
	}
	rep.Summary = "Se consultó el estado de " + unit + "."
	return rep
}

func (m *SystemdManager) appendState(ctx context.Context, rep *op.Report, unit string) {
	rep.Step("systemctl is-active "+unit, m.R.Run(ctx, nil, "systemctl", "is-active", "--", unit))
	rep.Step("systemctl is-enabled "+unit, m.R.Run(ctx, nil, "systemctl", "is-enabled", "--", unit))
}

// Deploy transfers the application files and installs the unit.
func (m *SystemdManager) Deploy(ctx context.Context, req DeployRequest) *op.Report {
	rep := op.New("Configurar servicio systemd")
	defer rep.Finish()

	unit, err := unitName(req.ServiceName)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Action = "Desplegar " + unit

	workDir, err := validate.AbsolutePath("el directorio de trabajo", req.WorkingDir)
	if err != nil {
		return rep.Fail(err)
	}
	execCmd, err := validate.ExecCommand(req.ExecCommand)
	if err != nil {
		return rep.Fail(err)
	}
	execArgs, err := validate.ExecArgs(req.ExecArgs)
	if err != nil {
		return rep.Fail(err)
	}
	desc, err := validate.SingleLine("la descripción", req.Description, 200)
	if err != nil {
		return rep.Fail(err)
	}
	if desc == "" {
		desc = unit
	}
	var runAs string
	if strings.TrimSpace(req.RunAs) != "" {
		runAs, err = validate.RunAsUser(req.RunAs)
		if err != nil {
			return rep.Fail(err)
		}
	}

	if _, ok := require(ctx, m.R, rep, "Crear "+workDir, nil, "mkdir", "-p", workDir); !ok {
		return rep
	}

	for _, f := range req.Files {
		name, err := validate.Filename("el nombre del archivo", f.Name)
		if err != nil {
			return rep.Fail(err)
		}
		target := strings.TrimRight(workDir, "/") + "/" + name
		mode := uint32(0644)
		if f.Executable {
			mode = 0755
		}
		if err := writeFile(ctx, m.R, rep, target, f.Data, mode); err != nil {
			return rep.Fail(err)
		}
		rep.Note(fmt.Sprintf("Transferido %s (%d bytes) a %s.", name, len(f.Data), target))
	}

	if runAs != "" {
		if _, ok := require(ctx, m.R, rep, "Cambiar propietario de "+workDir, nil, "chown", "-R", runAs+":"+runAs, workDir); !ok {
			rep.Warn("Si el usuario no existe, cree la cuenta antes de iniciar el servicio.")
			return rep.Fail(fmt.Errorf("no se pudo asignar el propietario %s a %s", runAs, workDir))
		}
	}

	unitBody, err := buildUnit(unit, desc, workDir, execCmd, execArgs, runAs, req.Environment)
	if err != nil {
		return rep.Fail(err)
	}
	unitPath := systemdUnitDir + "/" + unit
	if err := writeFile(ctx, m.R, rep, unitPath, []byte(unitBody), 0644); err != nil {
		return rep.Fail(err)
	}
	rep.Note("Unidad escrita en " + unitPath + ".")

	if _, ok := require(ctx, m.R, rep, "systemctl daemon-reload", nil, "systemctl", "daemon-reload"); !ok {
		return rep
	}
	if req.Enable {
		if _, ok := require(ctx, m.R, rep, "systemctl enable "+unit, nil, "systemctl", "enable", "--", unit); !ok {
			return rep
		}
	}
	switch {
	case req.Restart:
		if _, ok := require(ctx, m.R, rep, "systemctl restart "+unit, nil, "systemctl", "restart", "--", unit); !ok {
			return rep
		}
	case req.Start:
		if _, ok := require(ctx, m.R, rep, "systemctl start "+unit, nil, "systemctl", "start", "--", unit); !ok {
			return rep
		}
	}

	m.appendState(ctx, rep, unit)
	rep.Summary = "El servicio " + unit + " quedó configurado."
	return rep
}

// buildUnit renders the systemd unit file. It is a pure function, unit tested.
func buildUnit(unit, desc, workDir, execCmd, execArgs, runAs string, env []string) (string, error) {
	var b strings.Builder
	b.WriteString("# Generado por la consola de automatización NP2.\n")
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + desc + "\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("WorkingDirectory=" + workDir + "\n")
	exec := execCmd
	if execArgs != "" {
		exec += " " + execArgs
	}
	b.WriteString("ExecStart=" + exec + "\n")
	if runAs != "" {
		b.WriteString("User=" + runAs + "\n")
		b.WriteString("Group=" + runAs + "\n")
	}
	for _, kv := range env {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return "", fmt.Errorf("variable de entorno %q inválida: use el formato CLAVE=valor", kv)
		}
		k, err := validate.EnvKey(key)
		if err != nil {
			return "", err
		}
		v, err := validate.SingleLine("el valor de "+k, value, 500)
		if err != nil {
			return "", err
		}
		b.WriteString("Environment=" + k + "=" + v + "\n")
	}
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=3\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String(), nil
}

// Remove stops, disables and deletes a unit.
func (m *SystemdManager) Remove(ctx context.Context, rawName string) *op.Report {
	unit, err := unitName(rawName)
	rep := op.New("Eliminar servicio " + unit)
	defer rep.Finish()
	if err != nil {
		return rep.Fail(err)
	}

	// These are best effort: the unit may already be stopped.
	rep.Step("systemctl stop "+unit, m.R.Run(ctx, nil, "systemctl", "stop", "--", unit))
	rep.Step("systemctl disable "+unit, m.R.Run(ctx, nil, "systemctl", "disable", "--", unit))

	if _, ok := require(ctx, m.R, rep, "Eliminar "+systemdUnitDir+"/"+unit, nil, "rm", "-f", "--", systemdUnitDir+"/"+unit); !ok {
		return rep
	}
	if _, ok := require(ctx, m.R, rep, "systemctl daemon-reload", nil, "systemctl", "daemon-reload"); !ok {
		return rep
	}
	rep.Summary = "La unidad " + unit + " se eliminó."
	return rep
}
