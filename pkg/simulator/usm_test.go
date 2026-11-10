package simulator

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// v3Case is one simulated user, named in SnmpLens's words, beside the gosnmp
// constants a manager speaks the same thing with — so the matrix below checks
// that each name means what gosnmp means by it.
type v3Case struct {
	user     string
	level    string
	flags    gosnmp.SnmpV3MsgFlags
	authName string
	auth     gosnmp.SnmpV3AuthProtocol
	privName string
	priv     gosnmp.SnmpV3PrivProtocol
}

var v3Cases = []v3Case{
	{"noauth", "NoAuthNoPriv", gosnmp.NoAuthNoPriv, "", gosnmp.NoAuth, "", gosnmp.NoPriv},
	{"md5", "AuthNoPriv", gosnmp.AuthNoPriv, "MD5", gosnmp.MD5, "", gosnmp.NoPriv},
	{"sha", "AuthNoPriv", gosnmp.AuthNoPriv, "SHA", gosnmp.SHA, "", gosnmp.NoPriv},
	{"sha224", "AuthNoPriv", gosnmp.AuthNoPriv, "SHA224", gosnmp.SHA224, "", gosnmp.NoPriv},
	{"sha256", "AuthNoPriv", gosnmp.AuthNoPriv, "SHA256", gosnmp.SHA256, "", gosnmp.NoPriv},
	{"sha384", "AuthNoPriv", gosnmp.AuthNoPriv, "SHA384", gosnmp.SHA384, "", gosnmp.NoPriv},
	{"sha512", "AuthNoPriv", gosnmp.AuthNoPriv, "SHA512", gosnmp.SHA512, "", gosnmp.NoPriv},
	{"md5-des", "AuthPriv", gosnmp.AuthPriv, "MD5", gosnmp.MD5, "DES", gosnmp.DES},
	{"sha-des", "AuthPriv", gosnmp.AuthPriv, "SHA", gosnmp.SHA, "DES", gosnmp.DES},
	{"sha-aes", "AuthPriv", gosnmp.AuthPriv, "SHA", gosnmp.SHA, "AES", gosnmp.AES},
	{"sha256-aes192", "AuthPriv", gosnmp.AuthPriv, "SHA256", gosnmp.SHA256, "AES192", gosnmp.AES192},
	{"sha256-aes256", "AuthPriv", gosnmp.AuthPriv, "SHA256", gosnmp.SHA256, "AES256", gosnmp.AES256},
	{"sha512-aes256", "AuthPriv", gosnmp.AuthPriv, "SHA512", gosnmp.SHA512, "AES256", gosnmp.AES256},
	{"sha256-aes192c", "AuthPriv", gosnmp.AuthPriv, "SHA256", gosnmp.SHA256, "AES192C", gosnmp.AES192C},
	{"sha256-aes256c", "AuthPriv", gosnmp.AuthPriv, "SHA256", gosnmp.SHA256, "AES256C", gosnmp.AES256C},
}

func caseNamed(t *testing.T, name string) v3Case {
	t.Helper()
	for _, c := range v3Cases {
		if c.user == name {
			return c
		}
	}
	t.Fatalf("no case %q", name)
	return v3Case{}
}

func (c v3Case) simUser() User {
	return User{
		Name:      c.user,
		SecLevel:  c.level,
		AuthProto: c.authName,
		AuthPass:  "auth-" + c.user + "-pass",
		PrivProto: c.privName,
		PrivPass:  "priv-" + c.user + "-pass",
	}
}

func (c v3Case) manager(a *Agent) *gosnmp.GoSNMP {
	u := c.simUser()
	return v3Manager(a, c.user, c.flags, c.auth, u.AuthPass, c.priv, u.PrivPass)
}

func v3Agent(t *testing.T, cases ...v3Case) *Agent {
	t.Helper()
	users := make([]User, len(cases))
	for i, c := range cases {
		users[i] = c.simUser()
	}
	return startAgent(t, Config{Versions: []string{"v3"}, Users: users})
}

