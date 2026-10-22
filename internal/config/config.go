// Package config holds the runtime configuration of the automation console.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

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
			if err := c.SSH.validate(); err != nil {
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
	return nil
}

func (c SSHConfig) validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("--ssh-host es obligatorio")
	}
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("--ssh-user es obligatorio")
	}
	if strings.TrimSpace(c.PrivateKeyPath) == "" {
		return fmt.Errorf("--ssh-key es obligatorio")
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
