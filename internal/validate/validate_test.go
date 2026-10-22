package validate

import "testing"

func TestPackage(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"nginx", true},
		{"libssl-dev", true},
		{"g++", true},
		{"python3.11", true},
		{"", false},
		{"Nginx", false},
		{"nginx; rm -rf /", false},
		{"nginx$(id)", false},
		{"nginx`id`", false},
		{"nginx && reboot", false},
		{"../etc/passwd", false},
	}
	for _, c := range cases {
		_, err := Package(c.in)
		if (err == nil) != c.want {
			t.Errorf("Package(%q) ok=%v, esperaba ok=%v (err=%v)", c.in, err == nil, c.want, err)
		}
	}
}

func TestUsername(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"ana", true},
		{"_svc", true},
		{"user-1", true},
		{"root", false},      // reservado
		{"daemon", false},    // reservado
		{"Ana", false},       // mayúsculas
		{"1user", false},     // no empieza con letra
		{"user;id", false},   // inyección
		{"user name", false}, // espacio
		{"", false},
	}
	for _, c := range cases {
		_, err := Username(c.in)
		if (err == nil) != c.want {
			t.Errorf("Username(%q) ok=%v, esperaba ok=%v (err=%v)", c.in, err == nil, c.want, err)
		}
	}
}

func TestHostname(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"servidor-np2", true},
		{"a", true},
		{"Host1", true},
		{"-bad", false},
		{"bad-", false},
		{"bad_name", false},
		{"bad;id", false},
		{"", false},
	}
	for _, c := range cases {
		if _, err := Hostname(c.in); (err == nil) != c.want {
			t.Errorf("Hostname(%q) ok=%v, esperaba ok=%v", c.in, err == nil, c.want)
		}
	}
}

func TestServiceName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"nginx", true},
		{"catalogo.service", true},
		{"getty@tty1", true},
		{"a-b_c:d", true},
		{"-bad", false},
		{"svc;id", false},
		{"svc name", false},
		{"", false},
	}
	for _, c := range cases {
		if _, err := ServiceName(c.in); (err == nil) != c.want {
			t.Errorf("ServiceName(%q) ok=%v, esperaba ok=%v", c.in, err == nil, c.want)
		}
	}
}

func TestAddressAndPort(t *testing.T) {
	if _, err := Address("192.168.1.10"); err != nil {
		t.Errorf("Address IPv4: %v", err)
	}
	if _, err := Address("fe80::1"); err != nil {
		t.Errorf("Address IPv6: %v", err)
	}
	if _, err := Address("db.local"); err != nil {
		t.Errorf("Address hostname: %v", err)
	}
	for _, bad := range []string{"", "192.168.1.10:8080", "a b", "foo;bar"} {
		if _, err := Address(bad); err == nil {
			t.Errorf("Address(%q) debía fallar", bad)
		}
	}
	if p, err := Port("8080"); err != nil || p != 8080 {
		t.Errorf("Port(8080) = %d, %v", p, err)
	}
	for _, bad := range []string{"0", "65536", "-1", "abc", ""} {
		if _, err := Port(bad); err == nil {
			t.Errorf("Port(%q) debía fallar", bad)
		}
	}
}

func TestAbsolutePathAndFilename(t *testing.T) {
	if _, err := AbsolutePath("dir", "/opt/catalogo"); err != nil {
		t.Errorf("AbsolutePath válido: %v", err)
	}
	for _, bad := range []string{"opt/catalogo", "/opt/../etc", "/opt/cat alogo", "/opt/a;b", ""} {
		if _, err := AbsolutePath("dir", bad); err == nil {
			t.Errorf("AbsolutePath(%q) debía fallar", bad)
		}
	}
	if _, err := Filename("f", "app.bin"); err != nil {
		t.Errorf("Filename válido: %v", err)
	}
	for _, bad := range []string{"../app", "a/b", ".", "..", ""} {
		if _, err := Filename("f", bad); err == nil {
			t.Errorf("Filename(%q) debía fallar", bad)
		}
	}
}

func TestSingleLineRejectsNewlines(t *testing.T) {
	if _, err := SingleLine("x", "valor normal", 50); err != nil {
		t.Errorf("valor válido: %v", err)
	}
	for _, bad := range []string{"a\nb", "a\rb", "a\x00b", "a\x07b"} {
		if _, err := SingleLine("x", bad, 50); err == nil {
			t.Errorf("SingleLine(%q) debía fallar", bad)
		}
	}
}

func TestExecArgsRejectsShellMeta(t *testing.T) {
	if _, err := ExecArgs("--puerto 8080"); err != nil {
		t.Errorf("args válidos: %v", err)
	}
	for _, bad := range []string{"$(id)", "a;b", "a|b", "a&&b", "a>b", "a`id`", "a\\b", "a\"b"} {
		if _, err := ExecArgs(bad); err == nil {
			t.Errorf("ExecArgs(%q) debía fallar", bad)
		}
	}
}

func TestIssueMessageEscapesBackslashes(t *testing.T) {
	got, err := IssueMessage(`hola \n mundo`)
	if err != nil {
		t.Fatalf("IssueMessage: %v", err)
	}
	if got != `hola \\n mundo` {
		t.Errorf("IssueMessage = %q, esperaba %q", got, `hola \\n mundo`)
	}
	// Los saltos de línea son válidos: el banner puede ocupar varias líneas.
	if _, err := IssueMessage("linea\nnueva"); err != nil {
		t.Errorf("IssueMessage debía aceptar saltos de línea: %v", err)
	}
	if _, err := IssueMessage("linea\x00nueva"); err == nil {
		t.Error("IssueMessage debía rechazar el carácter nulo")
	}
	if _, err := IssueMessage("linea\rnueva"); err == nil {
		t.Error("IssueMessage debía rechazar el retorno de carro")
	}
}

func TestRunAsUserAllowsSystemAccounts(t *testing.T) {
	if _, err := RunAsUser("www-data"); err != nil {
		t.Errorf("RunAsUser(www-data) debía aceptarse: %v", err)
	}
	if _, err := RunAsUser("bad;id"); err == nil {
		t.Error("RunAsUser debía rechazar caracteres de shell")
	}
}

func TestIsSystemUser(t *testing.T) {
	if !IsSystemUser("root") || !IsSystemUser("www-data") {
		t.Error("root y www-data deben estar protegidos")
	}
	if IsSystemUser("ana") {
		t.Error("ana no es una cuenta del sistema")
	}
}
