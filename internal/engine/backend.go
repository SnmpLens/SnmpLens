package engine

import (
	"SnmpLens/pkg/events"
	"SnmpLens/pkg/notify"
	"SnmpLens/pkg/snmp"
	"SnmpLens/pkg/storage"
)

// Backend is what a window drives: the sessions and their history, the event
// journal, the notification destinations and routes, and the trap listener —
// every engine method the renderer calls, by the name and signature it calls
// it with.
//
// The application embeds it, so each of these is a method Wails binds without
// a forwarding stub apiece. Anything a window must reach and the renderer must
// not — the engine's database, its secret store, journalling an event of the
// window's own — stays off it: Wails binds every exported method of App,
// promoted ones included.
type Backend interface {
	// Monitoring sessions
	MonitorCreateSession(oid string, targets []string, intervalMs int, snmpVersion string, thresholds map[string]*storage.Thresholds, name string, conn MonitorConnection) (string, error)
	MonitorAcceptSlow(sessionID string)
	MonitorStart(sessionID string) error
	MonitorStop(sessionID string) error
	MonitorRunning() []string
	MonitorUpdateConnection(sessionID, snmpVersion string, conn MonitorConnection) error
	MonitorSaveDataPoints(points []storage.DataPoint)
	MonitorLoadSessions() ([]storage.Session, error)
	MonitorLoadSessionData(sessionID string, limit int) ([]storage.DataPoint, error)
	MonitorLoadHistoricalData(sessionID, from, to string) ([]storage.DataPoint, error)
	MonitorDeleteSession(sessionID string) error
	MonitorGetStats(sessionID string) (storage.SessionStats, error)
	MonitorCleanup(daysToKeep int) (int64, error)
	MonitorLoadBuckets(sessionID, from, to string, bucketSec int) ([]storage.Bucket, error)
	MonitorImportLegacyData(sessions []storage.Session, points map[string][]storage.DataPoint) error
	MonitorUpdateSession(sessionID string, active bool, stoppedAt string) error

	// The operations' history
	SaveHistoryEntry(entry map[string]interface{}) error
	LoadHistory() ([]map[string]interface{}, error)
	DeleteHistoryEntry(id string) error
	DeleteHistoryEntries(ids []string) error
	ClearHistory() error
	CountHistory() (int, error)
	ImportHistoryEntries(entries []map[string]interface{}) error

	// The event journal
	EventsQuery(filter events.Filter) (events.Page, error)
	EventsPayload(id string) (string, error)
	EventsCounts() (events.Counts, error)
	EventsAck(ids []string) error
	EventsAckAll(filter events.Filter) error
	EventsDelete(ids []string) error
	EventsClear() error

	// Notifications
	NotifyListSinks() ([]notify.SinkConfig, error)
	NotifySaveSink(cfg notify.SinkConfig) (notify.SinkConfig, error)
	NotifyClearSinkSecret(sinkID string) error
	NotifyDeleteSink(id string) error
	NotifyTestSink(cfg notify.SinkConfig) error
	NotifyTemplateVariables() []notify.VariableDoc
	NotifyValidateTemplate(tpl notify.MessageTemplate) []notify.TemplateError
	NotifyPreviewTemplate(cfg notify.SinkConfig, kind string) TemplatePreview
	NotifyListRoutes() ([]notify.Route, error)
	NotifySaveRoute(r notify.Route) (notify.Route, error)
	NotifyDeleteRoute(id string) error
	NotifyListDeliveries(state string, limit int) ([]storage.Delivery, error)
	NotifyRetryDelivery(id int64) error
	NotifyDefaultBody(format string) string

	// The trap listener
	StartTrapListener(req snmp.TrapListenerRequest) (snmp.TrapListenerInfo, error)
	UpdateTrapUsers(users []snmp.V3Params) (snmp.TrapListenerInfo, error)
	TrapListenerEngineID() string
	StopTrapListener()
}

// The engine is a Backend.
var _ Backend = (*Engine)(nil)

// Host is a Backend and what the application reaches that the renderer must
// not: the application holds one, and embeds only its Backend.
type Host interface {
	Backend
	// MonitorCreatePresetSession creates and starts the session a preset is
	// bound as, its OIDs and bands materialised and its snapshot kept.
	MonitorCreatePresetSession(name string, oids []string, target string, intervalMs int, snmpVersion string,
		thresholds map[string]*storage.Thresholds, conn MonitorConnection, snapshot *storage.SessionPreset) (storage.Session, error)
	// Journal records an event of the window's own: a refused SET.
	Journal(e events.Event)
	// RecordSystemEvent journals a system event: a MIB that did not load.
	RecordSystemEvent(kind, severity, summary string)
	// TrapListenerRunning reports whether the trap listener is listening.
	TrapListenerRunning() bool
}

// The engine is a Host.
var _ Host = (*Engine)(nil)
