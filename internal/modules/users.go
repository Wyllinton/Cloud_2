package modules

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/validate"
)

// UserManager automates user creation and deletion, including sudo privileges
// and key-based SSH access.
type UserManager struct {
	R runner.Runner
}

// NewUserManager builds the manager for a transport.
func NewUserManager(r runner.Runner) *UserManager { return &UserManager{R: r} }

// User is a system account as reported by getent.
type User struct {
	Name  string
	UID   int
	Home  string
	Shell string
}

// CreateUserRequest describes a new account.
type CreateUserRequest struct {
	Username string
	Sudo     bool
	SSHKey   bool
	Shell    string
	Comment  string
	// CreateHome creates the home directory with a skeleton (default true).
	CreateHome bool
}

// DeleteUserRequest describes the removal of an account.
type DeleteUserRequest struct {
	Username   string
	RemoveHome bool
	// Terminate kills the processes owned by the user before deletion.
	Terminate bool
}

// List returns the regular (uid >= 1000) accounts.
func (m *UserManager) List(ctx context.Context) ([]User, error) {
	res := m.R.Run(ctx, nil, "getent", "passwd")
	if !res.Ok() {
		return nil, fmt.Errorf("no se pudo listar los usuarios: %s", res.ErrorString())
	}
	var users []User
	for _, line := range splitLines(res.Stdout) {
		parts := strings.Split(line, ":")
		if len(parts) < 7 {
			continue
		}
		uid, err := strconv.Atoi(parts[2])
		if err != nil || uid < 1000 || uid >= 65534 {
			continue
		}
		users = append(users, User{Name: parts[0], UID: uid, Home: parts[5], Shell: parts[6]})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users, nil
}

func (m *UserManager) uid(ctx context.Context, name string) (int, bool, error) {
	res := m.R.Run(ctx, nil, "id", "-u", "--", name)
	if !res.Ok() {
		return 0, false, nil
	}
	uid, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		return 0, true, fmt.Errorf("salida inesperada de id -u para %s: %q", name, res.Stdout)
	}
	return uid, true, nil
}

// Create creates the account and optionally grants sudo and installs a key pair.
func (m *UserManager) Create(ctx context.Context, req CreateUserRequest) *op.Report {
	rep := op.New("Crear usuario")
	defer rep.Finish()

	name, err := validate.Username(req.Username)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Action = "Crear usuario " + name

	shell, err := validate.Shell(req.Shell)
	if err != nil {
		return rep.Fail(err)
	}
	comment, err := validate.Comment(req.Comment)
	if err != nil {
		return rep.Fail(err)
	}

	if _, found, err := m.uid(ctx, name); err != nil {
		return rep.Fail(err)
	} else if found {
		return rep.Fail(fmt.Errorf("el usuario %q ya existe", name))
	}

	args := []string{}
	if req.CreateHome {
		args = append(args, "-m")
	}
	args = append(args, "-s", shell)
	if comment != "" {
		args = append(args, "-c", comment)
	}
	args = append(args, "--", name)
	if _, ok := require(ctx, m.R, rep, "useradd "+name, nil, "useradd", args...); !ok {
		return rep
	}

	// The account is created without a password, so password login is already
	// impossible; locking makes the intent explicit.
	if _, ok := require(ctx, m.R, rep, "Bloquear contraseña de "+name, nil, "passwd", "-l", "--", name); !ok {
		rep.Warn("No se pudo bloquear la contraseña; verifíquelo manualmente con `passwd -l " + name + "`.")
	}

	if req.Sudo {
		if _, ok := require(ctx, m.R, rep, "Agregar "+name+" al grupo sudo", nil, "usermod", "-aG", "sudo", "--", name); !ok {
			return rep.Fail(fmt.Errorf("el usuario se creó, pero no se pudieron conceder privilegios de sudo"))
		}
		rep.Note("El usuario puede usar sudo. Deberá autenticarse con su contraseña o disponer de una regla NOPASSWD.")
	}

	if req.SSHKey {
		if err := m.installSSHKey(ctx, rep, name); err != nil {
			return rep.Fail(err)
		}
	}

	res := m.R.Run(ctx, nil, "id", "--", name)
	rep.Step("Verificar usuario "+name, res)
	rep.Summary = fmt.Sprintf("El usuario %s se creó correctamente.", name)
	return rep
}

