package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
)

// Failover uses a primary transport (normally SSH) and switches permanently to
// a secondary transport (Guest Additions) the first time the primary cannot be
// reached. Once switched, the secondary is used for every subsequent command so
// a dead connection is not retried on every request.
//
// A failover created with NewBootstrapFailover starts on the secondary
// transport (Guest Additions) because no key exists yet, and can be promoted to
// SSH at runtime with PromoteSSH as soon as a key pair has been generated.
type Failover struct {
	mu         sync.Mutex
	primary    Runner
	secondary  Runner
	connect    func([]byte) (Runner, error)
	sshTarget  string
	onFallback bool
}

// NewFailover combines two transports into one, starting on primary.
func NewFailover(primary, secondary Runner) *Failover {
	return &Failover{primary: primary, secondary: secondary}
}

// NewBootstrapFailover builds a failover that starts on the Guest Additions
// channel and keeps an SSH connector ready. The connector receives the private
// key that was generated inside the guest, so the console can move to SSH
// without being restarted and without the key having to exist on the host.
func NewBootstrapFailover(secondary Runner, sshTarget string, connect func([]byte) (Runner, error)) *Failover {
	return &Failover{secondary: secondary, sshTarget: sshTarget, connect: connect}
}

// activeLocked returns the transport currently in use. The caller must hold mu.
func (f *Failover) activeLocked() Runner {
	if f.primary != nil && !f.onFallback {
		return f.primary
	}
	return f.secondary
}

func (f *Failover) Kind() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.activeLocked(); r != nil {
		return r.Kind()
	}
	return "auto"
}

func (f *Failover) Target() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.primary != nil && f.secondary != nil && !f.onFallback:
		return f.primary.Target() + " | respaldo: " + f.secondary.Target()
	case f.primary != nil && f.secondary != nil:
		return f.secondary.Target() + " | SSH: " + f.primary.Target()
	case f.primary != nil:
		return f.primary.Target()
	case f.secondary != nil && f.sshTarget != "":
		return f.secondary.Target() + " | SSH pendiente: " + f.sshTarget
	case f.secondary != nil:
		return f.secondary.Target()
	default:
		return "sin transporte"
	}
}

func (f *Failover) UsesSudo() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.activeLocked(); r != nil {
		return r.UsesSudo()
	}
	return false
}

// FileTransfer exposes the file-transfer mechanism of the active transport.
// When SSH is active it returns ok=false, so callers fall back to stdin/stdout.
func (f *Failover) FileTransfer() (FileTransfer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ft, ok := f.activeLocked().(FileTransfer)
	return ft, ok
}

// Close releases both transports.
func (f *Failover) Close() error {
	f.mu.Lock()
	primary, secondary := f.primary, f.secondary
	f.mu.Unlock()

	var errPrimary error
	if primary != nil {
		errPrimary = primary.Close()
	}
	if secondary != nil && secondary != primary {
		if err := secondary.Close(); errPrimary == nil {
			errPrimary = err
		}
	}
	return errPrimary
}

// PromoteSSH connects with the supplied private key and makes SSH the active
// transport. It is a no-op when SSH is already active. On failure the current
// (Guest Additions) transport is kept, so the console remains usable.
func (f *Failover) PromoteSSH(privateKey []byte) error {
	f.mu.Lock()
	connect := f.connect
	already := f.primary != nil && !f.onFallback
	f.mu.Unlock()

	if already {
		return nil
	}
	if connect == nil {
		return errors.New("el transporte no admite promoción a SSH: configure --ssh-host")
	}

	promoted, err := connect(privateKey)
	if err != nil {
		return fmt.Errorf("conectar por SSH: %w", err)
	}

	f.mu.Lock()
	// Another request may have promoted the transport while this connection was
	// being established. Keep the existing one and discard the duplicate.
	if f.primary != nil && !f.onFallback {
		f.mu.Unlock()
		_ = promoted.Close()
		return nil
	}
	previous := f.primary
	f.primary = promoted
	f.onFallback = false
	f.mu.Unlock()

	if previous != nil && previous != promoted {
		_ = previous.Close()
	}
	log.Printf("transporte promovido a SSH (%s)", promoted.Target())
	return nil
}

// Run executes on the active transport, falling back when the primary fails at
// the transport level. A command that simply exits with a non-zero status is
// returned as-is, because that is a real result, not a connectivity problem.
func (f *Failover) Run(ctx context.Context, input []byte, name string, args ...string) Result {
	f.mu.Lock()
	primary := f.primary
	secondary := f.secondary
	onFallback := f.onFallback
	f.mu.Unlock()

	if primary != nil && !onFallback {
		res := primary.Run(ctx, input, name, args...)
		if !res.TransportFailed() || secondary == nil {
			return res
		}
		f.mu.Lock()
		// Do not mark a fallback if another request already promoted a new SSH
		// connection in the meantime.
		if f.primary == primary {
			f.onFallback = true
		}
		f.mu.Unlock()
		log.Printf("transporte primario no disponible (%v); se continúa con Guest Additions", res.Err)
		return secondary.Run(ctx, input, name, args...)
	}

	if secondary != nil {
		return secondary.Run(ctx, input, name, args...)
	}

	return Result{
		Transport: "auto",
		ExitCode:  -1,
		Err:       errors.New("no hay transporte disponible"),
	}
}
