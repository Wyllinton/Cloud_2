# Módulo HTTPaaS — servidores Apache bajo demanda

Este documento describe la integración del servicio **HTTP como servicio**
(HTTPaaS) en la consola `appgo`. El módulo recibe un nombre de host y un `.zip`,
crea una máquina virtual a partir de la plantilla `template_server`, le asigna
una IPv4 única, la registra en Bind9, despliega el contenido en Apache y publica
el enlace en el tablero. También permite apagar y eliminar la instancia junto con
su registro DNS y su disco diferencial.

La implementación sigue los mismos principios del resto de la consola:

- **No interactiva:** `VBoxManage`, `nsupdate`, `apt-get -y` y `sudo -n`.
- **Sin shell para datos del usuario:** todas las operaciones se ejecutan con un
  `argv` explícito. `nsupdate` recibe su protocolo por `stdin`, no una línea de
  shell.
- **Validación previa:** hostname RFC 1123, IPv4 literal, rutas absolutas y
  contenido del `.zip` (sin `..`, rutas absolutas, enlaces simbólicos ni tipos
  especiales).
- **Diagnóstico completo:** cada paso aparece en el informe con comando, salida,
  código, transporte y duración.

---

## 1. Arquitectura del laboratorio

```
┌──────────────────────────────────────────────────────────────────────┐
│ Host Debian (donde corre appgo)                                      │
│                                                                      │
│  appgo :8080 ── VBoxManage ── VirtualBox                             │
│      │             │                                                 │
│      │             ├── dns_server (ns) 192.168.10.10  Bind9          │
│      │             └── <host>          192.168.10.30+ Apache         │
│      │                                                               │
│      └── nsupdate -k /etc/bind/httpaas.key ──► 192.168.10.10:53     │
│                                                                      │
│  red host-only vboxnet0: 192.168.10.1/24 (sin DHCP)                  │
└──────────────────────────────────────────────────────────────────────┘
                              ▲
                              │ http://paisajes.cloud.local/
                        navegador del usuario
```

Componentes:

| Componente | Valor por omisión | Función |
| --- | --- | --- |
| Plantilla | `template_server` | Debian 13 + Apache + Guest Additions. Su disco base es `multiattach`; cada instancia usa un disco **diferencial** hijo. |
| DNS | `dns_server` / `192.168.10.10` | Bind9 con la zona `cloud.local` y `allow-update` con llave TSIG. |
| Red | `vboxnet0` / `192.168.10.0/24` | Red host-only sin DHCP. La primera IP de instancia es `192.168.10.30`. |
| Estado | `./httpaas/instances.json` | Inventario con hostname, IP, VM, disco, URL y estado. |
| Discos | `./httpaas/disks/` | Un `.vdi` diferencial por instancia. |

> **Por qué discos diferenciales:** VirtualBox permite que el disco base de la
> plantilla sea `multiattach`, pero varias VM no deben escribir simultáneamente
> en el mismo medio. El módulo crea un disco hijo
> (`createmedium disk --diffparent <base multiconexión>`) por instancia: los datos comunes se comparten y cada Apache escribe solo en su
> diferencial. Al eliminar la instancia se borra el hijo, nunca el disco base.
>
> Mientras existan instancias, no modifique ni elimine la cadena de discos de la
> plantilla (incluidas sus instantáneas): los hijos dependen de ella. Para
> actualizar la plantilla, elimine primero las instancias o cree una plantilla
> nueva.

---

## 2. Requisitos previos

### 2.1 Host de VirtualBox

- VirtualBox 7.x con `VBoxManage` en `PATH`.
- Go 1.23+ para compilar.
- `nsupdate` y `dig` (`bind9-dnsutils`).
- La llave TSIG `httpaas.key` copiada desde `dns_server` con permisos `0600`.
- Acceso de red del host a `192.168.10.0/24` (red host-only o puente).

```bash
sudo apt-get install -y bind9-dnsutils
```

### 2.2 Plantilla `template_server`

