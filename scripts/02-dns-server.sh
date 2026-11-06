#!/usr/bin/env bash
# Configura Bind9 y la zona cloud.local con actualizaciones dinámicas TSIG.
# Ejecute este script DENTRO de la VM dns_server (Debian 13) como root:
#
#   sudo ZONE=cloud.local NS_IP=192.168.10.10 ./02-dns-server.sh
#
# Al terminar, copie /etc/bind/httpaas.key al host donde corre appgo:
#
#   sudo scp root@192.168.10.10:/etc/bind/httpaas.key ./httpaas.key
#   chmod 600 ./httpaas.key
#
# El host necesita nsupdate (paquete bind9-dnsutils).
set -euo pipefail

ZONE="${ZONE:-cloud.local}"
ZONE="${ZONE%.}" # aceptar tanto cloud.local como cloud.local.
NS_NAME="${NS_NAME:-ns}"
NS_IP="${NS_IP:-192.168.10.10}"
# /var/lib/bind es escribible por el usuario `bind` y está permitido por el
# perfil AppArmor de Debian. Un archivo de zona en /etc/bind impediría crear el
# journal de las actualizaciones dinámicas (allow-update).
ZONE_FILE="${ZONE_FILE:-/var/lib/bind/db.${ZONE}}"
KEY_NAME="${KEY_NAME:-httpaas-key}"
KEY_FILE="${KEY_FILE:-/etc/bind/httpaas.key}"
NETWORK_CIDR="${NETWORK_CIDR:-192.168.10.0/24}"
FORCE_ZONE="${FORCE_ZONE:-0}"
FORCE_KEY="${FORCE_KEY:-0}"

log() { printf '\033[1;34m[dns]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || die "ejecute como root dentro de dns_server"
command -v named-checkconf >/dev/null 2>&1 || true

export DEBIAN_FRONTEND=noninteractive
log "instalando Bind9 y utilidades"
apt-get update -qq
apt-get install -y --no-install-recommends bind9 bind9-utils bind9-dnsutils

# --- llave TSIG --------------------------------------------------------------
if [[ ! -s "$KEY_FILE" || "$FORCE_KEY" == "1" ]]; then
  log "generando llave TSIG ${KEY_NAME}"
  command -v tsig-keygen >/dev/null 2>&1 || die "tsig-keygen no está disponible (bind9-utils)"
  umask 077
  tsig-keygen -a hmac-sha256 "$KEY_NAME" > "$KEY_FILE"
  chown root:bind "$KEY_FILE"
  chmod 640 "$KEY_FILE"
else
  log "reutilizando la llave existente ${KEY_FILE}"
fi
SECRET="$(sed -n 's/.*secret "\([^"]*\)".*/\1/p' "$KEY_FILE" | head -n1)"
[[ -n "$SECRET" ]] || die "no se pudo extraer el secreto de ${KEY_FILE}"

# --- zona --------------------------------------------------------------------
mkdir -p "$(dirname "$ZONE_FILE")"
if [[ ! -s "$ZONE_FILE" || "$FORCE_ZONE" == "1" ]]; then
  SERIAL="$(date +%Y%m%d%H)"
  log "escribiendo zona ${ZONE} en ${ZONE_FILE}"
  cat > "$ZONE_FILE" <<EOF
\$TTL 300
@   IN  SOA ${NS_NAME}.${ZONE}. admin.${ZONE}. (
        ${SERIAL} ; serial
        3600      ; refresh
        900       ; retry
        604800    ; expire
        300       ; minimum
)
    IN  NS  ${NS_NAME}.${ZONE}.
${NS_NAME} IN A ${NS_IP}
EOF
  chown bind:bind "$ZONE_FILE"
  chmod 644 "$ZONE_FILE"
else
  log "reutilizando la zona existente ${ZONE_FILE}"
fi
named-checkzone "$ZONE" "$ZONE_FILE"

# --- configuración de named --------------------------------------------------
NAMED_LOCAL="${NAMED_LOCAL:-/etc/bind/named.conf.local}"
touch "$NAMED_LOCAL"
if ! grep -qF "include \"${KEY_FILE}\";" "$NAMED_LOCAL"; then
  log "incluyendo la llave TSIG en ${NAMED_LOCAL}"
  printf '\ninclude "%s";\n' "$KEY_FILE" >> "$NAMED_LOCAL"
fi
if ! grep -qF "zone \"${ZONE}\"" "$NAMED_LOCAL"; then
  log "declarando la zona ${ZONE}"
  cat >> "$NAMED_LOCAL" <<EOF

zone "${ZONE}" {
    type master;
    file "${ZONE_FILE}";
    allow-update { key "${KEY_NAME}"; };
    allow-transfer { none; };
    notify no;
};
EOF
fi

OPTIONS_FILE="/etc/bind/named.conf.options"
if [[ ! -f "${OPTIONS_FILE}.np2bak" ]]; then
  cp -a "$OPTIONS_FILE" "${OPTIONS_FILE}.np2bak" 2>/dev/null || true
fi
cat > "$OPTIONS_FILE" <<EOF
// Generado por appgo HTTPaaS. Respaldo: ${OPTIONS_FILE}.np2bak
options {
    directory "/var/cache/bind";
    listen-on { ${NS_IP}; 127.0.0.1; };
    listen-on-v6 { none; };
    allow-query { ${NETWORK_CIDR}; 127.0.0.1; };
    recursion yes;
    forwarders { 1.1.1.1; 8.8.8.8; };
    dnssec-validation auto;
};
EOF

named-checkconf
if systemctl list-unit-files | grep -q '^named\.service'; then
  systemctl restart named
  systemctl enable named >/dev/null
elif systemctl list-unit-files | grep -q '^bind9\.service'; then
  systemctl restart bind9
  systemctl enable bind9 >/dev/null
else
  die "no se encontró la unidad systemd named/bind9"
fi

log "zona ${ZONE} activa en ${NS_IP}"
log "prueba local: dig @${NS_IP} ${NS_NAME}.${ZONE} A +short"
log "llave TSIG: ${KEY_FILE} (cópiela al host con permisos 600)"
printf '\n--- contenido de la llave (manéjela como secreto) ---\n'
cat "$KEY_FILE"
