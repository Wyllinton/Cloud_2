package modules

import (
	"context"
	"fmt"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
)

// RootSSHManager configures key-only SSH access for the root account: it creates
// the key pair, installs the public key and disables password authentication.
type RootSSHManager struct {
	R runner.Runner
}

// NewRootSSHManager builds the manager for a transport.
func NewRootSSHManager(r runner.Runner) *RootSSHManager { return &RootSSHManager{R: r} }

const (
	rootSSHDir       = "/root/.ssh"
	rootPrivateKey   = "/root/.ssh/id_ed25519"
	rootPublicKey    = "/root/.ssh/id_ed25519.pub"
	rootAuthorized   = "/root/.ssh/authorized_keys"
	sshdDropInDir    = "/etc/ssh/sshd_config.d"
	sshdDropInFile   = "/etc/ssh/sshd_config.d/99-np2-hardening.conf"
	sshdHardeningDoc = `# Generado por la consola de automatización NP2.
# Acceso exclusivo por llave para root; se deshabilita el login por contraseña.
PubkeyAuthentication yes
PasswordAuthentication no
PermitRootLogin prohibit-password
KbdInteractiveAuthentication no
ChallengeResponseAuthentication no
UsePAM yes
`
)

// RootSSHRequest describes a configuration request.
type RootSSHRequest struct {
	// Force regenerates the key pair when it already exists.
	Force bool
	// DisablePasswordAuth writes the sshd drop-in and reloads the service.
	DisablePasswordAuth bool
	// Comment is embedded in the generated key.
	Comment string
}

// Configure performs the full root key workflow.
func (m *RootSSHManager) Configure(ctx context.Context, req RootSSHRequest) *op.Report {
	rep := op.New("Configurar llave SSH para root")
	defer rep.Finish()

	if _, ok := require(ctx, m.R, rep, "Crear "+rootSSHDir, nil, "mkdir", "-p", rootSSHDir); !ok {
		return rep
	}
	if _, ok := require(ctx, m.R, rep, "Permisos de "+rootSSHDir, nil, "chmod", "0700", "--", rootSSHDir); !ok {
		return rep
	}

	hasKey, err := exists(ctx, m.R, rootPrivateKey)
	if err != nil {
		return rep.Fail(err)
	}
	if hasKey && !req.Force {
		rep.Note("Ya existe un par de llaves en " + rootPrivateKey + "; se reutiliza. Marque \"Regenerar\" para reemplazarlo.")
	} else {
		if hasKey {
			if _, ok := require(ctx, m.R, rep, "Eliminar llave anterior", nil, "rm", "-f", "--", rootPrivateKey, rootPublicKey); !ok {
				return rep
			}
		}
		comment := strings.TrimSpace(req.Comment)
		if comment == "" {
			comment = "root@np2-console"
		}
		if _, ok := require(ctx, m.R, rep, "Generar par de llaves ed25519", nil,
			"ssh-keygen", "-t", "ed25519", "-N", "", "-C", comment, "-f", rootPrivateKey); !ok {
			return rep
		}
	}

	if _, ok := require(ctx, m.R, rep, "Permisos de la llave privada", nil, "chmod", "0600", "--", rootPrivateKey); !ok {
		return rep
	}
	if _, ok := require(ctx, m.R, rep, "Permisos de la llave pública", nil, "chmod", "0644", "--", rootPublicKey); !ok {
		return rep
	}

	pub, err := m.readPublicKey(ctx)
	if err != nil {
		return rep.Fail(err)
	}
	if err := m.installAuthorizedKey(ctx, rep, pub); err != nil {
		return rep.Fail(err)
	}

	if req.DisablePasswordAuth {
		if err := m.hardenSSHD(ctx, rep); err != nil {
			return rep.Fail(err)
		}
	}

	priv, err := m.readPrivateKey(ctx)
	if err != nil {
		return rep.Fail(err)
	}
	rep.AddArtifact(op.Artifact{
		Name:        "Llave privada de root (id_ed25519)",
		Filename:    "root_id_ed25519",
		ContentType: "application/octet-stream",
		Data:        priv,
		Note:        "Guarde este archivo en un lugar seguro.",
	})
	rep.AddArtifact(op.Artifact{
		Name:        "Llave pública de root (id_ed25519.pub)",
		Filename:    "root_id_ed25519.pub",
		ContentType: "text/plain; charset=utf-8",
		Data:        []byte(pub),
		Note:        "Se agregó a /root/.ssh/authorized_keys.",
	})

	rep.Warn("La llave privada es un secreto: no la comparta, no la suba a repositorios y protéjala con permisos 0600. " +
		"Esta descarga es la única oportunidad de obtenerla desde la consola.")
	rep.Summary = "Llave SSH de root configurada y acceso por contraseña deshabilitado."
	return rep
}

