# Escenario de pruebas — HTTPaaS

Este documento describe el entorno real en el que se prueba el módulo HTTPaaS,
cómo prepararlo y los casos de prueba con su resultado esperado. Sirve como
guía para las capturas de pantalla del entregable.

---

## 1. Entorno

### 1.1 Host

| Dato | Valor |
| --- | --- |
| Sistema operativo | Fedora (kernel 7.2) con `systemd-resolved` y NetworkManager |
| Interfaz a internet | `wlp47s0` — `192.168.1.12/24` (Wi-Fi, DHCP del router `192.168.1.1`) |
| Interfaz cableada | `enp46s0` — sin cable, no se usa |
| Red del laboratorio | `vboxnet0` (host-only) — `192.168.10.1/24`, sin DHCP |
| VMs registradas | `dns_server`, `template_server` (y `Mint`, ajena al laboratorio) |
| Orquestador | `appgo` en `127.0.0.1:8080` |

> **La IP `192.168.1.12` no participa en el laboratorio.** Es solo la salida a
> internet del host. Toda la plataforma vive en la red host-only
> `192.168.10.0/24`, que está aislada de la Wi-Fi. Las dos redes deben ser
> subredes distintas: si `vboxnet0` usara `192.168.1.12`, el host tendría dos
> interfaces en `192.168.1.0/24` y se rompería el acceso a internet.

### 1.2 Topología

```
                 Internet
                    │
             Router 192.168.1.1
                    │  Wi-Fi 192.168.1.0/24
┌───────────────────┴──────────────────────────────────────────────┐
│ HOST Fedora                                                      │
│   wlp47s0:  192.168.1.12  ← salida a internet (no participa)     │
│   vboxnet0: 192.168.10.1  ← gateway de la red del laboratorio    │
│                                                                  │
│   appgo 127.0.0.1:8080 ──VBoxManage──► VirtualBox                │
│        └──── nsupdate (TSIG) → 192.168.10.10:53 ─┐               │
│                                                  ▼               │
│   ══════════ host-only vboxnet0 192.168.10.0/24 (sin DHCP) ═════ │
│        │                    │                  │                 │
│   dns_server (ns)      paisajes           negocios               │
│   192.168.10.10/24     192.168.10.30/24   192.168.10.31/24       │
│   Bind9 cloud.local    Apache             Apache                 │
│                                                                  │
│   template_server: apagada; aporta el disco multiconexión        │
└──────────────────────────────────────────────────────────────────┘
```

**Sentido de las conexiones.** La aplicación Go es quien inicia todo el
tráfico: `VBoxManage` (local) crea y configura las VM; `VBoxManage guestcontrol`
entra a cada instancia por Guest Additions (no necesita red); `nsupdate` envía
las actualizaciones a Bind9 en `192.168.10.10:53`. Ninguna VM necesita
conectarse a la aplicación.

### 1.3 Direccionamiento

| Elemento | Adaptador | IP | Gateway | DNS |
| --- | --- | --- | --- | --- |
| Host (`vboxnet0`) | — | 192.168.10.1/24 | — | 192.168.10.10 solo para `cloud.local` |
| Host (`wlp47s0`) | — | 192.168.1.12/24 | 192.168.1.1 | router |
| `dns_server` (`ns`) | nic1 host-only | 192.168.10.10/24 | 192.168.10.1 | 127.0.0.1 |
| Instancias | nic1 host-only (la pone la app) | 192.168.10.30 – .99 | 192.168.10.1 | 192.168.10.10 |

Las instancias **no tienen internet** (el host no hace NAT para `vboxnet0`). No
hace falta: la plantilla ya trae Apache y `unzip`. Si `dns_server` necesita
instalar paquetes, añádale temporalmente una `nic2` en NAT.

---

## 2. Preparación

### 2.1 Permitir el rango en VirtualBox (una vez, con sudo)

Desde VirtualBox 6.1.28, en Linux solo se admiten redes host-only dentro de
`192.168.56.0/21`. Cualquier otro rango devuelve `E_ACCESSDENIED`:

```bash
sudo mkdir -p /etc/vbox
echo '* 192.168.10.0/24' | sudo tee -a /etc/vbox/networks.conf
```

### 2.2 Red host-only (con su usuario, sin sudo)

```bash
./scripts/01-host-network.sh            # HOST_IP=192.168.10.1 por omisión
VBoxManage list hostonlyifs             # vboxnet0 → IPAddress 192.168.10.1
```

`vboxnet0` aparece `DOWN` en `ip a` hasta que arranca una VM conectada a ella;
es normal.

### 2.3 Servidor DNS

1. Arranque `dns_server` y, desde su consola, asigne `192.168.10.10/24` con
   gateway `192.168.10.1` (ver `docs/httpaas.md`, Paso 1).
