package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"SnmpLens/pkg/mib"
	"SnmpLens/pkg/preset"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"

)

// The preset library: where dashboard presets live on this machine, and how one
// gets in.
//
// A preset is a file somebody else wrote. Everything here exists because of
// that sentence — the directory it lands in, what is allowed to land there, and
// the fact that nothing is bound or polled as a side effect of importing one.

// presetSubdir is a SIBLING of mibs/, never a child.
//
// ListMibFiles enumerates the MIB directory and feeds what it finds to gosmi;
// a JSON file in there would be loaded as a module and reported as a broken
// MIB. mib-backups/, mib-drafts/ and mib-temp/ are siblings for the same
// reason, and this follows them.
const presetSubdir = "presets"

// PresetImportResult is one file's outcome, shaped like MibImportResult because
// it is shown in the same kind of list.
type PresetImportResult struct {
	FileName string `json:"fileName"`
	Success  bool   `json:"success"`
	Skipped  bool   `json:"skipped,omitempty"`
	Error    string `json:"error,omitempty"`
	// Problems is how many validation errors the imported file has. A preset
	// with problems still imports — see importSinglePreset — so this is the
	// difference between "in your library" and "ready to bind".
	Problems int `json:"problems"`
}

// PresetCost is what preset.Estimate says, plus the one number only this layer
// can compute.
type PresetCost struct {
	preset.Cost
	// RequestsPerDay is rounds multiplied by how many requests a round takes,
	// which depends on the transport's chunk size (snmp.MaxVarbindsPerGet).
	// pkg/preset deliberately does not know that number: a copy of it there
	// would stop matching the first time it is tuned.
	RequestsPerDay int `json:"requestsPerDay"`
	// VarbindsPerRequest is carried so the interface can say WHY the request
	// count is what it is rather than presenting it as a magic number.
	VarbindsPerRequest int `json:"varbindsPerRequest"`
}

// PresetDetail is everything one preset screen needs, in one call.
type PresetDetail struct {
	File   string         `json:"file"`
	Preset preset.Preset  `json:"preset"`
	Errors []preset.Error `json:"errors"`
	Cost   PresetCost     `json:"cost"`
}

// ensureBundledPresets writes the example presets out, ON FIRST RUN ONLY.
//
// Deliberately NOT the shape ensureStandardMibs has. That one runs on every
// startup and restores any bundled MIB that is absent, because nearly every
// other MIB imports from those three and a missing one breaks the tree for
// everything — self-healing is worth more there than obeying a deletion.
//
// A preset is the opposite: nothing depends on it, and it is a file the
// operator curates. Restoring one they deleted would be the application
// arguing with them, once per restart, forever. So the whole extraction is
// skipped as soon as the directory exists — which also means an operator who
// wants the examples back can delete the directory rather than hunt for a
// setting.
func (a *App) ensureBundledPresets() {
	if a.persistentMibDir == "" {
		return
	}
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), presetSubdir)

	// A MARKER, not the directory.
	//
	// "The directory exists" looked like a good proxy for "this machine has a
	// library" and is not one: presetDir() creates the directory as a SIDE
	// EFFECT, and it is called by ListPresets — so opening the preset settings
	// once, on any earlier version, was enough to make every later startup skip
	// the extraction. Which is exactly what happened: an installation that had
	// been running for weeks got no examples at all.
	//
	// The marker is written after the extraction and is the only thing consulted
	// afterwards, so an operator who deletes an example keeps it deleted and one
	// who deletes the whole directory gets them back.
	marker := filepath.Join(dir, extractedMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}

	entries, err := bundledDir(a.presets, presetSubdir)
	if err != nil {
		log.Printf("WARNING: could not read the embedded presets: %v", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("WARNING: could not create the preset directory: %v", err)
		return
	}

	// An existing library that predates the marker: leave it alone but record
	// that this ran, or the examples would land on top of a curated folder.
	if existing, err := os.ReadDir(dir); err == nil && len(existing) > 0 {
		writeMarker(marker)
		return
	}

	written := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := bundledFile(a.presets, presetSubdir+"/"+e.Name())
		if err != nil {
			log.Printf("WARNING: could not read the embedded preset %s: %v", e.Name(), err)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600); err != nil {
			log.Printf("WARNING: could not write the preset %s: %v", e.Name(), err)
			continue
		}
		written++
	}
	writeMarker(marker)
	log.Printf("Extracted %d example preset(s) to %s", written, dir)
}

