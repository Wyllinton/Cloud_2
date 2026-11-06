package modules

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"np2/appgo/internal/config"
	"np2/appgo/internal/op"
	"np2/appgo/internal/runner"
)

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("crear entrada %q: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("escribir entrada %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("cerrar zip: %v", err)
	}
	return buf.Bytes()
}

func TestValidateZipPayloadAcceptsSite(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"index.html":      "<h1>paisajes</h1>",
		"assets/app.css":  "body{}",
		"assets/app.js":   "console.log('ok')",
		"images/foto.png": "png",
	})
	if err := validateZipPayload(data, 10<<20, 100); err != nil {
		t.Fatalf("el zip válido fue rechazado: %v", err)
	}
}

func TestValidateZipPayloadRejectsTraversal(t *testing.T) {
	for _, name := range []string{"../index.html", "a/../../etc/passwd", "/etc/passwd", `a\b.html`} {
		data := zipBytes(t, map[string]string{name: "x"})
		if err := validateZipPayload(data, 10<<20, 100); err == nil {
			t.Errorf("el zip con %q debía rechazarse", name)
		}
	}
}

func TestValidateZipPayloadRejectsSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "enlace", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink | 0777)
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("crear symlink: %v", err)
	}
	if _, err := w.Write([]byte("/etc/passwd")); err != nil {
		t.Fatalf("escribir symlink: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("cerrar zip: %v", err)
	}
	if err := validateZipPayload(buf.Bytes(), 10<<20, 100); err == nil {
		t.Fatal("un zip con enlace simbólico debía rechazarse")
	}
}

func TestValidateZipPayloadRejectsTooManyEntries(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 11; i++ {
		files["f"+string(rune('a'+i))+".txt"] = "x"
	}
	data := zipBytes(t, files)
	if err := validateZipPayload(data, 10<<20, 10); err == nil {
		t.Fatal("se esperaba el límite de entradas")
	}
}

func TestBuildDNSUpdate(t *testing.T) {
	got := buildDNSUpdate("192.168.10.10", "cloud.local", "paisajes.cloud.local", "192.168.10.30", 300, false)
	for _, want := range []string{
		"server 192.168.10.10\n",
		"zone cloud.local.\n",
		"update delete paisajes.cloud.local. A\n",
		"update add paisajes.cloud.local. 300 A 192.168.10.30\n",
		"send\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script DNS no contiene %q:\n%s", want, got)
		}
	}
	del := buildDNSUpdate("192.168.10.10", "cloud.local", "paisajes.cloud.local", "", 0, true)
	if strings.Contains(del, "update add") {
		t.Errorf("el borrado no debe agregar registros:\n%s", del)
	}
	if !strings.Contains(del, "update delete paisajes.cloud.local. A") {
		t.Errorf("el borrado no contiene el delete:\n%s", del)
	}
}

func TestNextFreeIP(t *testing.T) {
	used := map[string]bool{"192.168.10.30": true, "192.168.10.31": true}
	ip, err := nextFreeIP("192.168.10.30", "192.168.10.32", used, "192.168.10.1", "192.168.10.10")
	if err != nil {
		t.Fatalf("nextFreeIP: %v", err)
	}
	if ip != "192.168.10.32" {
		t.Errorf("nextFreeIP = %s, esperaba 192.168.10.32", ip)
	}
	if _, err := nextFreeIP("192.168.10.30", "192.168.10.31", used); err == nil {
		t.Error("se esperaba error cuando el rango está lleno")
	}
	if _, err := nextFreeIP("192.168.10.40", "192.168.10.30", nil); err == nil {
		t.Error("se esperaba error con un rango invertido")
	}
}

func TestBuildStaticInterfaces(t *testing.T) {
	got := buildStaticInterfaces("enp0s3", "192.168.10.30", 24, "192.168.10.1", "192.168.10.10")
	for _, want := range []string{
		"iface enp0s3 inet static",
		"address 192.168.10.30",
		"netmask 255.255.255.0",
		"gateway 192.168.10.1",
		"dns-nameservers 192.168.10.10",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("configuración de red no contiene %q:\n%s", want, got)
		}
	}
}

func TestBuildVHost(t *testing.T) {
	got := buildVHost("paisajes", "paisajes.cloud.local", "/var/www/paisajes")
	for _, want := range []string{
		"ServerName paisajes.cloud.local",
		"ServerAlias paisajes",
		"DocumentRoot /var/www/paisajes",
		"Require all granted",
		"paisajes-error.log",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("vhost no contiene %q:\n%s", want, got)
		}
	}
}

