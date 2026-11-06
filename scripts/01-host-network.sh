#!/usr/bin/env bash
# Crea la red host-only del laboratorio HTTPaaS y conecta a ella la VM del DNS
# (dns_server/ns). Opcionalmente conecta también la plantilla con
# ATTACH_TEMPLATE=1. Ejecute este script en el HOST de VirtualBox, con
# dns_server apagada. Es idempotente.
#
# Ejecútelo con SU USUARIO, no con sudo: VirtualBox registra las VM por usuario
# y como root no vería dns_server ni template_server.
#
#   ./scripts/01-host-network.sh
#
# Requisito previo (una sola vez, este sí con sudo): desde VirtualBox 6.1.28 en
# Linux solo se permiten redes host-only dentro de 192.168.56.0/21, salvo que se
# declaren en /etc/vbox/networks.conf:
#
#   sudo mkdir -p /etc/vbox
#   echo '* 192.168.10.0/24' | sudo tee -a /etc/vbox/networks.conf
#
# Variables:
#   VBOX_NET    nombre de la red host-only (vboxnet0)
#   HOST_IP     IP del host dentro de la red del laboratorio (192.168.10.1).
#               NO es la IP de su Wi-Fi/LAN: debe estar en otra subred.
#   PREFIX      prefijo de red (24)
#   DNS_VM      VM del servidor DNS (dns_server)
#   TEMPLATE_VM VM plantilla (template_server)
#   ATTACH_TEMPLATE  1 para conectar también la plantilla (por omisión 0: la
#                    plantilla puede seguir en NAT para instalar paquetes)
set -euo pipefail

VBOX_NET="${VBOX_NET:-vboxnet0}"
HOST_IP="${HOST_IP:-192.168.10.1}"
PREFIX="${PREFIX:-24}"
DNS_VM="${DNS_VM:-dns_server}"
TEMPLATE_VM="${TEMPLATE_VM:-template_server}"
ATTACH_TEMPLATE="${ATTACH_TEMPLATE:-0}"
VBOXMANAGE="${VBOXMANAGE:-VBoxManage}"
NETWORKS_CONF="${NETWORKS_CONF:-/etc/vbox/networks.conf}"

