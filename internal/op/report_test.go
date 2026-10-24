package op

import (
	"testing"

	"np2/appgo/internal/runner"
)

func TestOptionalStepDoesNotFailReport(t *testing.T) {
	rep := New("consultar estado")
	rep.OptionalStep("systemctl is-active demo.service", runner.Result{Argv: []string{"systemctl", "is-active", "demo.service"}, ExitCode: 3})
	rep.Finish()

	if !rep.OK {
		t.Fatalf("un paso opcional con código distinto de cero no debe marcar el informe como fallido: %v", rep.Err)
	}
	if len(rep.Steps) != 1 || !rep.Steps[0].Optional {
		t.Fatalf("el paso no quedó registrado como opcional: %#v", rep.Steps)
	}
}

func TestRequiredStepFailsReport(t *testing.T) {
	rep := New("iniciar servicio")
	rep.Step("systemctl start demo.service", runner.Result{Argv: []string{"systemctl", "start", "demo.service"}, ExitCode: 1})
	rep.Finish()

	if rep.OK {
		t.Fatal("un paso obligatorio con código distinto de cero debe marcar el informe como fallido")
	}
}
