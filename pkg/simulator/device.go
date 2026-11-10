package simulator

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxDeviceName bounds a device's name, which is also its sysName.
const MaxDeviceName = 64

// Device is one simulated device as configured: what it is (a model and a
// name), where it answers, and who may ask it.
//
// It holds its secrets — the community and the users' passphrases — because
// starting it needs them. Whatever writes a device somewhere writes
// WithoutSecrets() and keeps Secrets() in the credential store.
type Device struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Model     string   `json:"model"`
	Address   string   `json:"address"`
	Port      int      `json:"port"`
	Versions  []string `json:"versions"`
	Community string   `json:"community"`
	// WriteCommunity writes in v1 and v2c, and reads as well; empty, the
	// device is read-only there. A secret, as the community is.
	WriteCommunity string `json:"writeCommunity"`
	Users          []User `json:"users"`
	// EngineID is hex, given once when the device is created (NewEngineID)
	// and never changed: managers cache it and localise their keys to it.
	EngineID string `json:"engineId"`
	// EngineBoots counts the device's starts. Whoever starts it raises the
	// count and keeps it (Fleet.Start).
	EngineBoots uint32 `json:"engineBoots"`
	// Traps is what the device sends, where to and when.
	Traps Traps `json:"traps"`
	// Location and Contact are the device's sysLocation and sysContact, in
	// place of its model's when they are given.
	Location string `json:"location"`
	Contact  string `json:"contact"`
	// AutoStart starts the device with the application.
	AutoStart bool `json:"autoStart"`
	// Faults are what the device is made to do wrong: changed while it runs
	// (Fleet.SetFaults), and kept with it.
	Faults Faults `json:"faults"`
	// Params are the numbers its model was given — ports, disks, outlets —
	// and Overrides the values it answers in place of its model's. Neither is
	// a secret.
	Params    map[string]int `json:"params,omitempty"`
	Overrides []Override     `json:"overrides,omitempty"`
	// Wiring is where the device sits on the bench (PlanBench). It is worked
	// out from the cables whenever the device is built, and kept nowhere.
	Wiring *Wiring `json:"-"`
}

// Listen is the address the device answers on, in the form CheckListen reads.
func (d Device) Listen() string {
	return net.JoinHostPort(strings.TrimSpace(d.Address), strconv.Itoa(d.Port))
}

// Validate reports the first thing wrong with d, of everything that can be
// checked without deriving a key. It carries the loopback rule, so a device read
// back from a file is held to it exactly as one typed in is.
func (d Device) Validate() error {
	name := strings.TrimSpace(d.Name)
	if name == "" || utf8.RuneCountInString(name) > MaxDeviceName {
		return fmt.Errorf("a device's name is 1 to %d characters", MaxDeviceName)
	}
	m, ok := findModel(d.Model)
	if !ok {
		return fmt.Errorf("%q is not a device model", d.Model)
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("%d is not a port", d.Port)
	}
	if _, err := CheckListen(d.Listen()); err != nil {
		return err
	}
	var v1, v2c, v3 bool
	for _, v := range d.Versions {
		switch v {
		case "v1":
			v1 = true
		case "v2c":
			v2c = true
		case "v3":
			v3 = true
		default:
			return fmt.Errorf("%q is not an SNMP version (v1, v2c or v3)", v)
		}
	}
	if !v1 && !v2c && !v3 {
		return errors.New("a device answers at least one SNMP version")
	}
	if (v1 || v2c) && (d.Community == "" || len(d.Community) > maxCommunity) {
		return fmt.Errorf("v1 and v2c need a community of 1 to %d octets", maxCommunity)
	}
	if d.WriteCommunity != "" {
		switch {
		case !v1 && !v2c:
			return errors.New("a write community is for v1 and v2c")
		case len(d.WriteCommunity) > maxCommunity:
			return fmt.Errorf("a write community is 1 to %d octets", maxCommunity)
		case d.WriteCommunity == d.Community:
			return errors.New("the write community is the read community: every manager reading would write")
		}
	}
	if v3 {
		if len(d.Users) == 0 {
			return errors.New("v3 needs a user")
		}
		seen := make(map[string]bool, len(d.Users))
		for _, u := range d.Users {
			if _, err := checkUser(u); err != nil {
				return err
			}
			if seen[u.Name] {
				return fmt.Errorf("user %q is declared twice", u.Name)
			}
			seen[u.Name] = true
		}
	}
	// A device's users exist only when it answers v3, so a v3 destination
	// needs that as well.
	var users []User
	if v3 {
		users = d.Users
	}
	if err := d.Traps.check(users, m.catalogue()); err != nil {
		return err
	}
	if len(d.Location) > 255 || len(d.Contact) > 255 {
		return errors.New("a device's location and contact are at most 255 octets")
	}
	if err := d.Faults.Check(); err != nil {
		return fmt.Errorf("faults: %w", err)
	}
	if err := m.checkParams(d.Params); err != nil {
		return err
	}
	// Where a value of its own sits is known once the model is built for the
	// device: under another instance, or with one under it, it could not be
	// answered, and that is said now rather than when the device starts.
	if len(d.Overrides) > 0 {
		objs, err := d.objects()
		if err != nil {
			return err
		}
		if _, err := newTree(objs); err != nil {
			return err
		}
	}
	if id, err := hex.DecodeString(d.EngineID); err != nil || len(id) < 5 || len(id) > 32 {
		return errors.New("a device's engine ID is 5 to 32 octets, in hex")
	}
	if d.EngineBoots >= maxBoots {
		return errors.New("the device's boots have reached their ceiling; it needs a new engine ID")
	}
	return nil
}

