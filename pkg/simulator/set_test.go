package simulator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

const (
	sysContactOID  = ".1.3.6.1.2.1.1.4.0"
	sysLocationOID = ".1.3.6.1.2.1.1.6.0"
)

// writable is a device answering v1 and v2c, read with "public" and written
// with "private", holding a contact, a location and a table whose third column
// the agent is told is a RowStatus.
func writable(t *testing.T) *Agent {
	t.Helper()
	return startAgent(t, Config{
		Versions: []string{"v1", "v2c"}, Community: "public", WriteCommunity: "private",
		Objects: []Object{
			{OID: sysContactOID, Type: gosnmp.OctetString, Value: Const("noc@example.net")},
			{OID: sysLocationOID, Type: gosnmp.OctetString, Value: Const("Lab")},
			{OID: "1.3.6.1.4.1.32473.10.1.2.1", Type: gosnmp.OctetString, Value: Const("first")},
			{OID: "1.3.6.1.4.1.32473.10.1.3.1", Type: gosnmp.Integer, Value: Const(rowActive)},
		},
		RowStatus: func(o string) (string, bool) {
			if strings.HasPrefix(o, ".1.3.6.1.4.1.32473.10.1.3.") {
				return ".1.3.6.1.4.1.32473.10.1.3", true
			}
			return "", false
		},
	})
}

func setOne(t *testing.T, g *gosnmp.GoSNMP, vars ...gosnmp.SnmpPDU) *gosnmp.SnmpPacket {
	t.Helper()
	res, err := g.Set(vars)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func getOne(t *testing.T, g *gosnmp.GoSNMP, name string) gosnmp.SnmpPDU {
	t.Helper()
	res, err := g.Get([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	return res.Variables[0]
}

// The read community reads and does not write — noAccess in v2c, the
// noSuchName RFC 3584 has a v1 agent say instead, and counted as a community
// used for what it may not do — and the write community writes, and reads too.
func TestOnlyTheWriteCommunityWrites(t *testing.T) {
	a := writable(t)
	contact := gosnmp.SnmpPDU{Name: sysContactOID, Type: gosnmp.OctetString, Value: "ops@example.net"}
	if res := setOne(t, connect(t, manager(a, gosnmp.Version2c, "public")), contact); res.Error != gosnmp.NoAccess {
		t.Errorf("v2c, read community: %v", res.Error)
	}
	if res := setOne(t, connect(t, manager(a, gosnmp.Version1, "public")), contact); res.Error != gosnmp.NoSuchName {
		t.Errorf("v1, read community: %v", res.Error)
	}
	if n := a.stats.badCommunityUses.Load(); n != 2 {
		t.Errorf("snmpInBadCommunityUses is %d", n)
	}
	w := connect(t, manager(a, gosnmp.Version2c, "private"))
	if res := setOne(t, w, contact); res.Error != gosnmp.NoError {
		t.Fatalf("v2c, write community: %v", res.Error)
	}
	if v := getOne(t, connect(t, manager(a, gosnmp.Version2c, "public")), sysContactOID); text(v) != "ops@example.net" {
		t.Errorf("read back as %q", text(v))
	}
	if v := getOne(t, w, sysLocationOID); text(v) != "Lab" {
		t.Errorf("the write community reads %q", text(v))
	}
	if n := a.stats.inTotalSetVars.Load(); n != 1 {
		t.Errorf("snmpInTotalSetVars is %d", n)
	}
}

// A SET is applied whole or not at all, and an error names its varbind; a v1
// manager is told badValue where v2c says wrongType.
func TestASetIsAppliedWholeOrNotAtAll(t *testing.T) {
	a := writable(t)
	w := connect(t, manager(a, gosnmp.Version2c, "private"))
	res := setOne(t, w,
		gosnmp.SnmpPDU{Name: sysContactOID, Type: gosnmp.OctetString, Value: "changed"},
		gosnmp.SnmpPDU{Name: sysLocationOID, Type: gosnmp.Integer, Value: 7})
	if res.Error != gosnmp.WrongType || res.ErrorIndex != 2 {
		t.Errorf("a location written as an INTEGER: %v at %d", res.Error, res.ErrorIndex)
	}
	if v := getOne(t, w, sysContactOID); text(v) != "noc@example.net" {
		t.Errorf("half a SET was applied: the contact is %q", text(v))
	}
	v1 := connect(t, manager(a, gosnmp.Version1, "private"))
	if res := setOne(t, v1, gosnmp.SnmpPDU{Name: sysLocationOID, Type: gosnmp.Integer, Value: 7}); res.Error != gosnmp.BadValue {
		t.Errorf("v1: %v", res.Error)
	}
}

// What the agent answers itself is not written, and an instance under another
// is not made; an instance nothing answered is, and a walk finds it.
func TestASetCreatesAnInstanceWhereOneCanBe(t *testing.T) {
	a := writable(t)
	w := connect(t, manager(a, gosnmp.Version2c, "private"))
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: ".1.3.6.1.2.1.11.30.0", Type: gosnmp.Integer, Value: 1}); res.Error != gosnmp.NotWritable {
		t.Errorf("snmpEnableAuthenTraps: %v", res.Error)
	}
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: sysContactOID + ".1", Type: gosnmp.Integer, Value: 1}); res.Error != gosnmp.NoCreation {
		t.Errorf("under an instance: %v", res.Error)
	}
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.32473.9.1.0", Type: gosnmp.Gauge32, Value: uint(42)}); res.Error != gosnmp.NoError {
		t.Fatalf("a new instance: %v", res.Error)
	}
	res, err := w.GetNext([]string{".1.3.6.1.4.1.32473.9"})
	if err != nil || res.Variables[0].Name != ".1.3.6.1.4.1.32473.9.1.0" || gosnmp.ToBigInt(res.Variables[0].Value).Int64() != 42 {
		t.Errorf("the walk finds %v, %v", res, err)
	}
}

