// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	bmclib "github.com/bmc-toolbox/bmclib/v2"
	"github.com/bmc-toolbox/bmclib/v2/bmc"
	bmclibErrs "github.com/bmc-toolbox/bmclib/v2/errors"
)

// BMCCredentials identifies and authenticates to a machine's BMC.
type BMCCredentials struct {
	Address  string
	Username string
	Password string
}

// BMC is the generic out-of-band control surface the reconciler needs.
// Unsupported operations return an error wrapping ErrUnsupported.
type BMC interface {
	PowerState(ctx context.Context) (PowerState, error)
	PowerOn(ctx context.Context) error
	PowerOff(ctx context.Context) error
	PowerCycle(ctx context.Context) error
	SetBootDevice(ctx context.Context, dev BootDevice, persistent, efi bool) error
	InsertMedia(ctx context.Context, isoURL string) error
	EjectMedia(ctx context.Context) error
	SetHTTPBootURI(ctx context.Context, uri string) error
	PostCode(ctx context.Context) (string, error)
}

// bmcSession is the slice of *bmclib.Client the implementation uses, so tests
// can substitute a fake.
type bmcSession interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
	GetPowerState(ctx context.Context) (string, error)
	SetPowerState(ctx context.Context, state string) (bool, error)
	SetBootDevice(ctx context.Context, bootDevice string, setPersistent, efiBoot bool) (bool, error)
	SetVirtualMedia(ctx context.Context, kind, mediaURL string) (bool, error)
	SetHTTPBootURI(ctx context.Context, uri string) (bool, error)
	PostCode(ctx context.Context) (string, int, error)
	GetMetadata() bmc.Metadata
}

const (
	defaultBMCTimeout  = 90 * time.Second
	perProviderTimeout = 30 * time.Second
	virtualMediaKindCD = "CD"
)

// BMCLib implements BMC with a fresh bmclib session per operation.
type BMCLib struct {
	creds      BMCCredentials
	logger     *log.Logger
	timeout    time.Duration
	newSession func() bmcSession
}

// NewBMCLib returns a BMC backed by bmclib with provider autodetection.
func NewBMCLib(creds BMCCredentials, logger *log.Logger) *BMCLib {
	if logger == nil {
		logger = log.New(log.Writer(), "[talos-bmc] ", log.LstdFlags)
	}
	b := &BMCLib{creds: creds, logger: logger, timeout: defaultBMCTimeout}
	b.newSession = func() bmcSession {
		return bmclib.NewClient(creds.Address, creds.Username, creds.Password,
			bmclib.WithPerProviderTimeout(perProviderTimeout))
	}
	return b
}

// withSession opens a session, runs fn, and closes the session. Errors are
// annotated with bmclib's provider metadata.
func (b *BMCLib) withSession(ctx context.Context, op string, fn func(context.Context, bmcSession) error) error {
	s := b.newSession()
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	if err := s.Open(ctx); err != nil {
		return wrapBMCError(op, err, s.GetMetadata())
	}
	defer func() {
		if cerr := s.Close(ctx); cerr != nil {
			b.logger.Printf("bmc %s: close: %v", op, cerr)
		}
	}()

	if err := fn(ctx, s); err != nil {
		return wrapBMCError(op, err, s.GetMetadata())
	}
	return nil
}

// wrapBMCError maps "no provider implements this" to ErrUnsupported and
// everything else to a *BMCError carrying provider metadata.
func wrapBMCError(op string, err error, md bmc.Metadata) error {
	if errors.Is(err, bmclibErrs.ErrProviderImplementation) ||
		errors.Is(err, bmclibErrs.ErrNotImplemented) ||
		strings.Contains(err.Error(), "implementations found") {
		return fmt.Errorf("bmc %s: %w", op, ErrUnsupported)
	}
	return &BMCError{Op: op, Err: err, Attempted: md.ProvidersAttempted, Failed: md.FailedProviderDetail}
}

// PowerState reads the BMC-reported power state, normalized to on/off/unknown.
func (b *BMCLib) PowerState(ctx context.Context) (PowerState, error) {
	state := PowerUnknown
	err := b.withSession(ctx, "power-state", func(ctx context.Context, s bmcSession) error {
		raw, err := s.GetPowerState(ctx)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "on":
			state = PowerOn
		case "off":
			state = PowerOff
		}
		return nil
	})
	return state, err
}

func (b *BMCLib) setPower(ctx context.Context, op, state string) error {
	return b.withSession(ctx, op, func(ctx context.Context, s bmcSession) error {
		_, err := s.SetPowerState(ctx, state)
		return err
	})
}

// PowerOn powers the machine on.
func (b *BMCLib) PowerOn(ctx context.Context) error { return b.setPower(ctx, "power-on", "on") }

// PowerOff hard-powers the machine off.
func (b *BMCLib) PowerOff(ctx context.Context) error { return b.setPower(ctx, "power-off", "off") }

// PowerCycle hard-resets the machine.
func (b *BMCLib) PowerCycle(ctx context.Context) error {
	return b.setPower(ctx, "power-cycle", "cycle")
}

// SetBootDevice sets the next-boot (or persistent) boot device.
func (b *BMCLib) SetBootDevice(ctx context.Context, dev BootDevice, persistent, efi bool) error {
	return b.withSession(ctx, "set-boot-device", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetBootDevice(ctx, string(dev), persistent, efi)
		return err
	})
}

// InsertMedia attaches an ISO as virtual CD media.
func (b *BMCLib) InsertMedia(ctx context.Context, isoURL string) error {
	return b.withSession(ctx, "insert-media", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, isoURL)
		return err
	})
}

// EjectMedia detaches virtual CD media.
func (b *BMCLib) EjectMedia(ctx context.Context) error {
	return b.withSession(ctx, "eject-media", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, "")
		return err
	})
}

// SetHTTPBootURI sets the UEFI HTTP boot URI. An empty uri clears it.
func (b *BMCLib) SetHTTPBootURI(ctx context.Context, uri string) error {
	return b.withSession(ctx, "set-http-boot-uri", func(ctx context.Context, s bmcSession) error {
		_, err := s.SetHTTPBootURI(ctx, uri)
		return err
	})
}

// PostCode returns the BIOS POST code as a diagnostic string.
func (b *BMCLib) PostCode(ctx context.Context) (string, error) {
	var out string
	err := b.withSession(ctx, "post-code", func(ctx context.Context, s bmcSession) error {
		status, code, err := s.PostCode(ctx)
		if err != nil {
			return err
		}
		out = fmt.Sprintf("%s (0x%02x)", status, code)
		return nil
	})
	return out, err
}
