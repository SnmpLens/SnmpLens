// Package portmap answers "what is plugged into which port of this switch": the
// MAC addresses a switch has learnt on each port, the IP addresses the network's
// ARP caches give them, and the neighbour a port's LLDP or CDP names.
//
// It is pure: what to walk is Roots, and Build reads what was walked. The walking,
// and the reverse DNS, are internal/app's. Every table here is standard except
// CDP and Cisco's VTP VLAN list, and each is read as RFC 4188, RFC 4363, RFC 4293,
// RFC 2863 and IEEE 802.1AB index it, never by position in the walk.
//
// Three facts about real switches decide the shape.
//
// A bridge port is not an interface. BRIDGE-MIB numbers the ports it bridges from
// one, and dot1dBasePortIfIndex says which interface each is; a FDB entry names
// the bridge port, so it is that table that puts a MAC on a port.
//
// A switch may keep its forwarding database in Q-BRIDGE-MIB alone, indexed by
// VLAN, or in BRIDGE-MIB alone, or both. Q-BRIDGE is read first because it says
// which VLAN a station is on; BRIDGE-MIB fills in when it is empty. And a Cisco
// switch answers BRIDGE-MIB one VLAN at a time, through community indexing
// ("public@20") or, in SNMPv3, the context "vlan-20": ByVLAN carries those walks.
//
// The number LLDP gives a local port is the agent's choice (lldpLocPortNum): an
// ifIndex on most switches, a bridge port on others. It is resolved through
// lldpLocPortTable by the interface name when the agent names it so, then as an
// ifIndex, then as a bridge port.
package portmap

import (
	"fmt"
	"math"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/gosnmp/gosnmp"
)

const (
	basePortIfIndex = "1.3.6.1.2.1.17.1.4.1.2"
	fdbPort         = "1.3.6.1.2.1.17.4.3.1.2"
	fdbStatus       = "1.3.6.1.2.1.17.4.3.1.3"
	qFdbPort        = "1.3.6.1.2.1.17.7.1.2.2.1.2"
	qFdbStatus      = "1.3.6.1.2.1.17.7.1.2.2.1.3"
	qPvid           = "1.3.6.1.2.1.17.7.1.4.5.1.1"
	ifDescr         = "1.3.6.1.2.1.2.2.1.2"
	ifOperStatus    = "1.3.6.1.2.1.2.2.1.8"
	ifName          = "1.3.6.1.2.1.31.1.1.1.1"
	ifHighSpeed     = "1.3.6.1.2.1.31.1.1.1.15"
	ifAlias         = "1.3.6.1.2.1.31.1.1.1.18"
	arpMedia        = "1.3.6.1.2.1.4.22.1.2"
	arpPhysical     = "1.3.6.1.2.1.4.35.1.4"
	lldpLocPort     = "1.0.8802.1.1.2.1.3.7.1"
	lldpRem         = "1.0.8802.1.1.2.1.4.1.1"
	cdpCache        = "1.3.6.1.4.1.9.9.23.1.2.1.1"
	sysObjectID     = "1.3.6.1.2.1.1.2"

	// VTPVlanState is CISCO-VTP-MIB's vtpVlanState, indexed by management domain
	// and VLAN: the VLANs a Cisco switch answers BRIDGE-MIB for.
	VTPVlanState = "1.3.6.1.4.1.9.9.46.1.3.1.1.2"
	// CiscoEnterprise is the sysObjectID prefix of Cisco's devices.
	CiscoEnterprise = "1.3.6.1.4.1.9."
)

// Roots are what to walk on the switch.
var Roots = []string{
	sysObjectID, basePortIfIndex, fdbPort, fdbStatus, qFdbPort, qFdbStatus, qPvid,
	ifDescr, ifOperStatus, ifName, ifHighSpeed, ifAlias, arpMedia, arpPhysical,
	lldpLocPort, lldpRem, cdpCache, VTPVlanState,
}