func TestAddHostEntryReplacesSameIP(t *testing.T) {
	original := "127.0.0.1 localhost\n127.0.1.1 paisajes\n192.168.10.30 viejo.cloud.local viejo\n"
	got := addHostEntry(original, "192.168.10.30", "paisajes.cloud.local", "paisajes")
	if strings.Contains(got, "viejo.cloud.local") {
		t.Errorf("la entrada anterior no se eliminó:\n%s", got)
	}
	if !strings.Contains(got, "192.168.10.30\tpaisajes.cloud.local paisajes") {
		t.Errorf("no se agregó la entrada nueva:\n%s", got)
	}
	if !strings.Contains(got, "127.0.1.1 paisajes") {
		t.Errorf("se perdió la entrada de loopback:\n%s", got)
	}
}

func TestParseNetworkHelpers(t *testing.T) {
	if got := parseDefaultInterface("default via 192.168.10.1 dev enp0s3 proto static\n"); got != "enp0s3" {
		t.Errorf("parseDefaultInterface = %q", got)
	}
	if got := parseFirstInterface("1: lo: <LOOPBACK,UP>\n2: enp0s3: <BROADCAST,UP>\n"); got != "enp0s3" {
		t.Errorf("parseFirstInterface = %q", got)
	}
}

func TestDiscoverTemplateDisk(t *testing.T) {
	info := `name="template_server"
VMState="poweroff"
"IDE-0-0"="emptydrive"
"SATA-0-0"="/home/will/VirtualBox VMs/template_server/template_server.vdi"
"SATA-ImageUUID-0-0"="4af34d95-db93-4dac-9ff4-10b6d564ad1c"
`
	if got := discoverTemplateDisk(info); got != "/home/will/VirtualBox VMs/template_server/template_server.vdi" {
		t.Errorf("discoverTemplateDisk = %q", got)
	}
	if got := discoverTemplateDisk("SATA-0-0=\"emptydrive\"\n"); got != "" {
		t.Errorf("una unidad vacía no debe devolver un disco: %q", got)
	}
}

func TestListContainsInterface(t *testing.T) {
	output := "Name:            vboxnet0\nGUID:            1234\n\nName:            vboxnet1\n"
	if !listContainsInterface(output, "vboxnet0") {
		t.Error("no se encontró vboxnet0")
	}
	if !listContainsInterface(output, "vboxnet1") {
		t.Error("no se encontró vboxnet1")
	}
	if listContainsInterface(output, "vboxnet2") {
		t.Error("vboxnet2 no debería existir")
	}
	if listContainsInterface("", "vboxnet0") {
		t.Error("una salida vacía no debe coincidir")
	}
}

func TestParseMachineReadable(t *testing.T) {
	props := parseMachineReadable("VMState=\"poweroff\"\nname=\"template_server\"\n\"SATA-0-0\"=\"/tmp/a.vdi\"\n")
	if props["VMState"] != "poweroff" || props["name"] != "template_server" {
		t.Errorf("parseMachineReadable = %#v", props)
	}
	if props["SATA-0-0"] != "/tmp/a.vdi" {
		t.Errorf("clave SATA no interpretada: %#v", props)
	}
}

func TestStatusFromVBoxState(t *testing.T) {
	cases := map[string]string{
		"running":        httpaaSStatusRunning,
		"paused":         httpaaSStatusRunning,
		"poweroff":       httpaaSStatusStopped,
		"saved":          httpaaSStatusStopped,
		"gurumeditation": httpaaSStatusUnknown,
	}
	for in, want := range cases {
		if got := statusFromVBoxState(in); got != want {
			t.Errorf("statusFromVBoxState(%q) = %q, esperaba %q", in, got, want)
		}
	}
}

func testHTTPaaSManager(t *testing.T) *HTTPaaSManager {
	t.Helper()
	dir := t.TempDir()
	opts := config.Default().HTTPaaS
	opts.StatePath = filepath.Join(dir, "instances.json")
	opts.DiskDir = filepath.Join(dir, "disks")
	m, err := NewHTTPaaSManager(opts)
	if err != nil {
		t.Fatalf("NewHTTPaaSManager: %v", err)
	}
	return m
}

