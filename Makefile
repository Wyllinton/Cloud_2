BINARY := appgo
GO ?= go

# Valores del laboratorio HTTPaaS. Sobrescríbalos en la línea de comandos:
#   make run-httpaas HTTPAAS_DNS_SERVER=192.168.10.10
HTTPAAS_TEMPLATE ?= template_server
HTTPAAS_NETWORK ?= vboxnet0
HTTPAAS_DOMAIN ?= cloud.local
HTTPAAS_DNS_SERVER ?= 192.168.10.10
HTTPAAS_DNS_ZONE ?= cloud.local
HTTPAAS_DNS_KEY ?= /etc/bind/httpaas.key
HTTPAAS_GUEST_USER ?= np2deploy

.PHONY: build run run-auto run-guestcontrol run-ssh run-httpaas test vet fmt clean

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

# Módulo HTTPaaS en el host: VBoxManage + nsupdate + Guest Additions.
# La contraseña del invitado se toma de NP2_HTTPAAS_PASSWORD.
run-httpaas:
	$(GO) run . -listen :8080 -mode local -httpaas \
		-httpaas-template $(HTTPAAS_TEMPLATE) \
		-httpaas-network-mode hostonly -httpaas-network $(HTTPAAS_NETWORK) \
		-httpaas-domain $(HTTPAAS_DOMAIN) \
		-httpaas-dns-server $(HTTPAAS_DNS_SERVER) \
		-httpaas-dns-zone $(HTTPAAS_DNS_ZONE) \
		-httpaas-dns-key $(HTTPAAS_DNS_KEY) \
		-httpaas-guest-user $(HTTPAAS_GUEST_USER)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -f $(BINARY)
