// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	bmclib "github.com/bmc-toolbox/bmclib/v2"
	"github.com/bmc-toolbox/bmclib/v2/bmc"
	bmclibErrs "github.com/bmc-toolbox/bmclib/v2/errors"
	"github.com/bmc-toolbox/bmclib/v2/providers/intelamt"
)

// BMCCredentials identifies and authenticates to a machine's BMC.
//
// Address accepts "host", "host:port", or "scheme://host[:port]". A port
// applies to the Redfish and Intel AMT providers; a scheme applies to Intel
// AMT (its default is http on 16992, while TLS-enabled AMT listens on 16993).
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
	// InsertMedia attaches an ISO. armed reports that the BMC already
	// selected the media as the next boot (Intel AMT's one-click recovery
	// does), in which case SetBootDevice must not be called: it would replace
	// the armed boot.
	InsertMedia(ctx context.Context, isoURL string) (armed bool, err error)
	EjectMedia(ctx context.Context) error
	// SetHTTPBootURI sets the UEFI HTTP boot URI; armed has the same meaning
	// as for InsertMedia. An empty uri clears it.
	SetHTTPBootURI(ctx context.Context, uri string) (armed bool, err error)
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

	mu sync.Mutex
	// provider is the bmclib provider that opened successfully; later
	// sessions try only that provider instead of every driver.
	provider string
}

// openSession is an open bmclib session plus what was learned at open time.
// bmclib overwrites the client metadata after every call, so facts from Open
// (which provider actually connected) must be captured before any operation.
type openSession struct {
	bmcSession
	// amt is true when the Intel AMT provider serves this session.
	amt bool
}

// parseAddress splits "host", "host:port", or "scheme://host[:port]".
func parseAddress(address string) (host string, port int, scheme string, err error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", 0, "", errors.New("bmc address is empty")
	}
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err != nil {
			return "", 0, "", fmt.Errorf("parsing bmc address %q: %w", address, err)
		}
		scheme = strings.ToLower(u.Scheme)
		host = u.Hostname()
		if p := u.Port(); p != "" {
			port, err = strconv.Atoi(p)
			if err != nil {
				return "", 0, "", fmt.Errorf("parsing bmc port in %q: %w", address, err)
			}
		}
		if host == "" {
			return "", 0, "", fmt.Errorf("bmc address %q has no host", address)
		}
		return host, port, scheme, nil
	}
	if h, p, err := net.SplitHostPort(address); err == nil {
		port, err = strconv.Atoi(p)
		if err != nil {
			return "", 0, "", fmt.Errorf("parsing bmc port in %q: %w", address, err)
		}
		return h, port, "", nil
	}
	return address, 0, "", nil
}

// NewBMCLib returns a BMC backed by bmclib with provider autodetection.
func NewBMCLib(creds BMCCredentials, logger *log.Logger) *BMCLib {
	if logger == nil {
		logger = log.New(log.Writer(), "[talos-bmc] ", log.LstdFlags)
	}
	b := &BMCLib{creds: creds, logger: logger, timeout: defaultBMCTimeout}

	host, port, scheme, err := parseAddress(creds.Address)
	if err != nil {
		// Let Open fail with the real message; keep the raw address.
		host = creds.Address
	}
	opts := []bmclib.Option{bmclib.WithPerProviderTimeout(perProviderTimeout)}
	if port > 0 {
		opts = append(opts, bmclib.WithIntelAMTPort(uint32(port)), bmclib.WithRedfishPort(strconv.Itoa(port)))
	}
	if scheme != "" {
		opts = append(opts, bmclib.WithIntelAMTHostScheme(scheme))
	}

	b.newSession = func() bmcSession {
		c := bmclib.NewClient(host, creds.Username, creds.Password, opts...)
		if p := b.preferredProvider(); p != "" {
			c.Registry.Drivers = c.Registry.For(p)
		}
		return c
	}
	return b
}