- Debian 13 CLI con Apache, `unzip`, `sudo` y **Guest Additions** activas.
- Usuario de despliegue (por omisión `np2deploy`) con contraseña y sudo sin
  contraseña. El módulo usa `VBoxManage guestcontrol`, no SSH, para configurar la
  instancia recién creada: así no necesita que la VM tenga IP antes de asignarla.
- La plantilla debe estar **apagada** cuando se aprovisiona: VirtualBox no debe
  escribir en el disco base mientras se crea un hijo.
- El disco base debe ser `multiattach` (en el laboratorio ya lo es). El módulo
  detecta el medio conectado al controlador `SATA` con
  `VBoxManage showvminfo <plantilla> --machinereadable`.

### 2.3 Servidor DNS `dns_server`

- Bind9 administrando `cloud.local`.
- `allow-update { key "httpaas-key"; };`.
- La IP `192.168.10.10` accesible desde el host.

---

## 3. Puesta en marcha paso a paso

Los scripts están en `scripts/` y son idempotentes en lo posible. Use `sudo`
donde se indica; ningún script pide datos por teclado.

### Paso 1 — Red host-only

En el **host**, con `dns_server` apagada:

```bash
# una sola vez: VirtualBox solo admite 192.168.56.0/21 si no se declara el rango
sudo mkdir -p /etc/vbox
echo '* 192.168.10.0/24' | sudo tee -a /etc/vbox/networks.conf

# con su usuario, SIN sudo (las VM están registradas por usuario)
VBOX_NET=vboxnet0 HOST_IP=192.168.10.1 ./scripts/01-host-network.sh
```

`HOST_IP` es la IP del host **dentro de la red del laboratorio**, no la de su
Wi-Fi/LAN: el script se niega a usar una subred que se solape con otra
interfaz del host.

El script:

1. crea `vboxnet0` si no existe;
2. le asigna `192.168.10.1/24`;
3. deshabilita el DHCP (las IP se asignan estáticamente);
4. conecta `dns_server` a esa red por `nic1`.

La plantilla **no se modifica**: las instancias reciben su propia `nic1` al
crearse y así `template_server` puede seguir usando NAT para instalar paquetes.
Si prefiere conectarla también, use `ATTACH_TEMPLATE=1`.

Compruebe:

```bash
VBoxManage list hostonlyifs
VBoxManage showvminfo dns_server --machinereadable | grep '^nic1='
VBoxManage showvminfo template_server --machinereadable | grep 'SATA-0-0'
```

Antes de instalar Bind9, asigne dentro de `dns_server` la IP estática
`192.168.10.10/24`. Desde la consola de la VM (sustituya `enp0s3` por la
interfaz real que muestre `ip -o link show`):

```bash
cat >/etc/network/interfaces.d/10-ns <<'EOF'
auto enp0s3
iface enp0s3 inet static
    address 192.168.10.10
    netmask 255.255.255.0
    gateway 192.168.10.1
    dns-nameservers 127.0.0.1
EOF
ifup enp0s3          # o: systemctl restart networking
ip -4 addr show enp0s3
```

> `template_server` no necesita IP ni pertenecer a `vboxnet0`: las instancias
> reciben la suya por Guest Additions. Mantener la plantilla en NAT permite
> actualizarla con `apt` sin tocar el orquestador.

### Paso 2 — Bind9 y zona `cloud.local`

Dentro de `dns_server`, como root:

```bash
sudo ZONE=cloud.local NS_IP=192.168.10.10 \
  ./scripts/02-dns-server.sh
```

El script instala `bind9`, genera `/etc/bind/httpaas.key` (TSIG
`hmac-sha256`), crea `/var/lib/bind/db.cloud.local` (directorio escribible por
`bind` y permitido por AppArmor para el *journal* de las actualizaciones
dinámicas), declara la zona con `allow-update` y reinicia `named`.

Copie la llave al host:

```bash
# desde el host
sudo scp root@192.168.10.10:/etc/bind/httpaas.key /etc/bind/httpaas.key
sudo chown "$USER" /etc/bind/httpaas.key
chmod 600 /etc/bind/httpaas.key
```

Prueba manual (desde el host):

