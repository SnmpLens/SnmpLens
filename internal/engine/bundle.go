package engine

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/service"
	"SnmpLens/pkg/storage"
)

// Bundle is what a supervision hands another, beside a copy of its monitoring
// database: the window to the system service when it installs it, and the
// service back to the window when it is removed. The database never holds a
// credential, so the credentials travel here, by the reference they are kept
// under, with the preferences and the identity the supervision runs with.
//
// A bundle holds credentials in the clear. Whoever writes one to a file deletes
// it as soon as it has been read.
type Bundle struct {
	// Config is the background-mode preferences: the trap port, whether the
	// listener starts and the sessions resume.
	Config service.Config `json:"config"`
	// TrapEngineID is the trap listener's snmpEngineID, in hex: what devices
	// sending SNMPv3 INFORMs are configured with.
	TrapEngineID string `json:"trapEngineId,omitempty"`
	// Secrets are the credentials, by reference: each session's, each
	// destination's, and the trap listener's SNMPv3 users.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// bundledRef reports whether a reference is one a bundle may carry: a
// session's, a destination's or the trap users'. A bundle is a file, and what
// it names is written into a store; nothing else of the store is its business.
func bundledRef(ref string) bool {
	return ref == secrets.TrapUsersRef() || shapedLike(ref, secrets.SessionRef) || shapedLike(ref, secrets.SinkRef)
}

// shapedLike reports whether ref is shape's reference for some ID: the shape's
// own text around one path element.
func shapedLike(ref string, shape func(id string) string) bool {
	before, after, _ := strings.Cut(shape("\x00"), "\x00")
	id, ok := strings.CutPrefix(ref, before)
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, after)
	return ok && id != "" && !strings.Contains(id, "/")
}

// ExportBundle is what this engine hands another supervision.
func (eng *Engine) ExportBundle() (Bundle, error) {
	if eng.storage == nil {
		return Bundle{}, errors.New("the monitoring database is not open")
	}
	return exportBundle(eng.storage, eng.secrets, eng.cfg, eng.trapEngineID)
}

// ExportBundleAt is what the supervision kept in dataDir hands another, read
// with no engine running there — the service stopped, being removed.
func ExportBundleAt(dataDir string, store secrets.Store) (Bundle, error) {
	st, err := storage.Init(filepath.Join(dataDir, "monitoring.db"))
	if err != nil {
		return Bundle{}, err
	}
	defer st.Close()
	cfg, _ := service.Load(dataDir)
	var id []byte
	if raw, err := os.ReadFile(filepath.Join(dataDir, trapEngineFile)); err == nil {
		id, _ = hex.DecodeString(strings.TrimSpace(string(raw)))
	}
	return exportBundle(st, store, cfg, id)
}

func exportBundle(st *storage.Storage, store secrets.Store, cfg service.Config, trapEngineID []byte) (Bundle, error) {
	b := Bundle{Config: cfg, Secrets: map[string]string{}}
	if len(trapEngineID) > 0 {
		b.TrapEngineID = hex.EncodeToString(trapEngineID)
	}
	if store == nil {
		return b, nil
	}
	refs := []string{secrets.TrapUsersRef()}
	sessions, err := st.ListSessions()
	if err != nil {
		return Bundle{}, fmt.Errorf("the monitoring sessions: %w", err)
	}
	for _, s := range sessions {
		refs = append(refs, secrets.SessionRef(s.ID))
	}
	sinks, err := st.ListSinks()
	if err != nil {
		return Bundle{}, fmt.Errorf("the notification destinations: %w", err)
	}
	for _, s := range sinks {
		refs = append(refs, secrets.SinkRef(s.ID))
	}
	for _, ref := range refs {
		v, err := store.Get(ref)
		switch {
		case err == nil && v != "":
			b.Secrets[ref] = v
		case err != nil && !errors.Is(err, secrets.ErrNotFound):
			return Bundle{}, fmt.Errorf("reading a credential: %w", err)
		}
	}
	return b, nil
}

// ImportBundle prepares dataDir for a supervision about to open there: b's
// credentials into store, the trap listener's engine ID, and service.json with
// cfg. dbCopy, when given, becomes dataDir's monitoring.db — the history the
// other supervision had — unless dataDir's holds something already: an
// installed service updated, or installed again, keeps its own history, and
// kept reports that. One that holds nothing — the service the installer put
// there, which started on the defaults — takes the copy. dbCopy is removed
// either way.
func ImportBundle(dataDir string, store secrets.Store, b Bundle, cfg service.Config, dbCopy string) (kept bool, err error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return false, err
	}
	if dbCopy != "" {
		defer os.Remove(dbCopy)
		target := filepath.Join(dataDir, "monitoring.db")
		has, err := holdsSomething(target)
		if err != nil {
			return false, fmt.Errorf("the monitoring history: %w", err)
		}
		if has {
			kept = true
		} else {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.Remove(target + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
					return false, fmt.Errorf("the monitoring history: %w", err)
				}
			}
			if err := copyFile(dbCopy, target); err != nil {
				return false, fmt.Errorf("the monitoring history: %w", err)
			}
		}
	}
	if store != nil {
		for ref, v := range b.Secrets {
			if !bundledRef(ref) {
				return kept, fmt.Errorf("the bundle names a credential it may not carry (%q)", ref)
			}
			if err := store.Set(ref, v); err != nil {
				return kept, fmt.Errorf("storing a credential: %w", err)
			}
		}
	}
	if b.TrapEngineID != "" {
		id, err := hex.DecodeString(b.TrapEngineID)
		if err != nil || len(id) < 5 || len(id) > 32 {
			return kept, errors.New("the bundle's trap engine ID is not one")
		}
		if err := os.WriteFile(filepath.Join(dataDir, trapEngineFile), []byte(b.TrapEngineID+"\n"), 0o600); err != nil {
			return kept, err
		}
	}
	if err := service.Save(dataDir, cfg); err != nil {
		return kept, err
	}
	return kept, nil
}

// holdsSomething reports whether the database at path holds anything a
// supervision would lose: a session, a destination, a route, a journalled event.
// No database holds nothing.
func holdsSomething(path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	st, err := storage.Init(path)
	if err != nil {
		return false, err
	}
	defer st.Close()
	sessions, err := st.ListSessions()
	if err != nil || len(sessions) > 0 {
		return len(sessions) > 0, err
	}
	sinks, err := st.ListSinks()
	if err != nil || len(sinks) > 0 {
		return len(sinks) > 0, err
	}
	routes, err := st.ListRoutes()
	if err != nil || len(routes) > 0 {
		return len(routes) > 0, err
	}
	page, err := st.QueryEvents(events.Filter{Limit: 1})
	if err != nil {
		return false, err
	}
	return len(page.Items) > 0, nil
}

// HoldsSomething reports whether the supervision kept in dataDir holds anything
// it would lose: what decides whether a window has something to hand over, and
// whether a service has a history of its own.
func HoldsSomething(dataDir string) (bool, error) {
	return holdsSomething(filepath.Join(dataDir, "monitoring.db"))
}

// SnapshotAt writes a copy of the database kept in dataDir to dst, with no
// engine running there: what a window driving the service hands over of the
// supervision it ran before.
func SnapshotAt(dataDir, dst string) error {
	st, err := storage.Init(filepath.Join(dataDir, "monitoring.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	return st.SnapshotTo(dst)
}

// copyFile copies src to dst through a temporary file, so that a stop half-way
// leaves no half of a database behind.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
