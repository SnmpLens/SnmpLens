package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"
)

// MonitorConnection is the connection half of a session, as the renderer sends
// it. The credential fields are write-only: they are split off into pkg/secrets
// and never travel back out.
type MonitorConnection struct {
	Port       int `json:"port"`
	TimeoutSec int `json:"timeoutSec"`
	Retries    int `json:"retries"`
	// Community and the v3 passphrases inside V3 are moved to the secret store
	// on the way in. See sessionCreds.
	Community string        `json:"community"`
	V3        snmp.V3Params `json:"v3"`
	// Profile names the identifiers this connection was built from: "default"
	// for the default identifiers, a credential profile's id, or empty for a
	// target's own overrides and for anything older. An opaque id and not a
	// credential, so it is safe in monitoring.db — and it is what lets a
	// session FOLLOW its profile: rotate a passphrase and every session polling
	// with it is updated, instead of failing authentication until rebound.
	Profile string `json:"profile"`
}

// sessionCreds is the blob held in the secret store for one session.
type sessionCreds struct {
	Community string `json:"community,omitempty"`
	AuthPass  string `json:"authPass,omitempty"`
	PrivPass  string `json:"privPass,omitempty"`
}

// split separates what may be stored in the clear from what may not.
func (c MonitorConnection) split() (*storage.SessionConn, sessionCreds) {
	return &storage.SessionConn{
			Port:          c.Port,
			TimeoutSec:    c.TimeoutSec,
			Retries:       c.Retries,
			V3User:        c.V3.User,
			V3AuthProto:   c.V3.AuthProto,
			V3PrivProto:   c.V3.PrivProto,
			V3SecLevel:    c.V3.SecLevel,
			V3ContextName: c.V3.ContextName,
			Profile:       c.Profile,
		}, sessionCreds{
			Community: c.Community,
			AuthPass:  c.V3.AuthPass,
			PrivPass:  c.V3.PrivPass,
		}
}

// MonitorCreateSession creates a persistent monitoring session and returns its
// UUID.
//
// The connection is persisted alongside it because the poll clock now runs in
// Go: a session has to be resumable with no window open, and after a restart
// there is no renderer left to ask for the settings.
func (eng *Engine) MonitorCreateSession(oid string, targets []string, intervalMs int, snmpVersion string, thresholds map[string]*storage.Thresholds, name string, conn MonitorConnection) (string, error) {
	if eng.storage == nil {
		return "", fmt.Errorf("storage not initialized")
	}
	sessConn, creds := conn.split()
	// nil: a session built by hand carries no preset snapshot. PresetBind is
	// the one caller that passes one.
	id, err := eng.storage.CreateSession(name, oid, targets, intervalMs, snmpVersion, time.Now().UTC().Format(time.RFC3339), thresholds, sessConn, nil)
	if err != nil {
		return "", err
	}
	eng.saveSessionCreds(id, creds)
	return id, nil
}

// saveSessionCreds stores the credentials outside the database.
func (eng *Engine) saveSessionCreds(sessionID string, creds sessionCreds) {
	if eng.secrets == nil {
		return
	}
	if creds.Community == "" && creds.AuthPass == "" && creds.PrivPass == "" {
		_ = eng.secrets.Delete(secrets.SessionRef(sessionID))
		return
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return
	}
	if err := eng.secrets.Set(secrets.SessionRef(sessionID), string(raw)); err != nil {
		log.Printf("WARNING: could not store the credentials of session %s: %v", sessionID, err)
	}
}

// loadSessionCreds reads them back. A session with none simply polls with what
// the profile alone provides, which is correct for a v2c agent that accepts an
// empty community.
func (eng *Engine) loadSessionCreds(sessionID string) sessionCreds {
	var creds sessionCreds
	if eng.secrets == nil {
		return creds
	}
	raw, err := eng.secrets.Get(secrets.SessionRef(sessionID))
	if err != nil {
		return creds
	}
	_ = json.Unmarshal([]byte(raw), &creds)
	return creds
}