// DeviceSecrets is what a Device holds that no file may: the community, each
// user's passphrases by user name, and each destination's community by
// destination ID.
type DeviceSecrets struct {
	Community      string                 `json:"community"`
	WriteCommunity string                 `json:"writeCommunity"`
	Users          map[string]UserSecrets `json:"users"`
	Destinations   map[string]string      `json:"destinations"`
}

// UserSecrets are one user's passphrases.
type UserSecrets struct {
	AuthPass string `json:"authPass"`
	PrivPass string `json:"privPass"`
}

// Secrets is d's secrets and nothing else.
func (d Device) Secrets() DeviceSecrets {
	s := DeviceSecrets{
		Community:      d.Community,
		WriteCommunity: d.WriteCommunity,
		Users:          make(map[string]UserSecrets, len(d.Users)),
		Destinations:   make(map[string]string, len(d.Traps.Destinations)),
	}
	for _, u := range d.Users {
		s.Users[u.Name] = UserSecrets{AuthPass: u.AuthPass, PrivPass: u.PrivPass}
	}
	for _, dest := range d.Traps.Destinations {
		s.Destinations[dest.ID] = dest.Community
	}
	return s
}

// WithoutSecrets is d with its secrets blanked, sharing nothing with d.
func (d Device) WithoutSecrets() Device {
	d.Community, d.WriteCommunity = "", ""
	d.Versions = slices.Clone(d.Versions)
	d.Users = slices.Clone(d.Users)
	for i := range d.Users {
		d.Users[i].AuthPass, d.Users[i].PrivPass = "", ""
	}
	d.Traps = d.Traps.clone()
	for i := range d.Traps.Destinations {
		d.Traps.Destinations[i].Community = ""
	}
	d.Params, d.Overrides = maps.Clone(d.Params), slices.Clone(d.Overrides)
	return d
}

// WithSecrets is d with s filled back in.
func (d Device) WithSecrets(s DeviceSecrets) Device {
	d.Community, d.WriteCommunity = s.Community, s.WriteCommunity
	d.Versions = slices.Clone(d.Versions)
	d.Users = slices.Clone(d.Users)
	for i := range d.Users {
		p := s.Users[d.Users[i].Name]
		d.Users[i].AuthPass, d.Users[i].PrivPass = p.AuthPass, p.PrivPass
	}
	d.Traps = d.Traps.clone()
	for i := range d.Traps.Destinations {
		d.Traps.Destinations[i].Community = s.Destinations[d.Traps.Destinations[i].ID]
	}
	d.Params, d.Overrides = maps.Clone(d.Params), slices.Clone(d.Overrides)
	return d
}

// NewDeviceID is a new device's ID. It is random, and says nothing about the
// device.
func NewDeviceID() string {
	b := make([]byte, 8)
	rand.Read(b) // never fails since Go 1.24
	return hex.EncodeToString(b)
}

// NewDestinationID is a new destination's ID, random as a device's is.
func NewDestinationID() string { return NewDeviceID() }

