package engine

import (
	"SnmpLens/pkg/events"
	"SnmpLens/pkg/monitor"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/storage"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// --- Monitoring Storage Methods ---

// MonitorSaveDataPoints queues data points for batch insertion.
func (eng *Engine) MonitorSaveDataPoints(points []storage.DataPoint) {
	if eng.storage == nil {
		return
	}
	eng.storage.QueueDataPoints(points)
}

// MonitorLoadSessions returns all persisted monitoring sessions.
func (eng *Engine) MonitorLoadSessions() ([]storage.Session, error) {
	if eng.storage == nil {
		return []storage.Session{}, nil
	}
	return eng.storage.ListSessions()
}

// MonitorLoadSessionData loads recent data points for a session.
func (eng *Engine) MonitorLoadSessionData(sessionID string, limit int) ([]storage.DataPoint, error) {
	if eng.storage == nil {
		return []storage.DataPoint{}, nil
	}
	return eng.storage.QueryDataPoints(sessionID, "", "", limit)
}

// MonitorLoadHistoricalData loads data points for a specific time range.
func (eng *Engine) MonitorLoadHistoricalData(sessionID, from, to string) ([]storage.DataPoint, error) {
	if eng.storage == nil {
		return []storage.DataPoint{}, nil
	}
	return eng.storage.QueryDataPoints(sessionID, from, to, 0)
}

// MonitorDeleteSession removes a session and all its data.
// MonitorDeleteSession removes a session AND the credentials it named.
//
// Deleting the row alone left the session's community and v3 passphrases in
// DPAPI, the Keychain or the file store forever, under a key nothing in the
// app will ever look up again — the same defect NotifyDeleteSink had, in the
// other place that writes to pkg/secrets. Session credentials are deliberately
// kept out of monitoring.db (storage.SessionConn holds only what is safe to
// read in a copied database), which is exactly why deleting the database row
// cannot be the whole of it.
//
// The credential goes first, for the reason NotifyDeleteSink gives: it is the
// part that cannot be retried once the row is gone. If the store refuses — a
// locked keychain — the session stays and the operator can try again after
// unlocking. With no store at all there is nothing that could have been
// written, so the deletion proceeds.
func (eng *Engine) MonitorDeleteSession(sessionID string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if eng.secrets != nil {
		if err := eng.secrets.Delete(secrets.SessionRef(sessionID)); err != nil {
			return fmt.Errorf("the session was kept: its stored credentials could not be removed: %w", err)
		}
	}
	return eng.storage.DeleteSession(sessionID)
}

// MonitorGetStats returns aggregate statistics for a monitoring session.
func (eng *Engine) MonitorGetStats(sessionID string) (storage.SessionStats, error) {
	if eng.storage == nil {
		return storage.SessionStats{}, nil
	}
	return eng.storage.GetSessionStats(sessionID)
}

// MonitorCleanup deletes data older than the specified number of days.
func (eng *Engine) MonitorCleanup(daysToKeep int) (int64, error) {
	if eng.storage == nil {
		return 0, fmt.Errorf("storage not initialized")
	}
	n, removedSessions, err := eng.storage.Cleanup(time.Duration(daysToKeep) * 24 * time.Hour)
	// The rows are gone; their credentials must go with them. Best effort and
	// logged rather than returned: retention has already done the part the
	// operator asked for, and a locked keychain is not a reason to report the
	// cleanup as failed.
	if eng.secrets != nil {
		for _, id := range removedSessions {
			if delErr := eng.secrets.Delete(secrets.SessionRef(id)); delErr != nil {
				log.Printf("monitor: session %s was removed but its credentials could not be: %v", id, delErr)
			}
		}
	}
	return n, err
}

// MonitorLoadBuckets returns a session's data aggregated into fixed-width time
// buckets (bucketSec seconds), so long ranges render from a bounded number of
// points instead of every raw sample.
func (eng *Engine) MonitorLoadBuckets(sessionID, from, to string, bucketSec int) ([]storage.Bucket, error) {
	if eng.storage == nil {
		return []storage.Bucket{}, nil
	}
	return eng.storage.QueryBuckets(sessionID, from, to, bucketSec)
}

// MonitorImportLegacyData imports data from localStorage migration.
func (eng *Engine) MonitorImportLegacyData(sessions []storage.Session, points map[string][]storage.DataPoint) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.ImportLocalStorageData(sessions, points)
}

// MonitorUpdateSession updates a session's active status.
func (eng *Engine) MonitorUpdateSession(sessionID string, active bool, stoppedAt string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.UpdateSession(sessionID, active, stoppedAt)
}

// --- Query History Persistence ---

// SaveHistoryEntry persists a single query-history entry to SQLite.
func (eng *Engine) SaveHistoryEntry(entry map[string]interface{}) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.SaveHistory(entry)
}

// LoadHistory returns all persisted query-history entries, newest first.
func (eng *Engine) LoadHistory() ([]map[string]interface{}, error) {
	if eng.storage == nil {
		return []map[string]interface{}{}, nil
	}
	return eng.storage.LoadHistory()
}