func usm(g *gosnmp.GoSNMP) *gosnmp.UsmSecurityParameters {
	return g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
}

// Every level, and every protocol at it, against one agent holding all the
// users at once — which is also what a device with several users is.
func TestSNMPv3AtEveryLevelWithEveryProtocol(t *testing.T) {
	a := v3Agent(t, v3Cases...)
	for _, c := range v3Cases {
		t.Run(c.user, func(t *testing.T) {
			g := connect(t, c.manager(a))
			res, err := g.Get([]string{sysDescrOID})
			if err != nil {
				t.Fatal(err)
			}
			if got := text(res.Variables[0]); got != sysDescr {
				t.Fatalf("sysDescr = %q", got)
			}
		})
	}
	s := a.Stats()
	if s.WrongDigests+s.DecryptionErrors+s.UnknownUserNames+s.UnsupportedSecLevels+
		s.NotInTimeWindows+s.ParseErrors+s.Faults != 0 {
		t.Errorf("refusals along the way: %+v", s)
	}
}

// A manager's first message names no engine. The Report it gets back is how it
// learns the engine ID, boots and time it must use, and it then asks with them.
func TestDiscoveryHandsTheManagerThisEngine(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	a := startAgent(t, Config{Versions: []string{"v3"}, Users: []User{c.simUser()}, EngineBoots: 7})
	g := connect(t, c.manager(a))

	if _, err := g.Get([]string{sysDescrOID}); err != nil {
		t.Fatal(err)
	}
	if sp := usm(g); sp.AuthoritativeEngineID != string(testEngineID) || sp.AuthoritativeEngineBoots != 7 {
		t.Errorf("the manager holds engine %x, boots %d; want %x, 7",
			sp.AuthoritativeEngineID, sp.AuthoritativeEngineBoots, testEngineID)
	}
	if n := a.Stats().UnknownEngineIDs; n != 1 {
		t.Errorf("%d discoveries, want 1", n)
	}
}

// The two passphrases are two different fixes, and each is reported as itself.
func TestAWrongAuthenticationPassphraseIsAWrongDigest(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	a := v3Agent(t, c)
	g := c.manager(a)
	usm(g).AuthenticationPassphrase = "not-the-passphrase"
	g.Timeout = silent
	connect(t, g)

	if _, err := g.Get([]string{sysDescrOID}); err == nil {
		t.Fatal("answered the wrong authentication passphrase")
	}
	if s := a.Stats(); s.WrongDigests != 1 || s.DecryptionErrors != 0 {
		t.Errorf("WrongDigests %d, DecryptionErrors %d; want 1, 0", s.WrongDigests, s.DecryptionErrors)
	}
}

func TestAWrongPrivacyPassphraseIsADecryptionError(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	a := v3Agent(t, c)
	g := c.manager(a)
	usm(g).PrivacyPassphrase = "not-the-passphrase"
	g.Timeout = silent
	connect(t, g)

	if _, err := g.Get([]string{sysDescrOID}); err == nil {
		t.Fatal("answered the wrong privacy passphrase")
	}
	if s := a.Stats(); s.DecryptionErrors != 1 || s.WrongDigests != 0 {
		t.Errorf("DecryptionErrors %d, WrongDigests %d; want 1, 0", s.DecryptionErrors, s.WrongDigests)
	}
}

func TestAnUnknownUserIsReported(t *testing.T) {
	a := v3Agent(t, caseNamed(t, "sha-aes"))
	g := connect(t, v3Manager(a, "nobody", gosnmp.NoAuthNoPriv, gosnmp.NoAuth, "", gosnmp.NoPriv, ""))

	if _, err := g.Get([]string{sysDescrOID}); !errors.Is(err, gosnmp.ErrUnknownUsername) {
		t.Fatalf("err = %v, want gosnmp's unknown username", err)
	}
	if n := a.Stats().UnknownUserNames; n != 1 {
		t.Errorf("UnknownUserNames = %d, want 1", n)
	}
}