// A row is made through its RowStatus — createAndGo leaves it active — and
// destroyed through it with every instance it has; what RFC 2579 refuses is
// refused.
func TestRowsAreMadeAndDestroyedThroughTheirRowStatus(t *testing.T) {
	a := writable(t)
	w := connect(t, manager(a, gosnmp.Version2c, "private"))
	const name, status = ".1.3.6.1.4.1.32473.10.1.2.5", ".1.3.6.1.4.1.32473.10.1.3.5"
	if res := setOne(t, w,
		gosnmp.SnmpPDU{Name: name, Type: gosnmp.OctetString, Value: "fifth"},
		gosnmp.SnmpPDU{Name: status, Type: gosnmp.Integer, Value: rowCreateAndGo}); res.Error != gosnmp.NoError {
		t.Fatalf("createAndGo: %v", res.Error)
	}
	if v := getOne(t, w, status); gosnmp.ToBigInt(v.Value).Int64() != rowActive {
		t.Errorf("a row made with createAndGo is %v", v.Value)
	}
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: status, Type: gosnmp.Integer, Value: rowCreateAndWait}); res.Error != gosnmp.InconsistentValue {
		t.Errorf("createAndWait on a row that exists: %v", res.Error)
	}
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: status, Type: gosnmp.Integer, Value: rowDestroy}); res.Error != gosnmp.NoError {
		t.Fatalf("destroy: %v", res.Error)
	}
	for _, gone := range []string{name, status} {
		if v := getOne(t, w, gone); v.Type != gosnmp.NoSuchInstance && v.Type != gosnmp.NoSuchObject {
			t.Errorf("%s outlived its row: %v", gone, v.Value)
		}
	}
	if v := getOne(t, w, ".1.3.6.1.4.1.32473.10.1.2.1"); text(v) != "first" {
		t.Errorf("destroying row 5 touched row 1: %v", v.Value)
	}
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.32473.10.1.3.7", Type: gosnmp.Integer, Value: rowActive}); res.Error != gosnmp.InconsistentValue {
		t.Errorf("active on a row that does not exist: %v", res.Error)
	}
	if res := setOne(t, w, gosnmp.SnmpPDU{Name: ".1.3.6.1.4.1.32473.10.1.3.8", Type: gosnmp.Integer, Value: 3}); res.Error != gosnmp.WrongValue {
		t.Errorf("notReady: %v", res.Error)
	}
}

