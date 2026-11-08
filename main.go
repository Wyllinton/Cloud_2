// Command appgo is the NP2 automation console: a small web application, written
// in Go, that automates the administration of a Debian 13 virtual machine
// (packages, SSH keys, login banner, host name, users, systemd services and an
// HAProxy load balancer).
//
// The console can reach the managed machine in three ways:
//
//	local          run inside the guest (for example through the Guest Additions
//	               shared folder during the bootstrap phase).
//	ssh            control the guest over key-only SSH.
//	guestcontrol   control the guest from the host through VBoxManage and the
//	               Guest Additions.
//
// The default mode, auto, tries SSH first and falls back to the Guest Additions
// when SSH cannot be reached.
//
// Usage examples:
//
//	# Host: SSH when possible, Guest Additions as a fallback.
//	appgo -mode auto \
//	  -ssh-host 192.168.1.10 -ssh-user root -ssh-key root_id_ed25519 \
//	  -guestcontrol-vm VM_A -guestcontrol-user root -guestcontrol-password '***'
//
//	# Host: Guest Additions only (no SSH yet).
//	appgo -mode guestcontrol -guestcontrol-vm VM_A -guestcontrol-user root -guestcontrol-password '***'
//
//	# Inside the guest.
//	appgo -mode local -sudo
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"np2/appgo/internal/config"
	"np2/appgo/internal/runner"
	"np2/appgo/internal/web"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatalf("error: %v", err)
	}
}

