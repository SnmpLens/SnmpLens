package snmp

import (
	"fmt"

	"github.com/gosnmp/gosnmp"
)

// usmFor turns one set of v3 identifiers into gosnmp's USM parameters and the
// message flags that carry its security level.
//
// ONE function for both directions — a request SnmpLens sends and a
// notification it receives — because the rule that makes the parameters valid
// is the same in both, and it had been written for requests only. Privacy
// applies at AuthPriv and authentication at the Auth* levels, and nowhere else.
// The UI keeps its default protocols while their fields are disabled, so a
// NoAuthNoPriv user arrives carrying MD5 and DES, and gosnmp then demands
// passphrases nobody was asked for — which surfaced as SNMPv3 authentication
// failures. Fixes #3 (reported by @JessonJiang).
func usmFor(v3 V3Params) (*gosnmp.UsmSecurityParameters, gosnmp.SnmpV3MsgFlags, error) {
	level, err := getSecurityLevel(v3.SecLevel)
	if err != nil {
		return nil, gosnmp.NoAuthNoPriv, err
	}
	authProto, err := getAuthProtocol(v3.AuthProto)
	if err != nil {
		return nil, gosnmp.NoAuthNoPriv, err
	}
	privProto, err := getPrivProtocol(v3.PrivProto)
	if err != nil {
		return nil, gosnmp.NoAuthNoPriv, err
	}
	if level != gosnmp.AuthPriv {
		privProto = gosnmp.NoPriv
	}
	if level == gosnmp.NoAuthNoPriv {
		authProto = gosnmp.NoAuth
	}
	return &gosnmp.UsmSecurityParameters{
		UserName:                 v3.User,
		AuthenticationProtocol:   authProto,
		AuthenticationPassphrase: v3.AuthPass,
		PrivacyProtocol:          privProto,
		PrivacyPassphrase:        v3.PrivPass,
	}, level, nil
}

// TrapUserRefusal says why one USM user was left out of the trap listener.
type TrapUserRefusal struct {
	User   string `json:"user"`
	Reason string `json:"reason"`
}

// TrapListenerInfo is what starting a listener, or changing its users, did.
//
// The accepted users themselves never cross the bridge: they carry the
// passphrases, and the renderer that sent them already has them. A count goes
// back, and a reason for every user that was refused.
type TrapListenerInfo struct {
	Users     int               `json:"users"`
	Refused   []TrapUserRefusal `json:"refused"`
	Restarted bool              `json:"restarted"`
	accepted  []V3Params
}

// AcceptedUsers is every user the listener took, reduced to what receiving
// needs, in the order given. It is what is worth remembering for the next
// start.
func (i TrapListenerInfo) AcceptedUsers() []V3Params { return i.accepted }

// trapUser reduces a set of identifiers to what RECEIVING uses.
//
// Two profiles that differ only in something a notification never consults are
// then one user, and a remembered set holds no passphrase the listener does not
// use. The context name goes — a notification carries its own and the listener
// does not select by it — and so do the protocols and passphrases above the
// security level, which is usmFor's rule applied to the stored value.
func trapUser(v3 V3Params) V3Params {
	u := V3Params{User: v3.User, SecLevel: v3.SecLevel}
	switch v3.SecLevel {
	case "AuthPriv":
		u.AuthProto, u.AuthPass = v3.AuthProto, v3.AuthPass
		u.PrivProto, u.PrivPass = v3.PrivProto, v3.PrivPass
	case "AuthNoPriv":
		u.AuthProto, u.AuthPass = v3.AuthProto, v3.AuthPass
	}
	return u
}

// checkTrapUser refuses a user the table would accept and every notification
// would then fail against.
//
// gosnmp's SnmpV3SecurityParametersTable.Add localises the keys and nothing
// more; it does not run the validation a request gets. An AuthNoPriv user with
// no authentication protocol is added, and then authenticates nothing, silently.
// An empty passphrase does fail, but as "hashPassword: password is empty", which
// names neither the user nor the field.
func checkTrapUser(u V3Params, sp *gosnmp.UsmSecurityParameters, level gosnmp.SnmpV3MsgFlags) error {
	if level&gosnmp.AuthNoPriv != 0 {
		if sp.AuthenticationProtocol <= gosnmp.NoAuth {
			return fmt.Errorf("%s needs an authentication protocol", u.SecLevel)
		}
		if sp.AuthenticationPassphrase == "" {
			return fmt.Errorf("%s needs an authentication passphrase", u.SecLevel)
		}
	}
	if level == gosnmp.AuthPriv {
		if sp.PrivacyProtocol <= gosnmp.NoPriv {
			return fmt.Errorf("%s needs a privacy protocol", u.SecLevel)
		}
		if sp.PrivacyPassphrase == "" {
			return fmt.Errorf("%s needs a privacy passphrase", u.SecLevel)
		}
	}
	return nil
}

// trapSecurity is the USM half of a trap listener.
type trapSecurity struct {
	table *gosnmp.SnmpV3SecurityParametersTable
	// first also becomes the listener's own SecurityParameters, and that is
	// not optional once a table is in use. gosnmp's receive loop asserts
	// t.Params.SecurityParameters to *UsmSecurityParameters for EVERY v3
	// notification in order to compare engine IDs, logs when the assertion
	// fails, and dereferences the result anyway. With a table and no
	// SecurityParameters the first v3 trap to arrive is a nil-pointer panic on
	// gosnmp's own goroutine, which no recover of ours covers, and the process
	// ends (gosnmp v1.45.0, trap.go, listenUDP).
	first *gosnmp.UsmSecurityParameters
	flags gosnmp.SnmpV3MsgFlags
	info  TrapListenerInfo
}

// newTrapSecurity builds the table of every usable user, and says why each of
// the others was refused.
func newTrapSecurity(users []V3Params, logger gosnmp.Logger) trapSecurity {
	ts := trapSecurity{info: TrapListenerInfo{Refused: []TrapUserRefusal{}}}
	seen := make(map[V3Params]bool, len(users))
	for _, given := range users {
		u := trapUser(given)
		if u.User == "" || seen[u] {
			continue
		}
		seen[u] = true

		sp, level, err := usmFor(u)
		if err == nil {
			err = checkTrapUser(u, sp, level)
		}
		if err == nil {
			if ts.table == nil {
				ts.table = gosnmp.NewSnmpV3SecurityParametersTable(logger)
			}
			// Keyed by user name, which is what gosnmp looks a notification up
			// by. Two entries under one name are both kept and tried in turn:
			// one USM user configured with different keys on two devices is an
			// ordinary thing to meet.
			err = ts.table.Add(u.User, sp)
		}
		if err != nil {
			ts.info.Refused = append(ts.info.Refused, TrapUserRefusal{User: u.User, Reason: err.Error()})
			continue
		}
		if ts.first == nil {
			// A second value, not the one in the table: the table's entries
			// are copied for every attempt and must stay as they were added.
			ts.first, ts.flags, _ = usmFor(u)
		}
		ts.info.accepted = append(ts.info.accepted, u)
	}
	ts.info.Users = len(ts.info.accepted)
	return ts
}

// sameTrapUsers compares two accepted sets, ignoring order: listing the same
// profiles in a different order is not a reason to close the port.
func sameTrapUsers(a, b []V3Params) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[V3Params]int, len(a))
	for _, u := range a {
		count[u]++
	}
	for _, u := range b {
		if count[u] == 0 {
			return false
		}
		count[u]--
	}
	return true
}
