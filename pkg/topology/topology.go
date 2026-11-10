// Package topology draws the network from what its devices say of their
// neighbours: each device walked is a node, each LLDP or CDP neighbour it names is
// a node too, and each neighbour is a link from the port it was heard on to the
// port it named.
//
// Pure, like pkg/portmap, whose neighbour reader it uses: what to walk is Roots,
// and Build reads what was walked. internal/app does the walking, and follows the
// neighbours' management addresses when asked to.
//
// Two problems decide the shape, and both come from each end of a link describing
// it on its own.
//
// The same device must be one node. A neighbour is matched to a device walked by
// its chassis ID first (LLDP's own, lldpLocChassisId), then by its name with the
// domain left off, then by an address the device answers on: the three a
// neighbour entry may carry, in the order they are reliable.
//
// The same link must be one line. A device says its own port as ifName gives it
// ("Gi1/0/1") and its neighbour often says that port's long form
// ("GigabitEthernet1/0/1"); two links between the same two nodes whose ports read
// the same once shortened are one, seen from both ends.
package topology

import (
	"net"
	"sort"
	"strings"
	"unicode"

	"SnmpLens/pkg/portmap"
)

const (
	sysDescr       = "1.3.6.1.2.1.1.1"
	sysObjectID    = "1.3.6.1.2.1.1.2"
	sysName        = "1.3.6.1.2.1.1.5"
	sysServices    = "1.3.6.1.2.1.1.7"
	ipAdEntAddr    = "1.3.6.1.2.1.4.20.1.1"
	lldpLocChassis = "1.0.8802.1.1.2.1.3.1" // subtype .1, then the ID .2
	lldpLocCapsOn  = "1.0.8802.1.1.2.1.3.6"
)

// Roots are what to walk on each device: who it is, and whom it hears.
var Roots = []string{
	sysDescr, sysObjectID, sysName, sysServices, ipAdEntAddr,
	"1.0.8802.1.1.2.1.3.1", "1.0.8802.1.1.2.1.3.2", lldpLocCapsOn,
	"1.3.6.1.2.1.31.1.1.1.1", "1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.17.1.4.1.2",
	"1.0.8802.1.1.2.1.3.7.1", "1.0.8802.1.1.2.1.4.1.1", "1.0.8802.1.1.2.1.4.2.1",
	"1.3.6.1.4.1.9.9.23.1.2.1.1",
}

// Device is one device walked, and the address it was walked at.
type Device struct {
	Address string
	Walk    portmap.Walk
}

// Node is a device on the map.
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Descr    string `json:"descr,omitempty"`
	ObjectID string `json:"objectId,omitempty"`
	// Managed is a device SnmpLens walked; the others are only named by one.
	Managed bool `json:"managed"`
}

// Link is a cable, as the devices at its ends describe it.
type Link struct {
	A        string `json:"a"`
	APort    string `json:"aPort,omitempty"`
	B        string `json:"b"`
	BPort    string `json:"bPort,omitempty"`
	Protocol string `json:"protocol"`
	// Both is a link both ends reported.
	Both bool `json:"both"`
}

// Graph is the map.
type Graph struct {
	Nodes    []Node   `json:"nodes"`
	Links    []Link   `json:"links"`
	Warnings []string `json:"warnings"`
}

// identity is what a device walked says of itself.
type identity struct {
	chassis   string
	name      string
	addresses []string
}

