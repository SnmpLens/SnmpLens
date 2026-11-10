package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"SnmpLens/pkg/portmap"
	"SnmpLens/pkg/snmp"

	"github.com/gosnmp/gosnmp"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The port mapper: what is plugged into which port of a switch. pkg/portmap reads
// the tables; this walks them, on the switch and on the devices that route for
// its stations, and asks DNS what the addresses are called.
//
// One scan at a time, cancellable: walking a switch's forwarding database takes
// seconds on a busy one and minutes on one that answers slowly, and a scan of the
// wrong device must not have to be waited out.

// PortMapRequest names the switch and the devices whose ARP caches put IP
// addresses on its stations. Each is a request the renderer built with that
// device's own identifiers.
type PortMapRequest struct {
	Switch       snmp.SnmpRequest   `json:"switch"`
	Gateways     []snmp.SnmpRequest `json:"gateways"`
	ResolveNames bool               `json:"resolveNames"`
}

// PortMapProgress is the "portmap:progress" event: which stage, and how far.
type PortMapProgress struct {
	Stage string `json:"stage"` // "switch", "vlans", "gateways", "names"
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// maxCiscoVLANs bounds the per-VLAN walks: each is a full walk of a forwarding
// database, and a switch with every VLAN of a campus defined answers for many it
// carries no station in.
const maxCiscoVLANs = 64

// maxNamesLooked bounds the reverse lookups of one scan.
const maxNamesLooked = 1024

type portMapState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// PortMapScan maps the ports of one switch.
func (a *App) PortMapScan(req PortMapRequest) (portmap.Map, error) {
	if a.snmpClient == nil {
		return portmap.Map{}, errors.New("SNMP is not ready")
	}
	if len(req.Switch.Targets) != 1 {
		return portmap.Map{}, errors.New("choose one switch")
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	a.portMaps.mu.Lock()
	if a.portMaps.cancel != nil {
		a.portMaps.mu.Unlock()
		cancel()
		return portmap.Map{}, errors.New("a port map is already being made")
	}
	a.portMaps.cancel = cancel
	a.portMaps.mu.Unlock()
	defer func() {
		cancel()
		a.portMaps.mu.Lock()
		a.portMaps.cancel = nil
		a.portMaps.mu.Unlock()
	}()
	return a.scanPorts(ctx, req)
}

// PortMapCancel stops the scan under way, if there is one.
func (a *App) PortMapCancel() {
	a.portMaps.mu.Lock()
	defer a.portMaps.mu.Unlock()
	if a.portMaps.cancel != nil {
		a.portMaps.cancel()
	}
}

func (a *App) scanPorts(ctx context.Context, req PortMapRequest) (portmap.Map, error) {
	progress := func(stage string, done, total int) {
		// Only to a window: the runtime ends the process for a context it did
		// not issue, and a test's App has none.
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "portmap:progress", PortMapProgress{Stage: stage, Done: done, Total: total})
		}
	}
	walk := func(r snmp.SnmpRequest, roots []string) (portmap.Walk, error) {
		var w portmap.Walk
		err := a.snmpClient.Record(ctx, r, roots, func(p gosnmp.SnmpPDU) error {
			w = append(w, p)
			return nil
		})
		return w, err
	}

	progress("switch", 0, 1)
	sw, err := walk(req.Switch, portmap.Roots)
	if err != nil {
		return portmap.Map{}, fmt.Errorf("%s: %w", req.Switch.Targets[0], err)
	}
	in := portmap.Input{Switch: sw}
	var warnings []string

	// A Cisco switch answers BRIDGE-MIB one VLAN at a time: without a VLAN it
	// answers VLAN 1's, so every other VLAN it lists is read in its own context
	// — and VLAN 1 as well when the default view has nothing, as some do.
	if portmap.IsCisco(sw) {
		vlans := portmap.CiscoVLANs(sw)
		if portmap.HasFDB(sw) {
			vlans = slices.DeleteFunc(vlans, func(v int) bool { return v == 1 })
		}
		if len(vlans) > maxCiscoVLANs {
			warnings = append(warnings, fmt.Sprintf("only the first %d of %d VLANs were read", maxCiscoVLANs, len(vlans)))
			vlans = vlans[:maxCiscoVLANs]
		}
		if len(vlans) > 0 {
			in.ByVLAN = map[int]portmap.Walk{}
		}
		for i, vlan := range vlans {
			progress("vlans", i, len(vlans))
			w, err := walk(inVLAN(req.Switch, vlan), portmap.VLANRoots)
			if ctx.Err() != nil {
				return portmap.Map{}, ctx.Err()
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("VLAN %d: %v", vlan, err))
				continue
			}
			in.ByVLAN[vlan] = w
		}
	}

	for i, g := range req.Gateways {
		progress("gateways", i, len(req.Gateways))
		w, err := walk(g, portmap.ARPRoots)
		if ctx.Err() != nil {
			return portmap.Map{}, ctx.Err()
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", targetOf(g), err))
			continue
		}
		in.ARP = append(in.ARP, w)
	}

	m := portmap.Build(in)
	m.Warnings = append(m.Warnings, warnings...)
	if req.ResolveNames {
		if err := resolveStationNames(ctx, &m, progress); err != nil {
			return portmap.Map{}, err
		}
	}
	return m, nil
}