// VLANRoots are what to walk on a Cisco switch in one VLAN's context.
var VLANRoots = []string{basePortIfIndex, fdbPort, fdbStatus}

// ARPRoots are what to walk on a device that routes for the switch's stations.
var ARPRoots = []string{arpMedia, arpPhysical}

// ManyStations is how many addresses on one port make it, without a neighbour
// that says so, look like a port to more of the network rather than to a host:
// a phone with a computer behind it is two, a hypervisor a handful.
const ManyStations = 6

// Map is a switch's ports and what is behind each.
type Map struct {
	Ports []Port `json:"ports"`
	// Unplaced are stations learnt on a bridge port no interface answers for.
	Unplaced []Station `json:"unplaced"`
	Stations int       `json:"stations"`
	// Source is the table the stations came from: "q-bridge", "bridge",
	// "bridge-per-vlan" or "" when there were none.
	Source string `json:"source"`
	// Cisco says the switch is one, which explains a map built from BRIDGE-MIB
	// walked VLAN by VLAN.
	Cisco bool `json:"cisco"`
	// Warnings are what could not be read, said rather than left out quietly.
	Warnings []string `json:"warnings"`
}

// Port is one bridged interface.
type Port struct {
	IfIndex    int        `json:"ifIndex"`
	BridgePort int        `json:"bridgePort"`
	Name       string     `json:"name"`
	Descr      string     `json:"descr,omitempty"`
	Alias      string     `json:"alias,omitempty"`
	Up         bool       `json:"up"`
	SpeedMbps  uint64     `json:"speedMbps,omitempty"`
	VLAN       int        `json:"vlan,omitempty"`
	Neighbour  *Neighbour `json:"neighbour,omitempty"`
	// Uplink is a port towards more of the network: the neighbour is a switch
	// or a router, or there are more stations behind it than a host has. An
	// access point or a phone is a neighbour and not an uplink.
	Uplink   bool      `json:"uplink"`
	Stations []Station `json:"stations"`
}

// Station is one address learnt on a port.
type Station struct {
	MAC   string   `json:"mac"`
	VLAN  int      `json:"vlan,omitempty"`
	IPs   []string `json:"ips"`
	Names []string `json:"names"`
	// Static is an entry an operator configured rather than one learnt.
	Static bool `json:"static,omitempty"`
}

// Neighbour is what LLDP or CDP says is at the other end of a port.
type Neighbour struct {
	Name     string `json:"name"`
	Port     string `json:"port,omitempty"`
	Chassis  string `json:"chassis,omitempty"`
	Address  string `json:"address,omitempty"`
	Protocol string `json:"protocol"`
	// Kind is what the neighbour says it is: "switch", "router", "ap", "phone",
	// "host", or "" when it says nothing.
	Kind string `json:"kind,omitempty"`
}

// Walk is what one walk returned, as gosnmp decoded it.
type Walk []gosnmp.SnmpPDU

// Input is everything walked for one map.
type Input struct {
	Switch Walk
	// ByVLAN is BRIDGE-MIB walked in each VLAN's context, for a switch that
	// answers it no other way.
	ByVLAN map[int]Walk
	// ARP are the ARP caches of the devices that route for the switch's stations.
	ARP []Walk
}

// IsCisco says whether a switch's walk names a Cisco sysObjectID.
func IsCisco(sw Walk) bool {
	for _, p := range sw {
		if strings.HasPrefix(trim(p.Name), sysObjectID) {
			if v, ok := p.Value.(string); ok && strings.HasPrefix(trim(v)+".", CiscoEnterprise) {
				return true
			}
		}
	}
	return false
}

// HasFDB says whether a switch's walk holds any forwarding database entry.
func HasFDB(sw Walk) bool {
	for _, p := range sw {
		n := trim(p.Name)
		if strings.HasPrefix(n, fdbPort+".") || strings.HasPrefix(n, qFdbPort+".") {
			return true
		}
	}
	return false
}

