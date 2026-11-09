package snmp

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/netaddr"

	"github.com/gosnmp/gosnmp"
)

// DefaultPort is the SNMP agent port used when none is given.
const DefaultPort = 161

// normalisePort clamps a port into the range a uint16 can hold.
//
// gosnmp takes a uint16, and Go truncates silently on conversion: a port of
// 70000 becomes 4464 and the request goes somewhere nobody asked for. The
// value arrives from the renderer, so it is not this package's to trust.
func normalisePort(port int, fallback uint16) uint16 {
	if port <= 0 || port > 65535 {
		return fallback
	}
	return uint16(port)
}

// V3Params holds all security parameters for an SNMPv3 connection.
type V3Params struct {
	User        string `json:"User"`
	AuthProto   string `json:"AuthProto"`
	AuthPass    string `json:"AuthPass"`
	PrivProto   string `json:"PrivProto"`
	PrivPass    string `json:"PrivPass"`
	SecLevel    string `json:"SecLevel"`
	ContextName string `json:"ContextName"`
}

// Result represents the outcome of an SNMP operation.
type Result struct {
	Oid   string      `json:"oid"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

// BulkResult wraps the result of an SNMP operation for a single target.
type BulkResult struct {
	Target         string  `json:"target"`
	Result         *Result `json:"result"`
	Error          string  `json:"error,omitempty"`
	ResponseTimeMs int64   `json:"responseTimeMs"`
}

// DebugEntry represents a single SNMP debug log entry.
type DebugEntry struct {
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
}

// Client handles SNMP operations.
type Client struct {
	emit func(name string, data any)

	// trapMu guards trapListener, which is written by StartTrapListener on the
	// caller's goroutine, set to nil by the listener goroutine when Listen
	// returns, and read by StopTrapListener. Nothing guarded the three and they
	// raced reproducibly under -race — and visibly without it, since a Start
	// following a Stop could be refused by a listener that had not cleared
	// itself yet, or clobber one that was still running.
	//
	// trapBound and trapDone belong to the SAME listener as trapListener and are
	// replaced with it. gosnmp's own Listening() cannot be used for this by more
	// than one waiter: it is `make(chan bool, 1)` carrying a VALUE, not a channel
	// that closes, so the first receiver takes it and every other one waits for
	// ever. These two are closed, which is a broadcast.
	trapMu       sync.Mutex
	trapListener *gosnmp.TrapListener
	trapBound    chan struct{} // closed once the socket is bound
	trapDone     chan struct{} // closed once Listen has returned
	// trapPort and trapUsers are what the running listener was started with,
	// under trapMu: UpdateTrapUsers restarts on the same port, and only when
	// the users differ.
	trapPort  int
	trapUsers []V3Params
	// trapEngineID is the snmpEngineID the listener stands for
	// (SetTrapEngineID), under trapMu.
	trapEngineID string
	// trapLife serialises the listener's LIFECYCLE — start, stop, and a change
	// of users, which is a stop and a start back to back; another start or
	// stop landing between the two would be undone by the second. Separate
	// from trapMu because a stop waits for the listen goroutine, and that
	// goroutine takes trapMu to clear the field.
	trapLife     sync.Mutex
	debugEnabled bool
	debugLog     []DebugEntry
	debugMu      sync.Mutex
	// recorder durably journals received traps. Defaults to events.Nop so a
	// trap listener goroutine can always call it without a nil check.
	recorder events.Recorder
}

// NewClient creates a new SNMP client.
func NewClient() *Client {
	return &Client{recorder: events.Nop{}}
}

// SetEmitter says how the client tells a window what is happening: a trap
// arriving, a listener failing, how far a scan has got. Without one — a test, a
// headless run — it does its work and says nothing. Set it before the client is
// used.
func (c *Client) SetEmitter(emit func(name string, data any)) {
	c.emit = emit
}

// SetRecorder wires the event journal. Without it, received traps exist only as
// a runtime event emitted at the webview: with no window listening they vanish
// with no error at all.
func (c *Client) SetRecorder(r events.Recorder) {
	if r == nil {
		r = events.Nop{}
	}
	c.recorder = r
}

// SetDebugMode enables or disables SNMP packet debug logging.
func (c *Client) SetDebugMode(enabled bool) {
	c.debugEnabled = enabled
}

// GetDebugLog returns a copy of the current debug log buffer.
func (c *Client) GetDebugLog() []DebugEntry {
	c.debugMu.Lock()
	defer c.debugMu.Unlock()
	out := make([]DebugEntry, len(c.debugLog))
	copy(out, c.debugLog)
	return out
}

// ClearDebugLog empties the debug log buffer.
func (c *Client) ClearDebugLog() {
	c.debugMu.Lock()
	defer c.debugMu.Unlock()
	c.debugLog = nil
}

// concurrentExecute runs fn for each target in parallel and collects BulkResults.
func concurrentExecute(targets []string, fn func(target string) *BulkResult) []*BulkResult {
	var wg sync.WaitGroup
	resultsChan := make(chan *BulkResult, len(targets))

	for _, target := range targets {
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			resultsChan <- fn(t)
		}(target)
	}

	wg.Wait()
	close(resultsChan)

	results := make([]*BulkResult, 0, len(targets))
	for res := range resultsChan {
		results = append(results, res)
	}
	return results
}

// newGoSNMP creates and configures a GoSNMP instance.
func (c *Client) newGoSNMP(target, community, version string, port, timeoutSec, retries int, v3 V3Params) (*gosnmp.GoSNMP, error) {
	// A target may name its own port ("127.0.0.1:1162"), which then wins over
	// the port field (netaddr.SplitTarget).
	host, port := netaddr.SplitTarget(target, port)
	g := &gosnmp.GoSNMP{
		Target:    host,
		Port:      normalisePort(port, DefaultPort),
		Community: community,
		Timeout:   time.Duration(timeoutSec) * time.Second,
		Retries:   retries,
	}

	switch version {
	case "v1":
		g.Version = gosnmp.Version1
	case "v2c":
		g.Version = gosnmp.Version2c
	case "v3":
		g.Version = gosnmp.Version3
		g.ContextName = v3.ContextName

		usm, secLevel, err := usmFor(v3)
		if err != nil {
			return nil, err
		}
		g.SecurityModel = gosnmp.UserSecurityModel
		g.MsgFlags = secLevel
		g.SecurityParameters = usm
	default:
		return nil, fmt.Errorf("unsupported SNMP version: %s", version)
	}

	// Attach debug logger if enabled
	if c.debugEnabled {
		g.Logger = gosnmp.NewLogger(log.New(&ringLogWriter{
			client:  c,
			secrets: []string{community, v3.AuthPass, v3.PrivPass},
		}, "", 0))
	}

	return g, nil
}

// ringLogWriter adapts ringLogger as io.Writer for log.New
type ringLogWriter struct {
	client *Client
	// secrets are the credential values of the request that created this
	// writer, removed from every line before it is buffered.
	secrets []string
}

func (w *ringLogWriter) Write(p []byte) (n int, err error) {
	msg := scrubSecrets(strings.TrimSpace(string(p)), w.secrets)
	if msg == "" {
		return len(p), nil
	}
	w.client.debugMu.Lock()
	defer w.client.debugMu.Unlock()
	w.client.debugLog = append(w.client.debugLog, DebugEntry{
		Timestamp: time.Now().Format("15:04:05.000"),
		Message:   msg,
	})
	if len(w.client.debugLog) > 500 {
		w.client.debugLog = w.client.debugLog[len(w.client.debugLog)-500:]
	}
	return len(p), nil
}

// --- Security helpers ---

func getSecurityLevel(level string) (gosnmp.SnmpV3MsgFlags, error) {
	switch level {
	case "NoAuthNoPriv":
		return gosnmp.NoAuthNoPriv, nil
	case "AuthNoPriv":
		return gosnmp.AuthNoPriv, nil
	case "AuthPriv":
		return gosnmp.AuthPriv, nil
	default:
		return gosnmp.NoAuthNoPriv, fmt.Errorf("invalid security level: %s", level)
	}
}

func getAuthProtocol(proto string) (gosnmp.SnmpV3AuthProtocol, error) {
	switch strings.ToUpper(proto) {
	case "MD5":
		return gosnmp.MD5, nil
	case "SHA":
		return gosnmp.SHA, nil
	case "SHA224":
		return gosnmp.SHA224, nil
	case "SHA256":
		return gosnmp.SHA256, nil
	case "SHA384":
		return gosnmp.SHA384, nil
	case "SHA512":
		return gosnmp.SHA512, nil
	case "NONE", "":
		return gosnmp.NoAuth, nil
	default:
		return gosnmp.NoAuth, fmt.Errorf("invalid authentication protocol: %s", proto)
	}
}

func getPrivProtocol(proto string) (gosnmp.SnmpV3PrivProtocol, error) {
	switch strings.ToUpper(proto) {
	case "DES":
		return gosnmp.DES, nil
	case "AES", "AES128":
		return gosnmp.AES, nil
	case "AES192":
		return gosnmp.AES192, nil
	case "AES256":
		return gosnmp.AES256, nil
	case "AES192C":
		return gosnmp.AES192C, nil
	case "AES256C":
		return gosnmp.AES256C, nil
	case "NONE", "":
		return gosnmp.NoPriv, nil
	default:
		return gosnmp.NoPriv, fmt.Errorf("invalid privacy protocol: %s", proto)
	}
}

func formatSnmpValue(variable gosnmp.SnmpPDU) interface{} {
	switch variable.Type {
	case gosnmp.OctetString:
		b, ok := variable.Value.([]byte)
		if !ok {
			return fmt.Sprintf("%v", variable.Value)
		}
		return formatOctetString(b)
	case gosnmp.ObjectIdentifier:
		// OID values are strings (e.g. ".1.3.6.1.6.3.1.1.5.3") — return as-is
		return fmt.Sprintf("%v", variable.Value)
	case gosnmp.IPAddress:
		return fmt.Sprintf("%v", variable.Value)
	case gosnmp.NoSuchObject:
		return "noSuchObject"
	case gosnmp.NoSuchInstance:
		return "noSuchInstance"
	case gosnmp.EndOfMibView:
		return "endOfMibView"
	default:
		// Convert *big.Int to primitive types for reliable JSON serialization
		// through the Wails bridge. *big.Int can serialize as an object instead
		// of a number, breaking frontend numeric parsing and chart rendering.
		bi := gosnmp.ToBigInt(variable.Value)
		if bi.IsInt64() {
			return bi.Int64()
		}
		if bi.IsUint64() {
			return bi.Uint64()
		}
		// Fallback for extremely large values
		return bi.String()
	}
}

// formatOctetString renders an OCTET STRING as text when it is printable, or as
// colon-separated uppercase hex otherwise (the convention for MAC/PhysAddress
// and other binary values) — mirroring what snmpwalk does for strings without a
// DISPLAY-HINT. The raw bytes are only available here, so this decision must be
// made on the Go side: once the value crosses the bridge as a mangled string the
// original bytes cannot be recovered.
func formatOctetString(b []byte) string {
	if isPrintableOctet(b) {
		return string(b)
	}
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprintf("%02X", c)
	}
	return strings.Join(parts, ":")
}

// isPrintableOctet reports whether b is valid UTF-8 containing only printable
// runes (common whitespace allowed). Non-printable bytes — as in a MAC address —
// make it false so the value is shown as hex.
func isPrintableOctet(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r == utf8.RuneError {
			return false
		}
		if !unicode.IsPrint(r) && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// redacted is what a credential looks like in the debug log.
const redacted = "[redacted]"

// Fields gosnmp prints in the clear. SnmpPacket.SafeString is not safe: it
// formats "Community:%s" on every SENDING PACKET (marshal.go:159), and
// unmarshalling logs "Parsed community %s" (marshal.go:1010).
var (
	// gosnmp's SafeString prints "…, Community:%s, PDUType:%s, …", so the
	// community is bounded by the NEXT FIELD NAME, not by the next comma. It
	// used to be matched as `Community:[^,]*`, which is right until a community
	// contains a comma — and then everything after it survived into the buffer
	// the debug panel shows. A community is an arbitrary octet string; nothing
	// stops an operator, or a trap sender, from putting a comma in one.
	//
	// Anchored first, loose second: the anchored pattern is exact for the shape
	// gosnmp actually logs, and the loose one still covers any other line that
	// names the field.
	communityInPacket = regexp.MustCompile(`Community:.*?, PDUType:`)
	communityField    = regexp.MustCompile(`Community:[^,
]*`)
	// "Parsed community %s" ends the line, so everything to the end of it is
	// the value. `\S*` stopped at the first space and leaked the rest of a
	// community containing one.
	parsedCommunity = regexp.MustCompile(`Parsed community .*`)
	// A raw packet, rendered by fmt as decimal bytes. gosnmp prints whole
	// datagrams this way — "GET RESPONSE OK: %+v" on a []byte (marshal.go:313),
	// and the same shape at "Last 4 Bytes" and "Enterprise" — and in SNMPv1 and
	// v2c the community IS in those bytes, by protocol design. There is no
	// scrubbing a packet dump: the credential is the payload.
	//
	// Four numbers minimum, so an ordinary "[1 2]" in a message is left alone.
	byteDump = regexp.MustCompile(`\[\d{1,3}( \d{1,3}){3,}\]`)
)

// scrubSecrets removes credential values from one debug line.
//
// At the writer, not at the display. The buffer is read by SnmpGetDebugLog and
// shown in the debug panel, where Anonymous Mode masked IP addresses and
// nothing else — so a screen share or a screenshot with debug enabled put the
// community string on someone else's monitor. Keeping it out of the buffer
// also covers every future reader of that buffer, which masking at one panel
// does not.
//
// Both halves matter. The patterns catch a community this writer was not told
// about — a trap sender's, say. The values catch a credential printed in a
// shape the patterns do not know, which is the failure mode a gosnmp upgrade
// would introduce silently.
func scrubSecrets(msg string, secrets []string) string {
	msg = communityInPacket.ReplaceAllString(msg, "Community:"+redacted+", PDUType:")
	msg = communityField.ReplaceAllString(msg, "Community:"+redacted)
	msg = parsedCommunity.ReplaceAllString(msg, "Parsed community "+redacted)
	msg = byteDump.ReplaceAllStringFunc(msg, redactBytes)

	for _, secret := range secrets {
		// Below three characters a value is as likely to be ordinary text as a
		// credential, and replacing every "a" in the log would destroy it.
		// Those are still covered by the patterns above.
		if len(secret) < 3 {
			continue
		}
		msg = strings.ReplaceAll(msg, secret, redacted)
	}
	return msg
}

// redactBytes replaces a decimal packet dump with its length.
//
// The length is the part worth keeping: "did the response arrive, and how big
// was it" is what the dump is read for, while the bytes themselves carry the
// community verbatim on every v1 and v2c exchange. Redacting only the
// credential inside would need to know it, and the listener does not know a
// trap sender's.
func redactBytes(match string) string {
	n := strings.Count(match, " ") + 1
	return fmt.Sprintf("[%d bytes %s]", n, redacted)
}
