package simulator

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
)

const (
	// timeWindow is how far, in seconds, a message's engine time may stray from
	// this engine's before the message is taken for a replay (RFC 3414 2.2.3).
	timeWindow = 150
	// maxBoots latches an engine: once its boots reach it, no authenticated
	// message is in its time window again (RFC 3414).
	maxBoots = 2147483647
	// minPassphrase is the shortest passphrase a user may have, in octets —
	// net-snmp refuses anything shorter.
	minPassphrase = 8
	// maxUserName is the longest USM user name, an SnmpAdminString (SIZE(1..32)).
	maxUserName = 32
)

// The counters a Report names (SNMP-USER-BASED-SM-MIB, SNMP-MPD-MIB,
// SNMP-TARGET-MIB). The OID is how a manager learns why it was not answered.
const (
	oidUnsupportedSecLevels = ".1.3.6.1.6.3.15.1.1.1.0"
	oidNotInTimeWindows     = ".1.3.6.1.6.3.15.1.1.2.0"
	oidUnknownUserNames     = ".1.3.6.1.6.3.15.1.1.3.0"
	oidUnknownEngineIDs     = ".1.3.6.1.6.3.15.1.1.4.0"
	oidWrongDigests         = ".1.3.6.1.6.3.15.1.1.5.0"
	oidDecryptionErrors     = ".1.3.6.1.6.3.15.1.1.6.0"
	oidUnknownPDUHandlers   = ".1.3.6.1.6.3.11.2.1.3.0"
	oidUnknownContexts      = ".1.3.6.1.6.3.12.1.5.0"
)

// User is an SNMPv3 user of the USM (RFC 3414), named in the words SnmpLens's
// own SNMPv3 settings use (snmp.V3Params), so one can be written from the other.
type User struct {
	Name      string `json:"name"`
	SecLevel  string `json:"secLevel"`  // NoAuthNoPriv, AuthNoPriv or AuthPriv
	AuthProto string `json:"authProto"` // MD5, SHA, SHA224, SHA256, SHA384 or SHA512
	AuthPass  string `json:"authPass"`
	PrivProto string `json:"privProto"` // DES, AES, AES192, AES256, AES192C or AES256C
	PrivPass  string `json:"privPass"`
	// Write lets the user SET; a user without it only reads.
	Write bool `json:"write"`
}

// The names pkg/snmp maps, mapped the same way; TestSnmpLensReadsTheSimulator
// drives the SnmpLens client against every one. A name the two mapped
// differently would authenticate on neither side, and read exactly like a wrong
// passphrase.
var (
	secLevels = map[string]gosnmp.SnmpV3MsgFlags{
		"NoAuthNoPriv": gosnmp.NoAuthNoPriv,
		"AuthNoPriv":   gosnmp.AuthNoPriv,
		"AuthPriv":     gosnmp.AuthPriv,
	}
	authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{
		"MD5":    gosnmp.MD5,
		"SHA":    gosnmp.SHA,
		"SHA224": gosnmp.SHA224,
		"SHA256": gosnmp.SHA256,
		"SHA384": gosnmp.SHA384,
		"SHA512": gosnmp.SHA512,
	}
	privProtocols = map[string]gosnmp.SnmpV3PrivProtocol{
		"DES":     gosnmp.DES,
		"AES":     gosnmp.AES,
		"AES128":  gosnmp.AES,
		"AES192":  gosnmp.AES192,
		"AES256":  gosnmp.AES256,
		"AES192C": gosnmp.AES192C,
		"AES256C": gosnmp.AES256C,
	}
)

// user is a User resolved for one engine: protocols mapped, and keys localised
// to the agent's engine ID once. Localising hashes a megabyte of passphrase
// (RFC 3414's password-to-key algorithm), which done per message would be most
// of what the agent does.
type user struct {
	name    string
	level   gosnmp.SnmpV3MsgFlags
	auth    gosnmp.SnmpV3AuthProtocol
	priv    gosnmp.SnmpV3PrivProtocol
	authKey []byte
	privKey []byte
	write   bool
}

func newUsers(list []User, engineID []byte) (map[string]*user, error) {
	users := make(map[string]*user, len(list))
	for _, u := range list {
		r, err := resolveUser(u, engineID)
		if err != nil {
			return nil, err
		}
		if users[r.name] != nil {
			return nil, fmt.Errorf("user %q is declared twice", r.name)
		}
		users[r.name] = r
	}
	return users, nil
}