func (m *UserManager) installSSHKey(ctx context.Context, rep *op.Report, name string) error {
	home := "/home/" + name
	sshDir := home + "/.ssh"
	priv := sshDir + "/id_ed25519"
	pub := priv + ".pub"
	authorized := sshDir + "/authorized_keys"

	if _, ok := require(ctx, m.R, rep, "Crear "+sshDir, nil, "mkdir", "-p", sshDir); !ok {
		return fmt.Errorf("preparación del directorio .ssh")
	}
	if _, ok := require(ctx, m.R, rep, "Generar llaves ed25519 para "+name, nil,
		"ssh-keygen", "-t", "ed25519", "-N", "", "-C", name+"@np2-console", "-f", priv); !ok {
		return fmt.Errorf("generación de llaves")
	}

	pubRes := m.R.Run(ctx, nil, "cat", "--", pub)
	if !pubRes.Ok() {
		return fmt.Errorf("no se pudo leer la llave pública de %s: %s", name, pubRes.ErrorString())
	}
	content := strings.TrimSpace(pubRes.Stdout) + "\n"
	if err := writeFile(ctx, m.R, rep, authorized, []byte(content), 0600); err != nil {
		return err
	}

	if _, ok := require(ctx, m.R, rep, "Ajustar propietario de "+sshDir, nil,
		"chown", "-R", name+":"+name, sshDir); !ok {
		return fmt.Errorf("cambio de propietario de %s", sshDir)
	}
	if _, ok := require(ctx, m.R, rep, "Permisos "+sshDir, nil, "chmod", "0700", "--", sshDir); !ok {
		return fmt.Errorf("permisos de %s", sshDir)
	}
	if _, ok := require(ctx, m.R, rep, "Permisos de la llave privada", nil, "chmod", "0600", "--", priv); !ok {
		return fmt.Errorf("permisos de la llave privada")
	}
	if _, ok := require(ctx, m.R, rep, "Permisos de la llave pública", nil, "chmod", "0644", "--", pub); !ok {
		return fmt.Errorf("permisos de la llave pública")
	}

	privRes := m.R.Run(ctx, nil, "cat", "--", priv)
	if !privRes.Ok() {
		return fmt.Errorf("no se pudo leer la llave privada de %s: %s", name, privRes.ErrorString())
	}
	rep.AddArtifact(op.Artifact{
		Name:        "Llave privada de " + name,
		Filename:    name + "_id_ed25519",
		ContentType: "application/octet-stream",
		Data:        []byte(privRes.Stdout),
		Note:        "Se instaló la llave pública en authorized_keys y se deshabilitó el acceso por contraseña.",
	})
	rep.Warn("Guarde la llave privada de " + name + " en un lugar seguro y asígnele permisos 0600 antes de usarla.")
	rep.Note("El acceso SSH de " + name + " usa exclusivamente llave; el login por contraseña quedó bloqueado.")
	return nil
}

// Delete removes an account, optionally deleting its files.
func (m *UserManager) Delete(ctx context.Context, req DeleteUserRequest) *op.Report {
	rep := op.New("Eliminar usuario")
	defer rep.Finish()

	name, err := validate.UsernameForDeletion(req.Username)
	if err != nil {
		return rep.Fail(err)
	}
	rep.Action = "Eliminar usuario " + name

	uid, found, err := m.uid(ctx, name)
	if err != nil {
		return rep.Fail(err)
	}
	if !found {
		return rep.Fail(fmt.Errorf("el usuario %q no existe", name))
	}
	if uid < 1000 {
		return rep.Fail(fmt.Errorf("el usuario %q tiene uid %d: es una cuenta del sistema y no se eliminará", name, uid))
	}

	if req.Terminate {
		res := m.R.Run(ctx, nil, "loginctl", "terminate-user", "--", name)
		rep.Step("Finalizar sesiones de "+name, res)
	}

	args := []string{}
	if req.RemoveHome {
		args = append(args, "-r")
	}
	args = append(args, "--", name)
	if _, ok := require(ctx, m.R, rep, "userdel"+homeSuffix(req.RemoveHome)+" "+name, nil, "userdel", args...); !ok {
		rep.Warn("Si el usuario tiene procesos activos, cierre su sesión y vuelva a intentarlo.")
		return rep
	}

	if _, found, _ := m.uid(ctx, name); found {
		rep.Warn("El usuario todavía aparece en la base de datos de cuentas; verifique /etc/passwd.")
	} else {
		rep.Note("El usuario ya no existe en la base de datos de cuentas.")
	}
	if req.RemoveHome {
		rep.Note("Se solicitaron eliminar también los archivos del usuario (userdel -r).")
	} else {
		rep.Note("El directorio personal se conservó. Marque \"Eliminar archivos\" para borrarlo en otro intento.")
	}
	rep.Summary = fmt.Sprintf("El usuario %s se eliminó correctamente.", name)
	return rep
}

func homeSuffix(remove bool) string {
	if remove {
		return " -r"
	}
	return ""
}
