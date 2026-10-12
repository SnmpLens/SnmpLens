package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"SnmpLens/pkg/preset"

	sqlite "modernc.org/sqlite"
)

// Storage manages SQLite persistence for monitoring data.
type Storage struct {
	db           *sql.DB
	mu           sync.Mutex
	batch        []DataPoint
	batchTicker  *time.Ticker
	closeOnce    sync.Once
	flushDone    sync.WaitGroup
	done         chan struct{}
	historySaves int // counter to trim query_history only periodically
	eventWrites  int // counter to trim the event journal only periodically
	outboxWrites int // same, for the delivery outbox
	// sessionsHavePreset records whether the preset column is really there.
	//
	// ensureColumn logs and continues when an ALTER fails, which is right for
	// an optional column — but a SELECT that names it unconditionally then
	// fails with "no such column" and EVERY session disappears from the
	// interface, including the ones still polling. Reading the answer once at
	// startup costs nothing and bounds that to a missing label.
	sessionsHavePreset bool
}

type Session struct {
	ID string `json:"id"`
	// Name is the operator's label for this monitoring ("Trafic WAN Paris").
	// Optional: the UI falls back to the polled OIDs when it is empty.
	Name        string   `json:"name,omitempty"`
	OID         string   `json:"oid"`
	Targets     []string `json:"targets"`
	IntervalMs  int      `json:"intervalMs"`
	SnmpVersion string   `json:"snmpVersion"`
	StartedAt   string   `json:"startedAt"`
	StoppedAt   string   `json:"stoppedAt,omitempty"`
	// Thresholds keyed by OID.
	Thresholds map[string]*Thresholds `json:"thresholds,omitempty"`
	Active     bool                   `json:"active"`
	// Conn is how to reach the targets. It is persisted because the poll clock
	// now lives in Go: a background poll has no renderer to ask for the
	// connection settings, and after a restart there is no renderer at all.
	Conn *SessionConn `json:"conn,omitempty"`
	// Preset is the dashboard description this session was created from, if it
	// was created from one at all. Nil for a session someone built by hand.
	Preset *SessionPreset `json:"preset,omitempty"`
}

// SessionPreset is what a bound preset left behind, and it is a SNAPSHOT.
//
// Not a reference to the file. Editing a preset afterwards changes nothing
// already bound, and deleting it stops nothing — rebinding is how an edit is
// adopted. The alternative reads well right up to the moment somebody fixes a
// typo in a preset and three sessions silently start polling different OIDs,
// with the charts keeping the old labels.
//
// It is also the reason the session row still carries oid and interval_ms in
// its own columns: what to poll is materialised there, so the poll clock never
// reads this blob and a session whose snapshot cannot be decoded still polls.
// This is notify_outbox's rule — self-contained, never joining back to what
// produced it — applied to the one other place a stranger's file reaches
// durable state.
type SessionPreset struct {
	// File is where it came from, for the label. It may no longer exist.
	File string `json:"file"`
	Name string `json:"name,omitempty"`
	// FormatVersion is the version that was READ, so a session bound by an
	// older release is recognisable rather than reinterpreted.
	FormatVersion int    `json:"formatVersion"`
	BoundAt       string `json:"boundAt,omitempty"`
	// Widgets is the layout, exactly as the file declared it.
	Widgets []preset.Widget `json:"widgets"`
}

// SessionConn holds the NON-SECRET SNMP connection parameters of a session.
//
// The community string and the v3 passphrases are deliberately absent: they go
// to pkg/secrets under SessionRef(id). Everything in this struct is written to
// monitoring.db in the clear, so anything added here must be safe to read in a
// backup copy of that file.
type SessionConn struct {
	Port       int `json:"port"`
	TimeoutSec int `json:"timeoutSec"`
	Retries    int `json:"retries"`
	// V3 identity and algorithm choice. The passphrases live in pkg/secrets.
	V3User        string `json:"v3User,omitempty"`
	V3AuthProto   string `json:"v3AuthProtocol,omitempty"`
	V3PrivProto   string `json:"v3PrivProtocol,omitempty"`
	V3SecLevel    string `json:"v3SecurityLevel,omitempty"`
	V3ContextName string `json:"v3ContextName,omitempty"`
	// Profile is the credential profile the connection was built from:
	// "default", a profile id, or empty. An id, never a credential.
	Profile string `json:"profile,omitempty"`
}

// Thresholds is the alert band for ONE monitored OID. Different OIDs on the
// same session rarely share bounds (a link rate and an uptime have nothing in
// common), so thresholds are keyed by OID on the session.
type Thresholds struct {
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// ForSeconds requires the breach to persist this long before alerting.
	// 0 alerts on the first sample outside the band. This is what separates a
	// real incident from a single noisy poll.
	ForSeconds   int  `json:"forSeconds,omitempty"`
	AlertEnabled bool `json:"alertEnabled"`
}