// A v3 user writes only with write access, and what a device was written
// holds until it restarts: a new agent answers as configured.
func TestAV3UserWritesWithWriteAccessUntilTheDeviceRestarts(t *testing.T) {
	cfg := Config{Versions: []string{"v3"}, EngineID: testEngineID, EngineBoots: 1,
		Users: []User{
			{Name: "reader", SecLevel: "AuthPriv", AuthProto: "SHA256", AuthPass: "authpass-1", PrivProto: "AES", PrivPass: "privpass-1"},
			{Name: "writer", SecLevel: "AuthPriv", AuthProto: "SHA256", AuthPass: "authpass-2", PrivProto: "AES", PrivPass: "privpass-2", Write: true},
		},
		Objects: []Object{{OID: sysContactOID, Type: gosnmp.OctetString, Value: Const("noc@example.net")}},
	}
	a := startAgent(t, cfg)
	as := func(name, auth, priv string) *gosnmp.GoSNMP {
		return connect(t, v3Manager(a, name, gosnmp.AuthPriv, gosnmp.SHA256, auth, gosnmp.AES, priv))
	}
	contact := gosnmp.SnmpPDU{Name: sysContactOID, Type: gosnmp.OctetString, Value: "written"}
	if res := setOne(t, as("reader", "authpass-1", "privpass-1"), contact); res.Error != gosnmp.NoAccess {
		t.Errorf("a user without write access: %v", res.Error)
	}
	writer := as("writer", "authpass-2", "privpass-2")
	if res := setOne(t, writer, contact); res.Error != gosnmp.NoError {
		t.Fatalf("a user with write access: %v", res.Error)
	}
	if v := getOne(t, writer, sysContactOID); text(v) != "written" {
		t.Errorf("read back as %q", text(v))
	}

	a.Stop()
	cfg.EngineBoots = 2
	again := startAgent(t, cfg)
	g := connect(t, v3Manager(again, "reader", gosnmp.AuthPriv, gosnmp.SHA256, "authpass-1", gosnmp.AES, "privpass-1"))
	if v := getOne(t, g, sysContactOID); text(v) != "noc@example.net" {
		t.Errorf("a write outlived the restart: %q", text(v))
	}
}

// A device recabled while a manager writes to it answers as it is cabled now. A
// SET runs on the receive goroutine and a rewire on the application's, and a SET
// that read the tree before the rewire stored a copy of the old one after it.
func TestASetInFlightDoesNotUndoARewire(t *testing.T) {
	cabled := func(i int) []Object {
		objs := []Object{
			{OID: sysContactOID, Type: gosnmp.OctetString, Value: Const("noc@example.net")},
			{OID: sysLocationOID, Type: gosnmp.OctetString, Value: Const(fmt.Sprintf("rack %d", i))},
		}
		// Enough of a device that copying its tree takes a while, as a
		// switch's does.
		for n := 1; n <= 2000; n++ {
			objs = append(objs, Object{OID: fmt.Sprintf("1.3.6.1.4.1.32473.20.1.%d", n),
				Type: gosnmp.Integer, Value: Const(n)})
		}
		return objs
	}
	a := newAgent(t, Config{Versions: []string{"v2c"}, Community: "public", WriteCommunity: "private",
		Objects: cabled(0)})
	req := &gosnmp.SnmpPacket{PDUType: gosnmp.SetRequest, Variables: []gosnmp.SnmpPDU{
		{Name: sysContactOID, Type: gosnmp.OctetString, Value: "ops@example.net"}}}
	location, err := parseArcs(sysLocationOID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 50; i++ {
		// A manager writing all the while the device is recabled.
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if ans := a.set(gosnmp.Version2c, req); ans.status != gosnmp.NoError {
					t.Errorf("SET: %v", ans.status)
					return
				}
			}
		}()
		if err := a.Rewire(cabled(i), nil); err != nil {
			t.Fatal(err)
		}
		close(stop)
		<-done
		want := fmt.Sprintf("rack %d", i)
		if got := textOf(a.tree.Load().get(location).val.read(clock{})); got != want {
			t.Fatalf("after rewire %d the location reads %q: a SET put back the tree as it was cabled before", i, got)
		}
	}
}