log() { printf '\033[1;34m[red]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

# --- utilidades IPv4 ---------------------------------------------------------
ip2int() {
  local IFS=. a b c d
  read -r a b c d <<<"$1"
  echo $(( (a << 24) | (b << 16) | (c << 8) | d ))
}
int2ip() {
  local n=$1
  echo "$(( (n >> 24) & 255 )).$(( (n >> 16) & 255 )).$(( (n >> 8) & 255 )).$(( n & 255 ))"
}
mask_of() { local p=$1; (( p == 0 )) && echo 0 || echo $(( (0xFFFFFFFF << (32 - p)) & 0xFFFFFFFF )); }
# in_cidr IP RED/PREFIJO
in_cidr() {
  local ip net p m
  ip=$(ip2int "$1"); net=${2%/*}; p=${2#*/}; m=$(mask_of "$p")
  (( (ip & m) == ($(ip2int "$net") & m) ))
}
valid_ipv4() {
  [[ "$1" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || return 1
  local o; for o in "${BASH_REMATCH[@]:1}"; do (( o <= 255 )) || return 1; done
}

# --- comprobaciones previas ----------------------------------------------------
command -v "$VBOXMANAGE" >/dev/null 2>&1 || die "no se encontró $VBOXMANAGE"
if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
  die "no ejecute este script con sudo: como root VirtualBox no ve las VM de ${SUDO_USER:-su usuario}. Ejecútelo como: ./scripts/01-host-network.sh"
fi
valid_ipv4 "$HOST_IP" || die "HOST_IP inválida: ${HOST_IP}"
[[ "$PREFIX" =~ ^[0-9]+$ ]] && (( PREFIX >= 8 && PREFIX <= 30 )) || die "PREFIX debe estar entre 8 y 30"

NETMASK="$(int2ip "$(mask_of "$PREFIX")")"
NETWORK="$(int2ip $(( $(ip2int "$HOST_IP") & $(mask_of "$PREFIX") )))/${PREFIX}"

# La red del laboratorio no puede solaparse con otra interfaz del host (por
# ejemplo la Wi-Fi 192.168.1.0/24): el tráfico a internet dejaría de funcionar.
while read -r ifname cidr; do
  [[ "$ifname" == "lo" || "$ifname" == "$VBOX_NET" ]] && continue
  addr=${cidr%/*}; plen=${cidr#*/}
  net_other="$(int2ip $(( $(ip2int "$addr") & $(mask_of "$plen") )))/${plen}"
  if in_cidr "$HOST_IP" "$net_other" || in_cidr "$addr" "$NETWORK"; then
    die "HOST_IP ${HOST_IP} (${NETWORK}) se solapa con ${ifname} ${cidr}. Use otra subred, por ejemplo HOST_IP=192.168.10.1"
  fi
done < <(ip -4 -o addr show | awk '{print $2, $4}')

# VirtualBox >= 6.1.28 rechaza (E_ACCESSDENIED) rangos no declarados.
range_allowed() {
  local line tok
  [[ -r "$NETWORKS_CONF" ]] || { in_cidr "$HOST_IP" 192.168.56.0/21; return; }
  while read -r line; do
    [[ "$line" == \** ]] || continue
    for tok in ${line#\*}; do
      [[ "$tok" == */* ]] && valid_ipv4 "${tok%/*}" && in_cidr "$HOST_IP" "$tok" && return 0
    done
  done < "$NETWORKS_CONF"
  return 1
}
if ! range_allowed; then
  die "VirtualBox no permite ${NETWORK} en redes host-only. Declárela una vez y repita:
    sudo mkdir -p /etc/vbox
    echo '* ${NETWORK}' | sudo tee -a ${NETWORKS_CONF}"
fi

# --- interfaz host-only --------------------------------------------------------
HOSTONLY_LIST="$("$VBOXMANAGE" list hostonlyifs 2>/dev/null || true)"
if ! grep -q "^Name:[[:space:]]*${VBOX_NET}$" <<<"$HOSTONLY_LIST"; then
  log "creando interfaz host-only ${VBOX_NET}"
  out="$("$VBOXMANAGE" hostonlyif create)"
  created="$(sed -n "s/.*'\(vboxnet[0-9]*\)'.*/\1/p" <<<"$out" | head -n1)"
  if [[ -n "$created" && "$created" != "$VBOX_NET" ]]; then
    log "VirtualBox creó ${created}; se usará ese nombre (ajuste -httpaas-network ${created})"
    VBOX_NET="$created"
  fi
fi

log "configurando ${VBOX_NET} con ${HOST_IP}/${NETMASK}"
"$VBOXMANAGE" hostonlyif ipconfig "$VBOX_NET" --ip "$HOST_IP" --netmask "$NETMASK"

# El direccionamiento de las instancias es estático; un DHCP en la misma red
# solo aportaría conflictos. `list dhcpservers` es el subcomando válido en
# VirtualBox 7.x (`dhcpserver list` no existe).
DHCP_NET="HostInterfaceNetworking-${VBOX_NET}"
DHCP_LIST="$("$VBOXMANAGE" list dhcpservers 2>/dev/null || true)"
if grep -qF "$DHCP_NET" <<<"$DHCP_LIST"; then
  log "deshabilitando DHCP en ${VBOX_NET}"
  "$VBOXMANAGE" dhcpserver modify --network="$DHCP_NET" --disable
fi

# --- conectar VMs ----------------------------------------------------------------
attach_vm() {
  local vm="$1" info state
  if ! info="$("$VBOXMANAGE" showvminfo "$vm" --machinereadable 2>/dev/null)"; then
    die "la VM ${vm} no existe para el usuario $(id -un) (VBoxManage list vms)"
  fi
  state="$(sed -n 's/^VMState="\(.*\)"$/\1/p' <<<"$info")"
  if [[ "$state" != "poweroff" && "$state" != "aborted" ]]; then
    die "la VM ${vm} debe estar apagada (estado: ${state})"
  fi
  log "conectando ${vm} a ${VBOX_NET} por nic1"
  "$VBOXMANAGE" modifyvm "$vm" --nic1 hostonly --hostonlyadapter1 "$VBOX_NET" --cableconnected1 on
}

attach_vm "$DNS_VM"
if [[ "$ATTACH_TEMPLATE" == "1" ]]; then
  attach_vm "$TEMPLATE_VM"
else
  log "la plantilla ${TEMPLATE_VM} no se modifica (ATTACH_TEMPLATE=1 para conectarla)"
fi

log "red lista: ${VBOX_NET} = ${HOST_IP}/${PREFIX}"
"$VBOXMANAGE" list hostonlyifs | sed -n "/^Name:[[:space:]]*${VBOX_NET}$/,/^VBoxNetworkName/p"
log "Nota: ${VBOX_NET} aparece DOWN en 'ip a' hasta que arranque una VM conectada a ella."
log "En el invitado ns configure la IP 192.168.10.10/${PREFIX} con gateway ${HOST_IP}."