type DataPoint struct {
	SessionID      string   `json:"sessionId"`
	Target         string   `json:"target"`
	Timestamp      string   `json:"timestamp"`
	Value          *float64 `json:"value"`
	Delta          *float64 `json:"delta"`
	Rate           *float64 `json:"rate"`
	ResponseTimeMs int      `json:"responseTimeMs"`
	Error          string   `json:"error,omitempty"`
	// SnmpType is the SNMP syntax of the sampled value (Counter32, Gauge32,
	// TimeTicks…). Kept so the UI can correct counter wraps and pick a unit.
	SnmpType string `json:"snmpType,omitempty"`
	// OID this sample came from. A session may poll several OIDs, each charted
	// as its own small multiple, so points must say which one they belong to.
	OID string `json:"oid,omitempty"`
}

// Bucket is an aggregated slice of data points over a time window, used to
// render long time ranges without shipping every raw sample to the frontend.
type Bucket struct {
	Target     string   `json:"target"`
	OID        string   `json:"oid"`
	Timestamp  string   `json:"timestamp"`
	AvgValue   *float64 `json:"avgValue"`
	MinValue   *float64 `json:"minValue"`
	MaxValue   *float64 `json:"maxValue"`
	AvgRate    *float64 `json:"avgRate"`
	AvgLatency *float64 `json:"avgLatency"`
	Count      int      `json:"count"`
	ErrorCount int      `json:"errorCount"`
}

type SessionStats struct {
	TotalPoints    int      `json:"totalPoints"`
	FirstTimestamp string   `json:"firstTimestamp"`
	LastTimestamp  string   `json:"lastTimestamp"`
	MinValue       *float64 `json:"minValue"`
	MaxValue       *float64 `json:"maxValue"`
	AvgValue       *float64 `json:"avgValue"`
	AvgLatency     *float64 `json:"avgLatency"`
	ErrorCount     int      `json:"errorCount"`
}

