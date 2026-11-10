package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
)

// ProtocolVersion is bumped when frames change shape. A window and a service
// speaking different versions refuse each other rather than misread.
const ProtocolVersion = 1

// frame is everything that crosses the wire, one JSON object per line:
//
//	service → window   {"hello":{…}}                       once, first
//	window → service   {"id":7,"method":"EventsQuery","params":[…]}
//	service → window   {"id":7,"result":[…]}  or  {"id":7,"error":"…"}
//	service → window   {"event":"event:new","data":[…]}
//
// Parameters and results are positional, in the order of the method's
// signature; a trailing error result travels as Error, not in Result.
type frame struct {
	Hello  *Hello            `json:"hello,omitempty"`
	ID     uint64            `json:"id,omitempty"`
	Method string            `json:"method,omitempty"`
	Params []json.RawMessage `json:"params,omitempty"`
	Result []json.RawMessage `json:"result,omitempty"`
	Error  string            `json:"error,omitempty"`
	Event  string            `json:"event,omitempty"`
	Data   []json.RawMessage `json:"data,omitempty"`
}

// Hello is what the service says first: who it is, so that the window can
// refuse a service it does not match before sending it anything.
type Hello struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	PID      int    `json:"pid"`
	// Build is the commit the service was built from. A release is named by
	// its version; an unversioned build — "dev" — by this alone.
	Build string `json:"build,omitempty"`
}

// devVersion is what an unversioned build calls itself (updater.Version):
// every one of them, whatever its code. Two match only when they were built
// from the same commit.
const devVersion = "dev"

var (
	buildOnce sync.Once
	buildID   string
)

// BuildID is the commit this executable was built from, marked when the tree
// had changes; "" when the build does not say.
func BuildID() string {
	buildOnce.Do(func() {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		var rev, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if rev == "" {
			return
		}
		buildID = rev
		if modified == "true" {
			buildID += "+changes"
		}
	})
	return buildID
}

// shortBuild names a build for a person: its abbreviated commit.
func shortBuild(b string) string {
	if b == "" {
		return "unknown build"
	}
	rev, changes, _ := strings.Cut(b, "+")
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if changes != "" {
		return rev + "+" + changes
	}
	return rev
}

// ErrClosed is returned by calls on a connection that has gone away.
var ErrClosed = errors.New("the connection to the service was lost")

// VersionError is returned when the window and the service are different
// builds. They are one executable, updated together by the installer; a
// mismatch means one of them was not, and the fix is to run the installer.
type VersionError struct {
	Service, Window string
	ServiceProtocol int
	WindowProtocol  int
}

func (e *VersionError) Error() string {
	if e.ServiceProtocol != e.WindowProtocol {
		return fmt.Sprintf("the service speaks protocol %d and this window %d; install the same version of SnmpLens for both",
			e.ServiceProtocol, e.WindowProtocol)
	}
	return fmt.Sprintf("the service runs SnmpLens %s and this window %s; install the same version for both",
		e.Service, e.Window)
}

// RemoteError is an error the service's engine returned, as opposed to a
// failure to reach it.
type RemoteError struct{ Message string }

func (e *RemoteError) Error() string { return e.Message }