```bash
DNS_SERVER=192.168.10.10 DNS_ZONE=cloud.local \
DNS_KEY=/etc/bind/httpaas.key ./scripts/04-nsupdate.sh add demo 192.168.10.30
dig @192.168.10.10 demo.cloud.local A +short
./scripts/04-nsupdate.sh delete demo
```

### Paso 3 — Preparar la plantilla

Dentro de `template_server`, como root:

```bash
sudo NP2_HTTPAAS_PASSWORD='cambie-esta-clave' ./scripts/03-template-server.sh
```

El script instala Apache/unzip, crea `np2deploy`, le da sudo sin contraseña,
habilita `interfaces.d` y deja Apache habilitado. En plantillas CLI (ifupdown)
también comenta el bloque DHCP que deja el instalador en
`/etc/network/interfaces` (respaldo en `interfaces.httpaas.bak`): las
instancias viven en `vboxnet0`, que no tiene DHCP, y con ese bloque activo cada
arranque se queda esperando una dirección. La sesión actual conserva su IP;
para actualizar la plantilla más adelante ejecute `dhclient enp0s3`. Use
`KEEP_DHCP=1` para omitir este paso. Después **apague la plantilla**:

```bash
sudo poweroff
```

#### Convertir el disco a multiconexión

Hágalo **al terminar** de configurar la plantilla. VirtualBox no permite cambiar
el tipo de un disco conectado, así que primero se desconecta (en el host, con su
usuario y la plantilla apagada):

```bash
DISK="$HOME/VirtualBox VMs/template_server/template_server.vdi"
VBoxManage storageattach template_server --storagectl SATA --port 0 --device 0 --medium none
VBoxManage modifymedium disk "$DISK" --type multiattach
VBoxManage showmediuminfo disk "$DISK" | grep -E 'Type|State'   # multiattach / created
```

Si vuelve a conectar el disco a `template_server`, VirtualBox le crea un hijo
automático (`Snapshots/{uuid}.vdi`) y todo lo que cambie después en la plantilla
queda en ese hijo: **las instancias no lo heredan**. El módulo detecta ese
caso, usa la raíz multiconexión y lo advierte en el informe. Si deja la
plantilla sin disco, indique la ruta con `-httpaas-template-disk "$DISK"`.

#### Recrear la plantilla si el disco base falta

Síntoma: «el disco multiconexión … está registrado en VirtualBox pero no es
accesible» o `VERR_FILE_NOT_FOUND`. El `.vdi` base se movió o se borró fuera de
VirtualBox y los hijos ya no sirven: sin el archivo base no se puede recuperar.
Si tiene una copia, colóquela en la misma ruta y listo. Si no:

```bash
VBoxManage showvminfo template_server --machinereadable | grep SATA   # hijo conectado
VBoxManage list hdds                                                   # UUID de la base inaccesible

# 1. Limpiar las referencias rotas (plantilla apagada)
VBoxManage storageattach template_server --storagectl SATA --port 0 --device 0 --medium none
VBoxManage closemedium disk <uuid-del-hijo> --delete
VBoxManage closemedium disk <uuid-de-la-base>        # inaccesible: solo se desregistra

# 2. Disco nuevo e instalación de Debian 13 CLI
VBoxManage createmedium disk --filename "$DISK" --size 20480 --format VDI
VBoxManage storageattach template_server --storagectl SATA --port 0 --device 0 --type hdd --medium "$DISK"
```

Instale Debian (ISO o `VBoxManage unattended install`), las Guest Additions y
ejecute `03-template-server.sh`; apague la VM y conviértala a multiconexión como
se indica arriba.

### Paso 4 — Verificar el host

```bash
./scripts/05-check-host.sh
```

Debe mostrar `VBoxManage`, `nsupdate`, la llave `0600`, la plantilla apagada y
`vboxnet0`.

### Paso 5 — Arrancar la consola