// extractedMarker records that the examples have been offered once. Dotted, so
// preset.List skips it the way it skips any dotfile.
const extractedMarker = ".examples-extracted"

func writeMarker(path string) {
	if err := os.WriteFile(path, []byte("The example presets have been extracted once.\n"+
		"Delete this file to have them written again.\n"), 0o600); err != nil {
		log.Printf("WARNING: could not record that the presets were extracted: %v", err)
	}
}

// presetDir returns the preset directory, creating it on first use.
func (a *App) presetDir() (string, error) {
	if a.persistentMibDir == "" {
		return "", fmt.Errorf("the configuration directory is not ready")
	}
	dir := filepath.Join(filepath.Dir(a.persistentMibDir), presetSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create the preset directory: %w", err)
	}
	return dir, nil
}

// resolvePresetPath turns a name the renderer supplied into a path inside the
// preset directory, or refuses.
//
// The same shape as mib.SafeMibPath, and for the same reason: monitoring.db,
// service.json and the secret store all sit one directory above this one, and
// every method here takes a file name from outside.
func (a *App) resolvePresetPath(name string) (string, error) {
	dir, err := a.presetDir()
	if err != nil {
		return "", err
	}
	clean := filepath.Base(strings.TrimSpace(name))
	if clean == "" || clean == "." || clean == ".." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("invalid preset file name %q", name)
	}
	full := filepath.Join(dir, clean)
	if filepath.Dir(full) != filepath.Clean(dir) {
		return "", fmt.Errorf("refusing a preset path outside the preset directory: %q", name)
	}
	return full, nil
}

// ListPresets returns the preset library.
//
// Named ListPresets rather than PresetList on purpose: tools/genbridge.mjs
// gives anything starting with List an empty ARRAY as its screenshot fixture,
// and a name it does not recognise gets null — which throws on the first .map
// in a generated file nobody reads.
func (a *App) ListPresets() []preset.Info {
	dir, err := a.presetDir()
	if err != nil {
		log.Printf("ListPresets: %v", err)
		return []preset.Info{}
	}
	list, err := preset.List(dir)
	if err != nil {
		log.Printf("ListPresets: %v", err)
		return []preset.Info{}
	}
	return list
}

// ReadPreset opens one preset: what it says, what is wrong with it, and what it
// would cost.
//
// One call rather than three, because they are one screen and three calls would
// let it show a cost computed from a file the error list no longer describes.
func (a *App) ReadPreset(name string) (PresetDetail, error) {
	path, err := a.resolvePresetPath(name)
	if err != nil {
		return PresetDetail{}, err
	}
	p, errs, err := preset.LoadFile(path)
	if err != nil {
		return PresetDetail{}, err
	}
	return PresetDetail{
		File:   filepath.Base(path),
		Preset: p,
		// Never nil. Validate builds its slice with append and returns nil on
		// success, which crosses the bridge as null and throws on .map — on
		// the VALID path, which is the one nobody tests by hand.
		Errors: nonNilErrors(errs),
		Cost:   presetCost(p),
	}, nil
}

// PresetEstimate is the cost alone, for a caller that has not opened the file.
func (a *App) PresetEstimate(name string) (PresetCost, error) {
	path, err := a.resolvePresetPath(name)
	if err != nil {
		return PresetCost{}, err
	}
	p, _, err := preset.LoadFile(path)
	if err != nil {
		return PresetCost{}, err
	}
	return presetCost(p), nil
}

// ListPresetWidgetKinds serves the frozen vocabulary to the interface.
//
// The descriptions are i18n key suffixes, not prose: the renderer looks up
// preset.widget.<kind>. Served from Go rather than mirrored in JavaScript
// because a hand-copied list drifts, and the first symptom would be a kind the
// library offers and the validator rejects.
func (a *App) ListPresetWidgetKinds() []preset.WidgetDoc {
	return preset.WidgetKinds()
}

