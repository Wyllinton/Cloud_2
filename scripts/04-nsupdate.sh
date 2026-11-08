#!/usr/bin/env bash
# Ayuda manual para probar Bind9 sin pasar por la webapp.
#
#   ./04-nsupdate.sh add paisajes 192.168.10.30
#   ./04-nsupdate.sh delete paisajes
#
# Variables:
#   DNS_SERVER (192.168.10.10), DNS_ZONE (cloud.local), DNS_KEY (/etc/bind/httpaas.key)
#   DNS_TTL (300)
set -euo pipefail

ACTION="${1:-}"
HOST="${2:-}"
IP="${3:-}"
DNS_SERVER="${DNS_SERVER:-192.168.10.10}"
DNS_ZONE="${DNS_ZONE:-cloud.local}"
DNS_ZONE="${DNS_ZONE%.}" # aceptar tanto cloud.local como cloud.local.
DNS_KEY="${DNS_KEY:-/etc/bind/httpaas.key}"
DNS_TTL="${DNS_TTL:-300}"

die() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

[[ "$ACTION" == "add" || "$ACTION" == "delete" ]] || die "uso: $0 add <host> <ip> | delete <host>"
[[ "$HOST" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]] || die "hostname inválido: ${HOST}"
if [[ "$ACTION" == "add" ]]; then
  [[ "$IP" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || die "IPv4 inválida: ${IP}"
  IFS=. read -r o1 o2 o3 o4 <<< "$IP"
  (( o1 <= 255 && o2 <= 255 && o3 <= 255 && o4 <= 255 )) || die "IPv4 fuera de rango: ${IP}"
fi
[[ -r "$DNS_KEY" ]] || die "no se puede leer la llave TSIG ${DNS_KEY}"

FQDN="${HOST}.${DNS_ZONE}."
if [[ "$ACTION" == "add" ]]; then
  UPDATE="update delete ${FQDN} A
update add ${FQDN} ${DNS_TTL} A ${IP}"
else
  UPDATE="update delete ${FQDN} A"
fi

printf 'server %s\nzone %s.\n%s\nsend\n' "$DNS_SERVER" "$DNS_ZONE" "$UPDATE" \
  | nsupdate -k "$DNS_KEY"

if [[ "$ACTION" == "add" ]]; then
  printf 'registro agregado: %s -> %s\n' "$FQDN" "$IP"
  command -v dig >/dev/null 2>&1 && dig "@${DNS_SERVER}" "$FQDN" A +short || true
else
  printf 'registro eliminado: %s\n' "$FQDN"
fi
