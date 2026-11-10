package app

import (
	"strings"
	"time"

	"SnmpLens/pkg/mib"
	"SnmpLens/pkg/simulator"
)

// SimulatorPreviewQuery is which of a device's answers to show: those under an
// OID, or whose MIB name holds the text given — and only the ones that move,
// when asked — read as they would be once the device has run SinceSeconds.
type SimulatorPreviewQuery struct {
	Filter       string  `json:"filter"`
	DynamicOnly  bool    `json:"dynamicOnly"`
	SinceSeconds float64 `json:"sinceSeconds"`
}

// SimulatorPreviewRow is one object a simulated device would answer, named from
// the loaded MIBs when they know it, and saying how its value behaves.
type SimulatorPreviewRow struct {
	OID       string `json:"oid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Behaviour string `json:"behaviour"`
}

// SimulatorPreviewPage is the first rows of what matched, and how many did.
type SimulatorPreviewPage struct {
	Rows  []SimulatorPreviewRow `json:"rows"`
	Total int                   `json:"total"`
}

// SimulatorPreview is what a device, as the editor holds it, would answer —
// saved or not, running or not. It needs no secret and keeps none: the device
// need only have a model, and parameters and values of its own the model can
// take.
func (a *App) SimulatorPreview(d simulator.Device, q SimulatorPreviewQuery) (SimulatorPreviewPage, error) {
	out := SimulatorPreviewPage{Rows: []SimulatorPreviewRow{}}
	// What a walk would find, cabled as the device is on the bench.
	d.Wiring = a.previewWiring(d)
	rows, err := simulator.PreviewRows(d.WithoutSecrets(), time.Duration(max(q.SinceSeconds, 0)*float64(time.Second)))
	if err != nil {
		return out, err
	}
	filter := strings.TrimSpace(q.Filter)
	subtree, byOID := oidSubtree(filter)
	byName := filter != "" && !byOID
	// A name is looked for across every object, so every object is named first;
	// otherwise only the page is.
	var names map[string]mib.OidName
	if byName {
		names = a.previewNames(rows)
	}
	var matched []simulator.PreviewRow
	for _, r := range rows {
		switch {
		case q.DynamicOnly && r.Behaviour == simulator.BehaviourStatic:
		case byOID && r.OID != subtree && !strings.HasPrefix(r.OID, subtree+"."):
		case byName && !containsFold(names[r.OID].String(), filter) && !strings.Contains(r.OID, filter):
		default:
			matched = append(matched, r)
		}
	}
	out.Total = len(matched)
	page := matched[:min(len(matched), simulator.MaxPreviewRows)]
	if !byName {
		names = a.previewNames(page)
	}
	for _, r := range page {
		out.Rows = append(out.Rows, SimulatorPreviewRow{OID: r.OID, Name: names[r.OID].String(), Type: r.Type,
			Value: r.Value, Behaviour: r.Behaviour})
	}
	return out, nil
}

// previewNames names rows from the loaded MIBs, in one hold of gosmi's lock.
func (a *App) previewNames(rows []simulator.PreviewRow) map[string]mib.OidName {
	if a.mibService == nil || len(rows) == 0 {
		return nil
	}
	oids := make([]string, len(rows))
	for i, r := range rows {
		oids[i] = r.OID
	}
	return a.mibService.NameOIDs(oids)
}

// oidSubtree reads a filter that is an OID — dotted numbers, the leading dot
// optional — as the subtree it names, written as a preview writes an OID.
func oidSubtree(filter string) (string, bool) {
	t := strings.TrimPrefix(filter, ".")
	if t == "" {
		return "", false
	}
	for _, arc := range strings.Split(t, ".") {
		if arc == "" || strings.Trim(arc, "0123456789") != "" {
			return "", false
		}
	}
	return "." + t, true
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