// DeleteHistoryEntry removes a single query-history entry by id.
func (eng *Engine) DeleteHistoryEntry(id string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.DeleteHistoryEntry(id)
}

// DeleteHistoryEntries removes several query-history entries at once.
func (eng *Engine) DeleteHistoryEntries(ids []string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.DeleteHistoryEntries(ids)
}

// ClearHistory removes every query-history entry.
func (eng *Engine) ClearHistory() error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.ClearHistory()
}

// CountHistory returns the number of persisted query-history entries.
func (eng *Engine) CountHistory() (int, error) {
	if eng.storage == nil {
		return 0, nil
	}
	return eng.storage.CountHistory()
}

// ImportHistoryEntries bulk-inserts query-history entries (localStorage
// migration and JSON import from the UI).
func (eng *Engine) ImportHistoryEntries(entries []map[string]interface{}) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.ImportHistoryEntries(entries)
}

// --- Event journal ---

// recordEvent persists an event and tells any open window about it. It is the
// single Recorder implementation handed to every producer.
//
// Persist first, notify second: the runtime event is a UI hint and is dropped
// when no window is listening, whereas the row is the record of what happened.
func (eng *Engine) recordEvent(e events.Event, payload string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	saved, err := eng.storage.InsertEvent(e, payload)
	if err != nil {
		return err
	}

	// Hand the event to the router rather than routing it here.
	//
	// For a trap, "here" is gosnmp's UDP receive loop — strictly serial, so
	// every millisecond costs datagrams. Routing was measured at 1.30 ms per
	// event against 0.97 ms for the insert itself, essentially all of it a
	// second write transaction.
	//
	// The INSERT above stays synchronous: gosnmp acknowledges an INFORM after
	// this handler returns, and acknowledging a confirmed notification before it
	// is durably journalled would be a lie.
	//
	// A full queue falls back to routing INLINE — never dropping. That is
	// exactly what this did before the router existed, so the worst case is the
	// behaviour we already had.
	if eng.router == nil || !eng.router.accept(saved) {
		eng.routeEvent(saved)
	}

	eng.emit("event:new", saved)
	return nil
}

// deadLetterSink names the sink a dead-letter event is ABOUT, or "".
//
// Read from the event rather than passed alongside it, so any future path that
// records one is protected without having to remember to.
func deadLetterSink(e events.Event) string {
	if e.Kind != events.KindSystemSinkDeadLetter {
		return ""
	}
	// "sinkId", not "sink". `sink` carries the sink's NAME, because that is what
	// an operator reads in "Delivery to {sink} given up" — and comparing a name
	// against the ids in a route silently disabled this guard the moment the
	// message was made legible. The event now carries both; the id is the one
	// that means something to the router.
	if id, ok := e.Params["sinkId"].(string); ok && id != "" {
		return id
	}
	// Events journalled before the id was recorded carry only the name, which
	// was also the id back when they were written.
	if id, ok := e.Params["sink"].(string); ok {
		return id
	}
	return ""
}

// deadLetterEvent builds the event announcing that a delivery was given up on.
//
// Extracted so a test can drive the same construction production does. The
// version of this test that built the event by hand passed throughout the
// window in which the guard above was broken, because a hand-written event puts
// whatever the test author expects into Params.
func (eng *Engine) deadLetterEvent(q notify.Queued, err error) events.Event {
	name := eng.sinkName(q.SinkID)
	return events.Event{
		Category: events.CategorySystem,
		Kind:     events.KindSystemSinkDeadLetter,
		Severity: events.SevMajor.String(),
		TitleKey: "events.kind." + events.KindSystemSinkDeadLetter,
		Params: map[string]any{
			// The NAME, because "{sink}" is what the operator reads. A raw UUID
			// names nothing they have ever seen, in the one message telling them
			// an alert never arrived.
			"sink": name,
			// And the ID, because the routing guard needs to compare it against
			// the ids a rule holds.
			"sinkId":   q.SinkID,
			"attempts": q.Attempts + 1,
			"error":    err.Error(),
		},
		Summary: "Delivery to " + name + " given up: " + err.Error(),
	}
}

