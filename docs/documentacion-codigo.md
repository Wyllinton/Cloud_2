# Documentación técnica de `appgo` — Consola de automatización NP2

Documento de referencia del código fuente completo de la aplicación Go. Explica
la arquitectura, cada paquete y archivo, las funciones más relevantes y los
mecanismos que intervienen en el flujo de ejecución: arranque, selección de
transporte, ciclo de una petición HTTP, ejecución de comandos, generación de
informes, transferencia de archivos, promoción en caliente a SSH, *failover* y
seguridad.

---

## Índice

1. [Resumen y propósito](#1-resumen-y-propósito)
2. [Arquitectura general](#2-arquitectura-general)
3. [Mapa del repositorio](#3-mapa-del-repositorio)
4. [Flujo de ejecución](#4-flujo-de-ejecución)
   - 4.1 [Arranque del programa](#41-arranque-del-programa)
   - 4.2 [Selección del transporte](#42-selección-del-transporte)
   - 4.3 [Ciclo de una petición HTTP](#43-ciclo-de-una-petición-http)
   - 4.4 [Ejecución de comandos y modelo de resultados](#44-ejecución-de-comandos-y-modelo-de-resultados)
   - 4.5 [Transferencia de archivos](#45-transferencia-de-archivos)
   - 4.6 [Promoción en caliente a SSH y *failover*](#46-promoción-en-caliente-a-ssh-y-failover)
   - 4.7 [Concurrencia, timeouts y apagado ordenado](#47-concurrencia-timeouts-y-apagado-ordenado)
5. [Módulos del código](#5-módulos-del-código)
   - 5.1 [`main`](#51-main)
   - 5.2 [`internal/config`](#52-internalconfig)
   - 5.3 [`internal/runner`](#53-internalrunner)
   - 5.4 [`internal/op`](#54-internalop)
   - 5.5 [`internal/validate`](#55-internalvalidate)
   - 5.6 [`internal/modules`](#56-internalmodules)
   - 5.7 [`internal/web`](#57-internalweb)
6. [Seguridad](#6-seguridad)
7. [Pruebas automáticas](#7-pruebas-automáticas)
8. [Compilación y ejecución](#8-compilación-y-ejecución)
9. [Observaciones y limitaciones](#9-observaciones-y-limitaciones)
10. [Apéndices](#10-apéndices)

---

## 1. Resumen y propósito

`appgo` es una aplicación web escrita en Go que automatiza la administración de
una máquina virtual **Debian 13**. Desde un navegador se pueden ejecutar tareas
que normalmente requerirían una sesión interactiva:

- instalar y eliminar paquetes con `apt`;
- configurar el acceso SSH por llave para `root`;
- personalizar el banner de inicio `/etc/issue`;
- cambiar el nombre de host y sincronizar `/etc/hosts`;
- crear y eliminar usuarios, con `sudo` y llave SSH opcionales;
- desplegar aplicaciones como servicios `systemd` y controlarlos;
- agregar o quitar servidores del *backend* de HAProxy y recargar el balanceador;
- aprovisionar servidores HTTP (HTTPaaS) desde una plantilla de VirtualBox,
  registrar su nombre en Bind9 y desplegar un `.zip` en Apache.

Tres principios recorren todo el código:

1. **Todo es no interactivo.** `apt` usa `DEBIAN_FRONTEND=noninteractive` y `-y`;
   `sudo` se invoca con `-n`; `ssh-keygen` usa `-N ""`.
2. **Toda entrada se valida antes de llegar a un comando.** El paquete
   `internal/validate` concentra las reglas.
3. **Cada comando ejecutado se conserva.** El paquete `internal/op` construye un
   informe con el comando, el código de salida, `stdout`, `stderr`, el transporte
   y la duración, para que un fallo sea diagnosticable desde la interfaz.

La aplicación puede ejecutarse en cuatro modos de transporte:

| Modo | Dónde corre la consola | Cómo alcanza el objetivo |
| --- | --- | --- |
| `local` | Dentro de la VM | `exec.Command` en el propio host |
| `ssh` | En el host | SSH con autenticación exclusiva por llave |
| `guestcontrol` | En el host | `VBoxManage guestcontrol` (Guest Additions) |
| `auto` | En el host | SSH primero; Guest Additions como respaldo |

---

## 2. Arquitectura general

La aplicación sigue una arquitectura en capas con dependencias unidireccionales:

```
┌──────────────────────────────────────────────────────────────┐
│ main.go                                                      │
│  banderas → config.Validate → buildRunner → web.New → HTTP   │
└───────────────┬──────────────────────────────┬───────────────┘
                │                              │
                ▼                              ▼
┌───────────────────────────┐   ┌──────────────────────────────┐
│ internal/web              │   │ internal/runner              │
│  server.go   rutas         │   │  Runner (interfaz)           │
│  handlers.go manejadores   │   │  Local | SSH | GuestControl   │
│  templates/  HTML          │   │  Failover (auto + promoción) │
│  static/     CSS           │   └──────────────┬───────────────┘
└───────────────┬───────────┘                  │
                │                              │
                ▼                              ▼
┌───────────────────────────┐   ┌──────────────────────────────┐
│ internal/modules          │──▶│ internal/op                  │
│  packages, rootssh, issue,│   │  Report, Step, Artifact       │
│  hostname, users, systemd,│   └──────────────────────────────┘
│  haproxy, common          │
└───────────────┬───────────┘
                │
                ▼
┌───────────────────────────┐
│ internal/validate         │
│  validadores estrictos    │
└───────────────────────────┘
```

Reglas de dependencia:

- `web` conoce a `modules`, `op`, `runner` y `config`.
- `modules` conoce a `runner`, `op` y `validate`.
- `runner` **no** conoce a `modules`, `web` ni `validate`.
- `validate` es una hoja: no depende de nada del proyecto.
- `op` depende solo de `runner` (para convertir un `runner.Result` en un `Step`).

El `Runner` es la pieza central: todos los módulos reciben una implementación de
la interfaz y no saben si el comando se ejecuta en local, por SSH o a través de
VirtualBox. Esto permite cambiar el transporte sin tocar la lógica de negocio.

---

## 3. Mapa del repositorio

```
AppGo/
├── main.go                         Punto de entrada, banderas, ciclo de vida
├── go.mod / go.sum                 Módulo np2/appgo, Go 1.23, x/crypto
├── Makefile                        build, run-*, test, vet, fmt, clean
├── README.md                       Manual de uso
├── appgo                           Binario compilado (artefacto)
├── docs/
│   ├── documentacion-codigo.md     Este documento
│   └── httpaas.md                  Guía operativa del módulo HTTPaaS
├── scripts/                        Red host-only, Bind9, plantilla y diagnóstico
│   ├── 01-host-network.sh … 05-check-host.sh
└── internal/
    ├── config/
    │   ├── config.go               Configuración y validación de coherencia
    │   └── config_test.go
    ├── runner/
    │   ├── runner.go               Interfaz Runner, Result, ShellQuote, FileTransfer
    │   ├── local.go                Transporte local
    │   ├── ssh.go                  Transporte SSH
    │   ├── guestcontrol.go         Transporte Guest Additions (VBoxManage)
    │   ├── failover.go             Auto, respaldo y promoción a SSH
    │   └── *_test.go
    ├── op/
    │   ├── report.go               Report, Step, Artifact
    │   └── report_test.go
    ├── validate/
    │   ├── validate.go             Validadores de entradas
    │   └── validate_test.go
    ├── modules/
    │   ├── common.go               Helpers de ejecución, lectura y escritura
    │   ├── packages.go             apt / dpkg
    │   ├── rootssh.go              Llave SSH de root y hardening
    │   ├── issue.go                /etc/issue
    │   ├── hostname.go             hostname + /etc/hosts
    │   ├── users.go                getent / useradd / userdel
    │   ├── systemd.go              Unidades systemd
    │   ├── haproxy.go              Backends de HAProxy
    │   ├── httpaas.go              HTTPaaS: VMs, discos, DNS y Apache
    │   └── *_test.go
    └── web/
        ├── server.go               Server, rutas, middleware, artefactos
        ├── handlers.go             Manejadores HTTP
        ├── templates/              layout, partials y una plantilla por página
        └── static/app.css          Hoja de estilos embebida
```

Tamaño aproximado: ~5.800 líneas de Go (incluyendo pruebas) y ~1.000 líneas de
plantillas/estilos.

---

## 4. Flujo de ejecución

### 4.1 Arranque del programa

`main()` es deliberadamente mínimo:

```go
func main() {
    if err := run(); err != nil {
        log.Fatalf("error: %v", err)
    }
}
```

Toda la lógica está en `run()` para poder devolver errores y probar el arranque.
La secuencia es:

1. **Configuración por defecto** (`config.Default()`).
2. **Definición de banderas** con `flag`. Las banderas escriben directamente sobre
   los campos de `cfg` (`flag.StringVar(&cfg.Listen, ...)`), salvo `-mode`, que se
   convierte a `*string` mediante `(*string)(&cfg.Mode)`.
3. **`flag.Parse()`**.
4. **`-version`**: imprime `appgo <versión>` y termina. La variable `version` se
   inyecta en tiempo de compilación con
   `-ldflags "-X main.version=..."`; por defecto vale `"dev"`.
5. **Contraseña del invitado desde el entorno**: si
   `-guestcontrol-password` está vacío, se usa `NP2_GUEST_PASSWORD`. Así la
   contraseña no queda en el historial del shell.
6. **`cfg.Validate()`**: comprueba coherencia entre modo, transportes y opciones.
7. **`buildRunner(cfg)`**: construye el transporte (ver 4.2).
8. **`defer transport.Close()`**: libera la conexión SSH o recursos al salir.
9. **`web.New(cfg, transport)`**: crea el servidor, instancia los módulos y parsea
   las plantillas embebidas.
10. **`http.Server`** con tiempos explícitos:
    - `ReadHeaderTimeout`: 10 s.
    - `ReadTimeout`: 30 s.
    - `WriteTimeout`: `cfg.CommandTimeout + 1 min` (las operaciones pueden tardar).
    - `IdleTimeout`: 2 min.
11. **Contexto de señales** con `signal.NotifyContext` para `SIGINT` y `SIGTERM`.
12. **Servidor en goroutine**; `select` entre error del servidor y señal.
13. **Apagado ordenado** con `httpServer.Shutdown` y un límite de 10 s.

### 4.2 Selección del transporte

`buildRunner(cfg)` es una máquina de estados que decide qué implementación de
`runner.Runner` se usará. Primero crea el transporte Guest Additions si
`cfg.HasGuestControl()` (es decir, si `-guestcontrol-vm` no está vacío):

```go
gc = runner.NewGuestControl(runner.GuestControlOptions{...})
```

Después ramifica según `cfg.Mode`:

#### Modo `local`

Devuelve `runner.NewLocal(cfg.Sudo)`. Si `-sudo` está activo, registra un aviso:
`sudo -n` fallará si el usuario exige contraseña.

#### Modo `ssh`

Llama a `newSSH(cfg)`, que:

1. lee la llave privada desde disco con `cfg.SSH.LoadPrivateKey()`;
2. llama a `newSSHWithKey(cfg, key)`;
3. `newSSHWithKey` avisa si no hay `known_hosts` y no se habilitó
   `--ssh-insecure-ignore-host-key`;
4. construye `runner.NewSSH` con host, puerto, usuario, llave, posible frase de
   paso, verificación de host, `sudo` y un timeout de diálogo de 15 s.

#### Modo `guestcontrol`

- Exige `-guestcontrol-vm`.
- Ejecuta `pingGuestControl(gc)` (comando `true` con timeout de 30 s). El ping es
  **informativo**: si falla, solo se registra un aviso y la consola arranca igual,
  porque la VM puede encenderse después.
- Si además hay `-ssh-host`, devuelve
  `runner.NewBootstrapFailover(gc, sshTarget, sshConnector)`, es decir, empieza en
  Guest Additions y puede promoverse a SSH cuando se genere la llave.
- Si no hay host SSH, devuelve el `GuestControl` puro.

#### Modo `auto`

Es el más elaborado:

1. Si hay `-ssh-host`:
   - si hay `-ssh-key`, intenta `newSSH(cfg)`;
   - si no hay `-ssh-key` y tampoco Guest Additions, error;
   - si `newSSH` falla y hay Guest Additions, se registra el error y se continúa;
   - si `newSSH` falla y no hay Guest Additions, se devuelve el error.
2. Según las combinaciones resultantes:
   - **SSH + Guest Additions**: `pingGuestControl` y `NewFailover(ssh, gc)`; SSH es
     principal y Guest Additions el respaldo.
   - **Solo SSH**: devuelve el runner SSH.
   - **Guest Additions + host SSH configurado** (aunque aún no haya llave):
     `NewBootstrapFailover(gc, target, connector)`.
   - **Solo Guest Additions**: `pingGuestControl` y devuelve `gc`.
   - **Nada configurado**: error.

La diferencia clave entre `NewFailover` y `NewBootstrapFailover`:

| Constructor | Transporte inicial | ¿Puede promoverse a SSH? |
| --- | --- | --- |
| `NewFailover(primary, secondary)` | `primary` (SSH) | Sí, pero ya está en SSH; `connect` es `nil` |
| `NewBootstrapFailover(secondary, target, connect)` | `secondary` (Guest Additions) | Sí, usando la llave que llega en memoria |

### 4.3 Ciclo de una petición HTTP

Todas las rutas se registran en `(*web.Server).Handler()`. Una petición típica de
operación sigue este recorrido:

```
Navegador
   │ POST /sistema/paquetes/instalar (form)
   ▼
middlewares: basicAuth → logRequests → recoverer
   ▼
mux (ServeMux con patrones "MÉTODO /ruta")
   ▼
handler (p. ej. handlePackageInstall)
   │ 1. newView(...)                   crea el modelo de la página
   │ 2. opContext(r)                   deriva context con CommandTimeout
   │ 3. lee r.FormValue(...)           extrae los campos
   │ 4. módulo.Instalar(ctx, req)      ejecuta la operación
   │      └─ runner.Run(...)           uno o varios comandos
   │      └─ op.Report                 acumula Steps/Notes/Warnings/Artifacts
   │ 5. finalize(v)                    guarda artefactos y prepara enlaces
   │ 6. loadInstalled/loadUsers/...     recarga datos de la página
   │ 7. render(w, page, v)             ejecuta html/template
   ▼
HTML con el informe y la información técnica
```

Detalles importantes:

- **`opContext`** envuelve `r.Context()` con un `context.WithTimeout` igual a
  `cfg.CommandTimeout` (10 min por defecto). Ese contexto llega hasta cada
  comando; si se agota, el transporte mata el proceso o cierra la sesión.
- **`newView`** rellena `Target`, `Mode`, `Form` y el título. `render` vuelve a
  leer `runner.Target()` y `runner.Kind()` justo antes de dibujar, porque el
  transporte puede haber cambiado a SSH durante la propia petición.
- **`finalize`** convierte los `op.Artifact` del informe en enlaces temporales y,
  si el informe falló, copia el resumen a `v.Error` para mostrarlo como alerta.
- **`render`** ejecuta siempre la plantilla `"layout"`, que a su vez invoca
  `"report"` y `"content"`.
- No hay patrón *Post/Redirect/Get*: el resultado se renderiza en la misma
  respuesta POST. Recargar el navegador reenvía el formulario.

Rutas registradas (Go 1.22+ permite método y patrón en el mismo string):

| Método y ruta | Handler | Función |
| --- | --- | --- |
| `GET /{$}` | `handleDashboard` | Panel de inicio |
| `GET /sistema/paquetes` | `handlePackages` | Formulario + paquetes instalados |
| `POST /sistema/paquetes/instalar` | `handlePackageInstall` | Instala |
| `POST /sistema/paquetes/eliminar` | `handlePackageRemove` | Elimina/purga |
| `GET /sistema/ssh-root` | `handleRootSSH` | Estado de `sshd` |
| `POST /sistema/ssh-root/configurar` | `handleRootSSHConfigure` | Genera/configura llave |
| `GET /sistema/issue` | `handleIssue` | Banner actual |
| `POST /sistema/issue/vista-previa` | `handleIssuePreview` | Previsualiza sin escribir |
| `POST /sistema/issue/aplicar` | `handleIssueApply` | Escribe `/etc/issue` |
| `GET /sistema/hostname` | `handleHostname` | Nombre actual y `/etc/hosts` |
| `POST /sistema/hostname/asignar` | `handleHostnameSet` | Cambia nombre |
| `GET /usuarios` | `handleUsers` | Lista usuarios |
| `POST /usuarios/crear` | `handleUserCreate` | Crea usuario |
| `POST /usuarios/eliminar` | `handleUserDelete` | Elimina usuario |
| `GET /servicios` | `handleServices` | Lista unidades |
| `POST /servicios/accion` | `handleServiceAction` | start/stop/restart/status/enable/disable |
| `POST /servicios/desplegar` | `handleServiceDeploy` | Sube archivos y crea unidad |
| `POST /servicios/eliminar` | `handleServiceRemove` | Detiene y elimina unidad |
| `GET /haproxy` | `handleHAProxy` | Backends actuales |
| `POST /haproxy/agregar` | `handleHAProxyAdd` | Agrega servidor |
| `POST /haproxy/eliminar` | `handleHAProxyRemove` | Elimina servidor |
| `GET /descargas/{id}` | `handleDownload` | Descarga artefacto |
| `GET /salud` | función anónima | `ok target=... mode=...` |
| `GET /estaticos/` | `http.FileServer` | CSS embebido |

### 4.4 Ejecución de comandos y modelo de resultados

#### `runner.Runner`

```go
type Runner interface {
    Run(ctx context.Context, input []byte, name string, args ...string) Result
    Kind() string
    Target() string
    UsesSudo() bool
    Close() error
}
```

`Run` recibe un **argv explícito**: nombre del ejecutable y argumentos por
separado. Ningún módulo concatena cadenas para formar un comando.

#### `runner.Result`

```go
type Result struct {
    Argv        []string
    Stdout      string
    Stderr      string
    ExitCode    int
    Err         error
    Duration    time.Duration
    Title       string
    Transport   string
    HostCommand string
}
```

Métodos relevantes:

- **`Ok()`**: éxito si `Err == nil` y `ExitCode == 0`.
- **`CommandLine()`**: `ShellQuote(Argv)`; solo para mostrar, nunca para ejecutar.
- **`ErrorString()`**: prioriza `Err` y la primera línea de `stderr`; si no hay
  error pero el código no es cero, devuelve la primera línea de `stderr`.
- **`TransportFailed()`**: distingue un fallo de conectividad de un comando que
  simplemente terminó mal. Devuelve `false` si el error es de tipo
  `*ssh.ExitError` o `*exec.ExitError`; en cualquier otro caso (`dial` fallido,
  `VBoxManage` inalcanzable, contexto cancelado) devuelve `true`. Esto es lo que
  usa el *failover* para decidir si debe cambiar de transporte.

#### `buildArgv` y `sudo`

```go
func buildArgv(sudo bool, name string, args ...string) []string
```

Si `sudo` es `true`, produce `["sudo", "-n", name, args...]`; si no, `[name,
args...]`. El `-n` garantiza que `sudo` falle en vez de pedir contraseña.

#### Cotización POSIX

`ShellQuote(argv)` une el argv en una sola cadena para los transportes que
necesitan un string (SSH y el envoltorio de Guest Additions). `quoteArg`:

- devuelve `''` para cadena vacía;
- deja sin comillas los caracteres seguros
  `[A-Za-z0-9_./:=@%+,]`;
- en cualquier otro caso rodea con comillas simples y sustituye `'` por
  `'\''`.

Esto neutraliza `;`, `|`, `$`, backticks, espacios, saltos de línea, etc.

#### Informe (`op.Report`)

Cada módulo crea un informe con `op.New(accion)`, agrega pasos con
`rep.Step`/`rep.OptionalStep`, notas con `rep.Note`, advertencias con `rep.Warn`
y artefactos con `rep.AddArtifact`. Al salir, un `defer rep.Finish()` calcula:

- `FinishedAt` y `OK`;
- `OK` es `false` si hay `Err` **o** si algún paso no opcional falló;
- `Summary` es el error si existe; si no, la acción + "operación completada
  correctamente".

El patrón `defer rep.Finish()` es lo que permite que todos los `return` tempranos
(por validación o por fallo de un paso) produzcan un informe consistente.

#### Helpers de módulos (`internal/modules/common.go`)

- **`step`**: ejecuta y registra; no aborta.
- **`require`**: ejecuta, registra y, si falla, hace `rep.Failf` y devuelve
  `(res, false)` para que el llamador aborte.
- **`exists`**: `test -e`; distingue "no existe" (`false, nil`) de "transporte
  caído" (`false, err`).
- **`executableFile`**: comprueba `test -f` y `test -x` con la misma distinción.
- **`readFile`**: comprueba existencia; si el transporte implementa
  `FileTransfer`, lo usa; si no, ejecuta `cat -- ruta`.
- **`writeFile`**: si hay `FileTransfer`, delega; si no, escribe con `tee -- ruta`
  por `stdin` y ajusta permisos con `chmod`.
- **`octal`**: convierte un `uint32` de permisos a la representación octal con
  prefijo `0`.

### 4.5 Transferencia de archivos

La interfaz `runner.FileTransfer` abstrae los transportes que no pueden usar
`stdin`/`stdout`:

```go
type FileTransfer interface {
    ReadFile(ctx context.Context, path string) ([]byte, Result, error)
    WriteFile(ctx context.Context, path string, data []byte, mode uint32) ([]Result, error)
}
```

- **Local** y **SSH** no la implementan: usan `cat`/`tee`, que funcionan porque
  ambos soportan entrada y salida estándar.
- **GuestControl** sí la implementa, porque `VBoxManage guestcontrol run` no
  ofrece un flujo fiable de `stdin`/`stdout` para archivos:
  - **Lectura**: `guestcontrol copyfrom --dereference <ruta_invitado>
    <archivo_host_temporal>` y luego `os.ReadFile` en el host.
  - **Escritura**: archivo temporal en el host → `copyto <host> /tmp/np2-<token>`
    → `mv -f -- /tmp/np2-<token> <destino>` → `chmod`.
  - Cada paso se registra en el informe.
- **Failover** implementa `FileTransferProvider`, que devuelve el mecanismo del
  transporte activo en ese instante:

```go
type FileTransferProvider interface {
    FileTransfer() (FileTransfer, bool)
}
```

`modules.fileTransfer(r)` primero pregunta al `Provider`; si no, hace type
assertion directa. Así, cuando el *failover* está en SSH se usa `tee`/`cat`, y
cuando está en Guest Additions se usa `copyto`/`copyfrom`, sin que los módulos
cambien.

### 4.6 Promoción en caliente a SSH y *failover*

Este es el mecanismo distintivo del proyecto.

#### `Failover`

```go
type Failover struct {
    mu         sync.Mutex
    primary    Runner
    secondary  Runner
    connect    func([]byte) (Runner, error)
    sshTarget  string
    onFallback bool
}
```

- **`NewFailover(primary, secondary)`**: arranca en `primary` (SSH) y usa
  `secondary` (Guest Additions) si el principal falla.
- **`NewBootstrapFailover(secondary, target, connect)`**: arranca en
  `secondary` (Guest Additions), guarda el destino SSH para mostrarlo y un
  `connect` que sabe crear un runner SSH a partir de una llave en memoria.
- **`activeLocked()`**: devuelve `primary` si existe y no se está en fallback; si
  no, `secondary`.
- **`Run`**:
  1. copia el estado bajo mutex;
  2. si hay primario y no se está en fallback, ejecuta en él;
  3. si el resultado **no** es un fallo de transporte (o no hay secundario), lo
     devuelve tal cual;
  4. si es un fallo de transporte, marca `onFallback = true` (solo si el primario
     sigue siendo el mismo) y ejecuta en el secundario.
  - Un comando que termina con código distinto de cero **no** provoca fallback:
    es un resultado real.
  - Tras el primer fallback, se queda en el secundario para no reintentar una
    conexión muerta en cada petición.
- **`PromoteSSH(privateKey)`**:
  1. si SSH ya está activo, no hace nada;
  2. si no hay `connect`, error;
  3. conecta con `connect(privateKey)`;
  4. vuelve a tomar el mutex; si otra petición ya promovió, cierra la conexión
     duplicada y descarta el cambio;
  5. si no, instala el nuevo primario, limpia `onFallback`, cierra el primario
     anterior y registra `transporte promovido a SSH (...)`.
  - Si la conexión falla, el transporte actual (Guest Additions) sigue
    funcionando.

#### Flujo completo de promoción

1. La consola arranca en `guestcontrol` o `auto` sin llave local, pero con
   `--ssh-host`.
2. `buildRunner` construye `NewBootstrapFailover(gc, "root@host:22", connect)`.
3. El usuario abre *Llave SSH de root* y pulsa configurar.
4. `RootSSHManager.Configure` genera la llave dentro del invitado, instala la
   pública, aplica el endurecimiento y lee la privada con `cat`.
5. `promoteToSSH(rep, priv)` comprueba si el runner implementa
   `runner.SSHPromoter`:
   ```go
   type SSHPromoter interface {
       PromoteSSH(privateKey []byte) error
   }
   ```
6. `Failover.PromoteSSH` llama a `connect(priv)`; ese callback es
   `sshConnector(cfg)`, que ejecuta `newSSHWithKey(cfg, key)`.
7. La llave nunca toca el disco del host: viaja del invitado a la memoria del
   proceso y de ahí al cliente SSH.
8. El informe agrega una nota indicando el cambio de `Kind()` (de
   `guestcontrol` a `ssh`), y las siguientes operaciones ya usan SSH.
9. Si la promoción falla (por ejemplo, falta `known_hosts`), se agrega una
   advertencia y la consola sigue por Guest Additions, sin perder la llave
   generada.

#### *Failover* inverso

Si SSH se cae durante una operación, `Result.TransportFailed()` devuelve `true`,
`Failover` conmuta a Guest Additions y la operación continúa por ese canal. El
cambio es permanente para el resto del proceso (hasta reiniciar).

### 4.7 Concurrencia, timeouts y apagado ordenado

- El `http.Server` atiende cada petición en su propia goroutine.
- Hay **un solo** `Runner` compartido por todas las peticiones. SSH permite
  varias sesiones concurrentes; GuestControl lanza un `VBoxManage` por comando;
  Local lanza un proceso por comando.
- `Failover` protege su estado con `sync.Mutex`, pero no serializa la ejecución
  de comandos: dos peticiones pueden ejecutarse a la vez.
- `artifactStore` protege el mapa de artefactos con `sync.Mutex` y purga los
  expirados en cada `put`.
- Las plantillas se parsean una vez al arrancar y después solo se leen; son
  seguras para uso concurrente.
- **Timeouts**:
  - por operación HTTP: `opContext` con `CommandTimeout` (10 min por defecto);
  - diálogo SSH: 15 s;
  - GuestControl: `--timeout` calculado a partir del contexto restante o del
    `Timeout` configurado (`timeoutMS`);
  - ping de Guest Additions: 30 s;
  - servidor HTTP: `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`,
    `IdleTimeout` como se describió.
- **Cancelación**:
  - Local: `exec.CommandContext` mata el proceso.
  - SSH: `session.Signal(ssh.SIGKILL)` y `session.Close()`.
  - GuestControl: `exec.CommandContext` mata `VBoxManage`; además se pasa
    `--timeout` al invitado.
- **Apagado**: una señal `SIGINT`/`SIGTERM` cancela el contexto, se llama a
  `Shutdown` con 10 s y `main` cierra el transporte con `defer`.

---

## 5. Módulos del código

### 5.1 `main`

Archivo: `main.go` (292 líneas).

| Función | Firma | Responsabilidad |
| --- | --- | --- |
| `main` | `func main()` | Llama a `run` y termina con `log.Fatalf` si hay error |
| `run` | `func run() error` | Banderas, configuración, servidor HTTP y ciclo de vida |
| `buildRunner` | `func buildRunner(cfg config.Config) (runner.Runner, error)` | Máquina de estados de transportes |
| `newSSH` | `func newSSH(cfg config.Config) (runner.Runner, error)` | Lee la llave de disco y conecta |
| `newSSHWithKey` | `func newSSHWithKey(cfg config.Config, key []byte) (runner.Runner, error)` | Conecta con una llave explícita (también en memoria) |
| `sshConnector` | `func sshConnector(cfg config.Config) func([]byte) (runner.Runner, error)` | *Callback* de promoción |
| `sshTarget` | `func sshTarget(cfg config.Config) string` | Renderiza `usuario@host:puerto` |
| `pingGuestControl` | `func pingGuestControl(gc *runner.GuestControl)` | Comprueba el canal sin impedir el arranque |

Banderas principales (todas documentadas en el README):

- `-listen`, `-mode`, `-sudo`, `-timeout`, `-version`;
- `-ssh-host`, `-ssh-port`, `-ssh-user`, `-ssh-key`,
  `-ssh-key-passphrase`, `-ssh-known-hosts`,
  `-ssh-insecure-ignore-host-key`;
- `-guestcontrol-vm`, `-guestcontrol-user`, `-guestcontrol-password`,
  `-guestcontrol-password-file`, `-guestcontrol-domain`,
  `-guestcontrol-vboxmanage`;
- `-haproxy-config`, `-haproxy-service`;
- `-auth-user`, `-auth-pass`.

### 5.2 `internal/config`

Archivo: `internal/config/config.go` (196 líneas).

Define el tipo `Mode` y las constantes `ModeLocal`, `ModeSSH`,
`ModeGuestControl`, `ModeAuto`, además de las estructuras `Config`, `SSHConfig`,
`GuestControlConfig` y `HAProxyConfig`.

| Función / método | Descripción |
| --- | --- |
| `Default() Config` | Valores seguros: `:8080`, `auto`, 10 min, SSH puerto 22/usuario `root`, `VBoxManage`, `/etc/haproxy/haproxy.cfg`, servicio `haproxy` |
| `(Config) HasSSH() bool` | `SSH.Host` no vacío |
| `(Config) HasGuestControl() bool` | `GuestControl.VM` no vacío |
| `(Config) Validate() error` | Coherencia global |
| `(SSHConfig) validate() error` | Exige host, usuario y `--ssh-key` |
| `(SSHConfig) validateBootstrap() error` | Exige host y usuario, pero **no** llave (promoción) |
| `(GuestControlConfig) validate() error` | Exige VM y usuario |
| `(SSHConfig) LoadPrivateKey() ([]byte, error)` | Lee la llave privada del disco |

Reglas de `Validate()`:

- `local`: no exige nada.
- `ssh`: `validate()` (llave obligatoria).
- `guestcontrol`: VM y usuario.
- `auto`: al menos SSH o Guest Additions; si hay SSH y también Guest Additions,
  usa `validateBootstrap` (llave opcional); si hay Guest Additions, valida su
  configuración.
- Siempre comprueba que `SSH.Port` esté entre 1 y 65535.
- `AuthUser` y `AuthPass` deben definirse juntos.

### 5.3 `internal/runner`

#### 5.3.1 `runner.go`

| Elemento | Descripción |
| --- | --- |
| `Result` | Resultado rico de un comando |
| `Result.Ok()` | `Err == nil && ExitCode == 0` |
| `Result.CommandLine()` | Argv citado para mostrar |
| `Result.ErrorString()` | Mensaje diagnóstico |
| `Result.TransportFailed()` | Distingue fallo de transporte de fallo de comando |
| `FileTransfer` | Interfaz de transferencia dedicada |
| `FileTransferProvider` | Interfaz para transportes cuyo mecanismo activo cambia |
| `SSHPromoter` | Interfaz de promoción a SSH |
| `Runner` | Interfaz principal |
| `buildArgv` | Añade `sudo -n` si corresponde |
| `ShellQuote` | Convierte argv a comando POSIX seguro |
| `quoteArg` | Cotiza un argumento individual |
| `firstLine` | Primera línea de `stderr` |

#### 5.3.2 `local.go`

`Local` implementa `Runner` con `exec.CommandContext`. Flujo de `Run`:

1. `buildArgv(l.sudo, name, args...)`;
2. `exec.CommandContext(ctx, argv[0], argv[1:]...)`;
3. conecta `Stdin` si `input != nil`;
4. captura `stdout` y `stderr` en buffers;
5. mide duración;
6. si hay error, extrae el código con `*exec.ExitError`; si no es un error de
   salida, usa `-1`.

Es el transporte usado cuando la consola se ejecuta dentro del invitado y el que
hace posible probar la aplicación sin una VM.

#### 5.3.3 `ssh.go`

`SSH` encapsula un `*ssh.Client`.

- **`NewSSH(opts)`**:
  1. `parsePrivateKey` (con o sin frase de paso);
  2. decide `HostKeyCallback`:
     - `knownhosts.New(archivo)` si hay `KnownHostsFile`;
     - `ssh.InsecureIgnoreHostKey()` solo si se pidió explícitamente;
     - en caso contrario, error: no se permite conectar sin verificación;
  3. puerto por defecto 22 y timeout por defecto 15 s;
  4. `ssh.Dial("tcp", host:puerto, cfg)`;
  5. guarda `target = usuario@host:puerto`.
- **`Run`**:
  1. `buildArgv`;
  2. `client.NewSession()`;
  3. si hay `input`, lo asigna a `session.Stdin`;
  4. lanza `session.Run(ShellQuote(argv))` en una goroutine;
  5. `select` entre `ctx.Done()` (envía `SIGKILL`, cierra sesión, código `-1`) y
     el resultado;
  6. extrae el código de `*ssh.ExitError`;
  7. devuelve `Result` con `Transport: "ssh"`.

SSH necesita una cadena de comando, no un argv; por eso es el único lugar
(además del envoltorio de GuestControl) donde se cotiza.

#### 5.3.4 `guestcontrol.go`

`GuestControl` ejecuta comandos en el invitado a través de `VBoxManage
guestcontrol` en el host.

| Función | Descripción |
| --- | --- |
| `NewGuestControl(opts)` | Valores por defecto `VBoxManage` y usuario `root` |
| `Kind()` / `Target()` / `UsesSudo()` / `Close()` | Metadatos triviales |
| `Ping(ctx)` | Ejecuta `true` y comprueba el canal |
| `Run(ctx, input, name, args...)` | Rechaza `input != nil`; delega en `runGuest` |
| `runGuest(ctx, argv)` | Envoltorio con token y `parseGuestExit` |
| `runArgs(ctx, token, argv)` | Construye el argv de `VBoxManage` |
| `ReadFile(ctx, guestPath)` | `copyfrom` a temporal + lectura |
| `WriteFile(ctx, target, data, mode)` | `copyto` + `mv` + `chmod` |
| `copyFromArgs` / `copyToArgs` | Funciones puras para probar el orden de operandos |
| `baseArgs(sub)` | `--nologo guestcontrol VM sub --username=... --password... --domain...` |
| `execHost(ctx, args)` | `exec.CommandContext` de `VBoxManage` |
| `timeoutMS(ctx)` | `--timeout` en ms según contexto/config |
| `parseGuestExit(stdout, token)` | Extrae el código del trailer |
| `redactArgs(args)` | Sustituye `--password=...` por `***` |
| `randomToken()` | 8 bytes aleatorios en hex |

Mecanismo del envoltorio:

```sh
eval "$1"; rc=$?; printf '\nNP2RC:<token>:%d\n' "$rc"; exit 0
```

Se ejecuta con:

```
/bin/sh -c <script> sh <comando citado>
```

Detalles:

- El comando completo llega como **un solo parámetro posicional** (`$1`), no
  interpolado en el script.
- Se usa `--unquoted-args` porque `VBoxManage` tiende a perder argumentos vacíos
  (por ejemplo `ssh-keygen -N ""`). Al pasar todo como un parámetro ya cotizado,
  los vacíos sobreviven.
- El script imprime un trailer `NP2RC:<token>:<código>`.
- `parseGuestExit` busca el **último** marcador con `strings.LastIndex`, extrae
  el número y elimina el trailer de `stdout`.
- Si no aparece el trailer, se interpreta que el proceso invitado no llegó a
  ejecutarse (VM apagada, credenciales incorrectas, Guest Additions no
  disponibles, timeout). Se devuelve `ExitCode: -1` y un `Err` explícito.
- `HostCommand` guarda la orden `VBoxManage` subyacente con la contraseña
  redactada, para mostrarla en el informe.

#### 5.3.5 `failover.go`

Ya descrito en 4.6. Funciones:

| Función | Descripción |
| --- | --- |
| `NewFailover(primary, secondary)` | SSH principal + respaldo |
| `NewBootstrapFailover(secondary, sshTarget, connect)` | Guest Additions inicial + promoción |
| `activeLocked()` | Transporte activo (requiere mutex) |
| `Kind()` / `Target()` / `UsesSudo()` | Metadatos del activo; `Target` compone ambos |
| `FileTransfer()` | Mecanismo del activo |
| `Close()` | Cierra ambos sin duplicar |
| `PromoteSSH(privateKey)` | Conexión y cambio atómico |
| `Run(...)` | Ejecución con fallback |

### 5.4 `internal/op`

Archivo: `internal/op/report.go` (151 líneas).

| Tipo / función | Descripción |
| --- | --- |
| `Step` | Un comando: título, comando, host command, transporte, salida, código, OK, opcional, error, duración |
| `Artifact` | Archivo descargable: nombre, nombre de archivo, content type, datos, nota |
| `Report` | Acción, OK, resumen, error, pasos, notas, advertencias, artefactos, tiempos |
| `New(action)` | Inicia el informe con `StartedAt` |
| `Step(title, res)` | Agrega paso obligatorio |
| `OptionalStep(title, res)` | Agrega paso informativo |
| `addStep` | Implementación común; si `res.Title` no está vacío, lo usa como título |
| `Failf` / `Fail` | Marcan error |
| `Note` / `Warn` | Agregan texto |
| `AddArtifact` | Registra descarga |
| `Failed()` | `Err != nil` |
| `Finish()` | Calcula `OK`, `Summary` y `FinishedAt` |
| `Duration()` | Duración real o transcurrida |

Matiz importante: `OK` no depende solo de `Err`; también se vuelve `false` si
algún paso no opcional falló. Por eso un módulo puede detectar un fallo con
`rep.Step` sin llamar a `Fail`, y aun así el informe final será incorrecto.

### 5.5 `internal/validate`

Archivo: `internal/validate/validate.go` (323 líneas). Es la primera línea de
defensa contra inyección y entradas malformadas.

| Función | Regla |
| --- | --- |
| `Package` | `^[a-z0-9][a-z0-9+.-]*$`, máx. 100 |
| `Username` | `^[a-z_][a-z0-9_-]{0,31}$`, no reservado |
| `UsernameForDeletion` | Igual, pero solo comprueba la denylist (el UID se valida en el módulo) |
| `IsSystemUser` | Consulta la denylist |
| `RunAsUser` | Igual que `Username` pero permite cuentas de sistema (solo se referencia) |
| `Hostname` | RFC 1123 de una etiqueta, máx. 63, devuelve minúsculas |
| `ServiceName` | `^[A-Za-z0-9][A-Za-z0-9_.:@-]*$`, máx. 128, sin empezar por `-` |
| `Identifier` | Letras, dígitos, `.`, `-`, `_`, máx. 64 (backends, servidores, balanceo) |
| `Address` | IP literal o nombre DNS/FQDN, máx. 253 |
| `Port` | Entero 1-65535 |
| `OptionalWeight` | Vacío → 0; si no, 1-256 |
| `AbsolutePath` | Ruta absoluta, sin `..`, sin NUL/saltos, limpia con `path.Clean` |
| `Filename` | Nombre simple sin `/`, distinto de `.` y `..` |
| `Shell` | Ruta absoluta tipo `/bin/bash`; vacío → `/bin/bash` |
| `EnvKey` | `^[A-Za-z_][A-Za-z0-9_]*$` |
| `SingleLine` | Rechaza `\n`, `\r`, NUL y caracteres de control; limita longitud |
| `Comment` | `SingleLine` con máximo 200 |
| `ExecCommand` | Ruta absoluta para `ExecStart` |
| `ExecArgs` | Rechaza metacaracteres de shell y limita a 500 |
| `IssueMessage` | Rechaza NUL/`\r`, máx. 500, duplica `\` para que agetty lo muestre literal |
| `NoShellMeta` | Rechaza metacaracteres (exportada, actualmente sin uso) |

Denylist de cuentas del sistema (`systemUsers`): `root`, `daemon`, `bin`, `sys`,
`sync`, `games`, `man`, `lp`, `mail`, `news`, `uucp`, `proxy`, `www-data`,
`backup`, `list`, `irc`, `gnats`, `nobody`, `systemd-network`,
`systemd-timesync`, `messagebus`, `sshd`, `haproxy`, `_apt`, `apt`, `mysql`,
`postgres`, entre otras.

### 5.6 `internal/modules`

Cada módulo es un tipo con un campo `R runner.Runner` y un constructor
`NewXxxManager(r)`. No guardan estado propio; toda la operación vive en el
`context` y en el `Report`. La excepción es `HTTPaaSManager` (5.6.9), que usa el
runner local para `VBoxManage`/`nsupdate` y crea un runner Guest Additions por
instancia, además de mantener un inventario persistente.

#### 5.6.1 `common.go`

Ya descrito en 4.4. Es la capa que evita repetir en cada módulo la lógica de
"ejecutar, registrar, abortar si falla" y de lectura/escritura compatible con
todos los transportes.

#### 5.6.2 `packages.go` — aplicaciones (apt)

Tipo: `PackagesManager`.

| Función | Descripción |
| --- | --- |
| `apt(ctx, args...)` | Ejecuta `env DEBIAN_FRONTEND=noninteractive apt-get ...` |
| `Status(ctx, name)` | `dpkg-query -W -f '${db:Status-Abbrev} ${Version}'`; si falla, "no instalado"; recorta a 120 caracteres |
| `Install(ctx, req)` | Valida, opcionalmente `apt-get update`, `apt-get install -y -- pkg`, verifica con `dpkg-query` |
| `Remove(ctx, req)` | `apt-get remove|purge -y -- pkg`, opcionalmente `autoremove` |
| `Installed(ctx, limit)` | `dpkg-query -W -f '${binary:Package}\t${Version}\n'`, limita el número de filas |
| `splitLines` / `trimCR` | Parseo de salida |
| `trimOr` | Recorta a 80 caracteres (actualmente sin uso) |

`Install` agrega una advertencia si falla sugiriendo revisar `stderr`, el nombre
del paquete o un lock de `dpkg`. `Remove` no verifica después con `dpkg-query`,
pero `apt-get` devuelve código distinto de cero si no pudo.

#### 5.6.3 `rootssh.go` — llave SSH de root

Tipo: `RootSSHManager`.

Constantes:

- `/root/.ssh` (0700).
- `/root/.ssh/id_ed25519` (0600) y `.pub` (0644).
- `/root/.ssh/authorized_keys` (0600).
- `/etc/ssh/sshd_config.d/99-np2-hardening.conf` (0644) con:
  `PubkeyAuthentication yes`, `PasswordAuthentication no`,
  `PermitRootLogin prohibit-password`, `KbdInteractiveAuthentication no`,
  `ChallengeResponseAuthentication no`, `UsePAM yes`.

`Configure(ctx, req)`:

1. Crea `/root/.ssh` y ajusta permisos.
2. Comprueba si existe la llave privada con `exists`.
3. Si existe y `Force == false`, la reutiliza y lo nota.
4. Si no existe o `Force`, borra el par anterior (`rm -f`) y ejecuta
   `ssh-keygen -t ed25519 -N "" -C <comentario> -f /root/.ssh/id_ed25519`.
5. Ajusta permisos de privada y pública.
6. Lee la pública con `readPublicKey`.
7. `installAuthorizedKey`: lee `authorized_keys`, comprueba si la pública ya está
   (sin duplicar), agrega salto de línea si hace falta y escribe con modo 0600.
8. Si `DisablePasswordAuth`, `hardenSSHD`:
   - crea el directorio *drop-in*;
   - escribe el archivo;
   - valida con `sshd -t`;
   - recarga el servicio probando `ssh` y luego `sshd`.
9. Lee la privada con `readPrivateKey`.
10. `promoteToSSH` (ver 4.6).
11. Agrega dos artefactos descargables (privada y pública) y una advertencia
    sobre el secreto.
12. Resume según si se deshabilitó o no la contraseña.

`SSHStatus(ctx)` ejecuta `sshd -T` y filtra las líneas de
`passwordauthentication`, `permitrootlogin`, `pubkeyauthentication` y
`kbdinteractiveauthentication`, unidas con `; `.

#### 5.6.4 `issue.go` — mensaje de inicio

Tipo: `IssueManager`.

`BuildIssue(req, custom)` es una función pura que genera el banner:

- Si `Hostname`: `Host: \n`.
- Si `OSName`: `Sistema: \s \r`.
- Si `Kernel` y no `OSName`: `Kernel: \r`.
- Si `IPv4`: `IP: \4`.
- Si `Date`: `Fecha: \d`.
- Si `Time`: `Hora: \t`.
- Si `TTY`: `Terminal: \l`.
- Une los campos con `   |   `.
- Agrega el mensaje personalizado (con salto final si falta) y un salto extra.

Secuencias de `agetty`: `\n` host, `\s` SO, `\r` kernel, `\4` IPv4, `\d` fecha,
`\t` hora, `\l` terminal.

| Función | Descripción |
| --- | --- |
| `Current(ctx)` | Lee `/etc/issue` o devuelve "(no existe /etc/issue)" |
| `Preview(req)` | Valida el mensaje y devuelve `BuildIssue` sin escribir |
| `Apply(ctx, req)` | Valida, construye, nota la vista previa, escribe `/etc/issue` (0644) y opcionalmente `/etc/issue.net`, y verifica con `cat` |

`validate.IssueMessage` duplica las barras invertidas para que un `\` escrito por
el usuario se muestre literal y no se interprete como secuencia de `agetty`.

#### 5.6.5 `hostname.go` — nombre de host

Tipo: `HostnameManager`.

| Función | Descripción |
| --- | --- |
| `Current(ctx)` | `hostname` + lectura de `/etc/hosts` |
| `UpdateHosts(content, name)` | Función pura que reescribe la entrada `127.0.1.1` |
| `Set(ctx, req)` | Valida, `hostnamectl set-hostname -- name`, `hostname -- name`, actualiza `/etc/hosts`, verifica |

`UpdateHosts` recorre las líneas:

- conserva vacías y comentarios;
- si encuentra una línea cuyo primer campo es `127.0.1.1`, la reemplaza por
  `127.0.1.1\t<name>` y marca `placed`;
- si no encontró ninguna, agrega `127.0.1.1\t<name>` al final;
- devuelve el texto unido con `\n` y salto final.

Nota: el segundo comando (`hostname -- name`) se ejecuta con `require`, pero si
falla solo se agrega una advertencia y se continúa; el informe queda marcado como
fallido porque `require` ya registró el error.

#### 5.6.6 `users.go` — usuarios

Tipo: `UserManager`.

| Función | Descripción |
| --- | --- |
| `List(ctx)` | `getent passwd`; conserva UID `>= 1000` y `< 65534`; ordena por nombre |
| `uid(ctx, name)` | `id -u -- name`; devuelve `(uid, existe, error)` |
| `Create(ctx, req)` | Valida nombre, shell y comentario; comprueba existencia; `useradd`; bloquea contraseña; opcional `sudo`; opcional llave; verifica con `id` |
| `installSSHKey` | Crea `.ssh`, `ssh-keygen`, lee pública, escribe `authorized_keys`, `chown -R`, permisos y artefacto con la privada |
| `Delete(ctx, req)` | Valida; comprueba existencia y UID `>= 1000`; opcional `loginctl terminate-user`; `userdel [-r] -- name`; verifica |
| `homeSuffix` | Texto para el título del paso |

Detalles:

- La cuenta se crea sin contraseña y además se bloquea con `passwd -l`, de modo
  que el login por contraseña es imposible.
- Con `Sudo`, se agrega al grupo `sudo` con `usermod -aG sudo`.
- La llave del usuario se genera **dentro** del invitado y la privada se ofrece
  como descarga.
- `Delete` nunca elimina cuentas con UID `< 1000`, aunque pasaran la validación
  de nombre.
- Si `userdel` falla, se advierte que puede haber procesos activos.

#### 5.6.7 `systemd.go` — servicios

Tipo: `SystemdManager`.

| Función | Descripción |
| --- | --- |
| `List(ctx)` | `systemctl list-unit-files --type=service ...`; filtra `.service` y ordena |
| `unitName(raw)` | Valida y añade `.service` si falta |
| `Action(ctx, rawName, action)` | start/stop/restart/enable/disable; `status` delega en `Status` |
| `Status(ctx, rawName)` | `systemctl status --no-pager -l -- unit` |
| `appendState` | `is-active` + `is-enabled` como pasos opcionales |
| `appendEnabled` | `is-enabled` opcional |
| `waitStarted` | Espera 750 ms, comprueba `is-active`, acepta `oneshot` con `Result=success`, o falla con `status` + `journalctl` |
| `parseProperties` | Convierte `clave=valor` en mapa |
| `Deploy(ctx, req)` | Flujo completo de despliegue |
| `fileMode(target, execCmd, executable)` | 0755 si es ejecutable o es el `ExecStart`; si no, 0644 |
| `buildUnit(...)` | Renderiza la unidad (función pura) |
| `Remove(ctx, rawName)` | stop/disable opcionales, `rm -f`, `daemon-reload` |

`Status` trata los códigos 3 (unidad inactiva/fallida) y 4 (unidad inexistente)
como respuestas válidas, no como fallo de la operación: los registra como
`OptionalStep` y agrega una nota.

`Deploy` paso a paso:

1. Normaliza el nombre a `.service`.
2. Valida directorio de trabajo, comando, argumentos, descripción y usuario de
   ejecución.
3. `mkdir -p <workDir>`.
4. Para cada archivo subido:
   - valida el nombre (sin rutas);
   - calcula el destino `<workDir>/<name>`;
   - calcula el modo con `fileMode`;
   - escribe con `writeFile`;
   - si el destino es el `ExecStart` y no se marcó ejecutable, lo nota.
5. Si hay `RunAs`, `chown -R usuario:usuario <workDir>`.
6. Comprueba que `ExecStart` exista y sea ejecutable (`executableFile`); si no,
   falla antes de tocar una unidad existente y lista los archivos transferidos.
7. `buildUnit` renderiza:
   - `[Unit]`: descripción, `After`/`Wants=network-online.target`.
   - `[Service]`: `Type=simple`, `WorkingDirectory`, `ExecStart`, `User`/`Group`
     opcionales, `Environment=` por cada `CLAVE=valor`, `Restart=on-failure`,
     `RestartSec=3`.
   - `[Install]`: `WantedBy=multi-user.target`.
8. Escribe la unidad en `/tmp/np2-validate-<nano>.service`, ejecuta
   `systemd-analyze verify`, borra el temporal y aborta si no es válida. Así no
   se reemplaza una unidad que ya funcionaba.
9. Escribe `/etc/systemd/system/<unit>` (0644).
10. `systemctl daemon-reload`.
11. Si `Enable`, `systemctl enable`.
12. Si `Restart`, `restart`; si no, si `Start`, `start`.
13. `waitStarted` y `is-enabled`, o solo estado si no se arrancó.
14. Resumen.

`waitStarted` es especialmente útil para `Type=simple`: espera 750 ms para dar
tiempo a que el proceso falle y luego comprueba que siga activo. Si no, adjunta
`systemctl status` y `journalctl -u <unit> -n 20` al informe.

#### 5.6.8 `haproxy.go` — balanceador

Tipo: `HAProxyManager` con `ConfigPath` y `Service`.

| Función | Descripción |
| --- | --- |
| `NewHAProxyManager` | Valores por defecto `/etc/haproxy/haproxy.cfg` y `haproxy` |
| `CurrentContent(ctx)` | Lee el archivo configurado |
| `Backends(cfg)` | Delega en `ParseBackends` |
| `ParseBackends(cfg)` | Extrae bloques `backend` y sus líneas `server` |
| `AddServer(cfg, req)` | Inserta una línea `server` en el backend, o crea el backend |
| `RemoveServer(cfg, req)` | Elimina la línea del servidor |
| `Add(ctx, req)` | Normaliza, lee, modifica y aplica |
| `Remove(ctx, req)` | Valida nombres, lee, modifica y aplica |
| `normalizeAdd` | Valida backend, servidor, dirección, puerto, peso, modo y balanceo |
| `buildServerLine(req)` | Renderiza `    server nombre addr:puerto check weight N backup` |
| `appendBackend` | Añade bloque `backend` con `mode` y `balance` |
| `applyAndReload` | Respaldo, escritura, `haproxy -c`, recarga y restauración |
| `configLines` / `joinConfig` | Normalización de saltos de línea |
| `sectionHeader` | Detecta cabeceras de sección de HAProxy |
| `findBackend` | Devuelve el rango `[inicio, fin)` del backend |

`applyAndReload` es el mecanismo de seguridad del módulo:

1. `cp -a <cfg> <cfg>.np2bak`.
2. Escribe la nueva configuración (0644).
3. `haproxy -c -f <cfg>`.
4. Si falla, `cp -a <cfg>.np2bak <cfg>` y falla el informe.
5. Si pasa, `systemctl reload <servicio>`.
6. Si la recarga falla, advierte y falla (la configuración válida queda escrita).
7. Nota la ubicación del respaldo y resume.

`ParseBackends` reconoce cabeceras `backend`, `frontend`, `listen`, `global`,
`defaults`, `peers`, `resolvers`, `userlist`, `cache`, `program`, `ring`,
`mailers`, `log-forward`, `http-errors`, `fcgi-app`, `crt-store`, `traces`. Solo
acumula servidores cuando la sección actual es `backend`.

#### 5.6.9 `httpaas.go` — HTTP como servicio

Tipo: `HTTPaaSManager`.

| Campo | Función |
| --- | --- |
| `opts config.HTTPaaSConfig` | Plantilla, red, rango de IP, DNS, credenciales y rutas. |
| `host runner.Runner` | Runner **local** para `VBoxManage` y `nsupdate` (opcionalmente con `sudo -n`). |
| `mu sync.Mutex` | Serializa aprovisionamiento, borrado, refresco e inventario. |
| `instances map[string]HTTPaaSInstance` | Inventario en memoria, indexado por hostname. |
| `baseDisk string` | Disco de la plantilla detectado; se usa para impedir su borrado. |

La persistencia está en `./httpaas/instances.json` (versión 1, JSON `0600`,
`CreateTemp` + `Rename`) y los discos diferenciales en `./httpaas/disks/`.

| Función | Descripción |
| --- | --- |
| `NewHTTPaaSManager(opts)` | Aplica valores por omisión, resuelve rutas absolutas y carga el inventario. |
| `load` / `saveLocked` | Lee y publica el JSON validando hostname, IP y nombre de VM. |
| `List` / `Refresh` | Copia ordenada para el tablero y consulta `showvminfo` para actualizar estado. |
| `Provision(ctx, req)` | Flujo completo de creación y despliegue; cualquier fallo dispara *rollback*. |
| `Delete(ctx, hostname)` | Apaga, borra DNS, desregistra la VM, elimina el disco y actualiza el inventario. |
| `checkPrerequisites` | Comprueba `VBoxManage`, red host-only/puente, plantilla apagada, disco SATA, `nsupdate` y llave TSIG. |
| `resolveBaseDisk` | Sube por la cadena `showmediuminfo` hasta la raíz y exige que sea `multiattach` y accesible. |
| `createDisk` / `createVM` | `createmedium disk --diffparent`, `createvm`, `modifyvm`, controlador SATA y `storageattach`. |
| `waitGuest` | Sondea Guest Additions con timeout (`BootTimeout`) antes de configurar. |
| `configureGuest` | Hostname, IP estática, `/etc/hosts`, `/etc/resolv.conf`, vhost y Apache. |
| `ensureApache` | Comprueba binarios y, si faltan, instala `apache2`/`unzip` con `apt-get`. |
| `deployContent` | Limpia el document root, transfiere el zip, extrae y ajusta `www-data`. |
| `registerDNS` / `removeDNS` | `nsupdate -k` con borrado previo y `send` por `stdin`. |
| `rollback` | Deshace DNS, VM y disco conservando el informe de cada paso. |
| `removeDisk` | `closemedium --delete` y, si el medio ya no está registrado, borrado directo. |
| `buildDNSUpdate` | Genera el protocolo `server`/`zone`/`update delete`/`update add`/`send`. |
| `buildStaticInterfaces` / `buildVHost` / `addHostEntry` | Archivos generados para red, Apache y `/etc/hosts`. |
| `validateZipPayload` | Inspecciona la tabla central del zip antes de transferirlo. |
| `nextFreeIP` | Recorre el rango y omite IP usadas, puerta de enlace y DNS. |
| `parseMachineReadable` / `discoverTemplateDisk` | Interpretan `showvminfo --machinereadable`. |

Flujo de `Provision`:

1. Valida hostname (RFC 1123 y nombres reservados), tamaño y contenido del zip.
2. Bloquea el mutex y reserva la primera IP libre del rango.
3. Ejecuta el *preflight* sin escribir nada.
4. Crea el disco hijo de la plantilla y la VM, la enciende en modo *headless*.
5. Espera Guest Additions y configura red, hostname, Apache y contenido.
6. Registra el A con `nsupdate`; solo entonces persiste la instancia.
7. Si algo falla, el `defer` de *rollback* limpia DNS, VM y disco.

Sin `.zip`, el paso 5 escribe un `index.html` provisional; `Publish` despliega
el contenido después sobre la VM existente (encendiéndola si hace falta).

`Delete` sigue el orden inverso y exige que `inst.VM` sea exactamente
`<prefijo><host>` (o el antiguo `httpaas-<host>`); así una entrada manipulada del inventario no puede borrar
`template_server`, `dns_server` ni una VM ajena.

### 5.7 `internal/web`

#### 5.7.1 `server.go`

| Elemento | Descripción |
| --- | --- |
| `artifactLink` | Enlace visible: ID, nombre, filename, nota, tamaño |
| `view` | Modelo de todas las plantillas |
| `Server` | Config, runner, ocho managers, plantillas y almacén de artefactos |
| `New(cfg, r)` | Construye managers, almacén (TTL 30 min) y parsea plantillas |
| `parseTemplates` | Parsea `layout.html`, `partials.html` y la página concreta |
| `Handler` | Registra rutas, estáticos y middlewares |
| `Close` | Cierra el runner |
| `newView` | Crea el modelo con target/modo actuales |
| `render` | Refresca target/modo y ejecuta `"layout"` |
| `finalize` | Guarda artefactos y copia el error |
| `opContext` | Contexto con `CommandTimeout` |
| `recoverer` | Recupera *panics* y devuelve 500 |
| `logRequests` | Registra método, ruta, duración y target |
| `basicAuth` | HTTP Basic opcional |
| `artifactStore` | Mapa ID → artefacto con expiración |
| `put` / `get` / `purgeLocked` | Alta, consulta y purga |
| `randomID` | 16 bytes aleatorios en hex |

Orden real de los middlewares (de fuera hacia dentro):

```
basicAuth → logRequests → recoverer → mux
```

Por eso `/salud` y `/estaticos/` también quedan protegidos si se configura
autenticación.

#### 5.7.2 `handlers.go`

Un manejador por ruta. Patrón general:

1. `v := s.newView(página, título, secciónActiva)`.
2. `ctx, cancel := s.opContext(r)` + `defer cancel()`.
3. Construir la petición del módulo desde `r.FormValue`.
4. `v.Report = s.<manager>.<Operación>(ctx, req)`.
5. `s.finalize(v)`.
6. Opcionalmente guardar el formulario (`formValues`) y recargar datos.
7. `s.render(w, página, v)`.

Funciones auxiliares:

- `loadInstalled`, `loadServices`, `loadHostname`, `loadUsers`, `loadHAProxy`:
  recargan los datos de la página y no pisan un error previo.
- `issueRequest(r)`: traduce los campos del formulario al `IssueRequest`.
- `readUploads(form)`: lee los archivos del `multipart`, limita cada uno a
  64 MiB (`maxUploadSize`) y descarta vacíos.
- `splitNonEmpty(s)`: convierte el textarea de entorno en líneas no vacías.
- `formValues(r)`: primer valor de cada campo para repintar el formulario.
- `handleDownload`: busca el artefacto por ID, responde 404 si expiró, fija
  `Content-Type`, `Content-Disposition: attachment` y `Cache-Control: no-store`.
- `sanitizeFilename`: quita comillas, saltos y sustituye `/` por `_`.

`handleServiceDeploy` es el único que usa `multipart`:

- `r.ParseMultipartForm(64 << 20)`;
- `readUploads(r.MultipartForm)`;
- la casilla `ejecutable` se aplica a **todos** los archivos;
- el resto de campos se mapea directamente a `modules.DeployRequest`.

#### 5.7.3 Plantillas

Se embeben en el binario con `//go:embed templates/*.html`:

- `layout.html`: cabecera, barra lateral, título, alertas, informe y `footer`.
- `partials.html`: `"links"` (descargas) y `"report"` (resultado completo con
  pasos y salida técnica).
- `dashboard.html`: tarjetas de acceso y explicación del flujo.
- `packages.html`, `rootssh.html`, `issue.html`, `hostname.html`, `users.html`,
  `services.html`, `haproxy.html`: formularios y tablas.

`html/template` escapa automáticamente los valores, incluidos los mensajes de
error y la salida de los comandos. El informe muestra:

- estado global y resumen;
- notas y advertencias;
- enlaces de descarga;
- por cada paso: transporte, título, comando (`$`), comando host (`↳`),
  `stdout`, `stderr` y error.

Los estilos viven en `static/app.css`, embebido con `//go:embed static/*` y
servido bajo `/estaticos/`.

---

## 6. Seguridad

El diseño aplica varias capas:

### 6.1 Ejecución sin shell

- Los módulos solo construyen **argv**. No hay `sh -c` con datos del usuario.
- SSH necesita una cadena: `ShellQuote` cotiza cada argumento con comillas
  simples POSIX y neutraliza metacaracteres.
- GuestControl usa un script **fijo**; el comando llega como un parámetro ya
  cotizado y se evalúa con `eval "$1"`. El script no interpola datos del usuario.

### 6.2 Validación estricta

- Cada valor que llega a un comando o archivo de configuración pasa por
  `internal/validate`.
- Se rechazan saltos de línea y caracteres de control en textos de una línea.
- `ExecArgs` rechaza `;&|<>$`\\"'(){}[]*?!~`.
- `IssueMessage` escapa barras invertidas.
- Las rutas absolutas se limpian y se rechaza el segmento `..`.
- Los nombres de archivo no admiten `/`.
- Los nombres de usuario se comparan con una denylist de cuentas del sistema.

### 6.3 Protecciones específicas

- **Usuarios**: no se eliminan cuentas con UID `< 1000` ni nombres reservados;
  se bloquea la contraseña al crear.
- **SSH**: la llave del host se verifica con `known_hosts` salvo que se habilite
  explícitamente `--ssh-insecure-ignore-host-key`. Sin ninguna de las dos
  opciones, `NewSSH` falla.
- **SSH de root**: la configuración se escribe en un *drop-in* y se valida con
  `sshd -t` antes de recargar.
- **systemd**: la unidad se valida en `/tmp` con `systemd-analyze verify` antes de
  reemplazar la instalada; el `ExecStart` debe existir y ser ejecutable.
- **HAProxy**: se respalda, se valida con `haproxy -c` y se restaura si falla.
- **HTTPaaS**: el hostname se valida con RFC 1123 y una denylist; el zip se
  inspecciona antes de transferirse (sin `..`, rutas absolutas, `\`, enlaces
  simbólicos, tipos especiales ni exceso de entradas/tamaño). `VBoxManage` usa
  `argv`; `nsupdate` recibe el protocolo por `stdin` y la llave TSIG `0600` por
  `-k` (nunca por argumentos). El inventario se escribe de forma atómica con
  permisos `0600` y las operaciones que lo modifican están serializadas. Las VM
  `template_server`/`dns_server` y el disco base están protegidos, y todo fallo
  de aprovisionamiento ejecuta *rollback*.
- **Contraseña de Guest Additions**: se redacta en `HostCommand` y en el argv
  mostrado; se recomienda `NP2_GUEST_PASSWORD` y un usuario con privilegios
  mínimos. Aun así, mientras `VBoxManage` se ejecuta, el argumento
  `--password=...` es visible en la lista de procesos del host.
- **HTTP**: autenticación Basic opcional, límite de subida de 64 MiB por archivo,
  `Cache-Control: no-store` en descargas, nombre de archivo saneado y
  `recoverer` para no filtrar *stack traces*.
- **Plantillas**: `html/template` escapa todo por defecto.

### 6.4 Qué **no** hace

- No implementa CSRF.
- No sirve TLS; se espera un proxy inverso si se expone en red.
- Basic Auth compara con `!=` (no en tiempo constante).
- No hay rate limiting ni bloqueo por intentos.
- No hay registro de auditoría persistente; los informes viven en la respuesta
  HTTP y los artefactos en memoria.

---

## 7. Pruebas automáticas

`go test ./...` cubre las piezas puras y los mecanismos delicados:

| Archivo | Qué prueba |
| --- | --- |
| `config/config_test.go` | `auto` permite *bootstrap* sin llave local; `ssh` y `auto` solo SSH siguen exigiendo llave; HTTPaaS por omisión válido y dominio inválido rechazado |
| `runner/runner_test.go` | `buildArgv` con/sin sudo, cotización ante inyección y comillas simples, captura de salida y códigos del runner local |
| `runner/guestcontrol_test.go` | Parseo del trailer de salida, token ajeno, redacción de contraseña, orden de `baseArgs`, `runArgs`, argumentos vacíos, `copyfrom`/`copyto`, rechazo de `stdin`, timeout |
| `runner/failover_test.go` | Fallback ante fallo de transporte, permanencia ante fallo de comando, promoción a SSH, conservación de Guest Additions si la promoción falla, transferencia según transporte activo, clasificación `TransportFailed` |
| `op/report_test.go` | Un paso opcional no falla el informe; uno obligatorio sí |
| `validate/validate_test.go` | Paquetes, usuarios, hostname, servicios, direcciones/puertos, rutas/archivos, saltos de línea, metacaracteres, mensaje de issue, usuario de ejecución y denylist |
| `modules/haproxy_test.go` | Parseo de backends, alta en backend existente, duplicados, backend inexistente, creación, baja y servidor inexistente |
| `modules/httpaas_test.go` | Validación de zip (normal, traversal, symlink, exceso de entradas), protocolo `nsupdate`, asignación de IP, interfaces/vhost `/etc/hosts`, detección de disco e interfaces y estados de VirtualBox |
| `modules/modules_test.go` | `/etc/hosts`, campos del banner, generación de unidad, entorno inválido, normalización de `.service`, permisos de `ExecStart`, parseo de propiedades |

El diseño favorece funciones puras (`BuildIssue`, `UpdateHosts`, `buildUnit`,
`ParseBackends`, `AddServer`, `RemoveServer`, `fileMode`, `parseGuestExit`,
`runArgs`, `copyFromArgs`, `copyToArgs`) precisamente para poder probarlas sin
una VM.

---

## 8. Compilación y ejecución

```bash
# Compilar
go build -o appgo .
make build

# Pruebas y análisis
make test
make vet
make fmt

# Versión inyectada
go build -ldflags "-X main.version=1.0.0" -o appgo .
```

Ejemplos de ejecución:

```bash
# Host: SSH con respaldo Guest Additions
./appgo -listen :8080 -mode auto \
  -ssh-host 192.168.1.10 -ssh-user root -ssh-key ./root_id_ed25519 \
  -guestcontrol-vm VM_A -guestcontrol-user root

# Host: solo Guest Additions (etapa inicial)
./appgo -mode guestcontrol \
  -guestcontrol-vm VM_A -guestcontrol-user root

# Dentro de la VM
sudo ./appgo -mode local -sudo

# Host: solo SSH
./appgo -mode ssh -ssh-host 192.168.1.10 -ssh-user root \
  -ssh-key ./root_id_ed25519 -ssh-known-hosts ~/.ssh/known_hosts

# Host: módulo HTTPaaS (VBoxManage + nsupdate + Guest Additions)
export NP2_HTTPAAS_PASSWORD='***'
./appgo -listen :8080 -mode local -httpaas \
  -httpaas-template template_server -httpaas-network vboxnet0 \
  -httpaas-dns-server 192.168.10.10 -httpaas-dns-key /etc/bind/httpaas.key \
  -httpaas-guest-user np2deploy
```

El `Makefile` ofrece además `run-auto`, `run-guestcontrol`, `run`, `run-ssh` y
`run-httpaas` con valores de ejemplo, y `clean` para borrar el binario.

---

## 9. Observaciones y limitaciones

Son comportamientos reales del código, útiles para una revisión o una mejora:

1. **El estado del transporte no persiste.** Si la consola se promueve a SSH y se
   reinicia, hay que volver a arrancar con una llave local o promover de nuevo.
2. **Tras un fallback en modo `auto` no se reintenta SSH.** `NewFailover` no
   configura `connect`, así que `PromoteSSH` no puede reconectar; una vez en
   Guest Additions, se queda ahí hasta reiniciar. Solo `NewBootstrapFailover`
   tiene `connect`.
3. **Un timeout puede contar como fallo de transporte.** En SSH, si el contexto
   se cancela, `Result.Err = ctx.Err()` no es un `*ssh.ExitError`, por lo que
   `TransportFailed()` devuelve `true` y el *failover* podría cambiar a Guest
   Additions. Es una decisión razonable, pero conviene conocerla.
4. **`guestcontrol` muestra la contraseña en la lista de procesos** del host
   mientras `VBoxManage` se ejecuta. Se redacta en el informe, no en el sistema
   operativo.
5. **Artefactos de descarga en memoria.** `artifactStore` guarda las llaves
   privadas en RAM durante 30 minutos. No se escriben a disco, pero tampoco
   sobreviven a un reinicio.
6. **Sin CSRF ni TLS.** Adecuado para un laboratorio; para producción haría falta
   al menos un proxy con TLS, CSRF y quizá autenticación más fuerte.
7. **Sin bloqueo global de operaciones.** Dos peticiones concurrentes pueden
   modificar el mismo archivo o servicio. Los módulos son idempotentes en algunos
   casos, pero no hay exclusión mutua por recurso.
8. **`-ssh-port` se valida incluso en modo `local`.** Es inofensivo, pero la
   comprobación es global.
9. **`hostname.Set` marca el informe como fallido si `hostname -- name` falla**,
   aunque luego continúe y actualice `/etc/hosts`; el `require` ya registró el
   error.
10. **Un *drop-in* inválido de `sshd` puede quedar en disco.** Si `sshd -t` falla,
    `hardenSSHD` no elimina el archivo; solo advierte de que la configuración
    anterior sigue activa hasta reiniciar. Una revocación automática sería más
    segura.
11. **`AddServer` tiene un mensaje de error redundante** cuando el backend no
    existe: repite el nombre en lugar de mostrar el archivo de configuración.
12. **Funciones sin uso actual**: `validate.NoShellMeta` y `modules.trimOr`.
13. **`readUploads` limita cada archivo a 64 MiB**, pero no el total del
    formulario; `ParseMultipartForm` recibe 64 MiB como memoria, no como límite
    estricto del conjunto.
14. **El formulario no usa PRG**; recargar tras un POST repite la operación.
15. **La lista de paquetes se limita a 300 filas** en la interfaz
    (`Installed(ctx, 300)`), aunque el sistema tenga más.
16. **HTTPaaS serializa todo el aprovisionamiento** con un mutex: una creación
    larga bloquea el resto de operaciones y las consultas del tablero hasta que
    termina. Es deliberado para no repartir dos veces la misma IP, pero limita la
    concurrencia.
17. **HTTPaaS depende de Guest Additions para el arranque** de cada instancia,
    porque todavía no tiene IP. La red se aplica en caliente y se persiste en
    `/etc/network/interfaces.d` (ifupdown); una plantilla con NetworkManager o
    systemd-networkd necesitaría otra configuración para conservarla al
    reiniciar.
18. **El disco hijo depende de la cadena de la plantilla.** Mientras existan
    instancias no deben modificarse ni eliminarse el disco base ni las
    instantáneas de `template_server`. El módulo protege el borrado, pero no
    puede impedir una modificación manual desde VirtualBox.
19. **Sin TLS, cuotas por usuario ni límites de CPU/disco en HTTPaaS.** El módulo
    otorga control sobre VirtualBox y Bind9; conviene proteger la consola y
    ejecutarla en un host de laboratorio.

---

## 10. Apéndices

### 10.1 Tabla resumen de comandos ejecutados

| Módulo | Comandos principales |
| --- | --- |
| Paquetes | `env DEBIAN_FRONTEND=noninteractive apt-get update/install/remove/purge/autoremove`, `dpkg-query` |
| Root SSH | `mkdir`, `chmod`, `rm`, `ssh-keygen`, `cat`, `tee`, `sshd -t`, `systemctl reload ssh|sshd`, `sshd -T` |
| Issue | `tee /etc/issue`, `chmod`, `cat` |
| Hostname | `hostnamectl set-hostname`, `hostname`, `tee /etc/hosts`, `chmod`, `cat` |
| Usuarios | `getent passwd`, `id -u`, `useradd`, `passwd -l`, `usermod -aG sudo`, `mkdir`, `ssh-keygen`, `tee`, `chown`, `chmod`, `loginctl terminate-user`, `userdel` |
| systemd | `systemctl list-unit-files/status/is-active/is-enabled/show/start/stop/restart/enable/disable/daemon-reload`, `systemd-analyze verify`, `journalctl`, `mkdir`, `chown`, `rm`, `tee`, `chmod` |
| HAProxy | `cp -a`, `tee`, `haproxy -c`, `systemctl reload` |
| GuestControl | `VBoxManage --nologo guestcontrol <VM> run/copyfrom/copyto ...` |
| SSH | cualquier argv cotizado a través de una sesión SSH |

### 10.2 Glosario

- **Transporte**: implementación de `runner.Runner` que ejecuta comandos en un
  destino (`local`, `ssh`, `guestcontrol`).
- **Promoción**: cambio en caliente de Guest Additions a SSH usando la llave
  recién generada, sin reiniciar la consola.
- **Failover**: cambio automático del transporte principal al de respaldo cuando
  falla la conectividad.
- **Artefacto**: archivo generado por una operación (por ejemplo, una llave
  privada) que el usuario puede descargar.
- **Paso (`Step`)**: registro de un comando ejecutado dentro de una operación.
- **Informe (`Report`)**: resultado completo de una operación, con estado,
  resumen, notas, advertencias, pasos y artefactos.
- **Bootstrap**: etapa inicial en la que aún no existe SSH y la consola usa
  Guest Additions para generar la llave.

### 10.3 Correspondencia entre el enunciado y el código

| Requisito | Implementación |
| --- | --- |
| Aplicación en Go | `main.go`, paquetes `internal/*` |
| Todas las operaciones no interactivas | `-y`, `DEBIAN_FRONTEND=noninteractive`, `sudo -n`, `ssh-keygen -N ""` |
| Validación de entradas | `internal/validate` |
| Informe con resultado y diagnóstico | `internal/op` |
| Instalación de aplicaciones | `internal/modules/packages.go` |
| Llave SSH de root | `internal/modules/rootssh.go` |
| Mensaje de inicio | `internal/modules/issue.go` |
| Nombre de host | `internal/modules/hostname.go` |
| Usuarios | `internal/modules/users.go` |
| Servicios systemd | `internal/modules/systemd.go` |
| Balanceador HAProxy | `internal/modules/haproxy.go` |
| HTTPaaS (VM, DNS, Apache y `.zip`) | `internal/modules/httpaas.go`, `internal/web/handlers.go`, `scripts/` |
| Transporte Guest Additions | `internal/runner/guestcontrol.go` |
| Transporte SSH | `internal/runner/ssh.go` |
| Cambio automático Guest Additions → SSH | `internal/runner/failover.go` + `rootssh.promoteToSSH` |
| Respaldo SSH → Guest Additions | `internal/runner/failover.go` |
| Interfaz web | `internal/web` + plantillas embebidas |

---

*Fin del documento.*