func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Init opens or creates the SQLite database and starts the batch flush goroutine.
func Init(dbPath string) (*Storage, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}

	// Pragmas MUST travel in the DSN, not through db.Exec. They are
	// connection-scoped, and *sql.DB is a pool: an Exec configures whichever
	// single connection it happens to grab, leaving every later one with
	// foreign_keys=OFF (so ON DELETE CASCADE silently does nothing) and
	// busy_timeout=0 (so concurrent writes fail with SQLITE_BUSY instead of
	// waiting). The DSN form is applied to every connection the pool opens.
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// WAL serialises writers anyway; a small pool bounds memory and keeps the
	// number of connections that must be configured predictable.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	// Create tables
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id           TEXT PRIMARY KEY,
		name         TEXT,
		oid          TEXT NOT NULL,
		targets      TEXT NOT NULL,
		interval_ms  INTEGER NOT NULL,
		snmp_version TEXT NOT NULL,
		started_at   TEXT NOT NULL,
		stopped_at   TEXT,
		thresholds   TEXT,
		active       INTEGER NOT NULL DEFAULT 1
	);

	CREATE TABLE IF NOT EXISTS data_points (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id       TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		target           TEXT NOT NULL,
		timestamp        TEXT NOT NULL,
		value            REAL,
		delta            REAL,
		rate             REAL,
		response_time_ms INTEGER,
		error            TEXT,
		snmp_type        TEXT,
		oid              TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_dp_session_ts ON data_points(session_id, timestamp);

	CREATE TABLE IF NOT EXISTS query_history (
		id         TEXT PRIMARY KEY,
		timestamp  TEXT NOT NULL,
		operation  TEXT NOT NULL,
		success    INTEGER NOT NULL DEFAULT 0,
		data       TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_qh_timestamp ON query_history(timestamp);

	-- ===================== EVENT JOURNAL =====================
	-- Separate from query_history on purpose: that table records what the
	-- operator ASKED for (renderer-owned, one big results blob per row); this
	-- one records what HAPPENED (Go-owned, routable, must work with no window).
	--
	-- seq is a named INTEGER PRIMARY KEY, i.e. a rowid ALIAS, because SQLite
	-- cannot index the implicit rowid: "CREATE INDEX ... ON events(category, rowid)"
	-- fails outright. The whole schema runs as ONE Exec and a failure leaves
	-- storage nil (app.go only logs a warning), which would silently disable
	-- ALL persistence -- sessions and query history included.
	CREATE TABLE IF NOT EXISTS events (
		seq          INTEGER PRIMARY KEY AUTOINCREMENT,
		id           TEXT    NOT NULL UNIQUE,
		ts           TEXT    NOT NULL,
		category     TEXT    NOT NULL,
		kind         TEXT    NOT NULL,
		severity     INTEGER NOT NULL,
		state        TEXT    NOT NULL DEFAULT 'oneshot',
		source       TEXT,
		oid          TEXT,
		session_id   TEXT,
		session_name TEXT,
		dedup_key    TEXT,
		corr_id      TEXT,
		title_key    TEXT    NOT NULL,
		params       TEXT    NOT NULL DEFAULT '{}',
		summary      TEXT    NOT NULL,
		value        REAL,
		payload_size INTEGER NOT NULL DEFAULT 0,
		acked        INTEGER NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_ev_cat_seq ON events(category, seq);
	CREATE INDEX IF NOT EXISTS idx_ev_ts      ON events(ts);
	CREATE INDEX IF NOT EXISTS idx_ev_unacked ON events(category) WHERE acked = 0;
	-- Payload retention walks back from the newest event that HAS one. Without
	-- this, a journal of sixty thousand payload-less traps is scanned end to
	-- end on every trim to discover there is nothing to cap.
	CREATE INDEX IF NOT EXISTS idx_ev_payload ON events(seq) WHERE payload_size > 0;

	-- Bulk detail kept out of the spine so listing the journal never reads a
	-- 1500-varbind trap. No FOREIGN KEY: reaping is explicit, because we refuse
	-- to bet deletion on a connection-scoped pragma.
	CREATE TABLE IF NOT EXISTS event_payloads (
		event_id TEXT PRIMARY KEY,
		body     TEXT NOT NULL
	);

	-- Restart-safe dwell state for "out of band for N seconds". Held in memory
	-- today, so an in-progress episode is lost on reload and re-fires as new.
	-- ===================== NOTIFICATION ROUTING =====================
	CREATE TABLE IF NOT EXISTS notify_sinks (
		id      TEXT PRIMARY KEY,
		name    TEXT NOT NULL,
		kind    TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		config  TEXT NOT NULL DEFAULT '{}'
	);

	CREATE TABLE IF NOT EXISTS notify_routes (
		id       TEXT PRIMARY KEY,
		name     TEXT NOT NULL,
		enabled  INTEGER NOT NULL DEFAULT 1,
		priority INTEGER NOT NULL DEFAULT 100,
		stop     INTEGER NOT NULL DEFAULT 0,
		match    TEXT NOT NULL DEFAULT '{}',
		sink_ids TEXT NOT NULL DEFAULT '[]'
	);

	-- The outbox is SELF-CONTAINED: subject and body are rendered at enqueue
	-- time. The dispatcher therefore never joins back to events, the two tables
	-- trim on independent schedules, and -- the real prize -- event retention
	-- can never blank or strand the dead-letter list, which is the only
	-- feedback channel once the window is closed.
	CREATE TABLE IF NOT EXISTS notify_outbox (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id    TEXT NOT NULL,
		sink_id     TEXT NOT NULL,
		event_json  TEXT NOT NULL,
		subject     TEXT NOT NULL DEFAULT '',
		body        TEXT NOT NULL DEFAULT '',
		attempts    INTEGER NOT NULL DEFAULT 0,
		next_try_at TEXT NOT NULL,
		last_error  TEXT,
		state       TEXT NOT NULL DEFAULT 'pending',  -- pending|sent|dead
		created_at  TEXT NOT NULL,
		UNIQUE(event_id, sink_id)
	);

	CREATE INDEX IF NOT EXISTS idx_ob_due ON notify_outbox(state, next_try_at);

	-- Partial, for the dead-letter ceiling: idx_ob_due leads on state and then
	-- next_try_at, which is the wrong order for "the newest N dead rows". The
	-- same reasoning as the partial index behind the payload cap — the trimmer
	-- runs on a write path and must not scan the table.
	CREATE INDEX IF NOT EXISTS idx_ob_dead ON notify_outbox(id) WHERE state = 'dead';

	-- How far routing has got through the event journal.
	--
	-- One row, enforced by the CHECK. It is written in the SAME transaction as
	-- the outbox rows it accounts for, so it can never name events whose
	-- deliveries were not written — which is the whole guarantee that lets
	-- routing happen off the producer's goroutine.
	CREATE TABLE IF NOT EXISTS notify_watermark (
		id                 INTEGER PRIMARY KEY CHECK (id = 1),
		routed_through_seq INTEGER NOT NULL
	);

	-- Seeded to the newest event that EXISTS, never to zero, and seeded here
	-- rather than by the router's first flush. Lazily created, a crash before
	-- that first flush leaves the row absent, and the "absent means MAX(seq)"
	-- rule then skips exactly the events it was meant to protect. Seeding at
	-- MAX(seq) on an existing database also means upgrading does not re-route
	-- two weeks of history.
	INSERT OR IGNORE INTO notify_watermark (id, routed_through_seq)
		SELECT 1, COALESCE(MAX(seq), 0) FROM events;

	CREATE TABLE IF NOT EXISTS event_episodes (
		dedup_key  TEXT PRIMARY KEY,
		kind       TEXT NOT NULL,
		session_id TEXT,
		target     TEXT NOT NULL,
		oid        TEXT,
		first_seen TEXT NOT NULL,
		fired_at   TEXT,
		corr_id    TEXT
	);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	// Migrate databases created before a column existed. CREATE TABLE IF NOT
	// EXISTS leaves older tables untouched, so add missing columns explicitly.
	ensureColumn(db, "data_points", "snmp_type", "TEXT")
	ensureColumn(db, "data_points", "oid", "TEXT")
	ensureColumn(db, "sessions", "name", "TEXT")
	ensureColumn(db, "sessions", "conn", "TEXT")
	havePreset := ensureColumn(db, "sessions", "preset", "TEXT")

	s := &Storage{
		db:                 db,
		batchTicker:        time.NewTicker(5 * time.Second),
		done:               make(chan struct{}),
		sessionsHavePreset: havePreset,
	}

	// Background batch flush goroutine. Close waits for it.
	s.flushDone.Add(1)
	go func() {
		defer s.flushDone.Done()
		for {
			select {
			case <-s.batchTicker.C:
				s.flushBatch()
			case <-s.done:
				return
			}
		}
	}()

	log.Printf("Monitoring storage initialized: %s", dbPath)
	return s, nil
}

// maxBufferedPoints caps the unwritten sample buffer.
//
// Roughly an hour of a busy session. Past this the database has been unwritable
// long enough that keeping more costs memory without buying anything.
const maxBufferedPoints = 100_000

// Close flushes pending data and closes the database.
//
// Idempotent, and it JOINS the flush goroutine. It used to close a channel
// unconditionally, so a second call panicked with "close of closed channel" —
// and a second call is normal: App.shutdown closes the database, and so does
// anything else holding it. A panic in Close takes the process down at the one
// moment there is nothing left to report it.
//
// The join matters for the same reason the dispatcher's does: without it the
// flush goroutine could still be mid-write when db.Close runs underneath it.
func (s *Storage) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.batchTicker.Stop()
		close(s.done)
		s.flushDone.Wait() // the ticker goroutine has returned
		s.flushBatch()     // final flush, now that nothing else is writing
		err = s.db.Close()
	})
	return err
}