// routedGroupsFor decides where an event goes and renders it for each
// destination, WITHOUT writing anything.
//
// Separated from the write so the same decision can be made on the producer's
// goroutine or on the router's, and replayed at startup, from one piece of code.
func (eng *Engine) routedGroupsFor(e events.Event) ([]storage.RoutedGroup, error) {
	// A read that FAILS is not the same as a configuration with nothing in it.
	//
	// Returning nil for both made a transient failure indistinguishable from
	// "no rule matched", and the router then recorded the watermark past the
	// event — so it was never delivered and never replayed, which is the one
	// outcome the watermark exists to prevent.
	routes, err := eng.storage.ListRoutes()
	if err != nil {
		return nil, fmt.Errorf("could not read the routing rules: %w", err)
	}
	if len(routes) == 0 {
		return nil, nil
	}
	// Evaluated at the EVENT's own time, in the local zone.
	//
	// Routing then depends only on the event, which is what lets it be replayed
	// after a crash and reach the same answer. The zone conversion is not
	// cosmetic: every producer writes Ts in UTC while quiet hours are wall-clock
	// times an operator typed, so comparing them directly rotates every window by
	// the machine's UTC offset.
	sinkIDs := notify.SelectAt(routes, e, time.Local)

	// A dead letter must never go back to the sink that produced it.
	//
	// The failure becomes an event, the event is routed, and a catch-all rule
	// — the natural thing to write — selects the sink that just failed. That
	// delivery fails, produces another dead letter, and so on: one unreachable
	// collector grows the event journal and the outbox without bound, and each
	// cycle is another six attempts against a machine that is not answering.
	//
	// It still goes to the OTHER sinks, which is the useful case: "the mail
	// relay is down" is exactly what you want to hear over syslog.
	if failing := deadLetterSink(e); failing != "" {
		kept := sinkIDs[:0]
		for _, id := range sinkIDs {
			if id != failing {
				kept = append(kept, id)
			}
		}
		sinkIDs = kept
	}

	if len(sinkIDs) == 0 {
		return nil, nil
	}

	// Redaction is decided per sink, so group by the rendering it needs rather
	// than rendering once for everyone.
	sinks, err := eng.storage.ListSinks()
	if err != nil {
		return nil, fmt.Errorf("could not read the destinations: %w", err)
	}
	configBySink := map[string]notify.SinkConfig{}
	for _, s := range sinks {
		configBySink[s.ID] = s
	}

	// Render once per sink, then group the identical results. Sinks usually
	// share a rendering, and each group is one transaction on the synchronous
	// insert path; grouping by the OUTPUT rather than by a guessed key means a
	// new template variable can never silently merge two sinks that should
	// have differed.
	type group struct {
		event   events.Event
		subject string
		body    string
		sinkIDs []string
	}
	groups := map[string]*group{}

	for _, id := range sinkIDs {
		cfg, known := configBySink[id]

		// A sink that is switched off is not written to, and a route naming a
		// sink that no longer exists queues nothing.
		//
		// Only the dispatcher used to look at Enabled — where a disabled sink
		// cannot be resolved and the delivery is dead-lettered, and a dead
		// letter is a MAJOR system event. So switching the mail sink off for
		// the weekend answered EVERY event with a major alarm saying the mail
		// sink could not be reached, which is the opposite of what the switch
		// means. A missing sink took the same path to the same place, having
		// never had any chance of being delivered.
		if !known || !cfg.Enabled {
			continue
		}

		// Masking happens BEFORE templating, always. A template can name
		// fields the built-in rendering never showed — dedupKey, params — so
		// masking inside the renderer would leave exactly those uncovered.
		ev := e
		if cfg.Redact {
			ev = notify.RedactEvent(e)
		}
		subject, body := eng.renderForSink(ev, cfg)

		key := fmt.Sprintf("%t|%q|%q", cfg.Redact, subject, body)
		g, ok := groups[key]
		if !ok {
			g = &group{event: ev, subject: subject, body: body}
			groups[key] = g
		}
		g.sinkIDs = append(g.sinkIDs, id)
	}

	out := make([]storage.RoutedGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, storage.RoutedGroup{
			Event: g.event, SinkIDs: g.sinkIDs, Subject: g.subject, Body: g.body,
		})
	}
	return out, nil
}

// routeEvent routes an event and queues its deliveries immediately.
//
// The inline path: used when the router's queue is full, and by tests. It is
// what the whole application did before routing moved to a background worker,
// so falling back to it can never be worse than not having one.
func (eng *Engine) routeEvent(e events.Event) {
	groups, err := eng.routedGroupsFor(e)
	if err != nil {
		log.Printf("notify: could not route event %s: %v", e.ID, err)
		return
	}
	for _, g := range groups {
		if err := eng.storage.EnqueueDeliveries(g.Event, g.SinkIDs, g.Subject, g.Body); err != nil {
			log.Printf("notify: could not queue deliveries: %v", err)
		}
	}
	if eng.dispatcher != nil {
		eng.dispatcher.Wake()
	}
}