// initScheduler wires the poll clock to storage, the threshold engine and the
// renderer.
func (eng *Engine) initScheduler() {
	s := monitor.NewScheduler()

	s.Persist = func(points []monitor.Point) {
		if eng.storage == nil {
			return
		}
		dps := make([]storage.DataPoint, 0, len(points))
		for _, p := range points {
			dps = append(dps, storage.DataPoint{
				SessionID: p.SessionID, Target: p.Target, Timestamp: p.Timestamp,
				Value: p.Value, Delta: p.Delta, Rate: p.Rate,
				ResponseTimeMs: p.ResponseTimeMs, Error: p.Error,
				SnmpType: p.SnmpType, OID: p.OID,
			})
		}
		eng.storage.QueueDataPoints(dps)
	}

	s.Evaluate = func(sessionID, name string, samples []monitor.Sample, th map[string]*monitor.Threshold) error {
		if eng.evaluator == nil {
			return nil
		}
		return eng.evaluator.Ingest(sessionID, name, samples, th)
	}

	// With no window open this does nothing, and nothing else changes: by the
	// time it runs the samples are already stored and already evaluated.
	s.Emit = func(sessionID string, points []monitor.Point) {
		eng.emit("monitor:samples", map[string]interface{}{
			"sessionId": sessionID,
			"points":    points,
		})
	}

	// A poll round that panicked is contained by the scheduler; this is what
	// makes it VISIBLE. A recovered panic that nobody records is a session
	// quietly returning nothing, which reads as a device that stopped
	// answering — the one explanation that is wrong.
	s.OnPanic = func(sessionID, name, recovered, stack string) {
		detail := fmt.Sprintf("the poll of %q panicked and was recovered: %s", name, recovered)
		_ = eng.recordEvent(events.Event{
			Category: events.CategorySystem,
			Kind:     events.KindSystemPollFailed,
			Severity: events.SevMajor.String(),
			TitleKey: "events.kind." + events.KindSystemPollFailed,
			Params:   map[string]any{"detail": detail, "sessionId": sessionID},
			Summary:  detail,
		}, stack)
	}

	s.OnStateChange = eng.sessionsChanged

	// The guardrail's report. Two destinations, because the two questions are
	// different: the journal answers "what happened while I was away" for an
	// operator running with no window open, and the renderer is what actually
	// ASKS — a slowdown the user is expected to accept explicitly has to be put
	// in front of them.
	s.OnOverrun = func(o monitor.Overrun) {
		sev, detail := events.SevWarning, fmt.Sprintf(
			"%q asks for a reading every %s but a round costs %s, so it now polls every %s "+
				"(%d OIDs across %d target(s))",
			o.Name, msText(o.IntervalMs), msText(o.CycleMs), msText(o.EffectiveMs), o.OIDs, o.Targets)
		switch {
		case o.Recovered:
			sev = events.SevInfo
			detail = fmt.Sprintf("%q is keeping up again and is back to a reading every %s",
				o.Name, msText(o.IntervalMs))
		case o.Accepted:
			detail = fmt.Sprintf(
				"%q asks for a reading every %s but a round costs %s; the slowdown has been "+
					"accepted, so the cadence is unchanged", o.Name, msText(o.IntervalMs), msText(o.CycleMs))
		}
		_ = eng.recordEvent(events.Event{
			Category: events.CategorySystem,
			Kind:     events.KindSystemInfo,
			Severity: sev.String(),
			TitleKey: "events.kind." + events.KindSystemInfo,
			Params:   map[string]any{"detail": detail},
			Summary:  detail,
		}, "")
		eng.emit("monitor:overrun", o)
	}

	eng.scheduler = s
}

// msText renders a millisecond count the way an operator reads a cadence.
func msText(ms int) string {
	return (time.Duration(ms) * time.Millisecond).Round(100 * time.Millisecond).String()
}

// MonitorAcceptSlow is the operator answering the overrun report: keep the
// cadence I chose, I accept that it slows things down.
//
// It applies to the running session only. Nothing persists it, on purpose — a
// decision about how hard to work THIS machine is not one to inherit silently
// after a restart on another one.
func (eng *Engine) MonitorAcceptSlow(sessionID string) {
	if eng.scheduler == nil {
		return
	}
	eng.scheduler.AcceptSlow(sessionID)
}

// buildFetch closes over the connection so the scheduler never sees a
// credential.
func (eng *Engine) buildFetch(sess storage.Session) monitor.FetchFunc {
	conn := sess.Conn
	if conn == nil {
		conn = &storage.SessionConn{}
	}
	creds := eng.loadSessionCreds(sess.ID)

	v3 := snmp.V3Params{
		User:        conn.V3User,
		AuthProto:   conn.V3AuthProto,
		PrivProto:   conn.V3PrivProto,
		SecLevel:    conn.V3SecLevel,
		ContextName: conn.V3ContextName,
		AuthPass:    creds.AuthPass,
		PrivPass:    creds.PrivPass,
	}
	version := sess.SnmpVersion
	community := creds.Community
	port, timeout, retries := conn.Port, conn.TimeoutSec, conn.Retries

	// One connection per target, every OID in one PDU, rather than one
	// connection per (target, OID) walked serially. The old shape cost a full
	// timeout per OID against an unreachable device — ten OIDs at the default
	// five seconds was a fifty-second tick on the clock that runs with the
	// window closed, and the scheduler could only check for a stop between
	// OIDs, so it could not be interrupted.
	return func(ctx context.Context, oids []string, targets []string) []monitor.Reading {
		// A session cannot poll without a client, and reaching through a nil one
		// panics inside newGoSNMP — on the poll goroutine, which used to end the
		// process. Reported as a failed reading per pair instead, which is the
		// shape every other failure on this path takes.
		if eng.snmpClient == nil {
			readings := make([]monitor.Reading, 0, len(targets)*len(oids))
			for _, t := range targets {
				for _, oid := range oids {
					readings = append(readings, monitor.Reading{
						Target: t, OID: oid, Error: "no SNMP client",
					})
				}
			}
			return readings
		}
		// The poll's context reaches the wire. Chunking is serial within a
		// target, so without it a stop waited for every remaining chunk's
		// timeout — measured at 12.0 s for 90 OIDs against a silent device.
		results := eng.snmpClient.GetMany(ctx, targets, oids, community, version, port, timeout, retries, v3)
		readings := make([]monitor.Reading, 0, len(results)*len(oids))
		for _, m := range results {
			for _, oid := range oids {
				readings = append(readings, toReading(m.Target, oid, m.Results[oid], m.Errors[oid], m.ResponseTimeMs))
			}
		}
		return readings
	}
}

