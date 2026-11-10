package engine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// trapEngineFile holds the trap listener's snmpEngineID, in hex, beside
// monitoring.db. It is no secret: it is what a device sending this machine
// SNMPv3 INFORMs is configured with.
const trapEngineFile = "trap-engine-id"

// loadTrapEngineID reads the listener's engine ID from dir, and makes and keeps
// one the first time. An ID that changed at every start would be one no device
// could be configured with (snmp.Client.SetTrapEngineID).
func loadTrapEngineID(dir string) []byte {
	path := filepath.Join(dir, trapEngineFile)
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if id, err := hex.DecodeString(strings.TrimSpace(string(raw))); err == nil && len(id) >= 5 && len(id) <= 32 {
			return id
		}
		log.Printf("trap listener: %s holds no engine ID; a new one replaces it", path)
	case !errors.Is(err, os.ErrNotExist):
		// Present and unreadable is not absent: writing over it could replace an
		// ID devices are configured with. This session goes without.
		log.Printf("trap listener: cannot read %s (%v); an engine ID is made for this session only", path, err)
		return newTrapEngineID()
	}
	id := newTrapEngineID()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("trap listener: cannot keep an engine ID in %s: %v", dir, err)
		return id
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(id)+"\n"), 0o600); err != nil {
		log.Printf("trap listener: cannot keep an engine ID in %s: %v", path, err)
	}
	return id
}

// newTrapEngineID is an snmpEngineID in RFC 3411's fifth format — octets an
// administrator assigns — under enterprise 0. SnmpLens has no enterprise number
// of its own, and borrowing a vendor's would pass the listener off as one of
// that vendor's engines; IANA reserves 0, which claims nobody.
func newTrapEngineID() []byte {
	id := make([]byte, 13)
	id[0], id[4] = 0x80, 5
	rand.Read(id[5:]) // never fails since Go 1.24
	return id
}

// TrapListenerEngineID is the trap listener's snmpEngineID, in hex: what a
// device sending this machine SNMPv3 INFORMs is configured with, and what a
// simulated device sending to SnmpLens is filled in with.
func (eng *Engine) TrapListenerEngineID() string {
	return hex.EncodeToString(eng.trapEngineID)
}

// TrapEngineID is the trap listener's snmpEngineID.
func (eng *Engine) TrapEngineID() []byte { return eng.trapEngineID }

// SetTrapEngineID makes id the trap listener's snmpEngineID, here and for every
// later start: what a backup restores, since the devices sending SNMPv3
// INFORMs were configured with it. The listener uses it from its next start.
func (eng *Engine) SetTrapEngineID(id []byte) error {
	if len(id) < 5 || len(id) > 32 {
		return errors.New("an snmpEngineID is 5 to 32 octets")
	}
	if err := os.WriteFile(filepath.Join(eng.dataDir, trapEngineFile), []byte(hex.EncodeToString(id)+"\n"), 0o600); err != nil {
		return err
	}
	eng.trapEngineID = id
	if eng.snmpClient != nil {
		eng.snmpClient.SetTrapEngineID(id)
	}
	return nil
}