func TestVMNameFollowsHostname(t *testing.T) {
	m := testHTTPaaSManager(t)
	if got := m.vmName("paisajes"); got != "paisajes" {
		t.Fatalf("vmName = %q, want paisajes", got)
	}
	m.opts.VMPrefix = "web-"
	if got := m.vmName("paisajes"); got != "web-paisajes" {
		t.Fatalf("vmName con prefijo = %q", got)
	}
}

func TestOwnedVM(t *testing.T) {
	m := testHTTPaaSManager(t)
	cases := []struct {
		vm, host string
		want     bool
	}{
		{"paisajes", "paisajes", true},
		{"httpaas-paisajes", "paisajes", true}, // inventario anterior
		{"negocios", "paisajes", false},
		{"template_server", "paisajes", false},
		{"dns_server", "paisajes", false},
	}
	for _, c := range cases {
		if got := m.ownedVM(c.vm, c.host); got != c.want {
			t.Errorf("ownedVM(%q, %q) = %v, want %v", c.vm, c.host, got, c.want)
		}
	}
}

func TestPublishRejectsBadRequests(t *testing.T) {
	m := testHTTPaaSManager(t)
	site := zipBytes(t, map[string]string{"index.html": "<h1>hola</h1>"})

	if rep := m.Publish(context.Background(), HTTPaaSPublishRequest{Hostname: "paisajes"}); rep.OK {
		t.Fatal("Publish sin zip debería fallar")
	}
	if rep := m.Publish(context.Background(), HTTPaaSPublishRequest{Hostname: "../x", ZipName: "a.zip", Zip: site}); rep.OK {
		t.Fatal("Publish con hostname inválido debería fallar")
	}
	rep := m.Publish(context.Background(), HTTPaaSPublishRequest{Hostname: "paisajes", ZipName: "a.zip", Zip: site})
	if rep.OK || !strings.Contains(rep.Summary, "Aceptar") {
		t.Fatalf("Publish sobre una instancia inexistente debe pedir «Aceptar» primero: %+v", rep)
	}
}

func TestPlaceholderPageEscapes(t *testing.T) {
	page := buildPlaceholderPage("a<b>.cloud.local", "192.168.10.30/24")
	if strings.Contains(page, "<b>") {
		t.Fatal("la página provisional no escapó el FQDN")
	}
	if !strings.Contains(page, "192.168.10.30/24") {
		t.Fatal("la página provisional debe mostrar la dirección")
	}
}

func TestInstanceAddress(t *testing.T) {
	if got := (HTTPaaSInstance{IP: "192.168.10.30", Prefix: 24}).Address(); got != "192.168.10.30/24" {
		t.Fatalf("Address = %q", got)
	}
	if got := (HTTPaaSInstance{IP: "192.168.10.30"}).Address(); got != "192.168.10.30" {
		t.Fatalf("Address sin prefijo = %q", got)
	}
}

// mediaRunner answers `VBoxManage showmediuminfo disk <id>` from a fixed table.
type mediaRunner struct{ media map[string]string }

func (f mediaRunner) Run(_ context.Context, _ []byte, _ string, args ...string) runner.Result {
	if len(args) == 3 && args[0] == "showmediuminfo" {
		if out, ok := f.media[args[2]]; ok {
			return runner.Result{Argv: args, Stdout: out}
		}
	}
	return runner.Result{Argv: args, ExitCode: 1, Stderr: "VBoxManage: error: not found"}
}
func (mediaRunner) Kind() string   { return "local" }
func (mediaRunner) Target() string { return "host" }
func (mediaRunner) UsesSudo() bool { return false }
func (mediaRunner) Close() error   { return nil }

const (
	childPath = "/vms/template_server/Snapshots/{d451afd3}.vdi"
	childInfo = "UUID:           d451afd3\nParent UUID:    4af34d95\nState:          created\nType:           normal (differencing)\nLocation:       " + childPath + "\n"
)

func TestResolveBaseDiskWalksToMultiattachRoot(t *testing.T) {
	m := testHTTPaaSManager(t)
	m.host = mediaRunner{media: map[string]string{
		childPath:  childInfo,
		"4af34d95": "UUID:           4af34d95\nParent UUID:    base\nState:          created\nType:           multiattach\nLocation:       /vms/template_server/template_server.vdi\n",
	}}
	rep := op.New("t")
	got, err := m.resolveBaseDisk(context.Background(), rep, childPath)
	if err != nil {
		t.Fatalf("resolveBaseDisk: %v", err)
	}
	if got != "/vms/template_server/template_server.vdi" {
		t.Fatalf("debe usar la raíz multiconexión, no el hijo de la plantilla: %q", got)
	}
}