func identityOf(d Device) (identity, Node) {
	id := identity{addresses: []string{d.Address}}
	n := Node{Address: d.Address, Managed: true}
	var chassisSubtype int
	var chassisRaw any
	var services int
	var caps []byte
	for _, p := range d.Walk {
		name := strings.TrimPrefix(p.Name, ".")
		switch {
		case name == sysName+".0":
			n.Name = str(p.Value)
		case name == sysDescr+".0":
			n.Descr = firstLine(str(p.Value))
		case name == sysObjectID+".0":
			n.ObjectID, _ = p.Value.(string)
		case name == sysServices+".0":
			services = num(p.Value)
		case name == lldpLocChassis+".0":
			chassisSubtype = num(p.Value)
		case name == "1.0.8802.1.1.2.1.3.2.0":
			chassisRaw = p.Value
		case name == lldpLocCapsOn+".0":
			caps, _ = p.Value.([]byte)
		case strings.HasPrefix(name, ipAdEntAddr+"."):
			if ip, ok := p.Value.(string); ok && ip != "" && !strings.HasPrefix(ip, "127.") {
				id.addresses = append(id.addresses, ip)
			}
		}
	}
	if chassisRaw != nil {
		id.chassis = chassisString(chassisSubtype, chassisRaw)
	}
	id.name = n.Name
	n.Kind = kindOf(caps, services)
	if n.Name == "" {
		n.Name = d.Address
	}
	return id, n
}

// Build draws the map of the devices walked.
func Build(devices []Device) Graph {
	g := Graph{Nodes: []Node{}, Links: []Link{}, Warnings: []string{}}
	nodes := map[string]*Node{}
	var order []string
	byChassis, byName, byAddress := map[string]string{}, map[string]string{}, map[string]string{}

	add := func(n Node) string {
		if _, ok := nodes[n.ID]; !ok {
			order = append(order, n.ID)
		}
		nn := n
		nodes[n.ID] = &nn
		return n.ID
	}
	index := func(id, chassis, name string, addresses ...string) {
		if c := normChassis(chassis); c != "" {
			byChassis[c] = id
		}
		if nm := normName(name); nm != "" {
			byName[nm] = id
		}
		for _, a := range addresses {
			if a != "" {
				byAddress[a] = id
			}
		}
	}
	find := func(chassis, name, address string) string {
		if id, ok := byChassis[normChassis(chassis)]; ok && normChassis(chassis) != "" {
			return id
		}
		if id, ok := byName[normName(name)]; ok && normName(name) != "" {
			return id
		}
		if id, ok := byAddress[address]; ok && address != "" {
			return id
		}
		return ""
	}

	// The devices walked first, so a neighbour that IS one of them finds it.
	walked := make([]string, len(devices))
	for i, d := range devices {
		id, n := identityOf(d)
		if existing := find(id.chassis, id.name, d.Address); existing != "" {
			walked[i] = existing // the same device, walked at two addresses
			index(existing, id.chassis, id.name, id.addresses...)
			continue
		}
		n.ID = "d:" + d.Address
		walked[i] = add(n)
		index(n.ID, id.chassis, id.name, id.addresses...)
	}

	for i, d := range devices {
		from := walked[i]
		for _, nb := range portmap.Neighbours(d.Walk) {
			to := find(nb.Chassis, nb.Name, nb.Address)
			if to == "" {
				key := normChassis(nb.Chassis)
				if key == "" {
					key = normName(nb.Name)
				}
				if key == "" {
					key = nb.Address
				}
				if key == "" {
					continue
				}
				name := nb.Name
				if name == "" {
					name = nb.Chassis
				}
				to = add(Node{ID: "n:" + key, Name: name, Address: nb.Address, Kind: nb.Kind, Descr: firstLine(nb.Descr)})
				index(to, nb.Chassis, nb.Name, nb.Address)
			} else if n := nodes[to]; !n.Managed {
				// Heard again: fill in what the first sighting lacked.
				if n.Address == "" {
					n.Address = nb.Address
				}
				if n.Kind == "" {
					n.Kind = nb.Kind
				}
				if n.Descr == "" {
					n.Descr = firstLine(nb.Descr)
				}
			}
			if to == from {
				continue // a device hearing itself, through a loop or a stack
			}
			g.Links = mergeLink(g.Links, Link{A: from, APort: nb.Port, B: to, BPort: nb.Neighbour.Port, Protocol: nb.Protocol})
		}
	}

	for _, id := range order {
		g.Nodes = append(g.Nodes, *nodes[id])
	}
	sort.SliceStable(g.Links, func(i, j int) bool {
		if g.Links[i].A != g.Links[j].A {
			return g.Links[i].A < g.Links[j].A
		}
		return g.Links[i].APort < g.Links[j].APort
	})
	return g
}

