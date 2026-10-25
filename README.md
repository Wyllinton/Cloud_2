# NP2 · Consola de automatización para Debian 13

Aplicación web escrita en Go que automatiza tareas de administración sobre una
máquina virtual con **Debian 13.7.0**. Todas las operaciones son **no
interactivas**, validan las entradas antes de ejecutarse y reportan al usuario si
la tarea terminó bien, junto con la información técnica necesaria para
diagnosticar un fallo.

## Módulos

| Módulo | Rutas | Descripción |
| --- | --- | --- |
| Aplicaciones (apt) | `/sistema/paquetes` | Instala (`apt-get install -y`) y elimina/purga paquetes. |
| Llave SSH de root | `/sistema/ssh-root` | Genera el par de llaves, instala la pública y deshabilita el login por contraseña. Permite descargar la llave privada. |
| Mensaje de inicio | `/sistema/issue` | Personaliza `/etc/issue` (host, fecha, hora, IPv4, kernel, SO, terminal) y un mensaje propio, con vista previa. |
| Nombre de host | `/sistema/hostname` | Ejecuta `hostnamectl set-hostname` y sincroniza `/etc/hosts`. |
| Usuarios | `/usuarios` | Crea usuarios (con sudo y/o llave SSH) y los elimina (opcionalmente con sus archivos). |
| Servicios systemd | `/servicios` | Transfiere archivos, genera la unidad y controla `start/stop/restart/status/enable/disable`. |
| Balanceador HAProxy | `/haproxy` | Agrega y elimina servidores del backend y recarga el balanceador. |

## Requisitos

- Go 1.23 o superior para compilar.
- Para administrar la máquina virtual: Debian 13 con `apt`, `systemd`, `openssh-server` y, para el balanceador, `haproxy`.
- Para el transporte Guest Additions: VirtualBox en el host con `VBoxManage`
  disponible y las Guest Additions instaladas y activas en la VM.
- Los comandos que modifican el sistema requieren privilegios. La consola usa
  `sudo -n` (no interactivo) cuando se ejecuta con `-sudo`, o bien se conecta
  como `root` por SSH.

## Compilación

```bash
go build -o appgo .
# o
make build
```

## Ejecución

La consola se conecta con la máquina virtual mediante un **transporte**
intercambiable. Es el punto clave del enunciado: primero se usa el canal de las
*Guest Additions* y, cuando existe SSH por llave, la comunicación es
exclusivamente SSH; si SSH no está disponible, la consola vuelve por sí sola al
canal de las *Guest Additions*.

### Modo `auto` (recomendado): SSH con respaldo Guest Additions

La consola se ejecuta en el **host** y controla la VM. Intenta primero SSH y, si
la conexión no se establece (o se cae), continúa automáticamente mediante
`VBoxManage guestcontrol`:

```bash
./appgo -listen :8080 -mode auto \
  -ssh-host 192.168.1.10 -ssh-user root -ssh-key ./root_id_ed25519 \
  -guestcontrol-vm VM_A -guestcontrol-user root -guestcontrol-password '***'
```

Si solo se configura `--guestcontrol-vm`, el modo `auto` usa Guest Additions
directamente. La contraseña puede pasarse con la variable `NP2_GUEST_PASSWORD`
para no dejarla en el historial del shell.

### Modo `guestcontrol`: solo Guest Additions

Útil en la etapa inicial, antes de que exista SSH:

```bash
./appgo -listen :8080 -mode guestcontrol \
  -guestcontrol-vm VM_A -guestcontrol-user root -guestcontrol-password '***'
```

La VM debe estar encendida y con las Guest Additions activas. El usuario
indicado debe existir en el sistema invitado (normalmente `root`); si no es
`root`, añada `-sudo`.

### Modo `auto`:

La app comprueba ssh, si no funciona, sigue con guestcontrol hasta que se generan
las llaves SSH

```bash
# modo auto
./appgo -listen :8080 -mode auto \
  -ssh-host IP_VM \
  -ssh-user root \
   -guestcontrol-vm NOMBRE_VM \
   -guestcontrol-user root \
  -guestcontrol-password '****' \

  -ssh-insecure-ignore-host-key
```

Use el módulo *Llave SSH de root* y **descargue la llave privada**.

### Modo `ssh`: control remoto solo con llave

A partir de ahí la consola controla el invitado de forma remota:

```bash
./appgo -listen :8080 -mode ssh \
  -ssh-host 192.168.1.10 \
  -ssh-user root \
  -ssh-key ./root_id_ed25519 \
  -ssh-known-hosts ~/.ssh/known_hosts
```

Si el usuario SSH no es `root`, añada `-sudo` (el usuario debe tener una regla
`NOPASSWD` en `/etc/sudoers`).

### Opciones