// checkUser reports what is wrong with u and maps its names, deriving no key.
// A protocol above the user's level is ignored rather than refused, as pkg/snmp
// ignores it: a form keeps its defaults in the fields a lower level disables.
func checkUser(u User) (*user, error) {
	if u.Name == "" || len(u.Name) > maxUserName {
		return nil, fmt.Errorf("user %q: a USM user name is 1 to %d octets", u.Name, maxUserName)
	}
	level, ok := secLevels[u.SecLevel]
	if !ok {
		return nil, fmt.Errorf("user %q: %q is not a security level (NoAuthNoPriv, AuthNoPriv or AuthPriv)", u.Name, u.SecLevel)
	}
	r := &user{name: u.Name, level: level, auth: gosnmp.NoAuth, priv: gosnmp.NoPriv, write: u.Write}
	if level&gosnmp.AuthNoPriv != 0 {
		if r.auth, ok = authProtocols[strings.ToUpper(u.AuthProto)]; !ok {
			return nil, fmt.Errorf("user %q: %q is not an authentication protocol", u.Name, u.AuthProto)
		}
		if len(u.AuthPass) < minPassphrase {
			return nil, fmt.Errorf("user %q: the authentication passphrase is shorter than %d octets", u.Name, minPassphrase)
		}
	}
	if level == gosnmp.AuthPriv {
		if r.priv, ok = privProtocols[strings.ToUpper(u.PrivProto)]; !ok {
			return nil, fmt.Errorf("user %q: %q is not a privacy protocol", u.Name, u.PrivProto)
		}
		if len(u.PrivPass) < minPassphrase {
			return nil, fmt.Errorf("user %q: the privacy passphrase is shorter than %d octets", u.Name, minPassphrase)
		}
	}
	return r, nil
}

// resolveUser checks u and localises its keys to engineID.
func resolveUser(u User, engineID []byte) (*user, error) {
	r, err := checkUser(u)
	if err != nil {
		return nil, err
	}
	sp := &gosnmp.UsmSecurityParameters{
		UserName:               u.Name,
		AuthoritativeEngineID:  string(engineID),
		AuthenticationProtocol: r.auth,
		PrivacyProtocol:        r.priv,
	}
	if r.level&gosnmp.AuthNoPriv != 0 {
		sp.AuthenticationPassphrase = u.AuthPass
	}
	if r.level == gosnmp.AuthPriv {
		sp.PrivacyPassphrase = u.PrivPass
	}
	if err := sp.InitSecurityKeys(); err != nil {
		return nil, fmt.Errorf("user %q: %w", u.Name, err)
	}
	r.authKey, r.privKey = sp.SecretKey, sp.PrivacyKey
	return r, nil
}

// params is u as gosnmp holds a user, for a message at level: the protocols
// that level uses and none above, with the keys ready so gosnmp derives none.
func (u *user) params(engineID []byte, level gosnmp.SnmpV3MsgFlags) *gosnmp.UsmSecurityParameters {
	sp := &gosnmp.UsmSecurityParameters{
		UserName:               u.name,
		AuthoritativeEngineID:  string(engineID),
		AuthenticationProtocol: gosnmp.NoAuth,
		PrivacyProtocol:        gosnmp.NoPriv,
	}
	if level&gosnmp.AuthNoPriv != 0 {
		sp.AuthenticationProtocol, sp.SecretKey = u.auth, u.authKey
	}
	if level == gosnmp.AuthPriv {
		sp.PrivacyProtocol, sp.PrivacyKey = u.priv, u.privKey
	}
	return sp
}

// EngineID builds an snmpEngineID in RFC 3411's MAC format: the vendor's
// enterprise number with its high bit set, the format octet 3, then a MAC
// address. It is the shape a Cisco reports (80 00 00 09 03 …), and it stays the
// same for as long as the MAC does — the property that matters, since a manager
// caches the ID and localises its keys to it.
func EngineID(enterprise uint32, mac net.HardwareAddr) ([]byte, error) {
	if enterprise >= 1<<31 {
		return nil, fmt.Errorf("enterprise number %d does not fit an engine ID", enterprise)
	}
	if len(mac) != 6 {
		return nil, fmt.Errorf("an engine ID's MAC is 6 octets, not %d", len(mac))
	}
	id := binary.BigEndian.AppendUint32(make([]byte, 0, 11), enterprise|1<<31)
	id = append(id, 3)
	return append(id, mac...), nil
}