func TestALevelAboveTheUsersIsUnsupported(t *testing.T) {
	c := caseNamed(t, "sha") // AuthNoPriv: this user has no privacy key
	a := v3Agent(t, c)
	g := v3Manager(a, c.user, gosnmp.AuthPriv, gosnmp.SHA, c.simUser().AuthPass, gosnmp.AES, "priv-invented-pass")
	g.Timeout = silent
	connect(t, g)

	if _, err := g.Get([]string{sysDescrOID}); err == nil {
		t.Fatal("answered at a level the user has no key for")
	}
	if n := a.Stats().UnsupportedSecLevels; n != 1 {
		t.Errorf("UnsupportedSecLevels = %d, want 1", n)
	}
}

// Below the level a user is held to, a genuine request reads nothing. The
// noAuthNoPriv case is doc.go's second gosnmp behaviour: trusted to take the
// level from the message, gosnmp would have checked nothing at all.
func TestALevelBelowTheUsersReadsNothing(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	a := v3Agent(t, c)
	u := c.simUser()

	for _, g := range []*gosnmp.GoSNMP{
		v3Manager(a, c.user, gosnmp.AuthNoPriv, gosnmp.SHA, u.AuthPass, gosnmp.NoPriv, ""),
		v3Manager(a, c.user, gosnmp.NoAuthNoPriv, gosnmp.NoAuth, "", gosnmp.NoPriv, ""),
	} {
		connect(t, g)
		res, err := g.Get([]string{sysDescrOID})
		if err != nil {
			t.Fatal(err)
		}
		if res.Error != gosnmp.AuthorizationError {
			t.Errorf("at %v: %v, want authorizationError", g.MsgFlags&gosnmp.AuthPriv, res.Error)
		}
		for _, v := range res.Variables {
			if text(v) == sysDescr {
				t.Errorf("at %v: sysDescr was read", g.MsgFlags&gosnmp.AuthPriv)
			}
		}
	}
}

// doc.go's first gosnmp behaviour. Handed this message, gosnmp would localise
// its keys to the engine the message names and find the digest good; the agent
// never asks it, because the engine is not this one.
func TestAMessageForAnotherEngineIsNotAnswered(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	a := v3Agent(t, c)
	g := c.manager(a)
	usm(g).AuthoritativeEngineID = string(mustEngineID(9, "02:00:00:00:00:02"))
	g.Timeout = silent
	connect(t, g)

	if _, err := g.Get([]string{sysDescrOID}); err == nil {
		t.Fatal("answered a message addressed to another engine")
	}
	if s := a.Stats(); s.UnknownEngineIDs != 1 || s.WrongDigests != 0 {
		t.Errorf("UnknownEngineIDs %d, WrongDigests %d; want 1, 0", s.UnknownEngineIDs, s.WrongDigests)
	}
}

// A device that restarts comes back with its boots one higher and its clock at
// zero, and a manager still holding the old ones sends a message outside the
// time window. The authenticated Report is how the manager catches up, with
// nobody reconnecting it.
func TestAManagerFollowsARestartedDevice(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	first := startAgent(t, Config{Versions: []string{"v3"}, Users: []User{c.simUser()}, EngineBoots: 1})
	g := connect(t, c.manager(first))
	if _, err := g.Get([]string{sysDescrOID}); err != nil {
		t.Fatal(err)
	}

	addr := first.Addr()
	first.Stop()
	second := startAgent(t, Config{Listen: addr.String(), Versions: []string{"v3"}, Users: []User{c.simUser()}, EngineBoots: 2})

	res, err := g.Get([]string{sysDescrOID})
	if err != nil {
		t.Fatalf("after the restart: %v", err)
	}
	if got := text(res.Variables[0]); got != sysDescr {
		t.Fatalf("sysDescr = %q", got)
	}
	if n := second.Stats().NotInTimeWindows; n != 1 {
		t.Errorf("NotInTimeWindows = %d, want 1", n)
	}
	if b := usm(g).AuthoritativeEngineBoots; b != 2 {
		t.Errorf("the manager holds boots %d, want 2", b)
	}
}