func run() error {
	cfg := config.Default()

	var showVersion bool
	flag.StringVar(&cfg.Listen, "listen", cfg.Listen, "dirección de escucha HTTP, por ejemplo :8080")
	flag.StringVar((*string)(&cfg.Mode), "mode", string(cfg.Mode), "transporte: local, ssh, guestcontrol o auto")
	flag.BoolVar(&cfg.Sudo, "sudo", cfg.Sudo, "prefijar cada comando (SSH/guestcontrol) con sudo -n")
	flag.DurationVar(&cfg.CommandTimeout, "timeout", cfg.CommandTimeout, "tiempo máximo por operación")
	flag.BoolVar(&showVersion, "version", false, "mostrar la versión y salir")

	flag.StringVar(&cfg.SSH.Host, "ssh-host", cfg.SSH.Host, "host de la máquina virtual (modo ssh/auto)")
	flag.IntVar(&cfg.SSH.Port, "ssh-port", cfg.SSH.Port, "puerto SSH (modo ssh/auto)")
	flag.StringVar(&cfg.SSH.User, "ssh-user", cfg.SSH.User, "usuario SSH (modo ssh/auto)")
	flag.StringVar(&cfg.SSH.PrivateKeyPath, "ssh-key", cfg.SSH.PrivateKeyPath, "ruta de la llave privada SSH (modo ssh/auto)")
	flag.StringVar(&cfg.SSH.PrivateKeyPassphrase, "ssh-key-passphrase", "", "frase de paso de la llave privada, si tiene una")
	flag.StringVar(&cfg.SSH.KnownHostsFile, "ssh-known-hosts", "", "archivo known_hosts para verificar el host remoto")
	flag.BoolVar(&cfg.SSH.InsecureIgnoreHostKey, "ssh-insecure-ignore-host-key", false, "no verificar la llave del host remoto (solo para laboratorio)")

	flag.StringVar(&cfg.GuestControl.VM, "guestcontrol-vm", cfg.GuestControl.VM, "nombre o UUID de la VM de VirtualBox (modo guestcontrol/auto)")
	flag.StringVar(&cfg.GuestControl.User, "guestcontrol-user", cfg.GuestControl.User, "usuario existente en el sistema invitado")
	flag.StringVar(&cfg.GuestControl.Password, "guestcontrol-password", cfg.GuestControl.Password, "contraseña del usuario invitado (o variable NP2_GUEST_PASSWORD)")
	flag.StringVar(&cfg.GuestControl.PasswordFile, "guestcontrol-password-file", cfg.GuestControl.PasswordFile, "valor opcional de --passwordfile de VBoxManage")
	flag.StringVar(&cfg.GuestControl.Domain, "guestcontrol-domain", cfg.GuestControl.Domain, "dominio del usuario (invitados Windows)")
	flag.StringVar(&cfg.GuestControl.VBoxManage, "guestcontrol-vboxmanage", cfg.GuestControl.VBoxManage, "ruta del ejecutable VBoxManage")

	flag.StringVar(&cfg.HAProxy.ConfigPath, "haproxy-config", cfg.HAProxy.ConfigPath, "ruta de haproxy.cfg")
	flag.StringVar(&cfg.HAProxy.Service, "haproxy-service", cfg.HAProxy.Service, "nombre del servicio HAProxy a recargar")

	flag.BoolVar(&cfg.HTTPaaS.Enabled, "httpaas", cfg.HTTPaaS.Enabled, "habilitar el módulo HTTP como servicio")
	flag.StringVar(&cfg.HTTPaaS.VBoxManage, "httpaas-vboxmanage", cfg.HTTPaaS.VBoxManage, "ruta de VBoxManage en el host")
	flag.BoolVar(&cfg.HTTPaaS.HostSudo, "httpaas-host-sudo", cfg.HTTPaaS.HostSudo, "ejecutar VBoxManage/nsupdate con sudo -n")
	flag.StringVar(&cfg.HTTPaaS.TemplateVM, "httpaas-template", cfg.HTTPaaS.TemplateVM, "máquina virtual plantilla")
	flag.StringVar(&cfg.HTTPaaS.TemplateDisk, "httpaas-template-disk", cfg.HTTPaaS.TemplateDisk, "disco base de la plantilla (se autodetecta si está vacío)")
	flag.StringVar(&cfg.HTTPaaS.VMPrefix, "httpaas-vm-prefix", cfg.HTTPaaS.VMPrefix, "prefijo del nombre de las VM (vacío: la VM se llama como el host)")
	flag.StringVar(&cfg.HTTPaaS.DiskDir, "httpaas-disk-dir", cfg.HTTPaaS.DiskDir, "directorio de discos diferenciales")
	flag.StringVar(&cfg.HTTPaaS.NetworkMode, "httpaas-network-mode", cfg.HTTPaaS.NetworkMode, "red de las instancias: hostonly o bridged")
	flag.StringVar(&cfg.HTTPaaS.NetworkName, "httpaas-network", cfg.HTTPaaS.NetworkName, "red host-only o adaptador puente")
	flag.IntVar(&cfg.HTTPaaS.Netmask, "httpaas-netmask", cfg.HTTPaaS.Netmask, "prefijo de red de las instancias (por ejemplo 24)")
	flag.StringVar(&cfg.HTTPaaS.Gateway, "httpaas-gateway", cfg.HTTPaaS.Gateway, "puerta de enlace de las instancias")
	flag.StringVar(&cfg.HTTPaaS.IPStart, "httpaas-ip-start", cfg.HTTPaaS.IPStart, "primera IP asignable")
	flag.StringVar(&cfg.HTTPaaS.IPEnd, "httpaas-ip-end", cfg.HTTPaaS.IPEnd, "última IP asignable")
	flag.StringVar(&cfg.HTTPaaS.Domain, "httpaas-domain", cfg.HTTPaaS.Domain, "dominio de los enlaces (cloud.local)")
	flag.StringVar(&cfg.HTTPaaS.DNSServer, "httpaas-dns-server", cfg.HTTPaaS.DNSServer, "IP del servidor Bind9")
	flag.StringVar(&cfg.HTTPaaS.DNSZone, "httpaas-dns-zone", cfg.HTTPaaS.DNSZone, "zona DNS que se actualiza")
	flag.StringVar(&cfg.HTTPaaS.DNSKeyFile, "httpaas-dns-key", cfg.HTTPaaS.DNSKeyFile, "llave TSIG para nsupdate (chmod 600)")
	flag.IntVar(&cfg.HTTPaaS.DNSTTL, "httpaas-dns-ttl", cfg.HTTPaaS.DNSTTL, "TTL de los registros A")
	flag.StringVar(&cfg.HTTPaaS.GuestUser, "httpaas-guest-user", cfg.HTTPaaS.GuestUser, "usuario del invitado con Guest Additions")
	flag.StringVar(&cfg.HTTPaaS.GuestPassword, "httpaas-guest-password", cfg.HTTPaaS.GuestPassword, "contraseña del usuario invitado (o NP2_HTTPAAS_PASSWORD)")
	flag.BoolVar(&cfg.HTTPaaS.GuestSudo, "httpaas-guest-sudo", cfg.HTTPaaS.GuestSudo, "prefijar comandos del invitado con sudo -n")
	flag.StringVar(&cfg.HTTPaaS.GuestInterface, "httpaas-guest-iface", cfg.HTTPaaS.GuestInterface, "interfaz de red del invitado (se autodetecta si está vacía)")
	flag.StringVar(&cfg.HTTPaaS.ApacheRoot, "httpaas-apache-root", cfg.HTTPaaS.ApacheRoot, "directorio padre de los sitios Apache")
	flag.IntVar(&cfg.HTTPaaS.HTTPPort, "httpaas-http-port", cfg.HTTPaaS.HTTPPort, "puerto HTTP de los enlaces")
	flag.IntVar(&cfg.HTTPaaS.MemoryMB, "httpaas-memory", cfg.HTTPaaS.MemoryMB, "memoria RAM de cada instancia (MiB)")
	flag.IntVar(&cfg.HTTPaaS.CPUs, "httpaas-cpus", cfg.HTTPaaS.CPUs, "vCPUs de cada instancia")
	flag.DurationVar(&cfg.HTTPaaS.BootTimeout, "httpaas-boot-timeout", cfg.HTTPaaS.BootTimeout, "espera máxima de Guest Additions")
	flag.IntVar(&cfg.HTTPaaS.MaxUploadMiB, "httpaas-max-upload", cfg.HTTPaaS.MaxUploadMiB, "tamaño máximo del .zip (MiB)")
	flag.StringVar(&cfg.HTTPaaS.StatePath, "httpaas-state", cfg.HTTPaaS.StatePath, "inventario JSON de instancias")

	flag.StringVar(&cfg.AuthUser, "auth-user", "", "usuario para proteger la consola con HTTP Basic")
	flag.StringVar(&cfg.AuthPass, "auth-pass", "", "contraseña para proteger la consola con HTTP Basic")

	flag.Parse()

	if showVersion {
		fmt.Printf("appgo %s\n", version)
		return nil
	}

	// Allow the guest password to be supplied through the environment so it is
	// not stored in shell history.
	if cfg.GuestControl.Password == "" {
		cfg.GuestControl.Password = os.Getenv("NP2_GUEST_PASSWORD")
	}
	if cfg.HTTPaaS.GuestPassword == "" {
		cfg.HTTPaaS.GuestPassword = os.Getenv("NP2_HTTPAAS_PASSWORD")
	}
	if cfg.HTTPaaS.GuestPassword == "" {
		cfg.HTTPaaS.GuestPassword = cfg.GuestControl.Password
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	transport, err := buildRunner(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = transport.Close() }()

	srv, err := web.New(cfg, transport)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// The write timeout must be generous: installing packages or
		// transferring application files can take minutes.
		WriteTimeout: cfg.CommandTimeout + time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	log.Printf("consola NP2 escuchando en %s | transporte=%s destino=%s | versión=%s",
		cfg.Listen, transport.Kind(), transport.Target(), version)
	if cfg.AuthUser != "" {
		log.Printf("autenticación HTTP Basic habilitada para el usuario %q", cfg.AuthUser)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("servidor HTTP: %w", err)
	case <-ctx.Done():
		log.Print("señal recibida, cerrando la consola…")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// buildRunner creates the transport that connects the console with the managed
// machine.
func buildRunner(cfg config.Config) (runner.Runner, error) {
	var gc *runner.GuestControl
	if cfg.HasGuestControl() {
		gc = runner.NewGuestControl(runner.GuestControlOptions{
			VBoxManage:   cfg.GuestControl.VBoxManage,
			VM:           cfg.GuestControl.VM,
			User:         cfg.GuestControl.User,
			Password:     cfg.GuestControl.Password,
			PasswordFile: cfg.GuestControl.PasswordFile,
			Domain:       cfg.GuestControl.Domain,
			Sudo:         cfg.Sudo,
			Timeout:      cfg.CommandTimeout,
		})
	}

	switch cfg.Mode {
	case config.ModeLocal:
		if cfg.Sudo {
			log.Print("modo local con sudo: se ejecutará `sudo -n`, que falla si se exige contraseña")
		}
		return runner.NewLocal(cfg.Sudo), nil

	case config.ModeSSH:
		return newSSH(cfg)

	case config.ModeGuestControl:
		if gc == nil {
			return nil, errors.New("el modo guestcontrol requiere -guestcontrol-vm")
		}
		pingGuestControl(gc)
		if cfg.HasSSH() {
			log.Printf("modo guestcontrol: SSH se activará automáticamente tras generar la llave para %s", sshTarget(cfg))
			return runner.NewBootstrapFailover(gc, sshTarget(cfg), sshConnector(cfg)), nil
		}
		return gc, nil

	case config.ModeAuto:
		var sshRunner runner.Runner
		var sshErr error
		if cfg.HasSSH() {
			if cfg.SSH.PrivateKeyPath != "" {
				sshRunner, sshErr = newSSH(cfg)
			} else if gc == nil {
				return nil, errors.New("SSH no disponible y Guest Additions no está configurado: falta --ssh-key")
			}
			if sshErr != nil {
				if gc == nil {
					return nil, fmt.Errorf("SSH no disponible y Guest Additions no está configurado: %w", sshErr)
				}
				log.Printf("no se pudo establecer SSH (%v); se usará Guest Additions hasta que exista una llave válida", sshErr)
			}
		}

		switch {
		case sshRunner != nil && gc != nil:
			pingGuestControl(gc)
			log.Print("modo auto: SSH como transporte principal y Guest Additions como respaldo")
			return runner.NewFailover(sshRunner, gc), nil
		case sshRunner != nil:
			return sshRunner, nil
		case gc != nil && cfg.HasSSH():
			pingGuestControl(gc)
			log.Printf("modo auto: Guest Additions como transporte inicial; SSH se activará tras generar la llave para %s", sshTarget(cfg))
			return runner.NewBootstrapFailover(gc, sshTarget(cfg), sshConnector(cfg)), nil
		case gc != nil:
			log.Print("modo auto: Guest Additions como único transporte configurado")
			pingGuestControl(gc)
			return gc, nil
		default:
			return nil, errors.New("modo auto sin transporte configurado")
		}
	}

	return nil, fmt.Errorf("modo %q no soportado: use local, ssh, guestcontrol o auto", cfg.Mode)
}

func newSSH(cfg config.Config) (runner.Runner, error) {
	key, err := cfg.SSH.LoadPrivateKey()
	if err != nil {
		return nil, err
	}
	return newSSHWithKey(cfg, key)
}

// newSSHWithKey dials SSH with an explicit private key, so the console can use
// a key that was generated inside the guest and only exists in memory.
func newSSHWithKey(cfg config.Config, key []byte) (runner.Runner, error) {
	if cfg.SSH.KnownHostsFile == "" && !cfg.SSH.InsecureIgnoreHostKey {
		log.Print("aviso: sin --ssh-known-hosts no se verifica la llave del host remoto; use esta opción solo en un laboratorio")
	}
	remote, err := runner.NewSSH(runner.SSHOptions{
		Host:                  cfg.SSH.Host,
		Port:                  cfg.SSH.Port,
		User:                  cfg.SSH.User,
		PrivateKey:            key,
		PrivateKeyPassphrase:  cfg.SSH.PrivateKeyPassphrase,
		KnownHostsFile:        cfg.SSH.KnownHostsFile,
		InsecureIgnoreHostKey: cfg.SSH.InsecureIgnoreHostKey,
		Sudo:                  cfg.Sudo,
		Timeout:               15 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("conexión SSH establecida con %s", remote.Target())
	return remote, nil
}

// sshConnector builds the callback used to promote the active transport to SSH
// once a key pair exists.
func sshConnector(cfg config.Config) func([]byte) (runner.Runner, error) {
	return func(key []byte) (runner.Runner, error) {
		return newSSHWithKey(cfg, key)
	}
}

// sshTarget renders the configured destination even before a connection exists.
func sshTarget(cfg config.Config) string {
	port := cfg.SSH.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s@%s:%d", cfg.SSH.User, cfg.SSH.Host, port)
}

// pingGuestControl checks the Guest Additions channel without preventing the
// console from starting: the VM may be powered off now and started later.
func pingGuestControl(gc *runner.GuestControl) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := gc.Ping(ctx); err != nil {
		log.Printf("aviso: Guest Additions todavía no responde (%v); las operaciones fallarán hasta que la VM esté encendida y las Guest Additions activas", err)
		return
	}
	log.Printf("Guest Additions operativas en %s", gc.Target())
}
