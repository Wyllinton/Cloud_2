#!/usr/bin/env bash
# Prepara la VM plantilla (template_server, Debian 13 CLI) para HTTPaaS.
# Ejecute DENTRO de la plantilla como root:
#
#   sudo NP2_HTTPAAS_PASSWORD='***' ./03-template-server.sh
#
# El script:
#   - instala Apache, unzip, sudo y OpenSSH;
#   - crea el usuario np2deploy con sudo sin contraseña;
#   - habilita el directorio /etc/network/interfaces.d;
#   - deja Apache habilitado;
#   - desactiva el DHCP que el instalador deja en /etc/network/interfaces
#     (plantillas CLI con ifupdown). Las instancias usan la red host-only sin
#     DHCP y reciben su IP estática en interfaces.d/99-httpaas; si el bloque
#     DHCP sigue activo, cada arranque se queda esperando una dirección.
#     La sesión actual conserva su IP hasta reiniciar. Para omitir este paso:
#     KEEP_DHCP=1. Para volver a tener red en la plantilla: dhclient <iface>.
#
# Después APAGUE la plantilla: el orquestador exige poweroff para crear discos
# diferenciales. Las instancias reciben su IP estática por Guest Additions.
set -euo pipefail

DEPLOY_USER="${DEPLOY_USER:-np2deploy}"
PASSWORD="${NP2_HTTPAAS_PASSWORD:-}"
KEEP_DHCP="${KEEP_DHCP:-0}"
INTERFACES_FILE="${INTERFACES_FILE:-/etc/network/interfaces}"

log() { printf '\033[1;34m[template]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; exit 1; }

[[ ${EUID:-$(id -u)} -eq 0 ]] || die "ejecute como root dentro de template_server"
[[ -n "$PASSWORD" ]] || die "defina NP2_HTTPAAS_PASSWORD con la contraseña de ${DEPLOY_USER}"

export DEBIAN_FRONTEND=noninteractive
log "instalando paquetes base"
apt-get update -qq
apt-get install -y --no-install-recommends \
  apache2 unzip sudo openssh-server ca-certificates curl

if ! id "$DEPLOY_USER" >/dev/null 2>&1; then
  log "creando usuario ${DEPLOY_USER}"
  useradd --create-home --shell /bin/bash "$DEPLOY_USER"
else
  log "el usuario ${DEPLOY_USER} ya existe"
fi

log "actualizando contraseña de ${DEPLOY_USER}"
printf '%s:%s\n' "$DEPLOY_USER" "$PASSWORD" | chpasswd

log "concediendo sudo sin contraseña a ${DEPLOY_USER}"
usermod -aG sudo "$DEPLOY_USER"
cat > "/etc/sudoers.d/90-${DEPLOY_USER}" <<EOF
${DEPLOY_USER} ALL=(ALL) NOPASSWD: ALL
EOF
chmod 0440 "/etc/sudoers.d/90-${DEPLOY_USER}"
visudo -cf "/etc/sudoers.d/90-${DEPLOY_USER}"

# El orquestador escribe /etc/network/interfaces.d/99-httpaas. Asegure que el
# archivo principal incluya el directorio (Debian lo hace por omisión).
if ! grep -Eq '^[[:space:]]*source[[:space:]]+/etc/network/interfaces\.d/\*' /etc/network/interfaces 2>/dev/null; then
  log "habilitando /etc/network/interfaces.d"
  printf '\nsource /etc/network/interfaces.d/*\n' >> /etc/network/interfaces
fi

systemctl enable apache2 >/dev/null
systemctl start apache2

log "comprobando Guest Additions"
if command -v VBoxControl >/dev/null 2>&1; then
  VBoxControl --version || true
else
  log "aviso: VBoxControl no está en PATH; instale las Guest Additions en la plantilla"
fi

# Comenta las estrofas DHCP (iface <if> inet dhcp), sus opciones indentadas,
# las demás estrofas de esa interfaz (p. ej. inet6 auto) y sus líneas
# auto/allow-hotplug. Es idempotente: las líneas ya comentadas no se tocan.
disable_dhcp() {
  local file="$1" ifaces
  [[ -f "$file" ]] || { log "no existe ${file}; nada que desactivar"; return 0; }
  ifaces=$(awk '$1 == "iface" && $3 == "inet" && $4 == "dhcp" && $2 != "lo" { print $2 }' "$file" | sort -u | tr '\n' ' ')
  if [[ -z "${ifaces// /}" ]]; then
    log "sin estrofas DHCP en ${file}; nada que desactivar"
    return 0
  fi
  log "desactivando DHCP de: ${ifaces}(respaldo en ${file}.httpaas.bak)"
  cp -p "$file" "${file}.httpaas.bak"
  awk -v list="$ifaces" '
    BEGIN { n = split(list, a, " "); for (i = 1; i <= n; i++) target[a[i]] = 1; mark = "# [httpaas] " }
    /^[ \t]*#/ || /^[ \t]*$/ { print; next }
    $1 == "auto" || $1 ~ /^allow-/ {
      inblock = 0; kept = $1; drop = 0
      for (i = 2; i <= NF; i++) { if ($i in target) drop = 1; else kept = kept " " $i }
      if (!drop) { print; next }
      print mark $0
      if (kept != $1) print kept
      next
    }
    $1 == "iface" { inblock = ($2 in target); print (inblock ? mark : "") $0; next }
    $1 == "mapping" || $1 ~ /^source/ { inblock = 0; print; next }
    { print (inblock ? mark : "") $0 }
  ' "${file}.httpaas.bak" > "$file"
}

if [[ "$KEEP_DHCP" == "1" ]]; then
  log "KEEP_DHCP=1: se conserva el DHCP de ${INTERFACES_FILE}"
else
  disable_dhcp "$INTERFACES_FILE"
fi

log "plantilla lista. Apague la VM antes de aprovisionar: sudo poweroff"
log "para actualizar la plantilla más adelante: dhclient <iface> (la red no se levanta sola)"
log "usuario de Guest Additions: ${DEPLOY_USER} (sudo NOPASSWD)"
log "prueba de Apache: curl -sS http://127.0.0.1/ | head"