// CreateSession inserts a new monitoring session and returns its UUID.
func (s *Storage) CreateSession(name, oid string, targets []string, intervalMs int, snmpVersion, startedAt string, thresholds map[string]*Thresholds, conn *SessionConn, bound *SessionPreset) (string, error) {
	id := generateUUID()
	targetsJSON, _ := json.Marshal(targets)
	var thresholdsJSON []byte
	if len(thresholds) > 0 {
		thresholdsJSON, _ = json.Marshal(thresholds)
	}
	var connJSON []byte
	if conn != nil {
		connJSON, _ = json.Marshal(conn)
	}
	cols := `(id, name, oid, targets, interval_ms, snmp_version, started_at, thresholds, active, conn)`
	vals := `VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`
	args := []any{id, nullableString([]byte(name)), oid, string(targetsJSON), intervalMs, snmpVersion, startedAt,
		nullableString(thresholdsJSON), nullableString(connJSON)}
	// The snapshot is written only when the column is really there. A session
	// that loses its layout still polls; a session that could not be INSERTed
	// at all does not exist.
	if s.sessionsHavePreset {
		var presetJSON []byte
		if bound != nil {
			presetJSON, _ = json.Marshal(bound)
		}
		cols = `(id, name, oid, targets, interval_ms, snmp_version, started_at, thresholds, active, conn, preset)`
		vals = `VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`
		args = append(args, nullableString(presetJSON))
	}
	_, err := s.db.Exec(`INSERT INTO sessions `+cols+` `+vals, args...)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return id, nil
}

// UpdateSession updates a session's active status and stopped_at timestamp.
func (s *Storage) UpdateSession(id string, active bool, stoppedAt string) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	var stopped interface{}
	if stoppedAt != "" {
		stopped = stoppedAt
	}
	_, err := s.db.Exec(
		`UPDATE sessions SET active = ?, stopped_at = ? WHERE id = ?`,
		activeInt, stopped, id,
	)
	return err
}

// UpdateSessionConn replaces the stored connection of a session, and its SNMP
// version when one is given; an empty version keeps the one it has.
func (s *Storage) UpdateSessionConn(id, snmpVersion string, conn *SessionConn) error {
	var connJSON []byte
	if conn != nil {
		connJSON, _ = json.Marshal(conn)
	}
	_, err := s.db.Exec(`UPDATE sessions SET conn = ?, snmp_version = COALESCE(NULLIF(?, ''), snmp_version) WHERE id = ?`,
		nullableString(connJSON), snmpVersion, id)
	return err
}