func (m *RootSSHManager) readPublicKey(ctx context.Context) (string, error) {
	res := m.R.Run(ctx, nil, "cat", "--", rootPublicKey)
	if !res.Ok() {
		return "", fmt.Errorf("no se pudo leer la llave pública: %s", res.ErrorString())
	}
	pub := strings.TrimSpace(res.Stdout)
	if pub == "" {
		return "", fmt.Errorf("la llave pública %s está vacía", rootPublicKey)
	}
	return pub, nil
}

func (m *RootSSHManager) readPrivateKey(ctx context.Context) ([]byte, error) {
	res := m.R.Run(ctx, nil, "cat", "--", rootPrivateKey)
	if !res.Ok() {
		return nil, fmt.Errorf("no se pudo leer la llave privada: %s", res.ErrorString())
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("la llave privada %s está vacía", rootPrivateKey)
	}
	return []byte(res.Stdout), nil
}

// installAuthorizedKey appends pub to authorized_keys without duplicating it.
func (m *RootSSHManager) installAuthorizedKey(ctx context.Context, rep *op.Report, pub string) error {
	current, found, err := readFile(ctx, m.R, rootAuthorized)
	if err != nil {
		return err
	}
	content := ""
	if found {
		content = string(current)
	}
	if !strings.Contains(content, pub) {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += pub + "\n"
		rep.Note("La llave pública se agregó a " + rootAuthorized + ".")
	} else {
		rep.Note("La llave pública ya estaba presente en " + rootAuthorized + ".")
	}
	if err := writeFile(ctx, m.R, rep, rootAuthorized, []byte(content), 0600); err != nil {
		return err
	}
	return nil
}

// hardenSSHD writes the drop-in configuration, validates it and reloads sshd.
func (m *RootSSHManager) hardenSSHD(ctx context.Context, rep *op.Report) error {
	if _, ok := require(ctx, m.R, rep, "Crear "+sshdDropInDir, nil, "mkdir", "-p", sshdDropInDir); !ok {
		return fmt.Errorf("preparación de sshd_config.d")
	}
	if err := writeFile(ctx, m.R, rep, sshdDropInFile, []byte(sshdHardeningDoc), 0644); err != nil {
		return err
	}
	if _, ok := require(ctx, m.R, rep, "Validar configuración de sshd (sshd -t)", nil, "sshd", "-t"); !ok {
		rep.Warn("La validación falló; la configuración anterior sigue activa hasta reiniciar el servicio.")
		return fmt.Errorf("sshd -t reportó una configuración inválida")
	}
	if !m.reloadSSHD(ctx, rep) {
		return fmt.Errorf("no se pudo recargar el servicio SSH")
	}
	rep.Note("PasswordAuthentication no y PermitRootLogin prohibit-password aplicados mediante " + sshdDropInFile + ".")
	return nil
}

// reloadSSHD reloads the SSH service, trying the Debian unit names in order.
func (m *RootSSHManager) reloadSSHD(ctx context.Context, rep *op.Report) bool {
	for _, unit := range []string{"ssh", "sshd"} {
		res := m.R.Run(ctx, nil, "systemctl", "reload", "--", unit)
		rep.Step("Recargar servicio "+unit, res)
		if res.Ok() {
			return true
		}
	}
	return false
}

// SSHStatus reports the effective authentication settings, which is useful for
// the UI to show whether hardening is in place.
func (m *RootSSHManager) SSHStatus(ctx context.Context) (string, error) {
	res := m.R.Run(ctx, nil, "sshd", "-T")
	if !res.Ok() {
		return "", fmt.Errorf("no se pudo consultar la configuración efectiva de sshd: %s", res.ErrorString())
	}
	var keep []string
	for _, line := range splitLines(res.Stdout) {
		l := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(l, "passwordauthentication") ||
			strings.HasPrefix(l, "permitrootlogin") ||
			strings.HasPrefix(l, "pubkeyauthentication") ||
			strings.HasPrefix(l, "kbdinteractiveauthentication") {
			keep = append(keep, strings.TrimSpace(line))
		}
	}
	if len(keep) == 0 {
		return "sin datos", nil
	}
	return strings.Join(keep, "; "), nil
}