// A context the device does not have is reported, never answered from the
// default one: an agent with no VLAN views has the default context alone.
func TestAContextTheDeviceHasNotIsUnknown(t *testing.T) {
	c := caseNamed(t, "sha-aes")
	a := v3Agent(t, c)
	g := c.manager(a)
	g.ContextName = "vlan-10"
	connect(t, g)

	// gosnmp has no name of its own for snmpUnknownContexts.
	if _, err := g.Get([]string{sysDescrOID}); !errors.Is(err, gosnmp.ErrUnknownReportPDU) {
		t.Fatalf("err = %v, want a Report", err)
	}
	if n := a.Stats().UnknownContexts; n != 1 {
		t.Errorf("UnknownContexts = %d, want 1", n)
	}
}

// Messages no engine may accept are dropped before the USM: a security model
// this engine does not have, and privacy asked for without authentication.
func TestAMessageNoEngineWouldAcceptIsDropped(t *testing.T) {
	a := newAgent(t, Config{Versions: []string{"v3"}, Users: []User{caseNamed(t, "sha-aes").simUser()}})
	message := func(model gosnmp.SnmpV3SecurityModel, flags gosnmp.SnmpV3MsgFlags) []byte {
		msg, err := (&gosnmp.SnmpPacket{
			Version:            gosnmp.Version3,
			MsgFlags:           flags,
			SecurityModel:      model,
			SecurityParameters: &gosnmp.UsmSecurityParameters{UserName: "sha-aes", AuthoritativeEngineID: string(testEngineID)},
			MsgID:              1,
			PDUType:            gosnmp.GetRequest,
			RequestID:          1,
		}).MarshalMsg()
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}

	if out := a.handle(message(2, gosnmp.Reportable), now()); out != nil {
		t.Error("answered security model 2")
	}
	if out := a.handle(message(gosnmp.UserSecurityModel, gosnmp.Reportable|0x2), now()); out != nil {
		t.Error("answered privacy without authentication")
	}
	if s := a.Stats(); s.UnknownSecurityModels != 1 || s.InvalidMsgs != 1 {
		t.Errorf("%+v", s)
	}
}

// A Report goes out only to a message that asked for one.
func TestAReportIsSentOnlyWhenAskedFor(t *testing.T) {
	a := newAgent(t, Config{Versions: []string{"v3"}, Users: []User{caseNamed(t, "sha-aes").simUser()}})
	discovery := func(flags gosnmp.SnmpV3MsgFlags) []byte {
		msg, err := (&gosnmp.SnmpPacket{
			Version:            gosnmp.Version3,
			MsgFlags:           flags,
			SecurityModel:      gosnmp.UserSecurityModel,
			SecurityParameters: &gosnmp.UsmSecurityParameters{},
			MsgID:              9,
			PDUType:            gosnmp.GetRequest,
			RequestID:          42,
		}).MarshalMsg()
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}

	if out := a.handle(discovery(gosnmp.NoAuthNoPriv), now()); out != nil {
		t.Error("sent a Report nobody asked for")
	}
	out := a.handle(discovery(gosnmp.Reportable), now())
	h, err := peekV3(out)
	if err != nil {
		t.Fatalf("the Report does not read: %v", err)
	}
	if h.msgID != 9 || h.requestID != 42 || !bytes.Equal(h.engineID, testEngineID) {
		t.Errorf("the Report carries msgID %d, request-id %d, engine %x; want 9, 42, %x",
			h.msgID, h.requestID, h.engineID, testEngineID)
	}
	if h.flags&gosnmp.Reportable != 0 {
		t.Error("a Report asking for a Report")
	}
}