2. Ejecute dentro de la VM `sudo ./02-dns-server.sh` (zona `cloud.local`,
   `allow-update` con la llave `httpaas-key`).
3. Copie la llave al host:

```bash
sudo dnf install -y bind-utils           # nsupdate y dig en Fedora
scp root@192.168.10.10:/etc/bind/httpaas.key ~/httpaas.key
chmod 600 ~/httpaas.key
```

### 2.4 Resolución de `cloud.local` en el host

Para que el navegador abra `http://paisajes.cloud.local/` sin perder el DNS de
internet, se delega solo el dominio `cloud.local` a Bind9 (DNS dividido):

```bash
sudo ./scripts/06-host-dns.sh           # aplicar (persistente)
getent hosts prueba.cloud.local         # debe devolver 192.168.10.30
sudo ./scripts/06-host-dns.sh --undo    # revertir al terminar el laboratorio
```

El script resuelve dos obstáculos de Fedora:

1. **`systemd-resolved` no sabe a quién preguntar.** Asigna `192.168.10.10` a
   `vboxnet0` con el dominio de enrutamiento `~cloud.local`. Como NetworkManager
   no gestiona `vboxnet0` (`unmanaged`), lo deja en una unidad systemd ligada al
   dispositivo, que se vuelve a aplicar cada vez que aparece la interfaz.
2. **mDNS se queda con `.local`.** `authselect` (función `with-mdns4`) pone en
   `/etc/nsswitch.conf` `mdns4_minimal [NOTFOUND=return]` antes de `resolve`:
   todo nombre `*.local` se pregunta solo a Avahi y, si no responde, la búsqueda
   se corta. El script desactiva esa función (`--undo` la restaura). El sufijo
   `.local` está reservado para mDNS (RFC 6762); se mantiene porque lo pide el
   enunciado.

Firefox usa el resolutor del sistema mientras DNS sobre HTTPS esté desactivado
(valor por omisión). Si lo activa, añada `cloud.local` a
`network.trr.excluded-domains`.

### 2.5 Plantilla y aplicación

1. `template_server` con Apache, `unzip`, Guest Additions y el usuario
   `np2deploy` (`scripts/03-template-server.sh`). Debe quedar **apagada**.
2. Diagnóstico y arranque:

```bash
DNS_KEY=~/httpaas.key ./scripts/05-check-host.sh
NP2_HTTPAAS_PASSWORD='clave-np2deploy' ./appgo -mode local -listen 127.0.0.1:8080 \
  -httpaas -httpaas-dns-key ~/httpaas.key -timeout 20m
```

Se escucha en `127.0.0.1` a propósito. Con `-listen :8080` otros equipos de la
Wi-Fi podrían abrir la consola en `http://192.168.1.12:8080`, pero no los
enlaces `*.cloud.local`, porque no tienen ruta hacia `192.168.10.0/24`. Si se
expone así, use `-auth-user` y `-auth-pass`.

---

## 3. Casos de prueba

| # | Acción | Resultado esperado | Verificación |
| --- | --- | --- | --- |
| CP-01 | Conectividad base | El host llega al DNS | `ping -c2 192.168.10.10`; `dig @192.168.10.10 ns.cloud.local +short` → `192.168.10.10` |
| CP-02 | Actualización manual | La llave TSIG funciona | `DNS_KEY=~/httpaas.key ./scripts/04-nsupdate.sh add prueba 192.168.10.99` y después `delete prueba` |
| CP-03 | `paisajes` → **Aceptar** | VM `paisajes`, IP `.30`, página provisional, fila en «Instancias disponibles» | `VBoxManage list runningvms`; `dig @192.168.10.10 paisajes.cloud.local +short` → `192.168.10.30` |
| CP-04 | `archivos.zip` → **Publicar** | Contenido en `/var/www/paisajes` | Clic en el enlace (pestaña nueva); `curl -s http://paisajes.cloud.local/ \| head` |
| CP-05 | `negocios` + `pagina.zip` | IP `.31`, dos registros A | `dig @192.168.10.10 negocios.cloud.local +short` → `192.168.10.31` |
| CP-06 | Disco multiconexión | VM con nombre del host y disco diferencial hijo | `VBoxManage showvminfo negocios --machinereadable \| grep SATA-0-0` → `httpaas/disks/negocios.vdi` |
| CP-07 | Dentro de la instancia | Hostname e IP correctos | Consola de la VM: `hostname` → `paisajes`; `ip -4 a` → `192.168.10.30/24` |
| CP-08 | Volver a publicar | Se reemplaza el contenido anterior | Publicar otro zip en `paisajes` y recargar |
| CP-09 | Validaciones | Se rechaza sin ejecutar nada | Host `Mal_Nombre` o `ns`; zip con `../x`; Publicar sin archivo; Publicar en un host inexistente |
| CP-10 | Duplicado | «ya existe una instancia» | Aceptar `paisajes` por segunda vez |
| CP-11 | **Eliminar** `negocios` | VM, disco y registro A eliminados; la fila desaparece | `dig … negocios.cloud.local +short` vacío; `ls httpaas/disks/` |
| CP-12 | Reutilizar la IP | Un host nuevo recibe la `.31` liberada | Aceptar `tienda` → `192.168.10.31` |
| CP-13 | Plantilla encendida | Falla antes de crear nada | Arrancar `template_server` y pulsar Aceptar → «debe estar apagada» |
| CP-14 | Persistencia | El inventario sobrevive a un reinicio | Reiniciar `appgo`; la tabla se mantiene (`httpaas/instances.json`) |

