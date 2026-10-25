package modules

import (
	"strings"
	"testing"
)

func TestUpdateHostsReplacesOldName(t *testing.T) {
	original := "127.0.0.1\tlocalhost\n127.0.1.1\tviejo.local viejo\n\n# The following lines are desirable for IPv6 capable hosts\n::1     localhost ip6-localhost ip6-loopback\n"
	got := UpdateHosts(original, "nuevo")

	if !strings.Contains(got, "127.0.1.1\tnuevo") {
		t.Errorf("no se agregó el nuevo nombre:\n%s", got)
	}
	if strings.Contains(got, "viejo") {
		t.Errorf("el nombre anterior sigue presente:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1\tlocalhost") {
		t.Errorf("se perdió la línea de localhost:\n%s", got)
	}
	if !strings.Contains(got, "::1     localhost ip6-localhost ip6-loopback") {
		t.Errorf("se perdió la línea IPv6:\n%s", got)
	}
}

func TestUpdateHostsAddsWhenMissing(t *testing.T) {
	got := UpdateHosts("127.0.0.1 localhost\n", "servidor")
	if !strings.Contains(got, "127.0.1.1\tservidor") {
		t.Errorf("no se agregó la entrada 127.0.1.1:\n%s", got)
	}
}

func TestBuildIssueFields(t *testing.T) {
	out := BuildIssue(IssueRequest{Hostname: true, Date: true, Time: true, IPv4: true}, "Bienvenido")
	for _, want := range []string{`\n`, `\d`, `\t`, `\4`, "Bienvenido"} {
		if !strings.Contains(out, want) {
			t.Errorf("BuildIssue no contiene %q:\n%s", want, out)
		}
	}
}

func TestBuildUnit(t *testing.T) {
	unit, err := buildUnit("catalogo.service", "Catálogo", "/opt/catalogo", "/opt/catalogo/catalogo", "--puerto 8081", "catalogo", []string{"PORT=8081"})
	if err != nil {
		t.Fatalf("buildUnit: %v", err)
	}
	for _, want := range []string{
		"Description=Catálogo",
		"WorkingDirectory=/opt/catalogo",
		"ExecStart=/opt/catalogo/catalogo --puerto 8081",
		"User=catalogo",
		"Environment=PORT=8081",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("la unidad no contiene %q:\n%s", want, unit)
		}
	}
}

func TestBuildUnitRejectsBadEnvironment(t *testing.T) {
	if _, err := buildUnit("s.service", "d", "/opt", "/opt/app", "", "", []string{"SIN VALOR"}); err == nil {
		t.Fatal("se esperaba error con una variable de entorno sin '='")
	}
}

func TestUnitNameNormalisation(t *testing.T) {
	if got, _ := unitName("nginx"); got != "nginx.service" {
		t.Errorf("unitName(nginx) = %q", got)
	}
	if got, _ := unitName("nginx.service"); got != "nginx.service" {
		t.Errorf("unitName(nginx.service) = %q", got)
	}
	if _, err := unitName("-bad"); err == nil {
		t.Error("unitName(-bad) debía fallar")
	}
}

func TestFileModeMarksExecStartExecutable(t *testing.T) {
	exec := "/opt/catalogo/catalogo"
	if got := fileMode(exec, exec, false); got != 0755 {
		t.Errorf("el archivo de ExecStart debe ser 0755 aunque la casilla esté desmarcada; se obtuvo %o", got)
	}
	if got := fileMode("/opt/catalogo/datos.txt", exec, false); got != 0644 {
		t.Errorf("los demás archivos deben conservar 0644; se obtuvo %o", got)
	}
	if got := fileMode("/opt/catalogo/datos.txt", exec, true); got != 0755 {
		t.Errorf("la casilla debe marcar todos los archivos como 0755; se obtuvo %o", got)
	}
}

func TestParseProperties(t *testing.T) {
	props := parseProperties("Type=simple\nResult=success\n\nSubState=running\n")
	if props["Type"] != "simple" || props["Result"] != "success" || props["SubState"] != "running" {
		t.Errorf("parseProperties devolvió %#v", props)
	}
}
