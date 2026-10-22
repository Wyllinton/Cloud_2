package runner

import (
	"context"
	"errors"
	"testing"
)

// fakeRunner is a Runner whose behaviour is scripted for tests.
type fakeRunner struct {
	kind    string
	target  string
	results []Result
	calls   int
}

func (f *fakeRunner) Run(context.Context, []byte, string, ...string) Result {
	if f.calls >= len(f.results) {
		f.calls++
		return Result{Transport: f.kind, Err: errors.New("sin resultados configurados")}
	}
	res := f.results[f.calls]
	f.calls++
	res.Transport = f.kind
	return res
}

func (f *fakeRunner) Kind() string   { return f.kind }
func (f *fakeRunner) Target() string { return f.target }
func (f *fakeRunner) UsesSudo() bool { return false }
func (f *fakeRunner) Close() error   { return nil }

func TestFailoverFallsBackOnTransportFailure(t *testing.T) {
	primary := &fakeRunner{kind: "ssh", results: []Result{{Err: errors.New("conexión perdida"), ExitCode: -1}}}
	secondary := &fakeRunner{kind: "guestcontrol", results: []Result{{ExitCode: 0}}}
	f := NewFailover(primary, secondary)

	res := f.Run(context.Background(), nil, "true")
	if !res.Ok() {
		t.Fatalf("se esperaba éxito en el respaldo, got %+v", res)
	}
	if res.Transport != "guestcontrol" {
		t.Errorf("transporte = %q, esperaba guestcontrol", res.Transport)
	}
	if primary.calls != 1 || secondary.calls != 1 {
		t.Errorf("llamadas primary=%d secondary=%d", primary.calls, secondary.calls)
	}

	// Once switched, the primary must not be used again.
	_ = f.Run(context.Background(), nil, "true")
	if primary.calls != 1 {
		t.Errorf("el primario se volvió a usar: %d llamadas", primary.calls)
	}
	if f.Kind() != "guestcontrol" {
		t.Errorf("Kind = %q, esperaba guestcontrol", f.Kind())
	}
}

func TestFailoverKeepsPrimaryOnCommandFailure(t *testing.T) {
	primary := &fakeRunner{kind: "ssh", results: []Result{{ExitCode: 1}}}
	secondary := &fakeRunner{kind: "guestcontrol", results: []Result{{ExitCode: 0}}}
	f := NewFailover(primary, secondary)

	res := f.Run(context.Background(), nil, "false")
	if res.Ok() {
		t.Fatal("se esperaba un resultado fallido")
	}
	if secondary.calls != 0 {
		t.Errorf("no debía usarse el respaldo: %d llamadas", secondary.calls)
	}
	if f.Kind() != "auto" {
		t.Errorf("Kind = %q, esperaba auto", f.Kind())
	}
}

func TestTransportFailedClassification(t *testing.T) {
	if !(Result{Err: errors.New("dial tcp: refused")}).TransportFailed() {
		t.Error("un error de conexión debe considerarse fallo de transporte")
	}
	if (Result{ExitCode: 1}).TransportFailed() {
		t.Error("un código de salida distinto de cero no es fallo de transporte")
	}
	if (Result{}).TransportFailed() {
		t.Error("un resultado vacío no es fallo de transporte")
	}
}