func TestResolveBaseDiskReportsMissingFile(t *testing.T) {
	m := testHTTPaaSManager(t)
	m.host = mediaRunner{media: map[string]string{
		childPath: childInfo,
		"4af34d95": "UUID:           4af34d95\nParent UUID:    base\nState:          inaccessible\n" +
			"Access Error:   Could not open the medium '/vms/template_server/template_server.vdi'.\n" +
			"Type:           multiattach\nLocation:       /vms/template_server/template_server.vdi\n",
	}}
	_, err := m.resolveBaseDisk(context.Background(), op.New("t"), childPath)
	if err == nil || !strings.Contains(err.Error(), "no es accesible") {
		t.Fatalf("un disco base inaccesible debe explicarse: %v", err)
	}
}

func TestResolveBaseDiskRequiresMultiattach(t *testing.T) {
	m := testHTTPaaSManager(t)
	m.host = mediaRunner{media: map[string]string{
		"/vms/t.vdi": "UUID:           1\nParent UUID:    base\nState:          created\nType:           normal (base)\nLocation:       /vms/t.vdi\n",
	}}
	_, err := m.resolveBaseDisk(context.Background(), op.New("t"), "/vms/t.vdi")
	if err == nil || !strings.Contains(err.Error(), "multiattach") {
		t.Fatalf("un disco normal debe rechazarse con la instrucción para convertirlo: %v", err)
	}
}

func zipNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip inválido: %v", err)
	}
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func TestFlattenZipRootStripsWrappingFolder(t *testing.T) {
	// Lo que produce comprimir ~/Desktop/prueba: todo dentro de prueba/.
	in := zipBytes(t, map[string]string{
		"prueba/":                       "",
		"prueba/index.html":             "<h1>hola</h1>",
		"prueba/librerias-go/main.go":   "package main",
		"prueba/librerias-go/.git/HEAD": "ref: refs/heads/main",
		"__MACOSX/prueba/._index.html":  "x",
		"prueba/.DS_Store":              "x",
	})
	out, root, hidden, err := flattenZipRoot(in)
	if err != nil {
		t.Fatal(err)
	}
	if root != "prueba" {
		t.Fatalf("root = %q", root)
	}
	got := strings.Join(zipNames(t, out), ",")
	want := "index.html,librerias-go/.git/HEAD,librerias-go/main.go"
	if got != want {
		t.Fatalf("entradas = %s, want %s", got, want)
	}
	if strings.Join(hidden, ",") != ".git" {
		t.Fatalf("hidden = %v", hidden)
	}
	if err := validateZipPayload(out, httpaaSMaxUncompressed, httpaaSMaxZipEntries); err != nil {
		t.Fatalf("el zip reescrito debe seguir siendo válido: %v", err)
	}
}

func TestFlattenZipRootKeepsRootLayout(t *testing.T) {
	in := zipBytes(t, map[string]string{"index.html": "a", "css/app.css": "b"})
	out, root, _, err := flattenZipRoot(in)
	if err != nil || root != "" || !bytes.Equal(out, in) {
		t.Fatalf("un zip con index.html en la raíz no debe cambiar (root=%q err=%v)", root, err)
	}
	two := zipBytes(t, map[string]string{"a/index.html": "a", "b/x.html": "b"})
	if _, root, _, _ := flattenZipRoot(two); root != "" {
		t.Fatalf("dos carpetas en la raíz no deben aplanarse: %q", root)
	}
}

func TestVHostDeniesHiddenFiles(t *testing.T) {
	v := buildVHost("prueba", "prueba.cloud.local", "/var/www/prueba")
	for _, want := range []string{`<DirectoryMatch "/\.(git|svn|hg)(/|$)">`, `<FilesMatch "^\.">`, "Require all denied"} {
		if !strings.Contains(v, want) {
			t.Errorf("el vhost no contiene %q", want)
		}
	}
}

func TestNMUnmanagedConf(t *testing.T) {
	c := buildNMUnmanagedConf("enp0s3")
	if !strings.Contains(c, "unmanaged-devices=interface-name:enp0s3") || !strings.Contains(c, "dns=none") {
		t.Fatalf("configuración de NetworkManager incompleta:\n%s", c)
	}
}