// inVLAN is a request for one VLAN's view of a Cisco switch: the community
// indexed by the VLAN, or in SNMPv3 the context named after it.
func inVLAN(r snmp.SnmpRequest, vlan int) snmp.SnmpRequest {
	if r.Version == "v3" {
		r.V3.ContextName = "vlan-" + strconv.Itoa(vlan)
	} else {
		r.Community = r.Community + "@" + strconv.Itoa(vlan)
	}
	return r
}

func targetOf(r snmp.SnmpRequest) string {
	if len(r.Targets) > 0 {
		return r.Targets[0]
	}
	return "?"
}

// resolveStationNames asks DNS for the name of every address on the map, a few
// at a time and each with a deadline: a resolver that does not answer must cost
// seconds, not the scan.
func resolveStationNames(ctx context.Context, m *portmap.Map, progress func(string, int, int)) error {
	var ips []string
	seen := map[string]bool{}
	visit := func(s portmap.Station) {
		for _, ip := range s.IPs {
			if !seen[ip] && len(ips) < maxNamesLooked {
				seen[ip] = true
				ips = append(ips, ip)
			}
		}
	}
	for _, p := range m.Ports {
		for _, s := range p.Stations {
			visit(s)
		}
	}
	for _, s := range m.Unplaced {
		visit(s)
	}
	if len(ips) == 0 {
		return nil
	}

	names := make(map[string][]string, len(ips))
	var mu sync.Mutex
	work := make(chan string)
	var wg sync.WaitGroup
	done := 0
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range work {
				lookup, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
				found, _ := net.DefaultResolver.LookupAddr(lookup, ip)
				cancel()
				mu.Lock()
				names[ip] = found
				done++
				if done%16 == 0 || done == len(ips) {
					progress("names", done, len(ips))
				}
				mu.Unlock()
			}
		}()
	}
	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		work <- ip
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}

	fill := func(s *portmap.Station) {
		set := map[string]bool{}
		for _, ip := range s.IPs {
			for _, n := range names[ip] {
				set[trimDot(n)] = true
			}
		}
		s.Names = s.Names[:0]
		for n := range set {
			s.Names = append(s.Names, n)
		}
		sort.Strings(s.Names)
	}
	for i := range m.Ports {
		for j := range m.Ports[i].Stations {
			fill(&m.Ports[i].Stations[j])
		}
	}
	for i := range m.Unplaced {
		fill(&m.Unplaced[i])
	}
	return nil
}

func trimDot(s string) string {
	if len(s) > 0 && s[len(s)-1] == '.' {
		return s[:len(s)-1]
	}
	return s
}
