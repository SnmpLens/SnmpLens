package engine

import (
	"encoding/json"
	"log"

	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/snmp"
)

// StartTrapListener starts listening for SNMP traps, accepting every SNMPv3 user
// in the request, and remembers the users it took for the next start.
func (eng *Engine) StartTrapListener(req snmp.TrapListenerRequest) (snmp.TrapListenerInfo, error) {
	info, err := eng.snmpClient.StartTrapListener(req.Port, req.Users)
	if err != nil {
		return info, err
	}
	eng.saveTrapUsers(info.AcceptedUsers())
	return info, nil
}

// UpdateTrapUsers applies a new set of SNMPv3 users.
//
// A running listener is restarted when the set it accepts changed, and the set
// is remembered either way: a listener started at login, before any window,
// then accepts what the settings say now rather than what they said the last
// time somebody pressed Start.
func (eng *Engine) UpdateTrapUsers(users []snmp.V3Params) (snmp.TrapListenerInfo, error) {
	info, err := eng.snmpClient.UpdateTrapUsers(users)
	if err != nil {
		return info, err
	}
	eng.saveTrapUsers(info.AcceptedUsers())
	return info, nil
}

// saveTrapUsers keeps the accepted users in the secret store. An empty set
// deletes the entry rather than storing "[]", so nothing is left behind once
// the last v3 profile goes.
func (eng *Engine) saveTrapUsers(users []snmp.V3Params) {
	if eng.secrets == nil {
		return
	}
	if len(users) == 0 {
		if err := eng.secrets.Delete(secrets.TrapUsersRef()); err != nil {
			log.Printf("WARNING: could not forget the trap listener's SNMPv3 users: %v", err)
		}
		return
	}
	raw, err := json.Marshal(users)
	if err != nil {
		return
	}
	if err := eng.secrets.Set(secrets.TrapUsersRef(), string(raw)); err != nil {
		log.Printf("WARNING: could not store the trap listener's SNMPv3 users: %v", err)
	}
}

// loadTrapUsers reads them back. Nothing stored, a store that cannot be read and
// a blob that does not decode all answer "no users": the listener then hears
// v1 and v2c, which is what it did before any of this existed.
func (eng *Engine) loadTrapUsers() []snmp.V3Params {
	if eng.secrets == nil {
		return nil
	}
	raw, err := eng.secrets.Get(secrets.TrapUsersRef())
	if err != nil || raw == "" {
		return nil
	}
	var users []snmp.V3Params
	if err := json.Unmarshal([]byte(raw), &users); err != nil {
		log.Printf("WARNING: the stored SNMPv3 users of the trap listener do not decode: %v", err)
		return nil
	}
	return users
}
