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
	if f.Kind() != "ssh" {
		t.Errorf("Kind = %q, esperaba ssh", f.Kind())
	}
}

func TestFailoverPromotesBootstrapToSSH(t *testing.T) {
	guest := &fakeRunner{kind: "guestcontrol", target: "VM_A", results: []Result{{ExitCode: 0}}}
	sshRunner := &fakeRunner{kind: "ssh", target: "root@192.168.1.10:22", results: []Result{{ExitCode: 0}}}
	var received string
	connect := func(key []byte) (Runner, error) {
		received = string(key)
		return sshRunner, nil
	}

	f := NewBootstrapFailover(guest, "root@192.168.1.10:22", connect)
	if f.Kind() != "guestcontrol" {
		t.Fatalf("Kind inicial = %q, esperaba guestcontrol", f.Kind())
	}
	if err := f.PromoteSSH([]byte("llave-privada")); err != nil {
		t.Fatalf("PromoteSSH: %v", err)
	}
	if received != "llave-privada" {
		t.Errorf("el conector recibió %q", received)
	}
	if f.Kind() != "ssh" {
		t.Errorf("Kind tras promover = %q, esperaba ssh", f.Kind())
	}

	res := f.Run(context.Background(), nil, "true")
	if res.Transport != "ssh" {
		t.Errorf("transporte = %q, esperaba ssh", res.Transport)
	}
	if guest.calls != 0 {
		t.Errorf("no debía usarse Guest Additions tras promover: %d llamadas", guest.calls)
	}
}

func TestFailoverKeepsGuestControlWhenPromotionFails(t *testing.T) {
	guest := &fakeRunner{kind: "guestcontrol", target: "VM_A", results: []Result{{ExitCode: 0}}}
	connect := func([]byte) (Runner, error) {
		return nil, errors.New("ssh no responde")
	}

	f := NewBootstrapFailover(guest, "root@192.168.1.10:22", connect)
	if err := f.PromoteSSH([]byte("llave")); err == nil {
		t.Fatal("se esperaba un error de promoción")
	}
	if f.Kind() != "guestcontrol" {
		t.Errorf("Kind = %q, esperaba guestcontrol", f.Kind())
	}
	if res := f.Run(context.Background(), nil, "true"); res.Transport != "guestcontrol" {
		t.Errorf("transporte = %q, esperaba guestcontrol", res.Transport)
	}
}

type fakeTransferRunner struct{ fakeRunner }

func (f *fakeTransferRunner) ReadFile(context.Context, string) ([]byte, Result, error) {
	return nil, Result{}, nil
}

func (f *fakeTransferRunner) WriteFile(context.Context, string, []byte, uint32) ([]Result, error) {
	return nil, nil
}

func TestFailoverFileTransferFollowsActiveTransport(t *testing.T) {
	guest := &fakeTransferRunner{fakeRunner{kind: "guestcontrol", results: []Result{{ExitCode: 0}}}}
	sshRunner := &fakeRunner{kind: "ssh", results: []Result{{ExitCode: 0}}}
	f := NewBootstrapFailover(guest, "root@192.168.1.10:22", func([]byte) (Runner, error) {
		return sshRunner, nil
	})

	if _, ok := f.FileTransfer(); !ok {
		t.Fatal("Guest Additions debe ofrecer transferencia de archivos")
	}
	if err := f.PromoteSSH([]byte("llave")); err != nil {
		t.Fatalf("PromoteSSH: %v", err)
	}
	if _, ok := f.FileTransfer(); ok {
		t.Fatal("SSH no debe anunciar la transferencia copyto/copyfrom de Guest Additions")
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