// nonNumericTypes are SNMP types that cannot be plotted. Such a value is stored
// with its type but no number, so the chart shows a gap rather than a
// meaningless zero.
var nonNumericTypes = []string{"octetstring", "objectidentifier", "ipaddress", "opaque", "nsapaddress", "bitstring"}

// errorSentinels are the "no such thing" answers an agent returns in place of a
// value. Treated as readings they would look like real data.
var errorSentinels = map[string]bool{"noSuchObject": true, "noSuchInstance": true, "endOfMibView": true}

// toReading converts one SNMP answer into a scheduler reading.
//
// Keyed on the pieces rather than on a *BulkResult, because the batched path
// (GetMany) has no BulkResult and the rules below — the error sentinels, and
// which SNMP types are not numbers — must not exist twice. A second copy that
// drifts would show a string OID as a chart point on one path and not the
// other.
func toReading(target, oid string, res *snmp.Result, errMsg string, ms int64) monitor.Reading {
	out := monitor.Reading{
		Target:         target,
		OID:            oid,
		Error:          errMsg,
		ResponseTimeMs: int(ms),
	}
	if res == nil {
		if out.Error == "" {
			// Neither a value nor an error: the agent answered without this
			// varbind. Silence would look like a paused session rather than a
			// device that did not reply.
			out.Error = "no result returned"
		}
		return out
	}
	out.SnmpType = res.Type

	if s, ok := res.Value.(string); ok && errorSentinels[s] {
		out.Error = s
		return out
	}
	lower := strings.ToLower(out.SnmpType)
	for _, t := range nonNumericTypes {
		if strings.Contains(lower, t) {
			return out
		}
	}
	if v, ok := numericValue(res.Value); ok {
		out.Value = &v
	}
	return out
}

// numericValue coerces the interface{} that crosses from gosnmp into a float64.
// formatSnmpValue has already narrowed *big.Int to int64/uint64, so the integer
// cases below are the whole surface.
func numericValue(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// toMonitorThresholds converts the stored bands into the evaluator's shape.
func toMonitorThresholds(in map[string]*storage.Thresholds) map[string]*monitor.Threshold {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*monitor.Threshold, len(in))
	for oid, t := range in {
		if t == nil {
			continue
		}
		out[oid] = &monitor.Threshold{
			Min: t.Min, Max: t.Max,
			ForSeconds:   t.ForSeconds,
			AlertEnabled: t.AlertEnabled,
		}
	}
	return out
}

// specFor turns a stored session into something the scheduler can run.
func (eng *Engine) specFor(sess storage.Session) monitor.SessionSpec {
	oids := []string{}
	for _, o := range strings.Split(sess.OID, ",") {
		if o = strings.TrimSpace(o); o != "" {
			oids = append(oids, o)
		}
	}
	return monitor.SessionSpec{
		ID:         sess.ID,
		Name:       sess.Name,
		OIDs:       oids,
		Targets:    sess.Targets,
		Interval:   time.Duration(sess.IntervalMs) * time.Millisecond,
		Thresholds: toMonitorThresholds(sess.Thresholds),
		Fetch:      eng.buildFetch(sess),
	}
}

// MonitorStart begins polling a stored session in Go.
func (eng *Engine) MonitorStart(sessionID string) error {
	if eng.storage == nil || eng.scheduler == nil {
		return fmt.Errorf("storage not initialized")
	}
	sess, err := eng.findSession(sessionID)
	if err != nil {
		return err
	}
	eng.scheduler.Start(eng.specFor(sess))
	return eng.storage.UpdateSession(sessionID, true, "")
}