```
-listen string                 dirección de escucha HTTP (por defecto ":8080")
-mode string                   transporte: auto, guestcontrol, ssh o local (por defecto "auto")
-sudo                          prefijar cada comando con sudo -n
-timeout duration              tiempo máximo por operación (por defecto 10m)
-ssh-host, -ssh-port, -ssh-user, -ssh-key string
-ssh-key-passphrase string     frase de paso de la llave privada
-ssh-known-hosts string        verificación de la llave del host remoto
-ssh-insecure-ignore-host-key  desactivar la verificación (solo laboratorio)
-guestcontrol-vm string        nombre o UUID de la VM de VirtualBox
-guestcontrol-user string      usuario existente en el invitado (por defecto root)
-guestcontrol-password string  contraseña del invitado (o NP2_GUEST_PASSWORD)
-guestcontrol-password-file string  valor opcional de --passwordfile de VBoxManage
-guestcontrol-domain string    dominio del usuario (invitados Windows)
-guestcontrol-vboxmanage string     ruta del ejecutable VBoxManage
-haproxy-config string         ruta de haproxy.cfg (por defecto /etc/haproxy/haproxy.cfg)
-haproxy-service string        servicio a recargar (por defecto haproxy)
-auth-user, -auth-pass string  protege la consola con HTTP Basic
-version                       muestra la versión
```

> Se recomienda proteger la consola con `-auth-user/-auth-pass` (o un proxy
> inverso) porque otorga privilegios administrativos sobre la máquina virtual.

## Seguridad

- **Nunca se invoca un shell con datos del usuario sin citarlos.** Cada comando
  se construye como un *argv* explícito (`Runner`). En los transportes SSH y
  Guest Additions cada argumento se cita con comillas simples POSIX antes de
  enviarlo; el canal Guest Additions usa además un envoltorio fijo `/bin/sh`
  para recuperar el código de salida, sin interpolar datos del usuario en el
  script.
- **Validación estricta** de todos los valores (`internal/validate`): nombres de
  paquete, usuarios, host, servicios, direcciones, puertos, rutas, nombres de
  archivo, variables de entorno, etc. Se rechazan caracteres de shell.
- Los archivos de configuración (`/etc/issue`, `/etc/hosts`, unidades de systemd,
  `haproxy.cfg`) se escriben de forma dirigida; los textos libres no admiten
  saltos de línea donde podrían inyectar directivas.
- El borrado de usuarios está protegido: las cuentas del sistema (`uid < 1000`) y
  una lista de nombres reservados no se pueden eliminar.
- El transporte SSH verifica la llave del host mediante `known_hosts` salvo que
  se habilite explícitamente lo contrario.
- El transporte Guest Additions no acepta entrada estándar: la lectura y
  escritura de archivos usa `VBoxManage copyfrom`/`copyto` con un destino
  explícito, nunca un shell. La contraseña del
  invitado se pasa a `VBoxManage` (queda visible en la lista de procesos del
  host mientras se ejecuta); prefiera la variable `NP2_GUEST_PASSWORD` y un
  usuario con privilegios mínimos. Nunca se registra en los informes: los
  comandos se muestran con la contraseña redactada.

## Servicios systemd

Al desplegar una aplicación, el archivo referenciado por `ExecStart` se marca
ejecutable (0755) aunque no se marque la casilla correspondiente. Antes de
instalar la unidad se comprueba que el comando exista y que la unidad pase
`systemd-analyze verify`; la validación se hace sobre una copia en `/tmp` para no
reemplazar una unidad que ya funcionaba. Después de `start`/`restart` se espera
un breve intervalo y se comprueba el estado: si el servicio no queda activo, el
informe incluye `systemctl status` y `journalctl` para diagnosticar el fallo.

## Resultado de las operaciones

Cada operación devuelve un informe con:

- estado global (correcta / con errores) y resumen;
- notas y advertencias;
- **información técnica**: comando exacto, código de salida, `stdout` y `stderr`
  de cada paso ejecutado, junto con el **transporte** utilizado (`local`, `ssh` o
  `guestcontrol`) y, en el caso de Guest Additions, la orden `VBoxManage`
  subyacente;
- **archivos descargables** (por ejemplo la llave privada SSH).

## Estructura

```
main.go                     punto de entrada, banderas y ciclo de vida
internal/config             configuración
internal/runner             transportes de comandos (local, SSH, Guest Additions
                            y failover auto), ejecución por argv
internal/op                 modelo del informe de resultados
internal/validate           validadores de entrada
internal/modules            lógica de cada módulo de automatización
internal/web                rutas, manejadores, plantillas y recursos embebidos
```

## Pruebas

```bash
make test     # o: go test ./...
go vet ./...
```

Las pruebas cubren los validadores (incluidos intentos de inyección), el
transporte local, el transporte Guest Additions (decodificación del código de
salida y redacción de la contraseña), el *failover* SSH → Guest Additions, la
edición de `/etc/hosts`, el generador de unidades systemd, el banner de
`/etc/issue` y las operaciones de backend de HAProxy.

Consulte `docs/` para el escenario de pruebas y el plan de verificación.
