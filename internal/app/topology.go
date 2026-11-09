package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"SnmpLens/pkg/portmap"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/topology"

	"github.com/gosnmp/gosnmp"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The network map: the devices chosen, what each says of its neighbours, and —
// when asked — the neighbours' neighbours, reached at the management address
// LLDP or CDP gives. pkg/topology draws; this walks.
//
// Following neighbours reaches devices nobody listed, with identifiers nobody
// chose for them: a device listed as a target is asked with its own, any other
// with the defaults. Bounded in depth and in count, a few at a time, and
// cancellable, because a campus answers with more than anyone meant to walk.

// TopologyRequest names the devices to start from and how far to go.
type TopologyRequest struct {
	Seeds []snmp.SnmpRequest `json:"seeds"`
	// Template is the default identifiers, for a device found by following a
	// neighbour that is not among the targets. Its own targets are ignored.
	Template snmp.SnmpRequest `json:"template"`
	// Known are the targets' own requests, by address, for a neighbour that is one.
	Known []snmp.SnmpRequest `json:"known"`
	// Depth is how many hops past the seeds to follow: 0 walks the seeds alone.
	Depth int `json:"depth"`
}

// TopologyProgress is the "topology:progress" event.
type TopologyProgress struct {
	Walked  int `json:"walked"`
	Pending int `json:"pending"`
}

const (
	maxTopologyDepth   = 3
	maxTopologyDevices = 64
	topologyParallel   = 4
)

type topologyState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

// TopologyScan draws the map.
func (a *App) TopologyScan(req TopologyRequest) (topology.Graph, error) {
	if a.snmpClient == nil {
		return topology.Graph{}, errors.New("SNMP is not ready")
	}
	if len(req.Seeds) == 0 {
		return topology.Graph{}, errors.New("choose at least one device")
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	a.topologies.mu.Lock()
	if a.topologies.cancel != nil {
		a.topologies.mu.Unlock()
		cancel()
		return topology.Graph{}, errors.New("a map is already being drawn")
	}
	a.topologies.cancel = cancel
	a.topologies.mu.Unlock()
	defer func() {
		cancel()
		a.topologies.mu.Lock()
		a.topologies.cancel = nil
		a.topologies.mu.Unlock()
	}()

	walk := func(r snmp.SnmpRequest) (portmap.Walk, error) {
		var w portmap.Walk
		err := a.snmpClient.Record(ctx, r, topology.Roots, func(p gosnmp.SnmpPDU) error {
			w = append(w, p)
			return nil
		})
		return w, err
	}
	progress := func(p TopologyProgress) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "topology:progress", p)
		}
	}
	return crawlTopology(ctx, req, walk, progress)
}

// TopologyCancel stops the map being drawn, if there is one.
func (a *App) TopologyCancel() {
	a.topologies.mu.Lock()
	defer a.topologies.mu.Unlock()
	if a.topologies.cancel != nil {
		a.topologies.cancel()
	}
}

// crawlTopology walks the seeds, then the neighbours they name, hop by hop.
func crawlTopology(ctx context.Context, req TopologyRequest, walk func(snmp.SnmpRequest) (portmap.Walk, error),
	progress func(TopologyProgress)) (topology.Graph, error) {
	depth := min(max(req.Depth, 0), maxTopologyDepth)
	known := map[string]snmp.SnmpRequest{}
	for _, r := range req.Known {
		if len(r.Targets) == 1 {
			known[r.Targets[0]] = r
		}
	}

	visited := map[string]bool{}
	var level []snmp.SnmpRequest
	for _, s := range req.Seeds {
		if len(s.Targets) == 1 && !visited[s.Targets[0]] {
			visited[s.Targets[0]] = true
			level = append(level, s)
		}
	}

	var devices []topology.Device
	var warnings []string
	walked := 0
	for hop := 0; hop <= depth && len(level) > 0; hop++ {
		results := make([]portmap.Walk, len(level))
		errs := make([]error, len(level))
		sem := make(chan struct{}, topologyParallel)
		var wg sync.WaitGroup
		for i, r := range level {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				results[i], errs[i] = walk(r)
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return topology.Graph{}, err
		}

		var next []snmp.SnmpRequest
		for i, r := range level {
			walked++
			address := r.Targets[0]
			if errs[i] != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", address, errs[i]))
				continue
			}
			devices = append(devices, topology.Device{Address: address, Walk: results[i]})
			if hop == depth {
				continue
			}
			for _, hopAddress := range topology.NextHops(results[i]) {
				if visited[hopAddress] {
					continue
				}
				if len(visited) >= maxTopologyDevices {
					warnings = append(warnings, fmt.Sprintf("the map stops at %d devices", maxTopologyDevices))
					break
				}
				visited[hopAddress] = true
				nr, ok := known[hopAddress]
				if !ok {
					nr = req.Template
					nr.Targets = []string{hopAddress}
				}
				next = append(next, nr)
			}
		}
		progress(TopologyProgress{Walked: walked, Pending: len(next)})
		level = next
	}

	g := topology.Build(devices)
	g.Warnings = append(g.Warnings, dedupe(warnings)...)
	return g, nil
}

func dedupe(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
