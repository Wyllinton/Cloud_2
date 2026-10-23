package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseGuestExitExtractsCode(t *testing.T) {
	stdout := "hola mundo\n\nNP2RC:abc123:0\n"
	cleaned, code, found := parseGuestExit(stdout, "abc123")
	if !found {
		t.Fatal("se esperaba encontrar el marcador")
	}
	if code != 0 {
		t.Errorf("code = %d, esperaba 0", code)
	}
	if cleaned != "hola mundo\n" {
		t.Errorf("cleaned = %q, esperaba %q", cleaned, "hola mundo\n")
	}
}

func TestParseGuestExitNonZero(t *testing.T) {
	cleaned, code, found := parseGuestExit("salida\n\nNP2RC:tok:100\n", "tok")
	if !found || code != 100 {
		t.Fatalf("found=%v code=%d, esperaba found=true code=100", found, code)
	}
	if cleaned != "salida\n" {
		t.Errorf("cleaned = %q, esperaba %q", cleaned, "salida\n")
	}
}

func TestParseGuestExitWithoutMarker(t *testing.T) {
	cleaned, _, found := parseGuestExit("solo salida\n", "tok")
	if found {
		t.Fatal("no debía encontrar marcador")
	}
	if cleaned != "solo salida\n" {
		t.Errorf("cleaned = %q", cleaned)
	}
}

func TestParseGuestExitIgnoresForeignToken(t *testing.T) {
	if _, _, found := parseGuestExit("NP2RC:otro:7\n", "tok"); found {
		t.Fatal("no debía aceptar un marcador de otro token")
	}
}

func TestRedactArgsHidesPassword(t *testing.T) {
	got := redactArgs([]string{"--username=root", "--password=secreto", "resto"})
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "secreto") {
		t.Fatalf("la contraseña no fue redactada: %v", got)
	}
	if got[1] != "--password=***" {
		t.Fatalf("--password no redactado: %q", got[1])
	}
	if got[0] != "--username=root" || got[2] != "resto" {
		t.Fatalf("se alteraron argumentos no sensibles: %v", got)
	}
}

func TestGuestControlBaseArgsIncludesCredentials(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{
		VBoxManage: "VBoxManage",
		VM:         "VM_A",
		User:       "root",
		Password:   "secreto",
		Domain:     "LAB",
	})
	args := g.baseArgs("run")
	joined := strings.Join(args, " ")
	for _, want := range []string{"--nologo", "guestcontrol", "VM_A", "run", "--username=root", "--password=secreto", "--domain=LAB"} {
		if !strings.Contains(joined, want) {
			t.Errorf("faltó %q en %v", want, args)
		}
	}
}

func TestGuestControlRunArgsLayout(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{
		VBoxManage: "VBoxManage",
		VM:         "VM_A",
		User:       "root",
		Password:   "secreto",
	})
	argv := []string{"apt-get", "install", "-y", "nginx"}
	args := g.runArgs(context.Background(), "tok", argv)

	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("falta el separador -- en %v", args)
	}
	// Everything after -- is the guest argv: sh -c <script> sh <quoted command>.
	want := []string{"-c", `eval "$1"; rc=$?; printf '\nNP2RC:tok:%d\n' "$rc"; exit 0`, "sh", ShellQuote(argv)}
	got := args[sep+1:]
	if len(got) != len(want) {
		t.Fatalf("argv invitado = %v, esperaba %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv invitado[%d] = %q, esperaba %q", i, got[i], want[i])
		}
	}

	// No user data may appear before the separator.
	head := strings.Join(args[:sep], " ")
	if strings.Contains(head, "nginx") {
		t.Errorf("los argumentos del usuario aparecen antes de --: %q", head)
	}
}

func TestGuestControlRunArgsPreserveEmptyArguments(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{VM: "VM_A", User: "root"})
	args := g.runArgs(context.Background(), "tok", []string{"ssh-keygen", "-N", "", "-f", "/root/.ssh/id_ed25519"})

	// VBoxManage drops empty arguments, so the empty passphrase must travel
	// inside the single, safely quoted command string as ''.
	last := args[len(args)-1]
	if !strings.Contains(last, "-N ''") {
		t.Fatalf("la cadena de comando no conserva el argumento vacío: %q", last)
	}
	for _, a := range args {
		if a == "" {
			t.Fatalf("no debe enviarse ningún argumento vacío a VBoxManage: %v", args)
		}
	}
}

func TestGuestControlCopyFromArgsHasHostDestination(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{VM: "VM_A", User: "root", Password: "secreto"})
	args := g.copyFromArgs("/etc/hosts", "/tmp/np2/hosts")

	if len(args) < 2 {
		t.Fatalf("argv de copyfrom demasiado corto: %v", args)
	}
	if got := args[len(args)-1]; got != "/tmp/np2/hosts" {
		t.Errorf("el último operando debe ser el destino en el host, se obtuvo %q en %v", got, args)
	}
	if got := args[len(args)-2]; got != "/etc/hosts" {
		t.Errorf("el penúltimo operando debe ser el origen en el invitado, se obtuvo %q en %v", got, args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--target-directory=") {
			t.Errorf("copyfrom no debe usar --target-directory sin destino: %v", args)
		}
	}
}

func TestGuestControlCopyToArgsHasGuestDestination(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{VM: "VM_A", User: "root", Password: "secreto"})
	args := g.copyToArgs("/tmp/host-file", "/tmp/guest-file")

	if len(args) < 2 {
		t.Fatalf("argv de copyto demasiado corto: %v", args)
	}
	if got := args[len(args)-1]; got != "/tmp/guest-file" {
		t.Errorf("el último operando debe ser el destino en el invitado, se obtuvo %q en %v", got, args)
	}
	if got := args[len(args)-2]; got != "/tmp/host-file" {
		t.Errorf("el penúltimo operando debe ser el origen en el host, se obtuvo %q en %v", got, args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--target-directory=") {
			t.Errorf("copyto no debe usar --target-directory sin destino: %v", args)
		}
	}
}

func TestGuestControlRunRejectsStdin(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{VM: "VM_A", User: "root"})
	res := g.Run(context.Background(), []byte("datos"), "cat")
	if res.Ok() {
		t.Fatal("se esperaba error por entrada estándar")
	}
	if res.Err == nil {
		t.Fatal("se esperaba un error descriptivo")
	}
}

func TestGuestControlTimeout(t *testing.T) {
	g := NewGuestControl(GuestControlOptions{VM: "VM_A", Timeout: 5 * time.Second})
	if ms := g.timeoutMS(context.Background()); ms != 5000 {
		t.Errorf("timeoutMS = %d, esperaba 5000", ms)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ms := g.timeoutMS(ctx)
	if ms <= 0 || ms > 2000 {
		t.Errorf("timeoutMS con contexto = %d, esperaba 1..2000", ms)
	}
}
