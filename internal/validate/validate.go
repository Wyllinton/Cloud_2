// Package validate contains strict validators for every value that can reach a
// system command or a configuration file. Validating here, in one place, is the
// primary defence against command injection and malformed input.
package validate

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	rePackage  = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	reUser     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	reHostname = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	reService  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]*$`)
	reIdent    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	reFilename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	reShell    = regexp.MustCompile(`^(/[A-Za-z0-9._+-]+)+$`)
	reEnvKey   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reAbsPath  = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	reDNSName  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
)

// systemUsers is a hard denylist of accounts that must never be removed through
// the web console, even if their uid happens to look like a regular one.
var systemUsers = map[string]bool{
	"root": true, "daemon": true, "bin": true, "sys": true, "sync": true,
	"games": true, "man": true, "lp": true, "mail": true, "news": true,
	"uucp": true, "proxy": true, "www-data": true, "backup": true, "list": true,
	"irc": true, "gnats": true, "nobody": true, "systemd-network": true,
	"systemd-timesync": true, "messagebus": true, "sshd": true, "haproxy": true,
	"_apt": true, "apt": true, "mysql": true, "postgres": true,
}

// Package validates a Debian package name.
func Package(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el nombre del paquete no puede estar vacío")
	}
	if len(s) > 100 {
		return "", fmt.Errorf("el nombre del paquete es demasiado largo")
	}
	if !rePackage.MatchString(s) {
		return "", fmt.Errorf("nombre de paquete inválido %q: use solo minúsculas, dígitos y los signos . + -", s)
	}
	return s, nil
}

// Username validates a Linux user name for creation.
func Username(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el nombre de usuario no puede estar vacío")
	}
	if !reUser.MatchString(s) {
		return "", fmt.Errorf("nombre de usuario inválido %q: empiece con letra o guion bajo y use hasta 32 caracteres [a-z0-9_-]", s)
	}
	if systemUsers[s] {
		return "", fmt.Errorf("el usuario %q está reservado por el sistema y no puede usarse", s)
	}
	return s, nil
}

// UsernameForDeletion validates a user name that is about to be deleted.
func UsernameForDeletion(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el nombre de usuario no puede estar vacío")
	}
	if !reUser.MatchString(s) {
		return "", fmt.Errorf("nombre de usuario inválido %q", s)
	}
	if systemUsers[s] {
		return "", fmt.Errorf("el usuario %q es una cuenta del sistema y no puede eliminarse", s)
	}
	return s, nil
}

// IsSystemUser reports whether name is in the protected denylist.
func IsSystemUser(name string) bool { return systemUsers[name] }

// RunAsUser validates the name of the account that will run a service. Unlike
// Username it allows system accounts such as www-data, because the account is
// only referenced, never created.
func RunAsUser(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el usuario de ejecución no puede estar vacío")
	}
	if !reUser.MatchString(s) {
		return "", fmt.Errorf("usuario de ejecución inválido %q", s)
	}
	return s, nil
}

// Hostname validates a host name according to RFC 1123 (single label).
func Hostname(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el nombre de host no puede estar vacío")
	}
	if len(s) > 63 {
		return "", fmt.Errorf("el nombre de host no puede superar 63 caracteres")
	}
	if !reHostname.MatchString(s) {
		return "", fmt.Errorf("nombre de host inválido %q: use letras, dígitos y guiones, sin empezar ni terminar en guion", s)
	}
	return strings.ToLower(s), nil
}

// ServiceName validates a systemd unit name (short or fully qualified).
func ServiceName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el nombre del servicio no puede estar vacío")
	}
	if len(s) > 128 {
		return "", fmt.Errorf("el nombre del servicio es demasiado largo")
	}
	if strings.HasPrefix(s, "-") || !reService.MatchString(s) {
		return "", fmt.Errorf("nombre de servicio inválido %q: use letras, dígitos y [_.:@-]", s)
	}
	return s, nil
}

// Identifier validates generic names used in configuration files, such as a
// HAProxy backend or server name.
func Identifier(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s no puede estar vacío", label)
	}
	if len(s) > 64 || !reIdent.MatchString(s) {
		return "", fmt.Errorf("%s inválido %q: use letras, dígitos, punto, guion o guion bajo", label, s)
	}
	return s, nil
}

// Address accepts either a literal IP address or a DNS name (possibly fully
// qualified).
func Address(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("la dirección no puede estar vacía")
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.String(), nil
	}
	if len(s) > 253 || !reDNSName.MatchString(s) {
		return "", fmt.Errorf("dirección inválida %q: indique una IP o un nombre de host (el puerto va en su propio campo)", s)
	}
	return strings.ToLower(s), nil
}

// Port validates a TCP/UDP port number.
func Port(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("el puerto no puede estar vacío")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("el puerto %q no es un número", s)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("el puerto %d está fuera del rango 1-65535", n)
	}
	return n, nil
}

// OptionalWeight validates an HAProxy server weight (1-256). An empty string is
// treated as "not set" and returns 0.
func OptionalWeight(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("el peso %q no es un número", s)
	}
	if n < 1 || n > 256 {
		return 0, fmt.Errorf("el peso %d está fuera del rango 1-256", n)
	}
	return n, nil
}

// AbsolutePath validates a directory path that will be created on the target.
func AbsolutePath(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s no puede estar vacío", label)
	}
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", fmt.Errorf("%s contiene caracteres no permitidos", label)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%s no puede contener el segmento «..»", label)
		}
	}
	clean := path.Clean(s)
	if !path.IsAbs(clean) {
		return "", fmt.Errorf("%s debe ser una ruta absoluta (empezar con /)", label)
	}
	if !reAbsPath.MatchString(clean) {
		return "", fmt.Errorf("%s inválido %q: use letras, dígitos, punto, guion, guion bajo y barras", label, s)
	}
	return clean, nil
}

// Filename validates a bare file name (no directory components).
func Filename(label, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%s no puede estar vacío", label)
	}
	if strings.Contains(s, "/") || s == "." || s == ".." || !reFilename.MatchString(s) {
		return "", fmt.Errorf("%s inválido %q: use un nombre de archivo simple, sin rutas", label, s)
	}
	return s, nil
}

// Shell validates an absolute path to a login shell.
func Shell(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "/bin/bash", nil
	}
	if !reShell.MatchString(s) {
		return "", fmt.Errorf("intérprete de comandos inválido %q: indique una ruta absoluta como /bin/bash", s)
	}
	return s, nil
}

// EnvKey validates an environment variable name.
func EnvKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el nombre de la variable de entorno no puede estar vacío")
	}
	if !reEnvKey.MatchString(s) {
		return "", fmt.Errorf("nombre de variable inválido %q: use letras, dígitos y guion bajo", s)
	}
	return s, nil
}

// SingleLine validates free text that will be placed on a single line of a
// configuration file. Newlines and control characters are rejected so a value
// cannot inject new directives.
func SingleLine(label, s string, max int) (string, error) {
	if strings.ContainsAny(s, "\n\r\x00") {
		return "", fmt.Errorf("%s no puede contener saltos de línea", label)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s contiene caracteres de control no permitidos", label)
		}
	}
	if len(s) > max {
		return "", fmt.Errorf("%s supera el máximo de %d caracteres", label, max)
	}
	return strings.TrimSpace(s), nil
}

// Comment validates free text, allowing an empty value.
func Comment(s string) (string, error) { return SingleLine("el comentario", s, 200) }

// ExecCommand validates the ExecStart path of a systemd unit.
func ExecCommand(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("el comando de ejecución no puede estar vacío")
	}
	if !reAbsPath.MatchString(s) {
		return "", fmt.Errorf("comando inválido %q: indique una ruta absoluta, por ejemplo /opt/app/app", s)
	}
	return s, nil
}

// ExecArgs validates the optional argument string of an ExecStart directive.
// Shell metacharacters are rejected because systemd would otherwise expand them.
func ExecArgs(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, "\n\r\x00;&|<>$`\\\"'(){}[]*?!~") {
		return "", fmt.Errorf("los argumentos contienen caracteres no permitidos: use solo texto, números, espacios, /, -, ., : y ,")
	}
	if len(s) > 500 {
		return "", fmt.Errorf("los argumentos superan los 500 caracteres")
	}
	return s, nil
}

// IssueMessage sanitises the custom login message written to /etc/issue.
// Backslashes are escaped so the text is shown literally and cannot be
// interpreted as an agetty escape sequence. Line breaks are preserved so the
// banner can span several lines.
func IssueMessage(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\r") {
		return "", fmt.Errorf("el mensaje contiene caracteres no permitidos")
	}
	if len(s) > 500 {
		return "", fmt.Errorf("el mensaje supera los 500 caracteres")
	}
	return strings.ReplaceAll(s, `\`, `\\`), nil
}

// NoShellMeta rejects shell metacharacters in otherwise free-form values.
func NoShellMeta(label, s string) (string, error) {
	if strings.ContainsAny(s, "\n\r\x00;&|<>$`\\\"'") {
		return "", fmt.Errorf("%s contiene caracteres no permitidos", label)
	}
	return s, nil
}