// renderForSink applies the sink's template, falling back to the built-in
// rendering if anything goes wrong.
//
// The recover is not decoration. This runs on the trap-listener and the
// monitor-scheduler goroutines; a panic in a formatting routine would take
// down the background monitoring that pkg/monitor exists to keep alive. A bug
// here must cost formatting, never the alert.
func (eng *Engine) renderForSink(ev events.Event, cfg notify.SinkConfig) (subject, body string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("notify: template for sink %q panicked, using the default rendering: %v", cfg.Name, r)
			subject, body = notify.Render(ev, false) // ev is already redacted
		}
	}()
	// The escaping follows the format the body is DECLARED as, because the body
	// carries values that arrived from the network unauthenticated: one quote in
	// a trap's OID turns a hand-written JSON payload into a parse error at the
	// receiver, and one "<" ends a paragraph in an HTML mail and takes the rest
	// of the message with it.
	switch {
	case cfg.Kind == notify.SinkWebhook &&
		strings.EqualFold(strings.TrimSpace(cfg.Webhook.PayloadMode), notify.PayloadTemplate):
		return notify.RenderWebhookTemplate(cfg.Webhook, ev, cfg.Name, cfg.Template)
	case cfg.Kind == notify.SinkEmail &&
		strings.EqualFold(strings.TrimSpace(cfg.Email.Format), notify.EmailFormatHTML):
		return notify.RenderHTMLTemplate(ev, cfg.Name, cfg.Template)
	}
	return notify.RenderTemplate(ev, cfg.Name, cfg.Template)
}

// EventsQuery returns one page of the journal, newest first.
func (eng *Engine) EventsQuery(filter events.Filter) (events.Page, error) {
	if eng.storage == nil {
		return events.Page{Items: []events.Event{}}, nil
	}
	return eng.storage.QueryEvents(filter)
}

// EventsPayload returns an event's bulk detail, loaded only when opened.
func (eng *Engine) EventsPayload(id string) (string, error) {
	if eng.storage == nil {
		return "", nil
	}
	return eng.storage.EventPayload(id)
}

// EventsCounts feeds the unacknowledged badge without fetching rows.
func (eng *Engine) EventsCounts() (events.Counts, error) {
	if eng.storage == nil {
		return events.Counts{UnackedBySev: map[string]int{}, UnackedByCatego: map[string]int{}}, nil
	}
	return eng.storage.EventCounts()
}

// EventsAck acknowledges specific events.
func (eng *Engine) EventsAck(ids []string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.AckEvents(ids)
}

// EventsAckAll acknowledges everything matching a filter.
func (eng *Engine) EventsAckAll(filter events.Filter) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.AckAllEvents(filter)
}

// EventsDelete removes events and their payloads.
func (eng *Engine) EventsDelete(ids []string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.DeleteEvents(ids)
}

// EventsClear empties the journal.
func (eng *Engine) EventsClear() error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.ClearEvents()
}

// --- Threshold and reachability evaluation ---

// generateEventCorrID returns the id that ties a resolution event back to the
// opening event of the same incident.
func generateEventCorrID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("corr-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("corr-%x", b)
}

// initEvaluator wires the breach evaluator to the journal and restores any
// episode that was still open when the app last closed. Without the restore, a
// restart would re-announce an incident that had already been reported.
func (eng *Engine) initEvaluator() {
	ev := monitor.NewEvaluator()
	ev.Record = eng.recordEvent
	ev.NewCorrID = func() string { return generateEventCorrID() }
	ev.SaveEpi = func(r monitor.EpisodeRecord) error {
		return eng.storage.SaveEpisode(storage.Episode{
			DedupKey:  r.DedupKey,
			Kind:      r.Kind,
			SessionID: r.SessionID,
			Target:    r.Target,
			OID:       r.OID,
			FirstSeen: r.FirstSeen,
			FiredAt:   r.FiredAt,
			CorrID:    r.CorrID,
		})
	}
	ev.DeleteEpi = eng.storage.DeleteEpisode

	if saved, err := eng.storage.LoadEpisodes(); err == nil {
		records := make([]monitor.EpisodeRecord, 0, len(saved))
		for _, e := range saved {
			records = append(records, monitor.EpisodeRecord{
				DedupKey:  e.DedupKey,
				Kind:      e.Kind,
				SessionID: e.SessionID,
				Target:    e.Target,
				OID:       e.OID,
				FirstSeen: e.FirstSeen,
				FiredAt:   e.FiredAt,
				CorrID:    e.CorrID,
			})
		}
		ev.Restore(records)
	} else {
		log.Printf("WARNING: failed to restore open incidents: %v", err)
	}

	eng.evaluator = ev
}

// --- Notification routing ---

// initDispatcher starts the outbox drain loop.
func (eng *Engine) initDispatcher() {
	resolve := func(sinkID string) (notify.Sink, bool) {
		sinks, err := eng.storage.ListSinks()
		if err != nil {
			return nil, false
		}
		for _, cfg := range sinks {
			if cfg.ID != sinkID {
				continue
			}
			if !cfg.Enabled {
				return nil, false
			}
			return notify.Build(cfg, eng.sinkSecret(cfg.ID))
		}
		return nil, false
	}

	d := notify.NewDispatcher(eng.storage, resolve, 15*time.Second)
	d.OnResult = func(q notify.Queued, err error, dead bool) {
		if err == nil || !dead {
			return
		}
		// A dead letter is the only signal an operator gets that a
		// notification never arrived, so it becomes an event of its own.
		eng.recordEvent(eng.deadLetterEvent(q, err), "")
	}
	d.Start()
	eng.dispatcher = d
}