func (b *BMCLib) preferredProvider() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.provider
}

func (b *BMCLib) rememberProvider(conns []string) {
	if len(conns) != 1 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.provider != conns[0] {
		b.provider = conns[0]
		b.logger.Printf("bmc %s: using provider %s for subsequent sessions", b.creds.Address, conns[0])
	}
}

// withSession opens a session, runs fn, and closes the session. Errors are
// annotated with bmclib's provider metadata.
func (b *BMCLib) withSession(ctx context.Context, op string, fn func(context.Context, *openSession) error) error {
	s := b.newSession()
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	if err := s.Open(ctx); err != nil {
		return wrapBMCError(op, err, s.GetMetadata())
	}
	opened := s.GetMetadata().SuccessfulOpenConns
	b.rememberProvider(opened)
	sess := &openSession{bmcSession: s, amt: slices.Contains(opened, intelamt.ProviderName)}
	defer func() {
		if cerr := s.Close(ctx); cerr != nil {
			b.logger.Printf("bmc %s: close: %v", op, cerr)
		}
	}()

	if err := fn(ctx, sess); err != nil {
		return wrapBMCError(op, err, s.GetMetadata())
	}
	b.logger.Printf("bmc %s: %s ok via %s", b.creds.Address, op, s.GetMetadata().SuccessfulProvider)
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
	err := b.withSession(ctx, "power-state", func(ctx context.Context, s *openSession) error {
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
	return b.withSession(ctx, op, func(ctx context.Context, s *openSession) error {
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
	return b.withSession(ctx, "set-boot-device", func(ctx context.Context, s *openSession) error {
		_, err := s.SetBootDevice(ctx, string(dev), persistent, efi)
		return err
	})
}

// InsertMedia attaches an ISO as virtual CD media. Intel AMT has no CD
// emulation: its "virtual media" is one-click recovery, which boots an EFI
// image over HTTPS, so an ISO is reported unsupported and the reconciler
// falls through to the UKI via SetHTTPBootURI.
func (b *BMCLib) InsertMedia(ctx context.Context, isoURL string) (bool, error) {
	err := b.withSession(ctx, "insert-media", func(ctx context.Context, s *openSession) error {
		if s.amt {
			return fmt.Errorf("intel amt boots EFI images over https, not ISOs: %w", ErrUnsupported)
		}
		_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, isoURL)
		return err
	})
	return false, err
}

// EjectMedia detaches virtual CD media (on Intel AMT: clears the armed boot).
func (b *BMCLib) EjectMedia(ctx context.Context) error {
	return b.withSession(ctx, "eject-media", func(ctx context.Context, s *openSession) error {
		_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, "")
		return err
	})
}

// SetHTTPBootURI sets the UEFI HTTP boot URI. An empty uri clears it. On
// Intel AMT a non-empty uri arms a one-shot boot of the image, and clearing
// is the same as ejecting.
func (b *BMCLib) SetHTTPBootURI(ctx context.Context, uri string) (bool, error) {
	var armed bool
	err := b.withSession(ctx, "set-http-boot-uri", func(ctx context.Context, s *openSession) error {
		if uri == "" && s.amt {
			_, err := s.SetVirtualMedia(ctx, virtualMediaKindCD, "")
			return err
		}
		_, err := s.SetHTTPBootURI(ctx, uri)
		if err != nil {
			return err
		}
		armed = uri != "" && s.amt
		return nil
	})
	return armed, err
}

// PostCode returns the BIOS POST code as a diagnostic string.
func (b *BMCLib) PostCode(ctx context.Context) (string, error) {
	var out string
	err := b.withSession(ctx, "post-code", func(ctx context.Context, s *openSession) error {
		status, code, err := s.PostCode(ctx)
		if err != nil {
			return err
		}
		out = fmt.Sprintf("%s (0x%02x)", status, code)
		return nil
	})
	return out, err
}