```bash
export NP2_HTTPAAS_PASSWORD='cambie-esta-clave'
go build -o appgo .
./appgo -listen :8080 -mode local -httpaas \
  -httpaas-template template_server \
  -httpaas-network-mode hostonly -httpaas-network vboxnet0 \
  -httpaas-domain cloud.local \
  -httpaas-dns-server 192.168.10.10 -httpaas-dns-zone cloud.local \
  -httpaas-dns-key /etc/bind/httpaas.key \
  -httpaas-guest-user np2deploy \
  -auth-user admin -auth-pass 'otra-clave'
```

> El módulo HTTPaaS corre en el **host** y no depende del transporte `-mode` de
> la consola. `-mode local` solo describe cómo la consola administra la VM
> "principal"; HTTPaaS usa su propio runner local para `VBoxManage` y un
> `guestcontrol` por instancia. Si la plantilla aún no tiene Apache/unzip y el
> aprovisionamiento debe ejecutar `apt-get`, aumente el tiempo máximo de la
> operación con `-timeout 20m`.

Abra `http://127.0.0.1:8080/httpaas`. El panel **Gestión de páginas y
aplicaciones web** sigue el flujo del enunciado en dos pasos:

1. Escriba `paisajes` en **Nombre del host** y pulse **Aceptar**. Se crea la VM,
   se le asigna IP y hostname, se configura Apache con una página provisional y
   se registra el host en Bind9. La instancia aparece en **Instancias
   disponibles**.
2. Con el mismo nombre de host en el campo, seleccione el `.zip` en **Contenido
   web (comprimido)** (con `index.html` en la raíz) y pulse **Publicar**. El zip
   se envía a la VM y se extrae en su directorio de Apache. Publicar otra vez
   reemplaza el contenido.

Si adjunta el `.zip` antes de pulsar **Aceptar**, ambos pasos se hacen en una
sola operación.

El resultado esperado:

- VM `paisajes` (el nombre de la VM es el hostname; `-httpaas-vm-prefix` añade
  un prefijo si lo prefiere);
- IP `192.168.10.30`;
- A `paisajes.cloud.local. 300 IN A 192.168.10.30`;
- vhost `/etc/apache2/sites-available/paisajes.conf`;
- document root `/var/www/paisajes`;
- enlace `http://paisajes.cloud.local/` en el tablero.

---

## 4. Flujo interno de una solicitud

```
POST /httpaas/crear  («Aceptar»: hostname; el .zip es opcional)
  │
  ├─ valida hostname (RFC 1123, reservados ns/template)
  ├─ valida zip en memoria (rutas, symlinks, tipos, tamaño)
  ├─ bloquea el inventario (mutex) y reserva la primera IP libre
  ├─ preflight: VBoxManage, plantilla poweroff, disco SATA, nsupdate, llave TSIG
  ├─ showmediuminfo: sube por la cadena de discos hasta la raíz multiattach
  ├─ createmedium disk --diffparent <base multiattach> --filename <disco hijo>
  ├─ createvm / modifyvm / storagectl SATA / storageattach
  ├─ startvm --type headless
  ├─ espera Guest Additions (sondeo con contexto y timeout)
  ├─ hostnamectl + /etc/network/interfaces.d/99-httpaas + ip addr/route
  ├─ /etc/hosts y /etc/resolv.conf
  ├─ apache2 + vhost + a2ensite + apache2ctl configtest + reload
  ├─ con zip: copyto + unzip + chown www-data · sin zip: index.html provisional
  ├─ nsupdate: delete A + add A (TSIG)
  └─ guarda httpaas/instances.json (0600, escritura atómica)

POST /httpaas/publicar  («Publicar»: hostname + archivo .zip)
  │
  ├─ valida hostname y zip en memoria
  ├─ exige que la instancia exista en el inventario y sea una VM del módulo
  ├─ enciende la VM si está apagada y espera Guest Additions
  ├─ limpia el document root, copyto del zip, unzip, chown www-data
  └─ guarda el nombre del zip y la fecha de publicación en el inventario
```

Si falla cualquier paso después de crear hardware, un `defer` ejecuta el
**rollback** con un contexto propio: elimina el registro DNS si ya se había
creado, apaga y desregistra la VM y borra el disco diferencial. Los pasos de
rollback también quedan en el informe.

La eliminación sigue el orden inverso:

