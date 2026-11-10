package simulator

import (
	"bytes"
	"reflect"
	"testing"
)

// A bench written to a file reads back as the same devices, without the ID, the
// engine and the boots that are this machine's; its secrets are in it when they
// were asked for and absent otherwise, and the file says which.
func TestADeviceFileRoundTrips(t *testing.T) {
	d := Device{ID: "0123456789abcdef", Name: "core-01", Model: "linux-server", Address: "127.0.0.2", Port: 1161,
		Versions: []string{"v2c", "v3"}, Community: "s3cret-community", WriteCommunity: "wr1te-community",
		Users: []User{{Name: "ops", SecLevel: "AuthPriv", AuthProto: "SHA256", AuthPass: "auth-pass-1",
			PrivProto: "AES", PrivPass: "priv-pass-1", Write: true}},
		EngineID: "8000000903001122334455", EngineBoots: 7,
		Traps: Traps{Destinations: []Destination{{ID: "d1", Host: "192.0.2.50", Port: 162, Version: "v2c",
			Community: "trap-s3cret"}}, OnStart: true, Schedules: []Schedule{}},
		Params:    map[string]int{"cpus": 8},
		Overrides: []Override{{OID: ".1.3.6.1.2.1.1.1.0", Type: "OctetString", Value: "core router"}}}
	for _, with := range []bool{true, false} {
		raw, err := ExportDevices([]Device{d}, nil, with)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"s3cret-community", "wr1te-community", "auth-pass-1", "priv-pass-1", "trap-s3cret"} {
			if bytes.Contains(raw, []byte(secret)) != with {
				t.Errorf("with secrets %v, the file holding %q is %v", with, secret, !with)
			}
		}
		for _, mine := range []string{d.ID, d.EngineID, `"engineBoots"`} {
			if bytes.Contains(raw, []byte(mine)) {
				t.Errorf("the file holds %s, which is this machine's", mine)
			}
		}
		f, err := ParseDeviceFile(raw)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{true: SecretsIncluded, false: SecretsOmitted}[with]; f.Secrets != want {
			t.Errorf("the file says its secrets are %q, want %q", f.Secrets, want)
		}
		want := d
		want.ID, want.EngineID, want.EngineBoots = "", "", 0
		if !with {
			want = want.WithoutSecrets()
		}
		if got := f.Devices[0].Device(); !reflect.DeepEqual(got, want) {
			t.Errorf("read back as\n%+v\nwant\n%+v", got, want)
		}
	}
}

// A device whose secrets are yet to be given is refused by Validate and passes
// ValidateWithoutSecrets, which still holds it to everything else.
func TestADeviceIsCheckedWithoutItsSecrets(t *testing.T) {
	d := Device{Name: "x", Model: "linux-server", Address: "127.0.0.2", Port: 1161, Versions: []string{"v2c", "v3"},
		Users:    []User{{Name: "ops", SecLevel: "AuthPriv", AuthProto: "SHA256", PrivProto: "AES"}},
		EngineID: "8000000903aabbccddeeff"}
	if d.Validate() == nil {
		t.Error("a device with neither community nor passphrases was valid")
	}
	if err := d.ValidateWithoutSecrets(); err != nil {
		t.Errorf("without its secrets: %v", err)
	}
	d.Address = "192.0.2.1"
	if d.ValidateWithoutSecrets() == nil {
		t.Error("the loopback rule was not held")
	}
}
