package storage

import (
	"path/filepath"
	"testing"
)

// A snapshot is a database of its own holding what the original held, the
// samples still waiting in the batch included.
func TestASnapshotHoldsWhatTheDatabaseHeld(t *testing.T) {
	dir := t.TempDir()
	s, err := Init(filepath.Join(dir, "monitoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.CreateSession("uplinks", "1.3.6.1.2.1.1.3.0", []string{"10.0.0.1"}, 5000, "v2c",
		"2026-11-10T00:00:00Z", nil, &SessionConn{Port: 161}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v := 42.0
	s.QueueDataPoints([]DataPoint{{SessionID: id, Target: "10.0.0.1", OID: "1.3.6.1.2.1.1.3.0",
		Value: &v, Timestamp: "2026-11-10T00:00:01Z"}})

	copyPath := filepath.Join(dir, "copy.db")
	if err := s.SnapshotTo(copyPath); err != nil {
		t.Fatal(err)
	}
	if err := s.SnapshotTo(copyPath); err == nil {
		t.Error("a snapshot wrote over an existing file")
	}
	c, err := Init(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sessions, err := c.ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].ID != id {
		t.Fatalf("sessions in the copy: %+v, %v", sessions, err)
	}
	points, err := c.QueryDataPoints(id, "", "", 10)
	if err != nil || len(points) != 1 {
		t.Fatalf("samples in the copy: %+v, %v", points, err)
	}
}