// sinkName resolves a destination's id to what the operator called it, falling
// back to the id when it has been deleted — which is itself informative.
func (eng *Engine) sinkName(sinkID string) string {
	if eng.storage == nil {
		return sinkID
	}
	sinks, err := eng.storage.ListSinks()
	if err != nil {
		return sinkID
	}
	for _, s := range sinks {
		if s.ID == sinkID && strings.TrimSpace(s.Name) != "" {
			return s.Name
		}
	}
	return sinkID
}

// sinkSecret returns a sink's credential from secure storage. Secrets are kept
// out of the sink config so it can be exported, logged or read from the
// database without leaking one.
func (eng *Engine) sinkSecret(sinkID string) string {
	if eng.secrets == nil || sinkID == "" {
		return ""
	}
	v, err := eng.secrets.Get(secrets.SinkRef(sinkID))
	if err != nil {
		return ""
	}
	return v
}

// storedSink returns the destination currently on file under id.
//
// There is no GetSink, and adding one for this would put a second read path
// against notify_sinks; the list is a handful of rows read on a settings
// screen, not a hot path.
func (eng *Engine) storedSink(id string) (notify.SinkConfig, bool) {
	if eng.storage == nil || id == "" {
		return notify.SinkConfig{}, false
	}
	sinks, err := eng.storage.ListSinks()
	if err != nil {
		return notify.SinkConfig{}, false
	}
	for _, s := range sinks {
		if s.ID == id {
			return s, true
		}
	}
	return notify.SinkConfig{}, false
}

// boundSinkSecret returns the stored credential for cfg.ID, but ONLY if cfg
// still names the destination that credential was stored against.
//
// See notify.Destination for why this exists: resolving by id while taking the
// address from the caller is the one path that turns a write-only credential
// store into a read primitive.
func (eng *Engine) boundSinkSecret(cfg notify.SinkConfig) (string, error) {
	stored, ok := eng.storedSink(cfg.ID)
	if !ok {
		// Nothing on file under that id: a sink being tested before it is
		// saved. There is no stored credential to hand out, and the caller
		// supplying one in cfg.Secret is the normal path for a new sink.
		return "", nil
	}
	if !notify.SameDestination(stored, cfg) {
		return "", fmt.Errorf("this destination differs from the saved one, so the stored credential " +
			"cannot be used with it. Enter the credential to test the new destination")
	}
	return eng.sinkSecret(cfg.ID), nil
}

// errNoStorage is what the notification bindings answer when monitoring.db
// could not be opened.
//
// They used to answer an EMPTY LIST and a nil error, so the settings page
// rendered "no destinations yet" — visually identical to a fresh install. An
// operator whose database failed to open saw their entire notification
// configuration as GONE, and only found out it was still on disk when a save
// answered "storage not initialized". Nothing is a fact; it must not be
// reported as one when it is really "I could not look".
var errNoStorage = errors.New("the monitoring database is not open, so the notification configuration cannot be read")

// NotifyListSinks returns the configured destinations. Credentials are never
// included — only whether one is on file.
func (eng *Engine) NotifyListSinks() ([]notify.SinkConfig, error) {
	if eng.storage == nil {
		return []notify.SinkConfig{}, errNoStorage
	}
	sinks, err := eng.storage.ListSinks()
	if err != nil {
		return sinks, err
	}
	for i := range sinks {
		sinks[i].Secret = ""
		sinks[i].HasSecret = eng.sinkSecret(sinks[i].ID) != ""
	}
	return sinks, nil
}

