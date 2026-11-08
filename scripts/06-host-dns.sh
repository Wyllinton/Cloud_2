#!/usr/bin/env bash
# Hace que el HOST (Fedora/Debian con systemd-resolved) resuelva *.cloud.local
# preguntando al Bind9 del laboratorio, sin tocar el DNS de internet (DNS
# dividido). Así el navegador abre http://<host>.cloud.local/.
#
#   sudo ./scripts/06-host-dns.sh           # aplicar y dejarlo persistente
#   sudo ./scripts/06-host-dns.sh --undo    # revertir
#
# Qué hace:
#   1. resolvectl: asigna DNS_SERVER a la interfaz VBOX_NET solo para el
#      dominio ~ZONE (routing domain) y sin ruta por omisión.
#   2. Instala una unidad systemd ligada al dispositivo VBOX_NET para volver a
#      aplicarlo cada vez que la interfaz aparece (arranque del equipo).
#   3. Si ZONE termina en .local y authselect tiene la función with-mdns4, la
#      desactiva: nss-mdns (mdns4_minimal [NOTFOUND=return]) captura todos los
#      nombres *.local para mDNS y la consulta nunca llega a systemd-resolved.
#
# Variables: VBOX_NET (vboxnet0), DNS_SERVER (192.168.10.10), ZONE (cloud.local)
set -euo pipefail

VBOX_NET="${VBOX_NET:-vboxnet0}"
DNS_SERVER="${DNS_SERVER:-192.168.10.10}"
ZONE="${ZONE:-cloud.local}"
ZONE="${ZONE%.}"
UNIT="np2-cloud-dns.service"
UNIT_PATH="/etc/systemd/system/${UNIT}"
MDNS_MARK="/etc/np2-cloud-dns.mdns-disabled"

log() { printf '\033[1;34m[dns-host]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || die "ejecute con sudo: modifica systemd-resolved y nsswitch"
command -v resolvectl >/dev/null 2>&1 || die "resolvectl no está disponible (se requiere systemd-resolved)"
systemctl is-active --quiet systemd-resolved || die "systemd-resolved no está activo"
[[ "$DNS_SERVER" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || die "DNS_SERVER inválido: ${DNS_SERVER}"
[[ "$ZONE" =~ ^[A-Za-z0-9.-]+$ ]] || die "ZONE inválida: ${ZONE}"
[[ "$VBOX_NET" =~ ^[A-Za-z0-9_.-]+$ ]] || die "VBOX_NET inválida: ${VBOX_NET}"

DEVICE_UNIT="sys-subsystem-net-devices-${VBOX_NET}.device"

if [[ "${1:-}" == "--undo" ]]; then
  if [[ -f "$UNIT_PATH" ]]; then
    systemctl disable --now "$UNIT" >/dev/null 2>&1 || true
    rm -f "$UNIT_PATH"
    systemctl daemon-reload
    log "unidad ${UNIT} eliminada"
  fi
  ip link show "$VBOX_NET" >/dev/null 2>&1 && resolvectl revert "$VBOX_NET" && log "DNS de ${VBOX_NET} revertido"
  if [[ -f "$MDNS_MARK" ]] && command -v authselect >/dev/null 2>&1; then
    authselect enable-feature with-mdns4 >/dev/null && authselect apply-changes >/dev/null
    rm -f "$MDNS_MARK"
    log "función with-mdns4 de authselect reactivada"
  fi
  exit 0
fi

ip link show "$VBOX_NET" >/dev/null 2>&1 || die "no existe la interfaz ${VBOX_NET}; ejecute antes scripts/01-host-network.sh"

# 1 y 2: unidad persistente ligada a la interfaz. Se reaplica cada vez que
# VirtualBox crea vboxnet0 (por ejemplo, tras reiniciar el equipo).
log "instalando ${UNIT} (${ZONE} -> ${DNS_SERVER} por ${VBOX_NET})"
cat > "$UNIT_PATH" <<EOF
[Unit]
Description=DNS dividido: ${ZONE} -> ${DNS_SERVER} por ${VBOX_NET} (laboratorio HTTPaaS)
BindsTo=${DEVICE_UNIT}
After=${DEVICE_UNIT} systemd-resolved.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/bin/resolvectl dns ${VBOX_NET} ${DNS_SERVER}
ExecStart=/usr/bin/resolvectl domain ${VBOX_NET} ~${ZONE}
ExecStart=/usr/bin/resolvectl default-route ${VBOX_NET} false
ExecStop=/usr/bin/resolvectl revert ${VBOX_NET}

[Install]
WantedBy=${DEVICE_UNIT}
EOF
systemctl daemon-reload
systemctl enable "$UNIT" >/dev/null 2>&1
systemctl restart "$UNIT"

# 3: nss-mdns intercepta *.local antes que systemd-resolved.
if [[ "$ZONE" == *.local ]] && command -v authselect >/dev/null 2>&1; then
  if authselect current 2>/dev/null | grep -q -- '- with-mdns4$'; then
    log "desactivando la función with-mdns4 de authselect (mDNS capturaba *.local)"
    authselect disable-feature with-mdns4 >/dev/null
    authselect apply-changes >/dev/null
    touch "$MDNS_MARK"
  fi
elif [[ "$ZONE" == *.local ]] && grep -q 'mdns4_minimal \[NOTFOUND=return\]' /etc/nsswitch.conf; then
  log "aviso: /etc/nsswitch.conf envía *.local a mDNS antes que a DNS;"
  log "       mueva 'resolve' antes de 'mdns4_minimal' o quite [NOTFOUND=return]"
fi
log "nsswitch: $(grep '^hosts' /etc/nsswitch.conf)"

resolvectl status "$VBOX_NET" | sed -n '/Link/,$p' | grep -E 'Link|DNS Servers|DNS Domain|Default Route' || true

PROBE="ns.${ZONE}"
if getent hosts "$PROBE" >/dev/null; then
  log "OK: ${PROBE} -> $(getent hosts "$PROBE" | awk '{print $1}')"
else
  log "aviso: ${PROBE} todavía no resuelve; compruebe que ${DNS_SERVER} esté encendido:"
  log "       dig @${DNS_SERVER} ${ZONE} SOA +short"
fi
log "Si el navegador ya estaba abierto, recárguelo (Firefox guarda en caché los fallos de DNS)."
