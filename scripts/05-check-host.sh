#!/usr/bin/env bash
# Diagnóstico de requisitos del orquestador HTTPaaS. Ejecute en el host.
#
#   ./05-check-host.sh
set -euo pipefail

VBOXMANAGE="${VBOXMANAGE:-VBoxManage}"
TEMPLATE_VM="${TEMPLATE_VM:-template_server}"
DNS_VM="${DNS_VM:-dns_server}"
DNS_KEY="${DNS_KEY:-/etc/bind/httpaas.key}"
HOSTONLY="${HOSTONLY:-vboxnet0}"

ok()   { printf '\033[1;32m[ok]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[aviso]\033[0m %s\n' "$*"; }
bad()  { printf '\033[1;31m[falta]\033[0m %s\n' "$*"; }

command -v "$VBOXMANAGE" >/dev/null 2>&1 && ok "$($VBOXMANAGE --version)" || bad "VBoxManage"
command -v nsupdate >/dev/null 2>&1 && ok "nsupdate disponible" || bad "instale bind9-dnsutils (nsupdate)"
command -v dig >/dev/null 2>&1 && ok "dig disponible" || warn "dig no está instalado (opcional)"

if [[ -r "$DNS_KEY" ]]; then
  perm="$(stat -c '%a' "$DNS_KEY")"
  if [[ "$perm" == "600" ]]; then
    ok "llave TSIG ${DNS_KEY} (0600)"
  else
    warn "llave TSIG ${DNS_KEY} con permisos ${perm}; use chmod 600"
  fi
else
  bad "no se puede leer la llave TSIG ${DNS_KEY}"
fi

for vm in "$TEMPLATE_VM" "$DNS_VM"; do
  if "$VBOXMANAGE" showvminfo "$vm" --machinereadable >/tmp/np2-check-vm 2>/dev/null; then
    state="$(sed -n 's/^VMState="\(.*\)"$/\1/p' /tmp/np2-check-vm)"
    ok "VM ${vm} (${state})"
    if [[ "$vm" == "$TEMPLATE_VM" && "$state" != "poweroff" ]]; then
      bad "la plantilla debe estar apagada para crear discos diferenciales"
    fi
    if [[ "$vm" == "$DNS_VM" ]]; then
      nic="$(sed -n 's/^nic1="\(.*\)"$/\1/p' /tmp/np2-check-vm)"
      [[ "$nic" == "hostonly" ]] && ok "${vm} usa nic1=hostonly" || warn "${vm} usa nic1=${nic}; ajústelo con 01-host-network.sh"
    fi
  else
    bad "no existe la VM ${vm}"
  fi
done
rm -f /tmp/np2-check-vm

HOSTONLY_LIST="$("$VBOXMANAGE" list hostonlyifs 2>/dev/null || true)"
if grep -q "^Name:[[:space:]]*${HOSTONLY}$" <<<"$HOSTONLY_LIST"; then
  ok "red host-only ${HOSTONLY}"
else
  bad "no existe la red host-only ${HOSTONLY}"
fi

echo
echo "Sugerencia: el host necesita una ruta hacia 192.168.10.0/24 y el navegador"
echo "debe usar 192.168.10.10 como DNS para abrir http://<host>.cloud.local/."
