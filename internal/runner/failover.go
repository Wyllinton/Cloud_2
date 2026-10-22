package runner

import (
	"context"
	"log"
	"sync"
)

// Failover uses a primary transport (normally SSH) and switches permanently to
// a secondary transport (Guest Additions) the first time the primary cannot be
// reached. Once switched, the secondary is used for every subsequent command so
// a dead connection is not retried on every request.
type Failover struct {
	mu         sync.Mutex
	primary    Runner
	secondary  Runner
	onFallback bool
}

// NewFailover combines two transports into one.
func NewFailover(primary, secondary Runner) *Failover {
	return &Failover{primary: primary, secondary: secondary}
}

func (f *Failover) Kind() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onFallback {
		return "guestcontrol"
	}
	return "auto"
}

func (f *Failover) Target() string {
	return f.primary.Target() + " | respaldo: " + f.secondary.Target()
}

func (f *Failover) UsesSudo() bool { return f.primary.UsesSudo() }

// Close releases both transports.
func (f *Failover) Close() error {
	errPrimary := f.primary.Close()
	errSecondary := f.secondary.Close()
	if errPrimary != nil {
		return errPrimary
	}
	return errSecondary
}

// Run executes on the active transport, falling back when the primary fails at
// the transport level. A command that simply exits with a non-zero status is
// returned as-is, because that is a real result, not a connectivity problem.
func (f *Failover) Run(ctx context.Context, input []byte, name string, args ...string) Result {
	f.mu.Lock()
	usingSecondary := f.onFallback
	f.mu.Unlock()

	if !usingSecondary {
		res := f.primary.Run(ctx, input, name, args...)
		if !res.TransportFailed() {
			return res
		}
		f.mu.Lock()
		f.onFallback = true
		f.mu.Unlock()
		log.Printf("transporte primario no disponible (%v); se continúa con Guest Additions", res.Err)
		return f.secondary.Run(ctx, input, name, args...)
	}
	return f.secondary.Run(ctx, input, name, args...)
}
