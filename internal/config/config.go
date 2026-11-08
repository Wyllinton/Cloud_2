// Package config holds the runtime configuration of the automation console.
package config

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"np2/appgo/internal/validate"
)

var reVMPrefix = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Mode selects how commands reach the managed machine.
type Mode string

const (
	// ModeLocal runs commands on the host where the console runs. It is used
	// when the console itself runs inside the managed virtual machine.
	ModeLocal Mode = "local"
	// ModeSSH runs commands on a remote host over SSH authenticated by key.
	ModeSSH Mode = "ssh"
	// ModeGuestControl runs commands through the VirtualBox Guest Additions
	// using VBoxManage on the host.
	ModeGuestControl Mode = "guestcontrol"
	// ModeAuto tries SSH first and falls back to the Guest Additions when SSH
	// cannot be reached.
	ModeAuto Mode = "auto"
)

// Config is the complete application configuration.
type Config struct {
	Listen string
	Mode   Mode

	// Sudo prefixes every guest command with `sudo -n`. Use it when the account
	// used for SSH or Guest Additions is not root.
	Sudo bool

	// CommandTimeout bounds a single command execution.
	CommandTimeout time.Duration

	SSH          SSHConfig
	GuestControl GuestControlConfig
	HAProxy      HAProxyConfig
	HTTPaaS      HTTPaaSConfig

	// AuthUser and AuthPass, when set, protect the whole console with HTTP
	// Basic authentication.
	AuthUser string
	AuthPass string
}

// SSHConfig configures the SSH transport.
type SSHConfig struct {
	Host                  string
	Port                  int
	User                  string
	PrivateKeyPath        string
	PrivateKeyPassphrase  string
	KnownHostsFile        string
	InsecureIgnoreHostKey bool
}

// GuestControlConfig configures the Guest Additions transport.
type GuestControlConfig struct {
	// VBoxManage is the executable used to control VirtualBox.
	VBoxManage string
	// VM is the VirtualBox machine name or UUID.
	VM string
	// User is an existing account on the guest operating system.
	User string
	// Password is the guest account password.
	Password string
	// PasswordFile is an optional --passwordfile value.
	PasswordFile string
	// Domain is the Windows domain, when applicable.
	Domain string
}

// HAProxyConfig points at the load balancer configuration file.
type HAProxyConfig struct {
	ConfigPath string
	Service    string
}

// HTTPaaSConfig configures the managed Apache hosting module (HTTP as a
// service). The console runs on the VirtualBox host and creates one VM per
// instance from the template's disk.
type HTTPaaSConfig struct {
	Enabled bool

	// VBoxManage is the VirtualBox CLI. HostSudo prefixes host-side commands
	// with `sudo -n` (needed by some host-only network operations).
	VBoxManage string
	HostSudo   bool

	// TemplateVM is the name or UUID of the Debian template. Its current disk
	// is used as the parent of a differencing disk, so every instance shares
	// the read-only template data (the base disk is multiattach).
	TemplateVM string
	// TemplateDisk optionally fixes the parent disk path. When empty it is
	// discovered from the template's SATA controller.
	TemplateDisk string
	// DiskDir stores the differencing disks created for the instances.
	DiskDir string
	// VMPrefix is prepended to the hostname to build the VM name. It is empty
	// by default so the VM is named after the host (e.g. "paisajes").
	VMPrefix string

	// NetworkMode is "hostonly" or "bridged". NetworkName is the host-only
	// network name or the bridge adapter, depending on the mode.
	NetworkMode string
	NetworkName string

	// IPv4 configuration for the instances. IPStart and IPEnd are inclusive.
	Netmask int
	Gateway string
	IPStart string
	IPEnd   string

	// DNS settings. The console runs nsupdate against DNSServer for every
	// instance. DNSKeyFile is the TSIG key (0600); leave it empty only in a
	// lab where the zone allows updates from the host address.
	Domain     string
	DNSServer  string
	DNSZone    string
	DNSKeyFile string
	DNSTTL     int

	// Guest access for the new VM. The account must exist in the template and
	// have Guest Additions running. GuestSudo is required when it is not root.
	GuestUser      string
	GuestPassword  string
	GuestSudo      bool
	GuestInterface string

	// ApacheRoot is the parent of the per-instance document roots. HTTPPort is
	// used to build the dashboard link and is normally 80.
	ApacheRoot string
	HTTPPort   int

	// Resources assigned to each instance.
	MemoryMB int
	CPUs     int

	// BootTimeout bounds the wait for Guest Additions after powering on.
	BootTimeout time.Duration
	// MaxUploadMiB bounds the accepted .zip upload.
	MaxUploadMiB int

	// StatePath is the JSON inventory used to allocate addresses, list the
	// instances and delete them safely.
	StatePath string
}

