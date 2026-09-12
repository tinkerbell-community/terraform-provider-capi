// Copyright IBM Corp. 2021, 2026
// SPDX-License-Identifier: MPL-2.0

package talos

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bmc-toolbox/bmclib/v2/bmc"
)

type fakeSession struct {
	opened, closed bool
	power          string
	powerErr       error
	setPowerCalls  []string
	bootCalls      []string
	mediaCalls     []string
	httpCalls      []string
	setErr         error
	metadata       bmc.Metadata
}

func (f *fakeSession) Open(context.Context) error  { f.opened = true; return nil }
func (f *fakeSession) Close(context.Context) error { f.closed = true; return nil }
func (f *fakeSession) GetPowerState(context.Context) (string, error) {
	return f.power, f.powerErr
}
func (f *fakeSession) SetPowerState(_ context.Context, state string) (bool, error) {
	f.setPowerCalls = append(f.setPowerCalls, state)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) SetBootDevice(_ context.Context, dev string, persistent, efi bool) (bool, error) {
	f.bootCalls = append(f.bootCalls, dev)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) SetVirtualMedia(_ context.Context, kind, url string) (bool, error) {
	f.mediaCalls = append(f.mediaCalls, kind+":"+url)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) SetHTTPBootURI(_ context.Context, uri string) (bool, error) {
	f.httpCalls = append(f.httpCalls, uri)
	return f.setErr == nil, f.setErr
}
func (f *fakeSession) PostCode(context.Context) (string, int, error) { return "ok", 0, nil }
func (f *fakeSession) GetMetadata() bmc.Metadata                     { return f.metadata }

func newTestBMC(s *fakeSession) *BMCLib {
	b := NewBMCLib(BMCCredentials{Address: "10.0.0.1", Username: "u", Password: "p"}, nil)
	b.newSession = func() bmcSession { return s }
	return b
}

func amtMetadata() bmc.Metadata {
	return bmc.Metadata{SuccessfulOpenConns: []string{"IntelAMT"}, ProvidersAttempted: []string{"IntelAMT"}}
}

func TestParseAddress(t *testing.T) {
	for _, tc := range []struct {
		in     string
		host   string
		port   int
		scheme string
		bad    bool
	}{
		{in: "10.0.0.1", host: "10.0.0.1"},
		{in: "bmc.example.com:443", host: "bmc.example.com", port: 443},
		{in: "https://10.0.0.160:16993", host: "10.0.0.160", port: 16993, scheme: "https"},
		{in: "http://10.0.0.160", host: "10.0.0.160", scheme: "http"},
		{in: "", bad: true},
		{in: "https://", bad: true},
	} {
		host, port, scheme, err := parseAddress(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("parseAddress(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil || host != tc.host || port != tc.port || scheme != tc.scheme {
			t.Errorf("parseAddress(%q) = %q,%d,%q,%v; want %q,%d,%q", tc.in, host, port, scheme, err, tc.host, tc.port, tc.scheme)
		}
	}
}

func TestBMCLib_PowerStateNormalizesCase(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want PowerState
	}{{"On", PowerOn}, {"on", PowerOn}, {"Off", PowerOff}, {"off", PowerOff}, {"", PowerUnknown}, {"Paused", PowerUnknown}} {
		s := &fakeSession{power: tc.raw}
		got, err := newTestBMC(s).PowerState(context.Background())
		if err != nil {
			t.Fatalf("PowerState(%q) error = %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("PowerState(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if !s.opened || !s.closed {
			t.Fatalf("session should be opened and closed, got opened=%v closed=%v", s.opened, s.closed)
		}
	}
}

func TestBMCLib_PowerCommands(t *testing.T) {
	s := &fakeSession{}
	b := newTestBMC(s)
	ctx := context.Background()
	if err := b.PowerOn(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.PowerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.setPowerCalls, ","); got != "on,off,cycle" {
		t.Fatalf("power calls = %q", got)
	}
}

func TestBMCLib_MediaAndBootRedfishStyle(t *testing.T) {
	s := &fakeSession{metadata: bmc.Metadata{SuccessfulOpenConns: []string{"gofish"}}}
	b := newTestBMC(s)
	ctx := context.Background()
	armed, err := b.InsertMedia(ctx, "https://example.com/talos.iso")
	if err != nil || armed {
		t.Fatalf("InsertMedia() = armed %v, err %v; want not armed", armed, err)
	}
	if err := b.EjectMedia(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.SetBootDevice(ctx, BootDeviceCDROM, false, true); err != nil {
		t.Fatal(err)
	}
	armed, err = b.SetHTTPBootURI(ctx, "https://example.com/uki.efi")
	if err != nil || armed {
		t.Fatalf("SetHTTPBootURI() = armed %v, err %v; want not armed", armed, err)
	}
	if _, err := b.SetHTTPBootURI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.mediaCalls, ","); got != "CD:https://example.com/talos.iso,CD:" {
		t.Fatalf("media calls = %q", got)
	}
	if got := strings.Join(s.bootCalls, ","); got != "cdrom" {
		t.Fatalf("boot calls = %q", got)
	}
	if got := strings.Join(s.httpCalls, ","); got != "https://example.com/uki.efi," {
		t.Fatalf("http calls = %q", got)
	}
}

func TestBMCLib_AMTArmsBootAndClearsViaEject(t *testing.T) {
	s := &fakeSession{metadata: amtMetadata()}
	b := newTestBMC(s)
	ctx := context.Background()
	armed, err := b.InsertMedia(ctx, "https://example.com/talos.iso")
	if err != nil || !armed {
		t.Fatalf("InsertMedia() = armed %v, err %v; want armed", armed, err)
	}
	armed, err = b.SetHTTPBootURI(ctx, "https://example.com/uki.efi")
	if err != nil || !armed {
		t.Fatalf("SetHTTPBootURI() = armed %v, err %v; want armed", armed, err)
	}
	if _, err := b.SetHTTPBootURI(ctx, ""); err != nil {
		t.Fatal(err)
	}
	// Clearing on AMT must go through the media eject path, never an empty URI.
	if got := strings.Join(s.httpCalls, ","); got != "https://example.com/uki.efi" {
		t.Fatalf("http calls = %q", got)
	}
	if got := strings.Join(s.mediaCalls, ","); got != "CD:https://example.com/talos.iso,CD:" {
		t.Fatalf("media calls = %q", got)
	}
	if b.preferredProvider() != "IntelAMT" {
		t.Fatalf("provider = %q, want IntelAMT remembered after first open", b.preferredProvider())
	}
}

func TestBMCLib_UnsupportedMapsToErrUnsupported(t *testing.T) {
	s := &fakeSession{setErr: errors.New("1 error occurred: no VirtualMediaSetter implementations found")}
	_, err := newTestBMC(s).InsertMedia(context.Background(), "https://example.com/talos.iso")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("InsertMedia() error = %v, want ErrUnsupported", err)
	}
}

func TestBMCLib_ErrorCarriesMetadata(t *testing.T) {
	s := &fakeSession{
		setErr: errors.New("boom"),
		metadata: bmc.Metadata{
			ProvidersAttempted:   []string{"gofish", "ipmitool"},
			FailedProviderDetail: map[string]string{"gofish": "401", "ipmitool": "timeout"},
		},
	}
	err := newTestBMC(s).PowerOn(context.Background())
	var bmcErr *BMCError
	if !errors.As(err, &bmcErr) {
		t.Fatalf("PowerOn() error = %T, want *BMCError", err)
	}
	msg := err.Error()
	for _, want := range []string{"bmc power-on: boom", "gofish, ipmitool", "gofish: 401", "ipmitool: timeout"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}
