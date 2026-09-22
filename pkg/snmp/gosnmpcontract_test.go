package snmp

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// What this repository depends on gosnmp DOING, asserted against gosnmp
// itself rather than described beside a version number.
//
// A citation is evidence the next reader can check once; a test is evidence
// CI checks on every commit, and it fails on the upgrade that breaks the
// claim rather than on whoever next reads the paragraph. So everything
// reachable through gosnmp's exported API is pinned here. What is not
// reachable -- that the receive loop is a single goroutine, that listenUDP
// dereferences a failed type assertion anyway -- stays a cited observation,
// held to the version we build against by citedversion_test.go.

const probeCommunity = "s3cr3t-community"

// scrubSecrets rewrites gosnmp's two community lines BY PATTERN, and the
// patterns are written against gosnmp's exact wording: `Community:.*?,
// PDUType:` and `Parsed community .*`. A change to either -- a space after
// the colon, a reordered field, a different verb -- would stop them matching
// with nothing else going wrong anywhere.
//
// The secrets list is deliberately EMPTY here. Passing the community would
// let the literal replacement mask exactly the failure this test is for: the
// community on "Parsed community" arrives from an INCOMING packet and is
// never in that list, so the pattern is the only thing between a trap
// sender's community and the debug panel.
func TestGosnmpStillPrintsTheCommunityInTheShapeWeScrub(t *testing.T) {
	sending := "SENDING PACKET: " + (&gosnmp.SnmpPacket{
		Version:   gosnmp.Version2c,
		Community: probeCommunity,
		PDUType:   gosnmp.GetRequest,
	}).SafeString()

	for _, line := range []string{sending, parsedCommunityLine(t)} {
		if !strings.Contains(line, probeCommunity) {
			t.Fatalf("gosnmp no longer prints the community here. That may be good news, but\n"+
				"it means this test is proving nothing and the patterns need re-reading:\n%s", line)
		}
		got := scrubSecrets(line, nil)
		if strings.Contains(got, probeCommunity) {
			t.Errorf("the community survived scrubbing by pattern alone:\n%s", got)
		}
		if !strings.Contains(got, redacted) {
			t.Errorf("nothing was marked as redacted:\n%s", got)
		}
	}
}

// parsedCommunityLine is gosnmp's own line, produced by round-tripping a v2c
// notification through the call the trap listener decodes with -- rather than
// a copy of it kept in this file, which is what used to stand in for it and
// could not notice a change.
func parsedCommunityLine(t *testing.T) string {
	t.Helper()

	raw, err := (&gosnmp.SnmpPacket{
		Version:   gosnmp.Version2c,
		Community: probeCommunity,
		PDUType:   gosnmp.SNMPv2Trap,
		RequestID: 1,
		Variables: []gosnmp.SnmpPDU{
			{Name: "1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(42)},
		},
	}).MarshalMsg()
	if err != nil {
		t.Fatalf("marshalling a v2c notification: %v", err)
	}

	var logged bytes.Buffer
	g := &gosnmp.GoSNMP{
		Version:   gosnmp.Version2c,
		Community: probeCommunity,
		Logger:    gosnmp.NewLogger(log.New(&logged, "", 0)),
	}
	if _, err := g.UnmarshalTrap(raw, false); err != nil {
		t.Fatalf("decoding it again: %v", err)
	}
	for _, line := range strings.Split(logged.String(), "\n") {
		if strings.HasPrefix(line, "Parsed community ") {
			return line
		}
	}
	t.Fatalf("gosnmp logged no \"Parsed community\" line at all:\n%s", logged.String())
	return ""
}

// handleTrap puts gosnmp's own version string on the event, and the Traps tab
// filters by that value. They are "1", "2c" and "3" -- not "SNMPv2c" -- and a
// filter written for the longer spelling matches nothing and empties the list.
func TestGosnmpNamesTheVersionsTheTrapListFiltersBy(t *testing.T) {
	for _, c := range []struct {
		version gosnmp.SnmpVersion
		want    string
	}{
		{gosnmp.Version1, "1"},
		{gosnmp.Version2c, "2c"},
		{gosnmp.Version3, "3"},
	} {
		if got := c.version.String(); got != c.want {
			t.Errorf("version string is %q, want %q", got, c.want)
		}
	}
}

// gosnmp's user table takes what it is given: Add localises the keys it can
// and says nothing about whether the result could ever authenticate. Both
// users below are accepted and then authenticate nothing at all, which is why
// checkTrapUser refuses them BEFORE the table sees them and names the missing
// field -- gosnmp's own account arrives later, as "hashPassword: password is
// empty" or as a v3 notification silently dropped.
func TestGosnmpsUserTableAcceptsAUserThatCanNeverAuthenticate(t *testing.T) {
	for _, c := range []struct {
		name string
		make func() *gosnmp.UsmSecurityParameters
	}{
		{"no authentication protocol", func() *gosnmp.UsmSecurityParameters {
			return &gosnmp.UsmSecurityParameters{
				UserName:                 "u",
				AuthenticationProtocol:   gosnmp.NoAuth,
				AuthenticationPassphrase: "a-passphrase",
			}
		}},
		{"no authentication passphrase", func() *gosnmp.UsmSecurityParameters {
			return &gosnmp.UsmSecurityParameters{
				UserName:                 "u",
				AuthenticationProtocol:   gosnmp.SHA,
				AuthenticationPassphrase: "",
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := V3Params{User: "u", SecLevel: "AuthNoPriv"}
			if err := checkTrapUser(u, c.make(), gosnmp.AuthNoPriv); err == nil {
				t.Error("checkTrapUser accepted a user that cannot authenticate")
			}
			table := gosnmp.NewSnmpV3SecurityParametersTable(gosnmp.Logger{})
			if err := table.Add("u", c.make()); err != nil {
				t.Fatalf("gosnmp refused this user itself, so checkTrapUser is no longer the\n"+
					"only thing standing between it and a listener that hears nothing: %v", err)
			}
		})
	}
}