// Default returns a configuration with safe defaults.
func Default() Config {
	return Config{
		Listen:         ":8080",
		Mode:           ModeAuto,
		CommandTimeout: 10 * time.Minute,
		SSH: SSHConfig{
			Port: 22,
			User: "root",
		},
		GuestControl: GuestControlConfig{
			VBoxManage: "VBoxManage",
			User:       "root",
		},
		HAProxy: HAProxyConfig{
			ConfigPath: "/etc/haproxy/haproxy.cfg",
			Service:    "haproxy",
		},
		HTTPaaS: HTTPaaSConfig{
			Enabled:      true,
			VBoxManage:   "VBoxManage",
			TemplateVM:   "template_server",
			DiskDir:      "./httpaas/disks",
			NetworkMode:  "hostonly",
			NetworkName:  "vboxnet0",
			Netmask:      24,
			Gateway:      "192.168.10.1",
			IPStart:      "192.168.10.30",
			IPEnd:        "192.168.10.99",
			Domain:       "cloud.local",
			DNSServer:    "192.168.10.10",
			DNSZone:      "cloud.local",
			DNSKeyFile:   "/etc/bind/httpaas.key",
			DNSTTL:       300,
			GuestUser:    "np2deploy",
			GuestSudo:    true,
			ApacheRoot:   "/var/www",
			HTTPPort:     80,
			MemoryMB:     1024,
			CPUs:         2,
			BootTimeout:  4 * time.Minute,
			MaxUploadMiB: 64,
			StatePath:    "./httpaas/instances.json",
		},
	}
}

// HasSSH reports whether an SSH target has been configured.
func (c Config) HasSSH() bool { return strings.TrimSpace(c.SSH.Host) != "" }

// HasGuestControl reports whether a Guest Additions target has been configured.
func (c Config) HasGuestControl() bool { return strings.TrimSpace(c.GuestControl.VM) != "" }

// Validate checks that the configuration is coherent.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeLocal:
	case ModeSSH:
		if err := c.SSH.validate(); err != nil {
			return err
		}
	case ModeGuestControl:
		if err := c.GuestControl.validate(); err != nil {
			return err
		}
	case ModeAuto:
		if !c.HasSSH() && !c.HasGuestControl() {
			return fmt.Errorf("el modo auto requiere --ssh-host (con --ssh-key) o --guestcontrol-vm")
		}
		if c.HasSSH() {
			// When Guest Additions is available the console can start without a
			// local key: the key is generated inside the guest later and used to
			// promote the transport to SSH. The key path is still required when
			// SSH is the only transport.
			validateSSH := c.SSH.validate
			if c.HasGuestControl() {
				validateSSH = c.SSH.validateBootstrap
			}
			if err := validateSSH(); err != nil {
				return fmt.Errorf("transporte SSH: %w", err)
			}
		}
		if c.HasGuestControl() {
			if err := c.GuestControl.validate(); err != nil {
				return fmt.Errorf("transporte Guest Additions: %w", err)
			}
		}
	default:
		return fmt.Errorf("modo %q desconocido: use local, ssh, guestcontrol o auto", c.Mode)
	}
	if c.SSH.Port < 1 || c.SSH.Port > 65535 {
		return fmt.Errorf("--ssh-port %d fuera de rango", c.SSH.Port)
	}
	if (c.AuthUser == "") != (c.AuthPass == "") {
		return fmt.Errorf("defina --auth-user y --auth-pass juntos")
	}
	if c.HTTPaaS.Enabled {
		if err := c.HTTPaaS.validate(); err != nil {
			return fmt.Errorf("módulo HTTPaaS: %w", err)
		}
	}
	return nil
}