### Secuencia de capturas para el entregable

Siga el orden de las imágenes del enunciado:

1. Formulario vacío y zona `cloud.local` sin registros de instancias.
2. `paisajes` + **Aceptar** → VM creada y registro A en Bind9.
3. `archivos.zip` + **Publicar** → tabla con una instancia.
4. `negocios` + `pagina.zip` → tabla con dos instancias y dos registros A.
5. Ambas páginas abiertas en pestañas nuevas desde los enlaces.
6. **Eliminar** una instancia y mostrar la zona y VirtualBox actualizados.

---

## 4. Problemas encontrados y solución

| Síntoma | Causa | Solución |
| --- | --- | --- |
| `01-host-network.sh` intentaba configurar `vboxnet0` con `192.168.1.12` | Valor por omisión de `HOST_IP` igual a la IP de la Wi-Fi | El script usa `192.168.10.1` y rechaza cualquier subred que se solape con otra interfaz del host |
| `E_ACCESSDENIED` en `EnableStaticIPConfig` | El rango no estaba declarado en `/etc/vbox/networks.conf` | Añadir `* 192.168.10.0/24` (sección 2.1); el script lo detecta y muestra el comando |
| Con `sudo`, el script no veía `dns_server` | VirtualBox registra las VM por usuario; root tiene otro registro | El script se niega a correr como root y falla si la VM no existe |
| El enlace no abre, pero `dig @192.168.10.10` responde | El host no consulta a Bind9 para `cloud.local` | DNS dividido (sección 2.4) y revisar DNS sobre HTTPS en el navegador |
| `nsupdate`: `tsig verify failure` / `REFUSED` | Llave distinta o zona sin `allow-update` | Copiar de nuevo `httpaas.key` y revisar `named.conf.local` |
| Tiempo de espera de Guest Additions | Guest Additions inactivas en la plantilla o contraseña incorrecta | Revisar `VBoxService` en la plantilla y `NP2_HTTPAAS_PASSWORD` |
| «no se pudo crear el disco diferencial» al pulsar Aceptar | (1) El módulo usaba `createmedium diff`, sintaxis que VirtualBox 7.2 rechaza; (2) tomaba como padre el hijo automático de la plantilla (`Snapshots/{uuid}.vdi`) en lugar de la raíz; (3) el archivo base `template_server.vdi` no existía (`VERR_FILE_NOT_FOUND`, estado `inaccessible`) | Se usa `createmedium disk --diffparent`, se sube por la cadena hasta la raíz `multiattach` y se valida que sea accesible antes de crear nada. La plantilla se recrea según `docs/httpaas.md`, «Recrear la plantilla» |
| La página abre por IP pero no por `prueba.cloud.local` (`dig @192.168.10.10` sí responde) | `systemd-resolved` sin DNS para `vboxnet0` y `mdns4_minimal [NOTFOUND=return]` cortando los nombres `*.local` antes de llegar a DNS | `sudo ./scripts/06-host-dns.sh` (DNS dividido persistente y desactiva `with-mdns4` de authselect) |
| La instancia responde unos minutos y luego deja de contestar por IP y por nombre (ARP `INCOMPLETE`; Guest Additions informa `Net/Count = 0`) | La plantilla se instaló con escritorio (GNOME) y trae NetworkManager, que seguía haciendo DHCP en `enp0s3`. En la red host-only no hay DHCP y su ciclo de reintentos borraba la IP estática | El módulo detecta NetworkManager y deja la interfaz como no administrada (`/etc/NetworkManager/conf.d/99-httpaas.conf` + `nmcli device set … managed no`) antes de asignar la IP. Lo ideal es una plantilla CLI, como pide el enunciado |
| La página muestra un listado de carpetas o un 404 aunque el zip tiene `index.html` | El zip se creó comprimiendo la carpeta: todo queda dentro de `prueba/` | El módulo quita esa carpeta envolvente antes de desplegar y lo indica en el informe. El vhost bloquea `.git` y los archivos ocultos |