// NewEngineID is the engine ID a new device of a model is created with: the MAC
// format of EngineID, carrying the model's vendor and a MAC drawn from the
// device's ID — the same for the device's whole life, and different from any
// other device's.
func NewEngineID(modelID, deviceID string) (string, error) {
	m, ok := findModel(modelID)
	if !ok {
		return "", fmt.Errorf("%q is not a device model", modelID)
	}
	id, err := EngineID(m.enterprise, deviceMAC(deviceSeed(deviceID), 0))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
}

// deviceSeed is where a device's values take their motion from.
func deviceSeed(deviceID string) uint64 {
	h := sha256.Sum256([]byte(deviceID))
	return binary.BigEndian.Uint64(h[:8])
}

// identity is what makes d differ from another device of its model.
func (d Device) identity() Identity {
	return Identity{Name: strings.TrimSpace(d.Name), Seed: deviceSeed(d.ID),
		Location: strings.TrimSpace(d.Location), Contact: strings.TrimSpace(d.Contact), Params: d.Params,
		Wiring: d.Wiring}
}

// objects is what d answers from: its model built for it, with its parameters,
// and its own values in place of the model's. For a switch answering BRIDGE-MIB
// per VLAN, it is the default view; views has the others.
func (d Device) objects() ([]Object, error) {
	objs, _, err := d.views()
	return objs, err
}

// views is d's default view and, for a model answering BRIDGE-MIB per VLAN, the
// view of each of its other VLANs by context name ("vlan-20").
func (d Device) views() ([]Object, map[string][]Object, error) {
	m, ok := findModel(d.Model)
	if !ok {
		return nil, nil, fmt.Errorf("%q is not a device model", d.Model)
	}
	if err := m.checkParams(d.Params); err != nil {
		return nil, nil, err
	}
	own, err := compileOverrides(d.Overrides)
	if err != nil {
		return nil, nil, err
	}
	all := withOverrides(m.build(d.identity()), own)
	if !m.vlanContexts {
		return all, nil, nil
	}
	return splitVLANs(all)
}

// splitVLANs cuts a switch's objects into the view of VLAN 1, which is the
// default context's, and one view for each other VLAN it has — every VLAN its
// tables name, whether or not anything was learnt in it, since a VLAN with no
// station is still a context the switch answers.
func splitVLANs(all []Object) ([]Object, map[string][]Object, error) {
	vlans := map[int]bool{}
	for _, o := range all {
		if o.VLAN > 1 {
			vlans[o.VLAN] = true
		}
		for _, table := range vlanTables {
			if rest, ok := strings.CutPrefix(o.OID, table); ok {
				if v, err := strconv.Atoi(rest); err == nil && v > 1 && v <= 4094 && (v < 1002 || v > 1005) {
					vlans[v] = true
				}
			}
		}
	}
	in := func(vlan int) []Object {
		out := make([]Object, 0, len(all))
		for _, o := range all {
			if o.VLAN == 0 || o.VLAN == vlan {
				out = append(out, o)
			}
		}
		return out
	}
	views := make(map[string][]Object, len(vlans))
	for v := range vlans {
		views["vlan-"+strconv.Itoa(v)] = in(v)
	}
	return in(1), views, nil
}

// vlanTables are the columns whose last arc is a VLAN a switch has: Q-BRIDGE-MIB's
// dot1qVlanStaticName and CISCO-VTP-MIB's vtpVlanState in management domain 1.
var vlanTables = []string{"1.3.6.1.2.1.17.7.1.4.3.1.1.", "1.3.6.1.4.1.9.9.46.1.3.1.1.2.1."}

// config is the agent d runs as.
func (d Device) config() (Config, error) {
	m, ok := findModel(d.Model)
	if !ok {
		return Config{}, fmt.Errorf("%q is not a device model", d.Model)
	}
	engineID, err := hex.DecodeString(d.EngineID)
	if err != nil {
		return Config{}, fmt.Errorf("engine ID: %w", err)
	}
	objs, contexts, err := d.views()
	if err != nil {
		return Config{}, err
	}
	return Config{
		Listen:         d.Listen(),
		Versions:       d.Versions,
		Community:      d.Community,
		WriteCommunity: d.WriteCommunity,
		Users:          d.Users,
		EngineID:       engineID,
		EngineBoots:    d.EngineBoots,
		Objects:        objs,
		Contexts:       contexts,
		Notifications:  m.catalogue(),
		Traps:          d.Traps.clone(),
		Faults:         d.Faults,
	}, nil
}
