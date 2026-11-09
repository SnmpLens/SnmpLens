package simulator

import (
	"maps"
	"slices"
	"testing"

	"SnmpLens/pkg/snmp"
)

// What the prototype was written to meet: SnmpLens itself, through pkg/snmp,
// reads the simulated device — in v1, in v2c, and in v3 at every level with
// every protocol, each named the way SnmpLens names it.
//
// It also holds the two vocabularies together. pkg/snmp maps "AES192" and the
// rest onto gosnmp's constants, and so does this package; a name the two mapped
// differently fails here, as a digest or a decryption error.
func TestSnmpLensReadsTheSimulator(t *testing.T) {
	users := []User{{Name: "noauth", SecLevel: "NoAuthNoPriv"}}
	for _, p := range slices.Sorted(maps.Keys(authProtocols)) {
		users = append(users, User{Name: "auth-" + p, SecLevel: "AuthNoPriv", AuthProto: p, AuthPass: "authpass-" + p})
	}
	for _, p := range slices.Sorted(maps.Keys(privProtocols)) {
		users = append(users, User{Name: "priv-" + p, SecLevel: "AuthPriv",
			AuthProto: "SHA256", AuthPass: "authpass-" + p, PrivProto: p, PrivPass: "privpass-" + p})
	}
	a := startAgent(t, Config{Versions: []string{"v1", "v2c", "v3"}, Community: "public", Users: users})
	host, port := a.Addr().Addr().String(), int(a.Addr().Port())
	client := snmp.NewClient()
	params := func(u User) snmp.V3Params {
		return snmp.V3Params{User: u.Name, SecLevel: u.SecLevel,
			AuthProto: u.AuthProto, AuthPass: u.AuthPass, PrivProto: u.PrivProto, PrivPass: u.PrivPass}
	}

	read := func(t *testing.T, version, community string, v3 snmp.V3Params) {
		t.Helper()
		res := client.Get([]string{host}, sysDescrOID, community, version, port, 2, 0, v3)
		if len(res) != 1 || res[0].Error != "" {
			t.Fatalf("%+v", res[0])
		}
		if got := res[0].Result.Value; got != sysDescr {
			t.Fatalf("read %v, want %q", got, sysDescr)
		}
	}
	for _, v := range []string{"v1", "v2c"} {
		t.Run(v, func(t *testing.T) { read(t, v, "public", snmp.V3Params{}) })
	}
	for _, u := range users {
		t.Run(u.Name, func(t *testing.T) { read(t, "v3", "", params(u)) })
	}

	// And the walk, which is the first thing a person does with a new device.
	t.Run("walk", func(t *testing.T) {
		i := slices.IndexFunc(users, func(u User) bool { return u.Name == "priv-AES" })
		res := client.Walk([]string{host}, ".1.3.6.1", "", "v3", port, 2, 0, params(users[i]))
		if res[0].Error != "" {
			t.Fatal(res[0].Error)
		}
		rows, _ := res[0].Result.Value.([]*snmp.Result)
		got := make([]string, len(rows))
		for j, r := range rows {
			got[j] = r.Oid
		}
		// The device answers v3, so its engine group and statistics are there too.
		if want := walkOrder(true); !slices.Equal(got, want) {
			t.Errorf("walked\n%v\nwant\n%v", got, want)
		}
	})
}