// validate checks the HTTPaaS settings that can be verified without touching
// VirtualBox or the DNS server.
func (c HTTPaaSConfig) validate() error {
	if strings.TrimSpace(c.VBoxManage) == "" {
		return fmt.Errorf("--httpaas-vboxmanage no puede estar vacío")
	}
	if strings.TrimSpace(c.TemplateVM) == "" {
		return fmt.Errorf("--httpaas-template no puede estar vacío")
	}
	if p := c.VMPrefix; p != "" && !reVMPrefix.MatchString(p) {
		return fmt.Errorf("--httpaas-vm-prefix solo admite letras, números, '-' y '_' (máximo 32)")
	}
	if c.NetworkMode != "hostonly" && c.NetworkMode != "bridged" {
		return fmt.Errorf("--httpaas-network-mode debe ser hostonly o bridged")
	}
	if strings.TrimSpace(c.NetworkName) == "" {
		return fmt.Errorf("--httpaas-network no puede estar vacío")
	}
	if c.Netmask < 1 || c.Netmask > 30 {
		return fmt.Errorf("--httpaas-netmask debe estar entre 1 y 30")
	}
	for label, value := range map[string]string{
		"--httpaas-gateway":    c.Gateway,
		"--httpaas-ip-start":   c.IPStart,
		"--httpaas-ip-end":     c.IPEnd,
		"--httpaas-dns-server": c.DNSServer,
	} {
		if ip := net.ParseIP(strings.TrimSpace(value)); ip == nil || ip.To4() == nil {
			return fmt.Errorf("%s debe ser una dirección IPv4 literal", label)
		}
	}
	if _, err := validate.Domain("--httpaas-domain", c.Domain); err != nil {
		return err
	}
	if _, err := validate.Domain("--httpaas-dns-zone", c.DNSZone); err != nil {
		return err
	}
	if c.DNSTTL < 0 || c.DNSTTL > 86400 {
		return fmt.Errorf("--httpaas-dns-ttl debe estar entre 0 y 86400")
	}
	if strings.TrimSpace(c.GuestUser) == "" {
		return fmt.Errorf("--httpaas-guest-user no puede estar vacío")
	}
	if !strings.HasPrefix(strings.TrimSpace(c.ApacheRoot), "/") {
		return fmt.Errorf("--httpaas-apache-root debe ser una ruta absoluta")
	}
	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		return fmt.Errorf("--httpaas-http-port debe estar entre 1 y 65535")
	}
	if c.MemoryMB < 256 {
		return fmt.Errorf("--httpaas-memory debe ser al menos 256 MiB")
	}
	if c.CPUs < 1 || c.CPUs > 64 {
		return fmt.Errorf("--httpaas-cpus debe estar entre 1 y 64")
	}
	if c.BootTimeout <= 0 {
		return fmt.Errorf("--httpaas-boot-timeout debe ser positivo")
	}
	if c.MaxUploadMiB < 1 || c.MaxUploadMiB > 1024 {
		return fmt.Errorf("--httpaas-max-upload debe estar entre 1 y 1024 MiB")
	}
	if strings.TrimSpace(c.StatePath) == "" {
		return fmt.Errorf("--httpaas-state no puede estar vacío")
	}
	return nil
}

func (c SSHConfig) validate() error {
	if err := c.validateBootstrap(); err != nil {
		return err
	}
	if strings.TrimSpace(c.PrivateKeyPath) == "" {
		return fmt.Errorf("--ssh-key es obligatorio")
	}
	return nil
}

// validateBootstrap checks the SSH options that are needed to connect once a
// key has been generated inside the guest. The key path itself may be empty
// because the key travels in memory from the generation step.
func (c SSHConfig) validateBootstrap() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("--ssh-host es obligatorio")
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("--ssh-user es obligatorio")
	}
	return nil
}

func (c GuestControlConfig) validate() error {
	if strings.TrimSpace(c.VM) == "" {
		return fmt.Errorf("--guestcontrol-vm es obligatorio")
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("--guestcontrol-user es obligatorio")
	}
	return nil
}

// LoadPrivateKey reads the SSH private key from disk.
func (c SSHConfig) LoadPrivateKey() ([]byte, error) {
	data, err := os.ReadFile(c.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh private key %s: %w", c.PrivateKeyPath, err)
	}
	return data, nil
}
