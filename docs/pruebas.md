# Diseño de pruebas

Cada prueba se ejecuta sobre la máquina virtual Debian 13.7.0 y se graba en
video mostrando el **estado anterior** y el **estado posterior**. La consola
reporta ambos estados en pantalla (comando ejecutado, salida y resultado), lo que
sirve como evidencia técnica complementaria.

## Pruebas automatizadas (en el repositorio)

```bash
go test ./...   # validadores, transporte local, /etc/hosts, units, issue, HAProxy,
                # Guest Additions (código de salida y redacción) y failover SSH→GA
go vet ./...
```

| Área | Qué verifica |
| --- | --- |
| `internal/validate` | Rechazo de intentos de inyección (`;`, `$(id)`, `&&`, backticks), nombres inválidos, puertos fuera de rango y rutas con `..`. |
| `internal/runner` | Construcción del *argv* con `sudo`, citado seguro para SSH, captura de código de salida, decodificación del código de salida de Guest Additions, redacción de la contraseña y *failover* SSH → Guest Additions. |
| `internal/modules` | Edición de `/etc/hosts`, generación de unidades systemd, banner de `/etc/issue` y operaciones de backend de HAProxy. |

## Pruebas funcionales por módulo

Antes de cada prueba, capture el estado anterior (por ejemplo
`dpkg -l | grep nginx`, `systemctl status X`, `cat /etc/hosts`). Después,
ejecute la operación desde la consola y repita la consulta.

1. **Aplicaciones.** Instale `htop` y confirme con `dpkg -l htop`. Elimine `htop`
   (con y sin purga) y confirme la desaparición del paquete. Pruebe un nombre
   inválido (`htop; reboot`) y muestre que la consola lo rechaza sin ejecutar.
2. **Llave SSH de root.** Antes: `ls /root/.ssh`. Después: `cat
   /root/.ssh/authorized_keys` y `sshd -T | grep -Ei
   'passwordauthentication|permitrootlogin'`. Descargue la llave privada y
   conéctese con `ssh -i`.
3. **Mensaje de inicio.** Vuelva a iniciar sesión (o `cat /etc/issue`) para ver
   el banner con host, fecha, hora e IP.
4. **Nombre de host.** `hostname` y `cat /etc/hosts` antes y después.
5. **Usuarios.** Cree `ana` con sudo y llave, verifique `id ana`, el contenido de
   `~ana/.ssh` y que el login por contraseña está bloqueado. Elimine el usuario
   con y sin la opción de borrar archivos.
6. **Servicios systemd.** Despliegue el **catálogo de libros** y el **script de
   log** de las precondiciones, y pruebe `start`, `stop`, `restart`, `status`,
   `enable` y `disable`. Verifique el archivo de log con fecha y hora.
7. **HAProxy.** Antes: `haproxy -c -f /etc/haproxy/haproxy.cfg` y el bloque
   `backend`. Agregue un servidor, valide y recargue; haga una petición al
   balanceador y cambie de backend. Elimine el servidor y valide de nuevo.

## Pruebas de transporte

1. **Guest Additions (`-mode guestcontrol`).** Con la VM encendida,
   `VBoxManage list runningvms` y la consola iniciada con `-guestcontrol-vm`, en
   cada operación verifique que el informe indica `transporte: guestcontrol` y
   muestra la orden `VBoxManage` subyacente con la contraseña redactada.
2. **Respaldo automático (`-mode auto`).** Inicie la consola con SSH y Guest
   Additions configurados. Ejecute una operación, apague
   `openssh-server` en el invitado (o cambie `-ssh-host`), vuelva a operar y
   compruebe que el paso muestra `transporte: guestcontrol` sin reiniciar la
   consola.
3. **Transferencia de archivos.** Genere la llave SSH de root y despliegue una
   unidad systemd en modo `guestcontrol`: los pasos deben mostrar `copyto`,
   `mv` y `chmod` sobre el invitado.

## Pruebas negativas

- Entradas con caracteres de shell en cada formulario.
- Puerto de backend fuera de rango y dirección no válida.
- Eliminación de una cuenta del sistema (por ejemplo `root`): la consola la
  rechaza.
- Archivo `haproxy.cfg` inválido: la consola restaura el respaldo y deja constancia.
- Operación con código de salida distinto de cero: el informe muestra `stderr` y
  el código.
