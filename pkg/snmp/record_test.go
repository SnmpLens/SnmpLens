package snmp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"SnmpLens/pkg/simulator"
	"SnmpLens/pkg/simulator/simtest"

	"github.com/gosnmp/gosnmp"
)

// A recording walks the whole device in both trees it answers in, keeps each
// varbind as gosnmp decoded it, and stops when it is told to.
func TestARecordingWalksTheWholeDevice(t *testing.T) {
	d := simtest.Start(t, simulator.Device{Model: "cisco-catalyst-24", Name: "sw-rec"})
	c := NewClient()
	req := SnmpRequest{Targets: []string{d.Target()}, Community: d.Community, Version: "v2c", Timeout: 2, Retries: 1}

	var pdus []gosnmp.SnmpPDU
	if err := c.Record(context.Background(), req, []string{".1.3.6.1", ".1.0.8802"}, func(p gosnmp.SnmpPDU) error {
		pdus = append(pdus, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var name, lldp bool
	for _, p := range pdus {
		switch {
		case p.Name == ".1.3.6.1.2.1.1.5.0":
			v, _ := p.Value.([]byte)
			name = p.Type == gosnmp.OctetString && string(v) == "sw-rec"
		case strings.HasPrefix(p.Name, ".1.0.8802."):
			lldp = true
		}
	}
	if len(pdus) < 1000 || !name || !lldp {
		t.Errorf("%d varbinds, sysName %v, LLDP %v", len(pdus), name, lldp)
	}

	// Stopped part of the way, it keeps nothing more and says why.
	ctx, cancel := context.WithCancel(context.Background())
	kept := 0
	err := c.Record(ctx, req, []string{".1.3.6.1"}, func(gosnmp.SnmpPDU) error {
		if kept++; kept == 100 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || kept != 100 {
		t.Errorf("stopped at 100: %v, %d kept", err, kept)
	}

	if err := c.Record(context.Background(), SnmpRequest{Targets: []string{"a", "b"}}, nil, nil); err == nil {
		t.Error("a recording of two devices was walked")
	}
}