```
Delete(hostname)
  ├─ comprueba que la VM no sea template_server ni dns_server
  ├─ apaga la VM (poweroff si está running/paused)
  ├─ elimina el A con nsupdate
  ├─ unregistervm --delete
  ├─ borra el disco hijo si quedó en disco
  └─ elimina la entrada del inventario
```

---

## 5. Banderas del módulo

| Bandera | Valor por omisión | Descripción |
| --- | --- | --- |
| `-httpaas` | `true` | Habilita el módulo. Use `-httpaas=false` para ocultarlo. |
| `-httpaas-vboxmanage` | `VBoxManage` | Ejecutable de VirtualBox. |
| `-httpaas-host-sudo` | `false` | Ejecuta `VBoxManage`/`nsupdate` con `sudo -n`. |
| `-httpaas-template` | `template_server` | VM plantilla. |
| `-httpaas-template-disk` | vacío | Disco base; se autodetecta del controlador SATA. |
| `-httpaas-vm-prefix` | vacío | Prefijo del nombre de la VM; vacío = la VM se llama como el host. |
| `-httpaas-disk-dir` | `./httpaas/disks` | Discos diferenciales. |
| `-httpaas-network-mode` | `hostonly` | `hostonly` o `bridged`. |
| `-httpaas-network` | `vboxnet0` | Red host-only o adaptador puente. |
| `-httpaas-netmask` | `24` | Prefijo de red. |
| `-httpaas-gateway` | `192.168.10.1` | Puerta de enlace. |
| `-httpaas-ip-start` | `192.168.10.30` | Primera IP asignable. |
| `-httpaas-ip-end` | `192.168.10.99` | Última IP asignable. |
| `-httpaas-domain` | `cloud.local` | Dominio de los enlaces. |
| `-httpaas-dns-server` | `192.168.10.10` | Servidor Bind9. |
| `-httpaas-dns-zone` | `cloud.local` | Zona que se actualiza. |
| `-httpaas-dns-key` | `/etc/bind/httpaas.key` | Llave TSIG. Vacío = sin TSIG (solo laboratorio). |
| `-httpaas-dns-ttl` | `300` | TTL de los A. |
| `-httpaas-guest-user` | `np2deploy` | Usuario del invitado para Guest Additions. |
| `-httpaas-guest-password` | vacío | Contraseña; prefiera `NP2_HTTPAAS_PASSWORD`. |
| `-httpaas-guest-sudo` | `true` | `sudo -n` dentro del invitado. |
| `-httpaas-guest-iface` | vacío | Interfaz de red; se autodetecta. |
| `-httpaas-apache-root` | `/var/www` | Padre de los document roots. |
| `-httpaas-http-port` | `80` | Puerto de los enlaces. |
| `-httpaas-memory` | `1024` | RAM por instancia (MiB). |
| `-httpaas-cpus` | `2` | vCPUs por instancia. |
| `-httpaas-boot-timeout` | `4m` | Espera máxima de Guest Additions. |
| `-httpaas-max-upload` | `64` | Tamaño máximo del `.zip` (MiB). |
| `-httpaas-state` | `./httpaas/instances.json` | Inventario. |

También se puede usar el objetivo `make run-httpaas` (revise las variables al
inicio del `Makefile`).

---

## 6. Seguridad

- **Hostname:** `validate.Hostname` aplica RFC 1123; se rechazan `ns`, `template`
  y nombres reservados. El FQDN se construye con el dominio validado.
- **IPv4:** `validate.IPv4` exige una dirección literal; no se aceptan puertos ni
  nombres en ese campo.