// NotifySaveSink creates or updates a destination.
//
// The credential is split off and stored separately: SinkConfig.Secret is
// write-only transport, and the config that reaches the database must never
// carry it.
func (eng *Engine) NotifySaveSink(cfg notify.SinkConfig) (notify.SinkConfig, error) {
	// Refuse a broken template here rather than at 03:00. Rendering itself
	// cannot fail — it runs at enqueue, where an error would lose the alert
	// instead of spoiling its formatting — so this is the only gate.
	if errs := notify.ValidateTemplate(cfg.Template); len(errs) > 0 {
		return notify.SinkConfig{}, fmt.Errorf("%s", errs[0].Error())
	}
	// A webhook that sends its template as the payload must produce JSON. Find
	// out here rather than at 03:00, when the only symptom is a receiver
	// rejecting a body nobody can see.
	if err := notify.ValidatePayloadTemplate(cfg); err != nil {
		return notify.SinkConfig{}, err
	}
	// The same reasoning, for syslog over TLS: a certificate whose key does not
	// match, a secret that is not PEM, a certificate cleared while its key stays
	// in the store. All four surface only at send, none is classified permanent,
	// so every routed event retried six times and dead-lettered — starting at
	// the first incident after the save.
	//
	// The key comes from the store when the form did not carry one, because an
	// edit that changes the address must be checked against the key already
	// held rather than against nothing.
	if cfg.Kind == notify.SinkSyslog {
		key := cfg.Secret
		if key == "" && cfg.ID != "" {
			key = eng.sinkSecret(cfg.ID)
		}
		if err := notify.ValidateTLSMaterial(cfg.Syslog, key); err != nil {
			return notify.SinkConfig{}, err
		}
	}
	if eng.storage == nil {
		return cfg, fmt.Errorf("storage not initialized")
	}

	incoming := cfg.Secret
	cfg.Secret = ""

	// Pointing an EXISTING sink at a new destination is the durable version of
	// the same problem NotifyTestSink has: the save path never compared the
	// address it was given with the address the credential was stored against,
	// so a renderer that cannot read a credential could still aim it. Read the
	// previous row BEFORE writing over it.
	previous, hadRow := eng.storedSink(cfg.ID)
	rebound := hadRow && !notify.SameDestination(previous, cfg)

	saved, err := eng.storage.SaveSink(cfg)
	if err != nil {
		return saved, err
	}

	// A credential does not follow its destination. Deleted rather than
	// refused: a collector really does move, and refusing the save would leave
	// the operator editing a form they cannot submit, holding a credential
	// they cannot read back. HasSecret then comes back false, which is what
	// the form shows, and the renderer says so out loud.
	if rebound && incoming == "" && eng.secrets != nil {
		if err := eng.secrets.Delete(secrets.SinkRef(saved.ID)); err != nil {
			log.Printf("NotifySaveSink: could not unbind the credential of %s: %v", saved.ID, err)
		}
	}

	// An empty field means "leave the stored credential alone", not "clear it":
	// the UI never receives the current value, so it cannot echo it back.
	if incoming != "" {
		// A credential with nowhere to go is a FAILURE, not a no-op. The guard
		// used to be `incoming != "" && eng.secrets != nil`, so with no protector
		// — a locked keychain, a config directory that cannot be written — the
		// typed token was silently dropped and the save reported success. The
		// operator sees the destination saved, believes the token is held, and
		// finds out at 03:00 when the webhook posts unauthenticated.
		if eng.secrets == nil {
			return saved, fmt.Errorf("saved the destination, but its credential could not be stored: %s", eng.secretsBackend())
		}
		if err := eng.secrets.Set(secrets.SinkRef(saved.ID), incoming); err != nil {
			return saved, fmt.Errorf("saved the destination but could not store its credential: %w", err)
		}
	}
	saved.Secret = ""
	saved.HasSecret = eng.sinkSecret(saved.ID) != ""
	return saved, nil
}

// NotifyClearSinkSecret removes a destination's stored credential.
func (eng *Engine) NotifyClearSinkSecret(sinkID string) error {
	if eng.secrets == nil {
		return fmt.Errorf("secret storage unavailable")
	}
	return eng.secrets.Delete(secrets.SinkRef(sinkID))
}

// NotifyDeleteSink removes a destination AND the credential it named.
//
// Deleting the row alone left the bearer token or SMTP password in DPAPI, the
// Keychain or the file store forever — a credential the operator believes they
// deleted, outliving the thing that named it, under a key nothing in the app
// will ever look up again.
//
// The credential goes first, because that is the part that cannot be retried
// once the row is gone: if the store refuses — a locked keychain — the
// destination stays and the operator can try again after unlocking. With no
// store at all there is nothing that could have been written, so the deletion
// proceeds.
func (eng *Engine) NotifyDeleteSink(id string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if eng.secrets != nil {
		if err := eng.secrets.Delete(secrets.SinkRef(id)); err != nil {
			return fmt.Errorf("the destination was kept: its stored credential could not be removed: %w", err)
		}
	}
	if err := eng.storage.DeleteSink(id); err != nil {
		return err
	}

	// And unbind it from every rule that named it.
	//
	// Deleting the destination left its id in each route's SinkIDs. The rule
	// then rendered a raw UUID in the list — sinkName() falls back to the id —
	// and every matching event queued a delivery that nothing could ever
	// resolve. A rule pointing at one live destination and one dead id looks
	// like it works and half of it does not.
	//
	// Best effort AFTER the delete: the destination is gone either way, and a
	// rule that still names it is a cosmetic and routing problem, not a reason
	// to refuse the operator's action.
	if err := eng.unbindSinkFromRoutes(id); err != nil {
		log.Printf("notify: the destination was deleted but some rules still name it: %v", err)
	}
	return nil
}

