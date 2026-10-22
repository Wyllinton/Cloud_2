package runner

import (
	"context"
	"testing"
)

func TestBuildArgvAddsSudo(t *testing.T) {
	got := buildArgv(true, "apt-get", "install", "-y", "nginx")
	want := []string{"sudo", "-n", "apt-get", "install", "-y", "nginx"}
	if len(got) != len(want) {
		t.Fatalf("buildArgv con sudo = %v, esperaba %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("buildArgv con sudo = %v, esperaba %v", got, want)
		}
	}
}

func TestBuildArgvWithoutSudo(t *testing.T) {
	got := buildArgv(false, "systemctl", "status", "nginx")
	if got[0] != "systemctl" || len(got) != 3 {
		t.Fatalf("buildArgv sin sudo = %v", got)
	}
}

func TestShellQuoteNeutralisesInjection(t *testing.T) {
	got := ShellQuote([]string{"echo", "a; rm -rf /"})
	want := `echo 'a; rm -rf /'`
	if got != want {
		t.Errorf("ShellQuote = %q, esperaba %q", got, want)
	}
}

func TestShellQuoteHandlesSingleQuotes(t *testing.T) {
	got := ShellQuote([]string{"printf", "it's"})
	want := `printf 'it'\''s'`
	if got != want {
		t.Errorf("ShellQuote = %q, esperaba %q", got, want)
	}
}

func TestLocalRunnerCapturesOutput(t *testing.T) {
	r := NewLocal(false)
	res := r.Run(context.Background(), []byte("hola\n"), "cat")
	if !res.Ok() {
		t.Fatalf("cat falló: %v (stderr=%q)", res.Err, res.Stderr)
	}
	if res.Stdout != "hola\n" {
		t.Errorf("stdout = %q, esperaba %q", res.Stdout, "hola\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, esperaba 0", res.ExitCode)
	}
}

func TestLocalRunnerReportsExitCode(t *testing.T) {
	r := NewLocal(false)
	res := r.Run(context.Background(), nil, "sh", "-c", "exit 3")
	if res.Ok() {
		t.Fatal("se esperaba fallo")
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, esperaba 3", res.ExitCode)
	}
}