// answerV3 answers one SNMPv3 message with a Response, with a Report saying why
// not, or with nothing — checking in the order RFC 3414 3.2 does, so a manager
// hears about the first thing wrong rather than a later one.
func (a *Agent) answerV3(msg []byte, c clock) []byte {
	h, err := peekV3(msg)
	if err != nil {
		a.stats.parseErrors.Add(1)
		return nil
	}
	if h.model != int64(gosnmp.UserSecurityModel) {
		// No Report can be built in a security model this engine does not have.
		a.stats.unknownSecurityModels.Add(1)
		return nil
	}
	level := h.level()
	if level != gosnmp.NoAuthNoPriv && level != gosnmp.AuthNoPriv && level != gosnmp.AuthPriv {
		// Privacy without authentication, which no message may ask for.
		a.stats.invalidMsgs.Add(1)
		return nil
	}

	// Steps 3 to 5, from what the message claims.
	if !bytes.Equal(h.engineID, a.engineID) {
		// Discovery lands here: a manager's first message names no engine, and
		// this Report is how it learns the ID, boots and time it needs next.
		return a.report(h, nil, gosnmp.NoAuthNoPriv, h.requestID, oidUnknownEngineIDs, &a.stats.unknownEngineIDs, c)
	}
	u := a.users[h.user]
	if u == nil {
		a.authFailed()
		return a.report(h, nil, gosnmp.NoAuthNoPriv, h.requestID, oidUnknownUserNames, &a.stats.unknownUserNames, c)
	}
	if level > u.level {
		return a.report(h, nil, gosnmp.NoAuthNoPriv, h.requestID, oidUnsupportedSecLevels, &a.stats.unsupportedSecLevels, c)
	}

	// Steps 6 and 8 — the digest, then decryption — are one call into gosnmp, so
	// step 7, the time window, comes after both instead of between them: a
	// message failing decryption AND the window is told about the decryption,
	// where the RFC would name the window.
	req, err := a.open(u, level, msg)
	if err != nil {
		return a.refuse(h, u, level, msg, c)
	}
	// A replay, or a manager still holding the clock of this device's previous
	// boot, which it resynchronises from this Report.
	if level != gosnmp.NoAuthNoPriv && !a.inTimeWindow(h, c) {
		return a.report(h, u, gosnmp.AuthNoPriv, req.RequestID, oidNotInTimeWindows, &a.stats.notInTimeWindows, c)
	}

	switch req.PDUType {
	case gosnmp.GetRequest, gosnmp.GetNextRequest, gosnmp.GetBulkRequest, gosnmp.SetRequest:
	default:
		// A Response, a Report or a notification: nothing here takes one.
		a.stats.unknownPDUHandlers.Add(1)
		return nil
	}
	if req.ContextEngineID != "" && req.ContextEngineID != string(a.engineID) {
		return a.report(h, u, level, req.RequestID, oidUnknownPDUHandlers, &a.stats.unknownPDUHandlers, c)
	}
	var view *tree
	if req.ContextName != "" {
		// A context the device has: a VLAN's view of a switch answering
		// BRIDGE-MIB per VLAN. Any other is unknown, which is said in a Report.
		if view = a.context(req.ContextName); view == nil {
			return a.report(h, u, level, req.RequestID, oidUnknownContexts, &a.stats.unknownContexts, c)
		}
	}

	var ans answer
	if level < u.level {
		// Genuine, but below the level this user is held to — what a device
		// configured with net-snmp's "rouser NAME priv" answers: an
		// authorizationError, and nothing read.
		ans = answer{vars: req.Variables, status: gosnmp.AuthorizationError}
	} else {
		ans = a.process(gosnmp.Version3, req, c, u.write, view)
	}
	out, err := fit(ans, bulkFloor(req), tooBig(gosnmp.Version3, req), min(a.maxSize, int(req.MsgMaxSize)),
		func(ans answer) ([]byte, error) {
			pkt := a.v3Packet(u, u.name, level, h.msgID, c)
			pkt.PDUType = gosnmp.GetResponse
			pkt.RequestID = req.RequestID
			pkt.Error, pkt.ErrorIndex, pkt.Variables = ans.status, ans.index, ans.vars
			return pkt.MarshalMsg()
		})
	if err != nil {
		a.stats.faults.Add(1)
		return nil
	}
	return out
}

// decoder is a gosnmp engine holding u at level, to read one message with.
func (a *Agent) decoder(u *user, level gosnmp.SnmpV3MsgFlags) *gosnmp.GoSNMP {
	return &gosnmp.GoSNMP{
		Version:            gosnmp.Version3,
		SecurityModel:      gosnmp.UserSecurityModel,
		MsgFlags:           level,
		SecurityParameters: u.params(a.engineID, level),
	}
}

// open authenticates, decrypts and decodes a message for u at level.
//
// UnmarshalTrap is the one gosnmp entry point that checks the digest of a
// message gosnmp did not send, and despite its name it decodes any PDU. It
// checks at the decoder's level, which answerV3 has held to the user's, and it
// is given a copy because it blanks the digest in place to recompute it.
func (a *Agent) open(u *user, level gosnmp.SnmpV3MsgFlags, msg []byte) (*gosnmp.SnmpPacket, error) {
	return a.decoder(u, level).UnmarshalTrap(bytes.Clone(msg), false)
}