// unbindSinkFromRoutes removes a deleted sink's id from every routing rule.
//
// A rule left with NO destinations is disabled rather than deleted: it carries
// the operator's match conditions, which are the part that took thought, and
// silently removing their rules is worse than leaving one they can re-point.
func (eng *Engine) unbindSinkFromRoutes(sinkID string) error {
	routes, err := eng.storage.ListRoutes()
	if err != nil {
		return err
	}
	for _, r := range routes {
		kept := make([]string, 0, len(r.SinkIDs))
		for _, id := range r.SinkIDs {
			if id != sinkID {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(r.SinkIDs) {
			continue
		}
		r.SinkIDs = kept
		if len(kept) == 0 {
			r.Enabled = false
		}
		if _, err := eng.storage.SaveRoute(r); err != nil {
			return err
		}
	}
	return nil
}

// NotifyTestSink sends a synthetic event so the operator can verify a
// destination without waiting for a real incident.
func (eng *Engine) NotifyTestSink(cfg notify.SinkConfig) error {
	// Resolve the stored credential BEFORE building the sink: Build captures
	// the secret by value, so fetching it afterwards left every test of an
	// already-saved sink authenticating with nothing. The symptom was the
	// confusing one — "Test fails but the alerts themselves arrive".
	//
	// Looking the credential up by sink id is BOUND to the destination stored
	// under that id. Without that, this method is a read primitive over a
	// write-only store: name an existing sink's id, point the URL at your own
	// server, press Test, receive the bearer token or the SMTP password.
	//
	// The comment that used to stand here accepted it, on the grounds that
	// anyone able to drive the renderer "could simply read the sink list
	// instead". The line above in NotifyListSinks — sinks[i].Secret = "" —
	// disproves that: the store is deliberately write-only from the renderer,
	// and this was the one hole in it.
	secret := cfg.Secret
	if secret == "" {
		bound, err := eng.boundSinkSecret(cfg)
		if err != nil {
			return err
		}
		secret = bound
	}
	sink, ok := notify.Build(cfg, secret)
	if !ok {
		return fmt.Errorf("unknown sink kind %q", cfg.Kind)
	}
	sample := events.Event{
		ID:       "test-" + generateEventCorrID(),
		Ts:       time.Now().UTC().Format(time.RFC3339),
		Category: events.CategorySystem,
		Kind:     events.KindSystemListenerStarted,
		Severity: events.SevInfo.String(),
		State:    events.StateOneshot,
		Source:   "SnmpLens",
		TitleKey: "events.kind." + events.KindSystemListenerStarted,
		Summary:  "SnmpLens test notification",
	}
	// Render through the SAME path as a real delivery, with the template as it
	// stands in the form. Otherwise the one button an operator presses to check
	// a template is the one path that never exercises it.
	ev := sample
	if cfg.Redact {
		ev = notify.RedactEvent(sample)
	}
	subject, body := eng.renderForSink(ev, cfg)
	return sink.Send(ev, subject, body)
}

// NotifyTemplateVariables returns the template vocabulary for the editor.
//
// Generated from Go rather than mirrored in JavaScript: a hand-copied list is
// a list that drifts, and the first symptom would be a variable the UI offers
// and the renderer does not know.
func (eng *Engine) NotifyTemplateVariables() []notify.VariableDoc {
	return notify.TemplateVariables()
}

// NotifyValidateTemplate reports every problem with a template at once, so the
// editor can mark them all rather than one per save attempt.
func (eng *Engine) NotifyValidateTemplate(tpl notify.MessageTemplate) []notify.TemplateError {
	errs := notify.ValidateTemplate(tpl)
	if errs == nil {
		return []notify.TemplateError{}
	}
	return errs
}

// TemplatePreview is what the editor shows while a template is being written.
type TemplatePreview struct {
	Subject string                 `json:"subject"`
	Body    string                 `json:"body"`
	Errors  []notify.TemplateError `json:"errors"`
	// Json is set when the sink posts its template as the payload. The editor
	// shows what will actually be sent rather than a plain-text approximation
	// of it, because the two differ exactly where mistakes live: escaping.
	Json bool `json:"json"`
	// JsonValid says whether the rendered body parses. Not "valid template" —
	// a template can look like JSON with its placeholders still in place and
	// stop being JSON the moment one of them expands.
	JsonValid bool `json:"jsonValid"`
	// JsonError is the parse failure, verbatim.
	JsonError string `json:"jsonError,omitempty"`
	// Bytes is the size of the body as it will go on the wire.
	Bytes int `json:"bytes"`
	// ContentType is the header a webhook will actually send. The header and the
	// body have to agree, and an operator who has just changed the body format
	// needs to see that they do.
	ContentType string `json:"contentType,omitempty"`
	// Format says WHAT the body below is, so the editor can label and highlight
	// it: "syslog", "email", "json", "xml", "form" or "text".
	//
	// It exists because the preview used to show a rendered subject and body for
	// every kind, which is only what a webhook sends. A syslog sink sends one
	// RFC5424 line with a priority, a timestamp and structured data in front of
	// the message, and a mail sink sends headers — including the From, the
	// recipients and the Content-Type the operator is configuring three fields
	// above. Neither was visible until something arrived at the far end.
	Format string `json:"format"`
}

// NotifyPreviewTemplate renders a template against a canned event.
//
// A preview is not a nicety here: buildMessage silently substitutes the event
// summary for an empty subject, so a template that renders to nothing looks
// exactly like one that works. The sample carries real Params, which the test
// notification does not, so {{params.*}} can actually be seen.
func (eng *Engine) NotifyPreviewTemplate(cfg notify.SinkConfig, kind string) TemplatePreview {
	out := TemplatePreview{Errors: eng.NotifyValidateTemplate(cfg.Template)}

	ev := notify.SampleEvent(kind)
	if cfg.Redact {
		ev = notify.RedactEvent(ev)
	}

	// The SAME call the dispatcher makes. It used to be a second copy of the
	// mode choice sitting eight hundred lines away from the first, which is how
	// a preview comes to show something the sink would never send — and the
	// escaping is decided here, so a divergence would be invisible until a
	// receiver rejected a real alert.
	out.Subject, out.Body = eng.renderForSink(ev, cfg)

	// Then: what does this transport actually put on the wire?
	switch cfg.Kind {
	case notify.SinkWebhook:
		// The REQUEST, not the text inside it. In envelope mode the rendered
		// text is one field of the object the receiver gets, and the shape is
		// exactly what is being configured.
		out.Format = notify.PreviewBodyFormat(cfg.Webhook)
		out.ContentType = cfg.Webhook.ContentType()
		// Json stays for the editor's monospace treatment, which suits every one
		// of these bodies; the specific check below is what differs.
		out.Json = true
		payload, err := notify.PreviewPayload(cfg.Webhook, ev, out.Subject, out.Body)
		if err != nil {
			// The failure IS the answer here: a body that will not go on the
			// wire is what the operator is trying to see. Show the rendered text
			// with the reason beside it, rather than nothing.
			out.JsonError = err.Error()
			out.Bytes = len(out.Body)
			return out
		}
		out.Body = string(payload)
		out.Bytes = len(payload)
		out.JsonValid = true
		return out

	case notify.SinkSyslog:
		// One line, with everything in front of the message that a collector
		// indexes on: the priority computed from facility and severity, the
		// timestamp, the host and app names, the event kind as MSGID, and the
		// structured data — which is the part the operator can now switch off
		// and until now had no way to see.
		out.Format = "syslog"
		out.Body = notify.FormatRFC5424(cfg.Syslog, ev, out.Body)
		// Subject is KEPT although the line does not carry one. It is still what
		// the template rendered, the editor still marks errors against it, and a
		// syslog sink whose subject template is broken should say so rather than
		// looking fine because the field it is checked against was blanked.
		out.Bytes = len(out.Body)
		return out

	case notify.SinkEmail:
		// The whole message, headers included. The Content-Type is the point:
		// it is what the body format setting changes, and reading it is the only
		// way to be sure the mail will render as markup rather than as markup
		// printed out.
		out.Format = "email"
		out.Body = notify.PreviewMessage(cfg.Email, ev, out.Subject, out.Body)
		out.Bytes = len(out.Body)
		return out
	}

	out.Format = "text"
	out.Bytes = len(out.Body)
	return out
}

// NotifyListRoutes returns the routing rules.
func (eng *Engine) NotifyListRoutes() ([]notify.Route, error) {
	if eng.storage == nil {
		return []notify.Route{}, errNoStorage
	}
	return eng.storage.ListRoutes()
}

// NotifySaveRoute creates or updates a routing rule.
func (eng *Engine) NotifySaveRoute(r notify.Route) (notify.Route, error) {
	if eng.storage == nil {
		return r, fmt.Errorf("storage not initialized")
	}
	// A source pattern that cannot match anything is a mistake, and every
	// failure path in matching ends at "no match" with no error — so a rule
	// meant to cover the whole estate delivered nothing and neither the save
	// nor the rule list said a word. "10.0.0/8" is the typo that does it.
	if errs := notify.ValidateSourcePatterns(r.Match.Sources); len(errs) > 0 {
		return r, fmt.Errorf("%s", errs[0])
	}
	return eng.storage.SaveRoute(r)
}

// NotifyDeleteRoute removes a routing rule.
func (eng *Engine) NotifyDeleteRoute(id string) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	return eng.storage.DeleteRoute(id)
}

// NotifyListDeliveries returns the delivery log. state may be "", "pending",
// "sent" or "dead".
func (eng *Engine) NotifyListDeliveries(state string, limit int) ([]storage.Delivery, error) {
	if eng.storage == nil {
		return []storage.Delivery{}, errNoStorage
	}
	return eng.storage.ListDeliveries(state, limit)
}

// NotifyRetryDelivery puts a dead letter back in the queue.
func (eng *Engine) NotifyRetryDelivery(id int64) error {
	if eng.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if err := eng.storage.RetryDelivery(id); err != nil {
		return err
	}
	if eng.dispatcher != nil {
		eng.dispatcher.Wake()
	}
	return nil
}

// NotifyDefaultBody is the starting point offered when a sink is switched to a
// body format that has one.
//
// Served from Go rather than duplicated in the editor, so the text the operator
// starts from is byte for byte the one the sink falls back to when the template
// is empty. An unknown format returns "", and the editor offers no button.
func (eng *Engine) NotifyDefaultBody(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return notify.DefaultJSONPayload
	case "html":
		return notify.DefaultHTMLPayload
	default:
		return ""
	}
}