- **Zip:** se inspecciona la tabla central antes de transferir. Se rechazan:
  rutas absolutas, `..`, separadores `\`, NUL, enlaces simbólicos, dispositivos,
  más de 5.000 entradas y más de 512 MiB descomprimidos (o 6× el límite de subida
  si es mayor). El document root se limpia antes de extraer.
- **Comandos:** `VBoxManage` recibe un `argv`; `nsupdate` recibe el protocolo por
  `stdin`; `ip`, `install`, `unzip`, `chown`, etc. nunca pasan por un shell con
  datos del usuario.
- **Inventario:** JSON `0600`, escritura atómica (`CreateTemp` + `Rename`) y
  mutex que serializa reserva de IP, creación y eliminación.
- **Protección de infraestructura:** una instancia no puede tener como VM
  `template_server` ni `dns_server`, y el disco base de la plantilla está
  protegido contra borrado.
- **TSIG:** la llave no se pasa por argumentos ni se imprime; `nsupdate -k` la lee
  del archivo `0600`. No use `-y` en la línea de comandos.
- **Consola:** proteja `appgo` con `-auth-user/-auth-pass` o un proxy inverso. El
  módulo otorga control total sobre VirtualBox y Bind9.

---

## 7. Diagnóstico

| Síntoma | Causa probable | Acción |
| --- | --- | --- |
| `la plantilla debe estar apagada` | La VM está encendida. | `VBoxManage controlvm template_server poweroff` o apáguela desde el invitado. |
| `no se encontró el disco de la plantilla` | No hay medio SATA o el nombre cambió. | `VBoxManage showvminfo template_server --machinereadable \| grep SATA`; use `-httpaas-template-disk`. |
| `nsupdate no está instalado` | Falta `bind9-dnsutils`. | `sudo apt-get install -y bind9-dnsutils`. |
| `update failed: REFUSED` | La zona no permite la llave o la IP de origen. | Revise `allow-update` y `/etc/bind/httpaas.key` en `dns_server`. |
| `Guest Additions no respondió` | La plantilla no tiene Guest Additions o el login es incorrecto. | Verifique `VBoxControl --version`, usuario/contraseña y `sudo -n`. |
| Apache muestra el listado o 404 | El `index.html` no está en la raíz del zip. | Vuelva a comprimir el contenido, no la carpeta contenedora. |
| El navegador no resuelve `*.cloud.local` | El cliente no usa `192.168.10.10` como DNS. | Configure el resolutor o use el enlace “probar por IP”. |
| `HTTP 413` al subir | El zip supera `-httpaas-max-upload`. | Aumente el límite o reduzca el archivo. |
| La eliminación falla en DNS | El registro ya no existe o Bind9 no responde. | `nsupdate` es idempotente; revise conectividad y reintente. |

Los informes de la interfaz incluyen el comando exacto y `stdout`/`stderr` de
cada paso, incluido el rollback.

---

## 8. Pruebas automáticas

```bash
go test ./...
go vet ./...
```

Las pruebas de `internal/modules/httpaas_test.go` cubren:

- aceptación de un zip normal y rechazo de traversal, rutas absolutas,
  backslashes, enlaces simbólicos y exceso de entradas;
- generación exacta del protocolo `nsupdate` (add/delete/FQDN/zone/TTL);
- asignación de la primera IP libre y rangos agotados/invertidos;
- contenido de `/etc/network/interfaces.d`, vhost de Apache y `/etc/hosts`;
- detección de interfaz, disco SATA y estado de VirtualBox.

---

## 9. Limitaciones y trabajo futuro

- El despliegue usa Guest Additions porque la VM aún no tiene IP; una variante
  con SSH por llave reduciría el acoplamiento a VirtualBox pero requiere que la
  plantilla arranque con una IP conocida.
- La IP se aplica en caliente con `ip` y se persiste en
  `/etc/network/interfaces.d` (ifupdown). Si la plantilla usa NetworkManager o
  systemd-networkd, adáptese la generación de esa configuración para conservar
  la IP tras un reinicio.
- La eliminación es síncrona y puede tardar si la VM no responde a `poweroff`.
  Se puede añadir un `acpipoweroff` con espera y luego `poweroff` forzado.
- No hay cuotas por usuario ni TLS. Para producción harían falta autenticación
  por usuario, HTTPS, límites de CPU/disco y una cola de trabajos.
- El DNS es local (`cloud.local`); para un dominio público habría que integrar
  un proveedor con API y validar la propagación.
- Se puede añadir un botón “redesplegar contenido” que reutilice la VM existente
  sin recrear hardware.