// MonitorStop ends the Go-side poll loop for a session.
func (eng *Engine) MonitorStop(sessionID string) error {
	if eng.scheduler == nil {
		return nil
	}
	eng.scheduler.Stop(sessionID)
	if eng.storage == nil {
		return nil
	}
	return eng.storage.UpdateSession(sessionID, false, time.Now().UTC().Format(time.RFC3339))
}

// MonitorRunning lists the sessions the Go scheduler is polling, so a window
// that has just opened can reconcile with what happened without it.
func (eng *Engine) MonitorRunning() []string {
	if eng.scheduler == nil {
		return []string{}
	}
	return eng.scheduler.Running()
}

// MonitorUpdateConnection replaces a session's connection settings, for when a
// community or passphrase changes after the session was created.
//
// The version travels with it because a credential profile carries one: a
// profile moved from v2c to v3 changes how its sessions must speak, not only
// what they say. Empty keeps the session's own.
func (eng *Engine) MonitorUpdateConnection(sessionID, snmpVersion string, conn MonitorConnection) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	switch snmpVersion {
	case "", "v1", "v2c", "v3":
	default:
		return fmt.Errorf("unsupported SNMP version %q", snmpVersion)
	}
	sessConn, creds := conn.split()
	if err := eng.storage.UpdateSessionConn(sessionID, snmpVersion, sessConn); err != nil {
		return err
	}
	eng.saveSessionCreds(sessionID, creds)
	// A running session holds its old closure, so restart it here rather than
	// letting the change appear to have been ignored.
	if eng.scheduler != nil && eng.scheduler.IsRunning(sessionID) {
		eng.scheduler.Stop(sessionID)
		if sess, err := eng.findSession(sessionID); err == nil {
			eng.scheduler.Start(eng.specFor(sess))
		}
	}
	return nil
}

func (eng *Engine) findSession(sessionID string) (storage.Session, error) {
	sessions, err := eng.storage.ListSessions()
	if err != nil {
		return storage.Session{}, err
	}
	for _, s := range sessions {
		if s.ID == sessionID {
			return s, nil
		}
	}
	return storage.Session{}, fmt.Errorf("unknown monitoring session %q", sessionID)
}

// resumeActiveSessions restarts what was running when the app last exited.
//
// This is the other half of service mode: without it a machine that reboots
// overnight comes back with every monitoring silently stopped, and the first
// anyone hears of it is the alert that never arrived.
func (eng *Engine) resumeActiveSessions() {
	if eng.storage == nil || eng.scheduler == nil || !eng.cfg.AutoResumeMonitors {
		return
	}
	sessions, err := eng.storage.ListSessions()
	if err != nil {
		log.Printf("WARNING: could not list sessions to resume: %v", err)
		return
	}
	resumed := 0
	for _, sess := range sessions {
		// A session stored before the connection was persisted cannot be
		// resumed headlessly: there is nothing to authenticate with.
		if !sess.Active || sess.Conn == nil {
			continue
		}
		eng.scheduler.Start(eng.specFor(sess))
		resumed++
	}
	if resumed > 0 {
		log.Printf("resumed %d monitoring session(s)", resumed)
		eng.RecordSystemEvent(events.KindSystemInfo, "info",
			fmt.Sprintf("Resumed %d monitoring session(s) after startup.", resumed))
	}
}

// MonitorCreatePresetSession creates the session a preset is bound as and
// starts it. The bands the preset carries are materialised into the session's
// OWN threshold map — the same column MonitorCreateSession fills and the same
// one the evaluator reads — and the preset is kept as a snapshot, never a
// reference.
//
// A session that cannot start is reported rather than rolled back: it exists
// and is stored, and deleting it would throw away the binding the operator just
// made and leave nothing to retry from.
func (eng *Engine) MonitorCreatePresetSession(name string, oids []string, target string, intervalMs int, snmpVersion string,
	thresholds map[string]*storage.Thresholds, conn MonitorConnection, snapshot *storage.SessionPreset) (storage.Session, error) {
	if eng.storage == nil || eng.scheduler == nil {
		return storage.Session{}, fmt.Errorf("storage not initialized")
	}
	sessConn, creds := conn.split()
	id, err := eng.storage.CreateSession(name, strings.Join(oids, ","), []string{target}, intervalMs, snmpVersion,
		time.Now().UTC().Format(time.RFC3339), thresholds, sessConn, snapshot)
	if err != nil {
		return storage.Session{}, err
	}
	eng.saveSessionCreds(id, creds)
	if err := eng.MonitorStart(id); err != nil {
		log.Printf("preset session %s created but not started: %v", id, err)
	}
	return eng.findSession(id)
}

// SessionSpec is what the poll clock is handed for sess: its OIDs and interval,
// and the bands the evaluator applies.
func (eng *Engine) SessionSpec(sess storage.Session) monitor.SessionSpec { return eng.specFor(sess) }
