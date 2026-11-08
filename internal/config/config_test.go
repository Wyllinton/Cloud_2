package config

import "testing"

func TestAutoAllowsGuestControlBootstrapWithoutLocalKey(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeAuto
	cfg.SSH.Host = "192.168.1.10"
	cfg.GuestControl.VM = "VM_A"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("el modo auto debía permitir arrancar por Guest Additions sin --ssh-key: %v", err)
	}
}

func TestSSHModeStillRequiresLocalKey(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeSSH
	cfg.SSH.Host = "192.168.1.10"

	if err := cfg.Validate(); err == nil {
		t.Fatal("el modo ssh debía exigir --ssh-key")
	}
}

func TestAutoWithOnlySSHRequiresLocalKey(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeAuto
	cfg.SSH.Host = "192.168.1.10"

	if err := cfg.Validate(); err == nil {
		t.Fatal("sin Guest Additions el modo auto debía exigir --ssh-key")
	}
}

func TestHTTPaaSDefaultsValidate(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeLocal

	if err := cfg.Validate(); err != nil {
		t.Fatalf("la configuración HTTPaaS por omisión debía ser válida: %v", err)
	}
}

func TestHTTPaaSRejectsInvalidDomain(t *testing.T) {
	cfg := Default()
	cfg.Mode = ModeLocal
	cfg.HTTPaaS.Domain = "cloud local"

	if err := cfg.Validate(); err == nil {
		t.Fatal("un dominio HTTPaaS inválido debía rechazarse")
	}
}