// mergeLink adds l, unless it is a link already there seen from its other end, or
// the same link heard over LLDP and CDP both.
func mergeLink(links []Link, l Link) []Link {
	for i, o := range links {
		switch {
		case o.A == l.A && o.B == l.B && samePort(o.APort, l.APort):
			links[i].Protocol = mergeProtocols(o.Protocol, l.Protocol)
			if links[i].BPort == "" {
				links[i].BPort = l.BPort
			}
			return links
		case o.A == l.B && o.B == l.A && (samePort(o.APort, l.BPort) || samePort(o.BPort, l.APort)):
			links[i].Both = true
			links[i].Protocol = mergeProtocols(o.Protocol, l.Protocol)
			if links[i].BPort == "" {
				links[i].BPort = l.APort
			}
			return links
		}
	}
	return append(links, l)
}

func mergeProtocols(a, b string) string {
	if a == b || strings.Contains(a, b) {
		return a
	}
	return a + "+" + b
}

// samePort compares two names of one port: "Gi1/0/1" and "GigabitEthernet1/0/1",
// "Te1/1/1" and "TenGigabitEthernet1/1/1", the same once each is its first two
// letters and its numbers.
func samePort(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return portKey(a) == portKey(b)
}

func portKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	i := strings.IndexFunc(s, func(r rune) bool { return unicode.IsDigit(r) })
	if i < 0 {
		return s
	}
	prefix := s[:i]
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return prefix + s[i:]
}

// NextHops are the management addresses a device's neighbours give: where to go
// next when the map is followed beyond the devices listed.
func NextHops(w portmap.Walk) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range portmap.Neighbours(w) {
		if n.Address != "" && !seen[n.Address] {
			seen[n.Address] = true
			out = append(out, n.Address)
		}
	}
	return out
}

// kindOf reads what a device says it is: LLDP's capabilities when it answers
// LLDP-MIB, else sysServices (RFC 3418), the layers it serves as a sum of powers
// of two. Read as devices fill it in rather than as the RFC words it: a switch is
// 2 and a layer-3 switch 6, a router 78 (2, 4, 8 and 64 — it serves sessions of
// its own), a server 72.
func kindOf(caps []byte, services int) string {
	bit := func(n int) bool { return n/8 < len(caps) && caps[n/8]&(0x80>>(n%8)) != 0 }
	switch {
	case bit(3):
		return "ap"
	case bit(5): // before the bridge a desk phone also is, for its PC port
		return "phone"
	case bit(2):
		return "switch"
	case bit(4):
		return "router"
	case bit(7):
		return "host"
	case services&4 != 0 && services&8 != 0:
		return "router"
	case services&2 != 0, services&4 != 0:
		return "switch"
	case services&72 != 0:
		return "host"
	}
	return ""
}

func chassisString(subtype int, v any) string {
	b, ok := v.([]byte)
	if !ok {
		return str(v)
	}
	if subtype == 4 && len(b) == 6 {
		return net.HardwareAddr(b).String()
	}
	if s := string(b); isText(s) {
		return s
	}
	return net.HardwareAddr(b).String()
}

func normChassis(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// normName is a name without its domain, lower-cased: one device answers as
// "core-sw-01" and its neighbours name it "core-sw-01.corp.example".
func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if net.ParseIP(s) != nil {
		return s
	}
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func isText(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func str(v any) string {
	switch x := v.(type) {
	case []byte:
		return strings.TrimRight(string(x), "\x00")
	case string:
		return x
	}
	return ""
}

func num(v any) int {
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