// refuse answers a message gosnmp could not open for u.
//
// The failure does not say whether the digest or the decryption failed, and
// decoding again WITHOUT the digest does: a message that then decrypts and
// parses had a wrong digest. One that does not has a privacy key that
// disagrees, and whether its digest was right as well cannot be learnt apart,
// since gosnmp checks it only together with decrypting. RFC 3414 names the
// digest when both are wrong and this names the decryption — either way the
// manager is told about a passphrase that is wrong.
func (a *Agent) refuse(h *v3Header, u *user, level gosnmp.SnmpV3MsgFlags, msg []byte, c clock) []byte {
	if level == gosnmp.NoAuthNoPriv {
		a.stats.parseErrors.Add(1)
		return nil
	}
	if _, err := a.decoder(u, level).SnmpDecodePacket(bytes.Clone(msg)); err == nil {
		a.authFailed()
		return a.report(h, nil, gosnmp.NoAuthNoPriv, h.requestID, oidWrongDigests, &a.stats.wrongDigests, c)
	}
	if level == gosnmp.AuthPriv {
		return a.report(h, nil, gosnmp.NoAuthNoPriv, h.requestID, oidDecryptionErrors, &a.stats.decryptionErrors, c)
	}
	a.stats.parseErrors.Add(1)
	return nil
}

// inTimeWindow is RFC 3414 3.2 step 7a: an authenticated message must name this
// engine's boots, and a time within timeWindow seconds of its own.
func (a *Agent) inTimeWindow(h *v3Header, c clock) bool {
	if a.boots >= maxBoots {
		return false
	}
	d := h.time - int64(a.engineTime(c))
	return h.boots == int64(a.boots) && d >= -timeWindow && d <= timeWindow
}

// engineTime is snmpEngineTime: seconds since the agent started.
func (a *Agent) engineTime(c clock) uint32 {
	return uint32(c.now.Sub(c.started) / time.Second)
}

// report answers a refused message with a Report naming the counter that
// counted it, if the message asked for one (its reportable flag).
//
// A Report is unauthenticated unless u is given. Before the digest checks out
// there is nothing to sign for; after, a manager holding the key can tell a
// genuine Report from a forged one — which matters for the time window, where a
// forged Report would move the manager to a clock of someone else's choosing.
func (a *Agent) report(h *v3Header, u *user, level gosnmp.SnmpV3MsgFlags, requestID uint32, name string, counter *atomic.Uint32, c clock) []byte {
	n := counter.Add(1)
	if h.flags&gosnmp.Reportable == 0 {
		return nil
	}
	pkt := a.v3Packet(u, h.user, level, h.msgID, c)
	pkt.PDUType = gosnmp.Report
	pkt.RequestID = requestID
	pkt.Variables = []gosnmp.SnmpPDU{{Name: name, Type: gosnmp.Counter32, Value: n}}
	out, err := pkt.MarshalMsg()
	if err != nil {
		a.stats.faults.Add(1)
		return nil
	}
	return out
}

// v3Packet is a message from this engine — its engine ID, boots and time — sent
// as userName at level, signed and encrypted with u's keys when level asks.
func (a *Agent) v3Packet(u *user, userName string, level gosnmp.SnmpV3MsgFlags, msgID uint32, c clock) *gosnmp.SnmpPacket {
	sp := &gosnmp.UsmSecurityParameters{AuthenticationProtocol: gosnmp.NoAuth, PrivacyProtocol: gosnmp.NoPriv}
	if u != nil {
		sp = u.params(a.engineID, level)
	}
	sp.UserName = userName
	sp.AuthoritativeEngineID = string(a.engineID)
	sp.AuthoritativeEngineBoots = a.boots
	sp.AuthoritativeEngineTime = a.engineTime(c)
	if level == gosnmp.AuthPriv {
		// The salt is the only part of the IV that changes between two messages
		// sent in the same second (RFC 3826 3.1.2.1, RFC 3414 8.1.1.1), and an IV
		// used twice under one key undoes what the encryption promises.
		// MarshalMsg sends whatever PrivacyParameters holds — gosnmp draws a salt
		// only on its own send path — so every message draws one here.
		sp.PrivacyParameters = make([]byte, 8)
		rand.Read(sp.PrivacyParameters) // never fails since Go 1.24
	}
	return &gosnmp.SnmpPacket{
		Version:            gosnmp.Version3,
		MsgFlags:           level,
		SecurityModel:      gosnmp.UserSecurityModel,
		SecurityParameters: sp,
		MsgID:              msgID,
		MsgMaxSize:         uint32(a.maxSize),
		ContextEngineID:    string(a.engineID),
	}
}
