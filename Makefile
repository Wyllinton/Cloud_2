BINARY := appgo
GO ?= go

.PHONY: build run run-auto run-guestcontrol run-ssh test vet fmt clean

build:
	$(GO) build -o $(BINARY) .

# Modo recomendado en el host: SSH con respaldo automático a Guest Additions.
# Ajuste los valores (host, llave, VM y credenciales del invitado).
run-auto:
	$(GO) run . -listen :8080 -mode auto \
		-ssh-host 192.168.1.10 -ssh-user root -ssh-key ./root_id_ed25519 \
		-guestcontrol-vm VM_A -guestcontrol-user root

# Solo Guest Additions (VBoxManage), útil antes de que exista SSH.
run-guestcontrol:
	$(GO) run . -listen :8080 -mode guestcontrol \
		-guestcontrol-vm VM_A -guestcontrol-user root

# Etapa inicial: se ejecuta dentro del invitado (canal de las Guest Additions).
run:
	$(GO) run . -listen :8080 -mode local -sudo

# Etapa final: control remoto por SSH solo con llave (ajuste los valores).
run-ssh:
	$(GO) run . -listen :8080 -mode ssh -ssh-host 192.168.1.10 -ssh-user root -ssh-key ./root_id_ed25519

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -f $(BINARY)