// DeleteSession removes a session and all its data points (via CASCADE).
func (s *Storage) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// ListSessions returns all persisted sessions.
func (s *Storage) ListSessions() ([]Session, error) {
	// The preset column is named only when it is really there. ensureColumn
	// logs and continues when an ALTER fails; a SELECT that named it anyway
	// would fail with "no such column" and take EVERY session with it,
	// including the ones still polling.
	presetCol := "NULL"
	if s.sessionsHavePreset {
		presetCol = "preset"
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(name, ''), oid, targets, interval_ms, snmp_version, started_at, stopped_at, thresholds, active, conn, ` +
		presetCol + ` FROM sessions ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		var targetsJSON string
		var stoppedAt sql.NullString
		var thresholdsJSON sql.NullString
		var connJSON sql.NullString
		var presetJSON sql.NullString
		var active int

		if err := rows.Scan(&sess.ID, &sess.Name, &sess.OID, &targetsJSON, &sess.IntervalMs, &sess.SnmpVersion, &sess.StartedAt, &stoppedAt, &thresholdsJSON, &active, &connJSON, &presetJSON); err != nil {
			return nil, err
		}

		json.Unmarshal([]byte(targetsJSON), &sess.Targets)
		if stoppedAt.Valid {
			sess.StoppedAt = stoppedAt.String
		}
		if thresholdsJSON.Valid && thresholdsJSON.String != "" {
			if err := json.Unmarshal([]byte(thresholdsJSON.String), &sess.Thresholds); err != nil {
				// Sessions created before thresholds were per-OID stored a single
				// object; attach it to the session's first OID.
				var legacy Thresholds
				if json.Unmarshal([]byte(thresholdsJSON.String), &legacy) == nil {
					first := strings.TrimSpace(strings.Split(sess.OID, ",")[0])
					if first != "" {
						sess.Thresholds = map[string]*Thresholds{first: &legacy}
					}
				}
			}
		}
		if connJSON.Valid && connJSON.String != "" {
			var c SessionConn
			if json.Unmarshal([]byte(connJSON.String), &c) == nil {
				sess.Conn = &c
			}
		}
		// Decoded defensively, like Conn: a snapshot that cannot be read costs
		// the dashboard its layout and must not cost the session its poll.
		if presetJSON.Valid && presetJSON.String != "" {
			var bound SessionPreset
			if json.Unmarshal([]byte(presetJSON.String), &bound) == nil {
				sess.Preset = &bound
			}
		}
		sess.Active = active == 1
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// QueueDataPoints adds data points to the batch buffer for async insertion.
func (s *Storage) QueueDataPoints(points []DataPoint) {
	if len(points) == 0 {
		return
	}
	s.mu.Lock()
	s.batch = append(s.batch, points...)
	s.mu.Unlock()
}

// flushBatch writes the buffered data points.
//
// A failed write puts the points BACK. They used to be taken out of the buffer
// before the transaction was even begun, so a SQLITE_BUSY — which is ordinary
// on a machine where the trap listener and the router are also writing — lost a
// whole batch of monitoring samples with nothing but a log line. Samples are the
// thing a monitoring session exists to produce; losing them silently makes a
// chart lie.
func (s *Storage) flushBatch() {
	s.mu.Lock()
	if len(s.batch) == 0 {
		s.mu.Unlock()
		return
	}
	points := s.batch
	s.batch = nil
	s.mu.Unlock()

	err := s.writePoints(points)
	if err == nil {
		return
	}

	// Transient: keep them, which is what the put-back is for.
	if !permanentWrite(err) {
		log.Printf("storage: %d data points could not be written (%v); keeping them "+
			"for the next flush", len(points), err)
		s.restoreBatch(points)
		return
	}

	// Permanent: one row has poisoned the batch, and putting it back poisons
	// every batch after it. See writeSalvaging.
	written, dropped, serr := s.writeSalvaging(points)
	if serr != nil {
		log.Printf("storage: %d data points could not be written (%v); keeping them "+
			"for the next flush", len(points), serr)
		s.restoreBatch(points)
		return
	}
	log.Printf("storage: %d data points the database refuses permanently were dropped "+
		"(%v); %d written", dropped, err, written)
}

// SQLITE_CONSTRAINT, the primary result code every constraint failure extends.
// A foreign key violation arrives as 787, whose low byte is this.
const sqliteConstraint = 19

// permanentWrite reports whether a write will fail the same way for ever.
//
// Decided on the CODE, never on the text -- the rule pkg/notify already follows
// for deciding whether a delivery is worth retrying, and for the same reason:
// the message ends with whatever the engine wrote, and matching on it is how a
// retryable failure gets discarded or an unretryable one gets retried for ever.
//
// A constraint failure is the permanent case: the row will be refused on every
// attempt. Everything else -- SQLITE_BUSY above all, which is ordinary on a
// machine where the trap listener and the router are also writing -- is
// transient, and keeping those points is the whole point of the put-back.
func permanentWrite(err error) bool {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return false
	}
	return e.Code()&0xff == sqliteConstraint
}

// writeSalvaging writes a batch one row at a time, dropping only the rows the
// database refuses permanently, and reports how many of each.
//
// It exists because the batch is written as ONE transaction, which is right: a
// partial batch is a chart with a hole in it. But pairing that with a put-back
// on EVERY failure had a hole of its own. A data point whose session has been
// deleted violates `data_points.session_id REFERENCES sessions(id)`, the whole
// transaction is refused, every point goes back into the buffer, and the next
// flush fails on the same row. Nothing is ever persisted again, for ANY
// session, until the process restarts -- while the charts keep drawing from
// memory and look perfectly healthy. Observed in the wild: 234 points, and one
// line in a log nobody reads.
//
// Row by row is slow, and is paid only here, after a batch has already failed.
// A statement refused inside a SQLite transaction does not abort the
// transaction, so the rows around it still commit.
func (s *Storage) writeSalvaging(points []DataPoint) (written, dropped int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO data_points (session_id, target, timestamp, value, delta, rate, response_time_ms, error, snmp_type, oid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	for _, p := range points {
		if _, err := stmt.Exec(p.SessionID, p.Target, p.Timestamp, p.Value, p.Delta,
			p.Rate, p.ResponseTimeMs, nullableString([]byte(p.Error)),
			nullableString([]byte(p.SnmpType)), nullableString([]byte(p.OID))); err != nil {
			// Transient here means the database went busy mid-salvage: give the
			// whole batch back rather than committing a part of it.
			if !permanentWrite(err) {
				return 0, 0, err
			}
			dropped++
			continue
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return written, dropped, nil
}

// restoreBatch puts unwritten points back at the front of the buffer.
//
// Capped, because a database that is permanently unwritable must not grow the
// buffer without bound: at that point the oldest samples are dropped, loudly,
// rather than the process running out of memory.
func (s *Storage) restoreBatch(points []DataPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.batch = append(points, s.batch...)
	if over := len(s.batch) - maxBufferedPoints; over > 0 {
		log.Printf("storage: dropping the %d oldest buffered data points; the "+
			"database has been unwritable for %d samples", over, len(s.batch))
		s.batch = s.batch[over:]
	}
}

// writePoints inserts a batch in one transaction, all or nothing.
//
// An individual row that fails used to be logged and skipped while the
// transaction committed anyway, so a batch could be silently partial.
func (s *Storage) writePoints(points []DataPoint) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO data_points (session_id, target, timestamp, value, delta, rate, response_time_ms, error, snmp_type, oid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range points {
		if _, err := stmt.Exec(p.SessionID, p.Target, p.Timestamp, p.Value, p.Delta,
			p.Rate, p.ResponseTimeMs, nullableString([]byte(p.Error)),
			nullableString([]byte(p.SnmpType)), nullableString([]byte(p.OID))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QueryDataPoints retrieves data points for a session, optionally filtered by time range and limited.
func (s *Storage) QueryDataPoints(sessionID, from, to string, limit int) ([]DataPoint, error) {
	query := `SELECT session_id, target, timestamp, value, delta, rate, response_time_ms, error, snmp_type, oid FROM data_points WHERE session_id = ?`
	args := []interface{}{sessionID}

	if from != "" {
		query += ` AND timestamp >= ?`
		args = append(args, from)
	}
	if to != "" {
		query += ` AND timestamp <= ?`
		args = append(args, to)
	}
	query += ` ORDER BY timestamp ASC`
	if limit > 0 {
		// For "last N points", use a subquery to get the tail
		query = fmt.Sprintf(`SELECT * FROM (%s ORDER BY timestamp DESC LIMIT ?) ORDER BY timestamp ASC`,
			`SELECT session_id, target, timestamp, value, delta, rate, response_time_ms, error, snmp_type, oid FROM data_points WHERE session_id = ?`+
				timeFilter(from, to))
		args = []interface{}{sessionID}
		if from != "" {
			args = append(args, from)
		}
		if to != "" {
			args = append(args, to)
		}
		args = append(args, limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []DataPoint
	for rows.Next() {
		var p DataPoint
		var value, delta, rate sql.NullFloat64
		var errStr, snmpType, oid sql.NullString
		var respTime sql.NullInt64

		if err := rows.Scan(&p.SessionID, &p.Target, &p.Timestamp, &value, &delta, &rate, &respTime, &errStr, &snmpType, &oid); err != nil {
			return nil, err
		}
		if snmpType.Valid {
			p.SnmpType = snmpType.String
		}
		if oid.Valid {
			p.OID = oid.String
		}
		if value.Valid {
			p.Value = &value.Float64
		}
		if delta.Valid {
			p.Delta = &delta.Float64
		}
		if rate.Valid {
			p.Rate = &rate.Float64
		}
		if respTime.Valid {
			p.ResponseTimeMs = int(respTime.Int64)
		}
		if errStr.Valid {
			p.Error = errStr.String
		}
		points = append(points, p)
	}
	return points, nil
}

// GetSessionStats computes aggregate statistics for a session.
func (s *Storage) GetSessionStats(sessionID string) (SessionStats, error) {
	var stats SessionStats
	err := s.db.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(MIN(timestamp), ''),
			COALESCE(MAX(timestamp), ''),
			MIN(value),
			MAX(value),
			AVG(value),
			AVG(response_time_ms),
			SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END)
		FROM data_points WHERE session_id = ?
	`, sessionID).Scan(
		&stats.TotalPoints,
		&stats.FirstTimestamp,
		&stats.LastTimestamp,
		&stats.MinValue,
		&stats.MaxValue,
		&stats.AvgValue,
		&stats.AvgLatency,
		&stats.ErrorCount,
	)
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// Cleanup deletes data points older than the given duration. Returns count deleted.
func (s *Storage) Cleanup(olderThan time.Duration) (int64, []string, error) {
	cutoff := time.Now().Add(-olderThan).UTC().Format(time.RFC3339)
	result, err := s.db.Exec(`DELETE FROM data_points WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, nil, err
	}
	// Also remove sessions with no data points that are inactive.
	//
	// Their ids are RETURNED, because a session's community and v3 passphrases
	// live in pkg/secrets under SessionRef(id) and not in this database — so a
	// caller that deletes the row and nothing else leaves credentials behind
	// under a key nothing will ever look up again. That was the defect in
	// MonitorDeleteSession; deleting them in bulk here is the same defect at a
	// larger scale, and the caller cannot fix it without knowing which ids went.
	removed, err := s.deleteEmptyInactiveSessions()
	if err != nil {
		log.Printf("storage: cleanup could not remove empty sessions: %v", err)
	}
	n, err := result.RowsAffected()
	return n, removed, err
}

// deleteEmptyInactiveSessions removes finished sessions that hold no data, and
// says which ones it removed. Selected before the delete rather than after, in
// one transaction, so the list matches what was actually removed.
func (s *Storage) deleteEmptyInactiveSessions() ([]string, error) {
	const where = `active = 0 AND id NOT IN (SELECT DISTINCT session_id FROM data_points)`

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id FROM sessions WHERE ` + where)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE ` + where); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// ImportLocalStorageData imports legacy data from the frontend's localStorage migration.
func (s *Storage) ImportLocalStorageData(sessions []Session, points map[string][]DataPoint) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	sessStmt, err := tx.Prepare(`INSERT OR IGNORE INTO sessions (id, name, oid, targets, interval_ms, snmp_version, started_at, thresholds, active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer sessStmt.Close()

	dpStmt, err := tx.Prepare(`INSERT INTO data_points (session_id, target, timestamp, value, delta, rate, response_time_ms, error, snmp_type, oid) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer dpStmt.Close()

	for _, sess := range sessions {
		targetsJSON, _ := json.Marshal(sess.Targets)
		var thresholdsJSON interface{}
		if len(sess.Thresholds) > 0 {
			b, _ := json.Marshal(sess.Thresholds)
			thresholdsJSON = string(b)
		}
		active := 0
		if sess.Active {
			active = 1
		}
		sessStmt.Exec(sess.ID, nullableString([]byte(sess.Name)), sess.OID, string(targetsJSON), sess.IntervalMs, sess.SnmpVersion, sess.StartedAt, thresholdsJSON, active)

		if pts, ok := points[sess.ID]; ok {
			for _, p := range pts {
				dpStmt.Exec(p.SessionID, p.Target, p.Timestamp, p.Value, p.Delta, p.Rate, p.ResponseTimeMs, nullableString([]byte(p.Error)), nullableString([]byte(p.SnmpType)), nullableString([]byte(p.OID)))
			}
		}
	}

	return tx.Commit()
}

// --- Query history persistence ---

// maxHistoryEntries caps how many query-history rows are retained. SQLite
// (unlike localStorage) has no small quota, so this is generous.
const maxHistoryEntries = 2000

// historyTrimInterval trims the table only once every N saves, so the common
// per-operation write path stays a single INSERT (the cap is soft: the table
// may briefly exceed maxHistoryEntries by up to this many rows).
const historyTrimInterval = 128

// trimHistory keeps only the newest maxHistoryEntries rows. Ordering by rowid
// (SQLite's implicit insertion-order key, which has an index) reflects true
// insertion order — independent of any missing/backdated timestamp — and lets
// the DELETE use an index scan instead of a full-table sort.
func (s *Storage) trimHistory() {
	s.db.Exec(
		`DELETE FROM query_history WHERE rowid NOT IN (
			SELECT rowid FROM query_history ORDER BY rowid DESC LIMIT ?
		)`,
		maxHistoryEntries,
	)
}

// SaveHistory inserts (or replaces) a single query-history entry. The full
// entry is stored as JSON in the data column; id/timestamp/operation/success
// are also extracted into their own columns for ordering and filtering.
func (s *Storage) SaveHistory(entry map[string]interface{}) error {
	id, _ := entry["id"].(string)
	if id == "" {
		return fmt.Errorf("history entry missing id")
	}
	ts, _ := entry["timestamp"].(string)
	op, _ := entry["operation"].(string)
	success := 0
	if b, ok := entry["success"].(bool); ok && b {
		success = 1
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal history entry: %w", err)
	}
	if _, err := s.db.Exec(
		`INSERT OR REPLACE INTO query_history (id, timestamp, operation, success, data) VALUES (?, ?, ?, ?, ?)`,
		id, ts, op, success, string(data),
	); err != nil {
		return fmt.Errorf("save history: %w", err)
	}
	// Trim only periodically to keep the hot write path a single INSERT.
	s.mu.Lock()
	s.historySaves++
	shouldTrim := s.historySaves%historyTrimInterval == 0
	s.mu.Unlock()
	if shouldTrim {
		s.trimHistory()
	}
	return nil
}

// LoadHistory returns all persisted history entries, newest first, decoded
// from their JSON blobs.
func (s *Storage) LoadHistory() ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`SELECT data FROM query_history ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []map[string]interface{}{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue // skip corrupt rows rather than failing the whole load
		}
		entries = append(entries, m)
	}
	return entries, rows.Err()
}

// DeleteHistoryEntry removes a single history entry by id.
func (s *Storage) DeleteHistoryEntry(id string) error {
	_, err := s.db.Exec(`DELETE FROM query_history WHERE id = ?`, id)
	return err
}

// DeleteHistoryEntries removes several history entries in one transaction.
func (s *Storage) DeleteHistoryEntries(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`DELETE FROM query_history WHERE id = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		stmt.Exec(id)
	}
	return tx.Commit()
}

// ClearHistory removes every history entry.
func (s *Storage) ClearHistory() error {
	_, err := s.db.Exec(`DELETE FROM query_history`)
	return err
}

// CountHistory returns the number of persisted history entries.
func (s *Storage) CountHistory() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM query_history`).Scan(&n)
	return n, err
}

// ImportHistoryEntries bulk-inserts entries; used for the localStorage
// migration and for JSON import from the UI.
func (s *Storage) ImportHistoryEntries(entries []map[string]interface{}) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO query_history (id, timestamp, operation, success, data) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, entry := range entries {
		id, _ := entry["id"].(string)
		if id == "" {
			continue
		}
		ts, _ := entry["timestamp"].(string)
		op, _ := entry["operation"].(string)
		success := 0
		if b, ok := entry["success"].(bool); ok && b {
			success = 1
		}
		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		stmt.Exec(id, ts, op, success, string(data))
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.trimHistory()
	return nil
}

// ensureColumn adds a column to an existing table when it is missing. Errors
// are logged, not fatal: a failure here only means the extra data is unavailable.
// ensureColumn adds a column to an existing table, and reports whether the
// column is there AFTERWARDS — which is not the same as whether it did
// anything: it is already present on a fresh database, and an ALTER can fail.
func ensureColumn(db *sql.DB, table, column, ddl string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		log.Printf("storage: inspect %s: %v", table, err)
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true // already present
		}
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl)); err != nil {
		log.Printf("storage: add %s.%s: %v", table, column, err)
		return false
	}
	return true
}

// QueryBuckets aggregates a session's data points into fixed-width time buckets
// so long ranges render from a bounded number of points instead of every raw
// sample. bucketSec is the window width in seconds; from/to are optional
// RFC3339 bounds. Buckets are returned per target, oldest first.
func (s *Storage) QueryBuckets(sessionID, from, to string, bucketSec int) ([]Bucket, error) {
	if bucketSec <= 0 {
		bucketSec = 60
	}
	// strftime('%s', timestamp) gives epoch seconds; integer-divide to bucket,
	// then multiply back to get the bucket's start time.
	query := `SELECT target, COALESCE(oid, ''),
		       strftime('%Y-%m-%dT%H:%M:%SZ', datetime((CAST(strftime('%s', timestamp) AS INTEGER) / ?) * ?, 'unixepoch')) AS bucket_ts,
		       AVG(value), MIN(value), MAX(value), AVG(rate), AVG(response_time_ms),
		       COUNT(*), SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END)
		FROM data_points WHERE session_id = ?`
	args := []interface{}{bucketSec, bucketSec, sessionID}
	if from != "" {
		query += ` AND timestamp >= ?`
		args = append(args, from)
	}
	if to != "" {
		query += ` AND timestamp <= ?`
		args = append(args, to)
	}
	query += ` GROUP BY target, oid, bucket_ts ORDER BY bucket_ts ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := []Bucket{}
	for rows.Next() {
		var b Bucket
		var avg, min, max, rate, lat sql.NullFloat64
		if err := rows.Scan(&b.Target, &b.OID, &b.Timestamp, &avg, &min, &max, &rate, &lat, &b.Count, &b.ErrorCount); err != nil {
			return nil, err
		}
		if avg.Valid {
			b.AvgValue = &avg.Float64
		}
		if min.Valid {
			b.MinValue = &min.Float64
		}
		if max.Valid {
			b.MaxValue = &max.Float64
		}
		if rate.Valid {
			b.AvgRate = &rate.Float64
		}
		if lat.Valid {
			b.AvgLatency = &lat.Float64
		}
		buckets = append(buckets, b)
	}
	return buckets, rows.Err()
}

func nullableString(b []byte) interface{} {
	if len(b) == 0 || string(b) == "" {
		return nil
	}
	return string(b)
}

func timeFilter(from, to string) string {
	s := ""
	if from != "" {
		s += ` AND timestamp >= ?`
	}
	if to != "" {
		s += ` AND timestamp <= ?`
	}
	return s
}