// CiscoVLANs are the operational VLANs vtpVlanState lists, the reserved
// 1002-1005 left out: those are FDDI and Token Ring, and a switch answers nothing
// in their context.
func CiscoVLANs(w Walk) []int {
	var out []int
	for _, p := range w {
		arcs := arcsAfter(trim(p.Name), VTPVlanState)
		if len(arcs) != 2 || asInt(p.Value) != 1 {
			continue
		}
		if v := arcs[1]; v >= 1 && v <= 4094 && (v < 1002 || v > 1005) {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

type fdbEntry struct {
	mac  string
	vlan int
	port int
}

// Build reads a map out of what was walked.
func Build(in Input) Map {
	sw := in.Switch
	m := Map{Ports: []Port{}, Unplaced: []Station{}, Warnings: []string{}, Cisco: IsCisco(sw)}

	baseIf := map[int]int{} // bridge port → ifIndex
	ifs := map[int]*Port{}
	pvid := map[int]int{} // bridge port → VLAN
	ifByName := map[string]int{}
	var fdb, qfdb []fdbEntry
	qStatus, status := map[string]int{}, map[string]int{}

	ensure := func(ifIndex int) *Port {
		p := ifs[ifIndex]
		if p == nil {
			p = &Port{IfIndex: ifIndex, Stations: []Station{}}
			ifs[ifIndex] = p
		}
		return p
	}

	read := func(w Walk, vlan int) {
		for _, p := range w {
			name := trim(p.Name)
			switch {
			case under(name, basePortIfIndex):
				if a := arcsAfter(name, basePortIfIndex); len(a) == 1 {
					baseIf[a[0]] = asInt(p.Value)
				}
			case under(name, fdbPort):
				if a := arcsAfter(name, fdbPort); len(a) == 6 {
					if mac, ok := macOf(a); ok {
						fdb = append(fdb, fdbEntry{mac: mac, vlan: vlan, port: asInt(p.Value)})
					}
				}
			case under(name, fdbStatus):
				if a := arcsAfter(name, fdbStatus); len(a) == 6 {
					if mac, ok := macOf(a); ok {
						status[fmt.Sprintf("%d/%s", vlan, mac)] = asInt(p.Value)
					}
				}
			}
		}
	}
	read(sw, 0)
	for _, vlan := range sortedVLANs(in.ByVLAN) {
		read(in.ByVLAN[vlan], vlan)
	}

	for _, p := range sw {
		name := trim(p.Name)
		switch {
		case under(name, qFdbPort):
			if a := arcsAfter(name, qFdbPort); len(a) == 7 {
				if mac, ok := macOf(a[1:]); ok {
					qfdb = append(qfdb, fdbEntry{mac: mac, vlan: a[0], port: asInt(p.Value)})
				}
			}
		case under(name, qFdbStatus):
			if a := arcsAfter(name, qFdbStatus); len(a) == 7 {
				if mac, ok := macOf(a[1:]); ok {
					qStatus[fmt.Sprintf("%d/%s", a[0], mac)] = asInt(p.Value)
				}
			}
		case under(name, qPvid):
			if a := arcsAfter(name, qPvid); len(a) == 1 {
				pvid[a[0]] = asInt(p.Value)
			}
		case under(name, ifName):
			if a := arcsAfter(name, ifName); len(a) == 1 {
				ensure(a[0]).Name = asString(p.Value)
				ifByName[asString(p.Value)] = a[0]
			}
		case under(name, ifDescr):
			if a := arcsAfter(name, ifDescr); len(a) == 1 {
				ensure(a[0]).Descr = asString(p.Value)
				if _, taken := ifByName[asString(p.Value)]; !taken {
					ifByName[asString(p.Value)] = a[0]
				}
			}
		case under(name, ifAlias):
			if a := arcsAfter(name, ifAlias); len(a) == 1 {
				ensure(a[0]).Alias = asString(p.Value)
			}
		case under(name, ifOperStatus):
			if a := arcsAfter(name, ifOperStatus); len(a) == 1 {
				ensure(a[0]).Up = asInt(p.Value) == 1
			}
		case under(name, ifHighSpeed):
			if a := arcsAfter(name, ifHighSpeed); len(a) == 1 {
				ensure(a[0]).SpeedMbps = uint64(asInt(p.Value))
			}
		}
	}

	// The ports are the bridge's; an interface no bridge port names is not one.
	var ports []*Port
	byBridge := map[int]*Port{}
	for bp, ifIndex := range baseIf {
		p := ensure(ifIndex)
		p.BridgePort = bp
		p.VLAN = pvid[bp]
		if p.Name == "" {
			p.Name = p.Descr
		}
		byBridge[bp] = p
		ports = append(ports, p)
	}

	entries, statuses := qfdb, qStatus
	m.Source = "q-bridge"
	if len(qfdb) == 0 {
		entries, statuses = fdb, status
		m.Source = "bridge"
		if len(in.ByVLAN) > 0 {
			m.Source = "bridge-per-vlan"
		}
	}
	if len(entries) == 0 {
		m.Source = ""
	}

	macToIP := arpTable(append([]Walk{sw}, in.ARP...))
	seen := map[string]bool{}
	for _, e := range entries {
		st, known := statuses[fmt.Sprintf("%d/%s", e.vlan, e.mac)]
		// learned(3) and mgmt(5) are stations; self(4) is the switch's own
		// address and invalid(2) is an entry being aged out.
		if known && st != 3 && st != 5 && st != 1 {
			continue
		}
		if e.port <= 0 {
			continue
		}
		key := fmt.Sprintf("%d/%d/%s", e.port, e.vlan, e.mac)
		if seen[key] {
			continue
		}
		seen[key] = true
		s := Station{MAC: e.mac, VLAN: e.vlan, IPs: macToIP[e.mac], Names: []string{}, Static: st == 5}
		if s.IPs == nil {
			s.IPs = []string{}
		}
		m.Stations++
		if p := byBridge[e.port]; p != nil {
			p.Stations = append(p.Stations, s)
		} else {
			m.Unplaced = append(m.Unplaced, s)
		}
	}

	for _, n := range neighbours(sw, ifByName, ifs, byBridge) {
		if p := ifs[n.ifIndex]; p != nil && p.Neighbour == nil {
			nb := n.Neighbour
			p.Neighbour = &nb
		}
	}

	for _, p := range ports {
		sort.Slice(p.Stations, func(i, j int) bool {
			if p.Stations[i].VLAN != p.Stations[j].VLAN {
				return p.Stations[i].VLAN < p.Stations[j].VLAN
			}
			return p.Stations[i].MAC < p.Stations[j].MAC
		})
		p.Uplink = len(p.Stations) >= ManyStations ||
			(p.Neighbour != nil && (p.Neighbour.Kind == "switch" || p.Neighbour.Kind == "router"))
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].IfIndex < ports[j].IfIndex })
	for _, p := range ports {
		m.Ports = append(m.Ports, *p)
	}
	return m
}

// arpTable is MAC → IP addresses, from every ARP cache walked.
func arpTable(walks []Walk) map[string][]string {
	out := map[string][]string{}
	add := func(mac, ip string) {
		if mac == "" || ip == "" || slices.Contains(out[mac], ip) {
			return
		}
		out[mac] = append(out[mac], ip)
	}
	for _, w := range walks {
		for _, p := range w {
			name := trim(p.Name)
			switch {
			case under(name, arpMedia):
				// ipNetToMediaPhysAddress.<ifIndex>.<a>.<b>.<c>.<d>
				if a := arcsAfter(name, arpMedia); len(a) == 5 {
					add(macValue(p.Value), fmt.Sprintf("%d.%d.%d.%d", a[1], a[2], a[3], a[4]))
				}
			case under(name, arpPhysical):
				// ipNetToPhysicalPhysAddress.<ifIndex>.<type>.<length>.<address>
				a := arcsAfter(name, arpPhysical)
				if len(a) < 3 || len(a) != 3+a[2] {
					continue
				}
				raw, ok := octets(a[3:])
				if ok && ((a[1] == 1 && a[2] == 4) || (a[1] == 2 && a[2] == 16)) {
					add(macValue(p.Value), net.IP(raw).String())
				}
			}
		}
	}
	for _, ips := range out {
		sort.Strings(ips)
	}
	return out
}

type located struct {
	ifIndex int
	Neighbour
}

// neighbours are LLDP's, then CDP's for a port LLDP says nothing of.
func neighbours(sw Walk, ifByName map[string]int, ifs map[int]*Port, byBridge map[int]*Port) []located {
	locSubtype, locID := map[int]int{}, map[int]string{}
	rem := map[string]map[int]gosnmp.SnmpPDU{} // timeMark.localPort.index → column → value
	cdp := map[string]map[int]gosnmp.SnmpPDU{} // ifIndex.deviceIndex → column → value
	for _, p := range sw {
		name := trim(p.Name)
		switch {
		case under(name, lldpLocPort):
			if a := arcsAfter(name, lldpLocPort); len(a) == 2 {
				switch a[0] {
				case 2:
					locSubtype[a[1]] = asInt(p.Value)
				case 3:
					locID[a[1]] = asString(p.Value)
				}
			}
		case under(name, lldpRem):
			if a := arcsAfter(name, lldpRem); len(a) == 4 {
				key := fmt.Sprintf("%d.%d.%d", a[1], a[2], a[3])
				if rem[key] == nil {
					rem[key] = map[int]gosnmp.SnmpPDU{}
				}
				rem[key][a[0]] = p
			}
		case under(name, cdpCache):
			if a := arcsAfter(name, cdpCache); len(a) == 3 {
				key := fmt.Sprintf("%d.%d", a[1], a[2])
				if cdp[key] == nil {
					cdp[key] = map[int]gosnmp.SnmpPDU{}
				}
				cdp[key][a[0]] = p
			}
		}
	}

	// What LLDP calls a local port, as an ifIndex.
	local := func(num int) int {
		if locSubtype[num] == 5 || locSubtype[num] == 7 {
			if i, ok := ifByName[locID[num]]; ok {
				return i
			}
		}
		// A switch port first, under either reading: on a switch whose VLAN
		// interface is ifIndex 1, LLDP's port 1 is bridge port 1, not the VLAN.
		if p := ifs[num]; p != nil && p.BridgePort != 0 {
			return num
		}
		if p := byBridge[num]; p != nil {
			return p.IfIndex
		}
		if _, ok := ifs[num]; ok {
			return num
		}
		return 0
	}

	var out []located
	for _, key := range sortedKeys(rem) {
		cols := rem[key]
		parts := strings.Split(key, ".")
		num, _ := strconv.Atoi(parts[1])
		ifIndex := local(num)
		if ifIndex == 0 {
			continue
		}
		n := Neighbour{Protocol: "lldp"}
		n.Name = asString(cols[9].Value)
		n.Chassis = idString(asInt(cols[4].Value), cols[5].Value)
		n.Port = idString(asInt(cols[6].Value), cols[7].Value)
		if d := asString(cols[8].Value); d != "" && (n.Port == "" || isHexID(n.Port)) {
			n.Port = d
		}
		if n.Name == "" {
			n.Name = n.Chassis
		}
		n.Kind = lldpKind(cols[12].Value)
		out = append(out, located{ifIndex, n})
	}
	for _, key := range sortedKeys(cdp) {
		cols := cdp[key]
		ifIndex, _ := strconv.Atoi(strings.Split(key, ".")[0])
		n := Neighbour{Protocol: "cdp", Name: asString(cols[6].Value), Port: asString(cols[7].Value),
			Kind: cdpKind(cols[9].Value)}
		// cdpCacheAddress is a raw IPv4 address when cdpCacheAddressType is ip(1).
		if asInt(cols[3].Value) == 1 {
			if b, ok := cols[4].Value.([]byte); ok && len(b) == 4 {
				n.Address = net.IP(b).String()
			}
		}
		if n.Name != "" {
			out = append(out, located{ifIndex, n})
		}
	}
	return out
}

// lldpKind reads lldpRemSysCapEnabled, a BITS whose bit 0 is the high bit of the
// first octet: bridge 2, WLAN access point 3, router 4, telephone 5, station 7.
// An access point bridges too, and is still an access point.
func lldpKind(v any) string {
	b, _ := v.([]byte)
	bit := func(n int) bool { return n/8 < len(b) && b[n/8]&(0x80>>(n%8)) != 0 }
	switch {
	case bit(3):
		return "ap"
	case bit(2):
		return "switch"
	case bit(4):
		return "router"
	case bit(5):
		return "phone"
	case bit(7):
		return "host"
	}
	return ""
}

// cdpKind reads cdpCacheCapabilities, four octets: router 0x01, transparent
// bridge 0x02, switch 0x08, host 0x10, phone 0x80.
func cdpKind(v any) string {
	b, _ := v.([]byte)
	if len(b) != 4 {
		return ""
	}
	c := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	switch {
	case c&0x0a != 0:
		return "switch"
	case c&0x01 != 0:
		return "router"
	case c&0x80 != 0:
		return "phone"
	case c&0x10 != 0:
		return "host"
	}
	return ""
}

// idString reads an LLDP ID by its subtype: a MAC for chassis 4 and port 3,
// otherwise text when it is text and hex when it is not.
func idString(subtype int, v any) string {
	b, ok := v.([]byte)
	if !ok {
		return asString(v)
	}
	if len(b) == 6 && (subtype == 4 || subtype == 3) {
		return net.HardwareAddr(b).String()
	}
	if printable(b) {
		return string(b)
	}
	return net.HardwareAddr(b).String()
}

func isHexID(s string) bool {
	return strings.Count(s, ":") >= 2 && len(s)%3 == 2
}

func printable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, r := range string(b) {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// macOf reads a MAC address an index carries as six arcs.
func macOf(arcs []int) (string, bool) {
	b, ok := octets(arcs)
	if !ok {
		return "", false
	}
	return net.HardwareAddr(b).String(), true
}

// octets reads index arcs that stand for an OCTET STRING, one octet per arc.
// An arc above 255 is no octet, and the index is refused rather than truncated:
// 256 read as a byte is 0, and would name another address.
func octets(arcs []int) ([]byte, bool) {
	b := make([]byte, len(arcs))
	for i, a := range arcs {
		if a < 0 || a > math.MaxUint8 {
			return nil, false
		}
		b[i] = byte(a)
	}
	return b, true
}

func macValue(v any) string {
	if b, ok := v.([]byte); ok && len(b) == 6 {
		return net.HardwareAddr(b).String()
	}
	return ""
}

func trim(oid string) string { return strings.TrimPrefix(oid, ".") }

func under(name, root string) bool { return strings.HasPrefix(name, root+".") }

// arcsAfter is the index of name below root, or nil when an arc is not a number.
func arcsAfter(name, root string) []int {
	rest := strings.TrimPrefix(name, root+".")
	if rest == name {
		return nil
	}
	parts := strings.Split(rest, ".")
	out := make([]int, len(parts))
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil
		}
		out[i] = n
	}
	return out
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case uint:
		return int(x)
	case uint32:
		return int(x)
	case uint64:
		return int(x)
	}
	return 0
}

func asString(v any) string {
	switch x := v.(type) {
	case []byte:
		return strings.TrimRight(string(x), "\x00")
	case string:
		return x
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedVLANs(m map[int]Walk) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