// ImportPresetFiles copies preset files into the library.
//
// The shape of ImportMibFiles, including the part that matters most: this
// method reads any absolute path the renderer names, and ReadPreset hands back
// the content of anything in the destination — so the two together are an
// arbitrary-file-read primitive over the bridge, and what bounds its worth is
// what is allowed to land. The gate here is POSITIVE rather than a blacklist:
// a file gets in only if it parses as JSON and declares a formatVersion. A
// private key, a password file, a browser profile: none of those do.
//
// A preset that FAILS validation still imports, exactly as a MIB with a syntax
// error does. The error list with its field paths is what the library is for,
// and you cannot fix a file you were not allowed to keep. What it cannot do is
// bind — that check lives where binding happens.
func (a *App) ImportPresetFiles(paths []string) []PresetImportResult {
	results := make([]PresetImportResult, 0, len(paths))
	for _, src := range paths {
		results = append(results, a.importSinglePreset(src))
	}
	return results
}

func (a *App) importSinglePreset(src string) PresetImportResult {
	name := filepath.Base(src)
	fail := func(format string, args ...any) PresetImportResult {
		return PresetImportResult{FileName: name, Success: false, Error: fmt.Sprintf(format, args...)}
	}

	dst, err := a.resolvePresetPath(name)
	if err != nil {
		return fail("%v", err)
	}

	// Bounded before it is read: os.ReadFile on a renderer-named path is an
	// unbounded allocation, so a path to a large file — or to a device that
	// never ends — is a way to take the process down without a single packet.
	if info, err := os.Stat(src); err == nil && info.Size() > preset.MaxFileBytes {
		return fail("this file is %d KB; a preset is a small JSON document and nothing legitimate is over %d KB",
			info.Size()/1024, preset.MaxFileBytes/1024)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		log.Printf("ImportPresetFiles: could not read %s: %v", src, err)
		return fail("read error: %v", err)
	}

	// The gate. Not a validity check — a preset with a bad OID is exactly what
	// the error list exists for — but a check that this is a preset AT ALL.
	var probe struct {
		FormatVersion *int `json:"formatVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		// Not JSON. pkg/mib already recognises the files people download by
		// mistake — an HTML error page, a PDF, a zip, UTF-16 — so the refusal
		// names what the file actually is instead of saying "not JSON".
		if summary, hint, reject := mib.ImportRejection(data); reject && summary != "" {
			msg := summary
			if hint != "" {
				msg += " — " + hint
			}
			return fail("%s", msg)
		}
		return fail("this file is not a preset: it does not parse as JSON (%v)", err)
	}
	if probe.FormatVersion == nil {
		return fail("this file is JSON but not a preset: it declares no formatVersion")
	}

	if existing, err := os.ReadFile(dst); err == nil && bytes.Equal(existing, data) {
		_, errs := preset.Parse(data)
		return PresetImportResult{FileName: name, Success: true, Skipped: true, Problems: len(errs)}
	}

	if err := os.WriteFile(dst, data, 0o600); err != nil {
		log.Printf("ImportPresetFiles: could not write %s: %v", dst, err)
		return fail("write error: %v", err)
	}
	_, errs := preset.Parse(data)
	log.Printf("ImportPresetFiles: imported %s (%d problem(s))", name, len(errs))
	return PresetImportResult{FileName: name, Success: true, Problems: len(errs)}
}

// ImportPresetDialog is the button next to the drop zone.
//
// Drag-and-drop is the path most files take, but a file manager is not always
// the way somebody has the file in front of them — and a dialog is also the
// only route when the window is not focused enough to receive a drop.
func (a *App) ImportPresetDialog() ([]PresetImportResult, error) {
	paths, err := a.openFiles("Add dashboard presets", fileFilter("Preset files", "*.json;*"))
	if err != nil {
		return nil, err
	}
	// Cancelled. An empty ARRAY rather than nil: the caller renders a result
	// list, and null throws on .map.
	return a.ImportPresetFiles(paths), nil
}

// DeviceIdentity is what a device says it is, and which presets claim it.
type DeviceIdentity struct {
	Target string `json:"target"`
	// SysObjectID is the vendor and model as an OID. Presets match on it and
	// not on sysDescr, which is prose that differs between two firmware
	// revisions of the same switch.
	SysObjectID string `json:"sysObjectId,omitempty"`
	SysDescr    string `json:"sysDescr,omitempty"`
	SysName     string `json:"sysName,omitempty"`
	Error       string `json:"error,omitempty"`
	// Presets is the whole library, ORDERED for this device rather than
	// filtered to it. Match is advice to the person binding, never an automatic
	// action — hiding the rest would turn that advice into a decision, and a
	// preset that says nothing about which device it is for is still one
	// somebody downloaded for this one.
	Presets []preset.Info `json:"presets"`
	// Matched is how many of them claim this device, so the interface can say
	// so without recomputing the rule in JavaScript.
	Matched int `json:"matched"`
}

// IdentifyDevice asks a device what it is and orders the preset library for it.
//
// A separate method from TestConnection, which answers a different question —
// "did these credentials work" — and answers it with one varbind. This one is
// three varbinds in one request, and it exists because nothing in this
// application read sysObjectID before: preset.Match had no data source at all,
// so the matching rule was written and never wired.
//
// It never fails hard. A device that does not answer still gets the library
// back, unordered, because the operator picking a preset by hand is the normal
// path and an unreachable device must not take the picker away with it.
func (a *App) IdentifyDevice(req snmp.TestRequest) DeviceIdentity {
	out := DeviceIdentity{Target: req.Target, Presets: a.ListPresets()}

	const (
		oidSysDescr    = "1.3.6.1.2.1.1.1.0"
		oidSysObjectID = "1.3.6.1.2.1.1.2.0"
		oidSysName     = "1.3.6.1.2.1.1.5.0"
	)
	results := a.snmpClient.GetMany(context.Background(),
		[]string{req.Target}, []string{oidSysDescr, oidSysObjectID, oidSysName},
		req.Community, req.Version, req.Port, req.Timeout, 1, req.V3)

	if len(results) == 0 {
		out.Error = "no response"
		return out
	}
	m := results[0]
	if err, bad := m.Errors[oidSysObjectID]; bad {
		out.Error = err
	}
	text := func(oid string) string {
		if r := m.Results[oid]; r != nil {
			return fmt.Sprintf("%v", r.Value)
		}
		return ""
	}
	out.SysObjectID = text(oidSysObjectID)
	out.SysDescr = text(oidSysDescr)
	out.SysName = text(oidSysName)

	out.Presets, out.Matched = rankForDevice(out.Presets, out.SysObjectID)
	return out
}

// rankForDevice orders the library for one device and counts what claims it.
//
// Split out because the counting rule is the part worth testing and the SNMP
// round trip is the part that needs a device: with them in one function the
// only way to check that "3 presets match" is 3 is to have a switch.
func rankForDevice(list []preset.Info, sysObjectID string) ([]preset.Info, int) {
	if strings.TrimSpace(sysObjectID) == "" {
		// Nothing to rank by. The library comes back in its own order rather
		// than empty: picking by hand is the normal path.
		return list, 0
	}
	ranked := preset.Rank(list, sysObjectID)
	matched := 0
	for _, p := range ranked {
		if preset.MatchDepth(p.SysObjectIDPrefix, sysObjectID) > 0 {
			matched++
		}
	}
	return ranked, matched
}

// discoverFor walks what a preset asks for and expands its templates.
//
// One walk per COLUMN, not per widget: a grid of port states and a chart of
// port counters discover from the same ifDescr, and that is one walk. Then each
// widget takes the instances of its own column.
//
// A walk that fails leaves that widget with NO OIDs, and Validate then refuses
// the expanded preset — which is the honest outcome. Binding a discovering
// preset to a device that cannot answer the walk would otherwise produce a
// dashboard of empty widgets polling nothing, and a session that looks bound.
func (a *App) discoverFor(p preset.Preset, target, snmpVersion string, conn MonitorConnection) (preset.Preset, error) {
	walks := preset.DiscoveryWalks(p)
	if len(walks) == 0 {
		return p, nil
	}
	if a.snmpClient == nil {
		return p, fmt.Errorf("no SNMP client")
	}
	sessConn, creds := conn.split()
	v3 := snmp.V3Params{
		User: sessConn.V3User, AuthProto: sessConn.V3AuthProto, PrivProto: sessConn.V3PrivProto,
		SecLevel: sessConn.V3SecLevel, ContextName: sessConn.V3ContextName,
		AuthPass: creds.AuthPass, PrivPass: creds.PrivPass,
	}

	byColumn := map[string][]preset.Instance{}
	for _, column := range walks {
		results := a.snmpClient.Walk([]string{target}, column,
			creds.Community, snmpVersion, sessConn.Port, sessConn.TimeoutSec, sessConn.Retries, v3)
		if len(results) == 0 || results[0].Error != "" || results[0].Result == nil {
			why := "no answer"
			if len(results) > 0 && results[0].Error != "" {
				why = results[0].Error
			}
			return p, fmt.Errorf("discovering %s on %s: %s", column, target, why)
		}

		rows, ok := results[0].Result.Value.([]*snmp.Result)
		if !ok {
			return p, fmt.Errorf("discovering %s on %s: unexpected walk result", column, target)
		}
		oids := make([]string, 0, len(rows))
		values := make(map[string]string, len(rows))
		for _, r := range rows {
			if r == nil {
				continue
			}
			oids = append(oids, r.Oid)
			values[r.Oid] = fmt.Sprintf("%v", r.Value)
		}
		byColumn[column] = preset.InstancesFrom(column, oids, func(oid string) string { return values[oid] })
	}

	found := map[int][]preset.Instance{}
	for i, w := range p.Widgets {
		if w.Discover == nil {
			continue
		}
		found[i] = byColumn[strings.TrimPrefix(w.Discover.Walk, ".")]
	}
	return preset.Expand(p, found), nil
}

// PresetBind is the moment a preset stops being a file and starts being a
// monitoring: it creates one session for one equipment and starts it.
//
// ONE SESSION PER TARGET, never one session across several. The cost was stated
// per equipment, the credentials are per equipment, and the guardrail in
// pkg/monitor is per session — so one slow device backing off must not slow the
// healthy ones down with it.
//
// What to poll is materialised into the session's OWN columns: the OID list and
// the interval are what the poll clock reads, and it never opens the snapshot.
// A session whose layout cannot be decoded keeps polling and loses its
// dashboard; the other way round it would keep its dashboard and poll nothing.
//
// A preset with problems cannot be bound. It can be imported, listed and read —
// that is what the error list is for — but binding is the step that puts
// traffic on somebody's network, and doing that from a file this application
// has already said it does not understand is not a thing to do quietly.
func (a *App) PresetBind(fileName, target, snmpVersion string, conn MonitorConnection) (storage.Session, error) {
	if a.storage == nil || a.scheduler == nil {
		return storage.Session{}, fmt.Errorf("storage not initialized")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return storage.Session{}, fmt.Errorf("a preset is bound to an equipment; no target was given")
	}

	path, err := a.resolvePresetPath(fileName)
	if err != nil {
		return storage.Session{}, err
	}
	p, errs, err := preset.LoadFile(path)
	if err != nil {
		return storage.Session{}, err
	}
	if len(errs) > 0 {
		return storage.Session{}, fmt.Errorf(
			"%s has %d problem(s) and cannot be bound until they are fixed", filepath.Base(path), len(errs))
	}

	// The walk happens HERE and only here. What is stored afterwards is a plain
	// preset with concrete OIDs, so the poll path stays GET-only and the cost is
	// arithmetic before the first tick.
	if preset.HasDiscovery(p) {
		expanded, err := a.discoverFor(p, target, snmpVersion, conn)
		if err != nil {
			return storage.Session{}, err
		}
		// Validated AGAIN, because the walk decides how many OIDs there are: a
		// device with four hundred interfaces can push a preset past the sanity
		// bound its author never came close to.
		if errs := preset.Validate(expanded); len(errs) > 0 {
			return storage.Session{}, fmt.Errorf(
				"%s does not fit this equipment: %s (%d problem(s) after discovery)",
				filepath.Base(path), errs[0].Field, len(errs))
		}
		p = expanded
	}

	oids := preset.PollOIDs(p)
	if len(oids) == 0 {
		return storage.Session{}, fmt.Errorf("%s polls nothing", filepath.Base(path))
	}

	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(path)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sessConn, creds := conn.split()

	id, err := a.storage.CreateSession(
		name, strings.Join(oids, ","), []string{target},
		p.IntervalSec*1000, snmpVersion, now,
		// The bands the preset carries, materialised into the session's OWN
		// threshold map — the same column MonitorCreateSession fills and the
		// same one the evaluator reads. Nothing new evaluates anything, and the
		// operator can edit them afterwards like any other session's.
		//
		// This used to be nil, with a comment saying what counts as too much is
		// the operator's judgement about their own network. That is true of a
		// percentage on a link the author has never seen and false of what a
		// preset is mostly written for: a port that is not up(1), a UPS running
		// on battery. Those are properties of the MIB, and the author knows
		// them better than whoever binds the file.
		presetThresholds(p), sessConn,
		&storage.SessionPreset{
			File:          filepath.Base(path),
			Name:          p.Name,
			FormatVersion: p.FormatVersion,
			BoundAt:       now,
			Widgets:       p.Widgets,
		},
	)
	if err != nil {
		return storage.Session{}, err
	}
	a.saveSessionCreds(id, creds)

	if err := a.MonitorStart(id); err != nil {
		// The session exists and is stored; it simply is not polling. Reported
		// rather than rolled back, because deleting it would throw away the
		// binding the operator just made and leave nothing to retry from.
		log.Printf("PresetBind: %s created but not started: %v", id, err)
	}
	return a.findSession(id)
}

// DeletePreset removes one preset from the library.
//
// Sessions already bound to it are untouched: what they poll and what they draw
// was snapshotted when they were bound, so a preset is a file you started from
// rather than a file they depend on. Deleting one cannot silently stop a
// monitoring.
func (a *App) DeletePreset(name string) error {
	path, err := a.resolvePresetPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// presetCost turns a preset into the numbers shown before it is bound.
//
// One target. Binding is per equipment, so multiplying here would state a cost
// for a device nobody named.
func presetCost(p preset.Preset) PresetCost {
	c := preset.Estimate(p)
	out := PresetCost{Cost: c, VarbindsPerRequest: snmp.MaxVarbindsPerGet}
	if c.OIDs == 0 {
		return out
	}
	// Ceiling: thirty-one OIDs is two requests, not one and a bit.
	perRound := (c.OIDs + snmp.MaxVarbindsPerGet - 1) / snmp.MaxVarbindsPerGet
	out.RequestsPerDay = c.PollsPerDay * perRound
	return out
}

// presetThresholds turns the bands a preset carries into the map a session
// stores, keyed by the full OID exactly as MonitorCreateSession's is.
//
// Called AFTER discovery, so a widget that watches its ports watches the ports
// the equipment turned out to have. nil when the preset carries none, which is
// the same value the column held before presets could carry any — a session
// with an empty map and one with no map behave identically, and nil is the one
// storage already knows.
func presetThresholds(p preset.Preset) map[string]*storage.Thresholds {
	bands := preset.ThresholdsFor(p)
	if len(bands) == 0 {
		return nil
	}
	out := make(map[string]*storage.Thresholds, len(bands))
	for oid, b := range bands {
		out[oid] = &storage.Thresholds{
			Min: b.Min, Max: b.Max,
			ForSeconds: b.ForSeconds, AlertEnabled: b.Alerts(),
		}
	}
	return out
}

func nonNilErrors(errs []preset.Error) []preset.Error {
	if errs == nil {
		return []preset.Error{}
	}
	return errs
}
