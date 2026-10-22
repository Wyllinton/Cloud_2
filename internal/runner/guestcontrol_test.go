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
	args := g.runArgs(context.Background(), "tok", []string{"apt-get", "install", "-y", "nginx"})

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
	// Everything after -- is the guest argv: sh -c <script> sh <command...>.
	want := []string{"-c", `command "$@"; rc=$?; printf '\nNP2RC:tok:%d\n' "$rc"; exit 0`, "sh", "apt-get", "install", "-y", "nginx"}
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
