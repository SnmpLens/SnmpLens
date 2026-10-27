package snmp

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"SnmpLens/pkg/events"
	"SnmpLens/pkg/netaddr"

	"github.com/gosnmp/gosnmp"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// TrapVariable represents a varbind to include in a sent trap.
type TrapVariable struct {
	Oid   string `json:"oid"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// DefaultTrapPort is the IANA port for SNMP traps.
const DefaultTrapPort = 162

// trapStopGrace bounds how long a stop waits for the listen goroutine to let go
// of the listener it closed. It normally takes microseconds.
const trapStopGrace = 2 * time.Second

// StartTrapListener binds the trap port, accepting every USM user given.
//
// v1 and v2c notifications are received whatever the users are: gosnmp does not
// check a received community, and this listener never did either. A v3
// notification is received when it authenticates as ONE of the users — all of
// them at once, through gosnmp's SnmpV3SecurityParametersTable, which is what
// lets one listener hear devices configured with different USM users. It used
// to take exactly one user, the default identifiers' v3 block, so a device
// sending as anybody else was dropped with nothing on screen to say why.
//
// A user that cannot be used is refused and reported, never kept: the listener
// starts with the others, because receiving nothing over one stale profile is
// worse than receiving from everyone else.
func (c *Client) StartTrapListener(port int, users []V3Params) (TrapListenerInfo, error) {
	c.trapLife.Lock()
	defer c.trapLife.Unlock()
	return c.startTrapListener(port, users)
}

func (c *Client) startTrapListener(port int, users []V3Params) (TrapListenerInfo, error) {
	c.trapMu.Lock()
	defer c.trapMu.Unlock()

	if c.trapListener != nil {
		return TrapListenerInfo{Refused: []TrapUserRefusal{}}, fmt.Errorf("trap listener is already running")
	}

	// Through the scrubbed ring buffer, and only when debug is on.
	//
	// This used to be log.New(os.Stdout, ...) unconditionally: not the ring
	// writer, so scrubSecrets never saw it, and not gated on debug, so every
	// arriving trap printed "Parsed community <whatever the sender used>" to
	// stdout whether or not anyone had asked for a debug log. A trap sender's
	// community is not ours to disclose, and stdout in a packaged app goes
	// somewhere nobody chose. The USM table logs through the same writer.
	var logger gosnmp.Logger
	if c.debugEnabled {
		logger = gosnmp.NewLogger(log.New(&ringLogWriter{client: c}, "", 0))
	}

	params := &gosnmp.GoSNMP{
		Port:    normalisePort(port, DefaultTrapPort),
		Version: gosnmp.Version2c,
		Logger:  logger,
	}
	sec := newTrapSecurity(users, logger)
	if sec.table != nil {
		params.Version = gosnmp.Version3
		params.SecurityModel = gosnmp.UserSecurityModel
		params.MsgFlags = sec.flags
		// The engine a message is compared with; see SetTrapEngineID.
		sec.first.AuthoritativeEngineID = c.trapEngineID
		// Both, never the table alone: see trapSecurity.first.
		params.SecurityParameters = sec.first
		params.SetTrapSecurityParametersTable(sec.table)
	}

	listener := gosnmp.NewTrapListener()
	listener.OnNewTrap = c.handleTrap
	listener.Params = params

	// Enlarge the socket buffer as soon as it is bound.
	//
	// This is what decides whether a trap storm is recorded. Measured with a
	// burst of 2000 datagrams: 417 journalled with the system default, 2000
	// with 8 MiB. The loss is silent — the kernel drops the datagram before Go
	// sees it, so there is no error, no journal entry and nothing to count.
	//
	// Listening() delivers once the socket is bound, which is the earliest moment
	// there is a socket to configure. In its own goroutine so a listener that
	// never binds cannot hold up the caller.
	//
	// THIS IS THE ONLY RECEIVE FROM Listening() IN THE PROGRAM, and it has to be:
	// gosnmp makes it `make(chan bool, 1)` and SENDS a value rather than closing
	// it, so a second waiter elsewhere does not learn the socket is up — it
	// blocks until whatever timeout it carries. `bound` is closed instead, and
	// closing is a broadcast, so StopTrapListener can wait on the same fact.
	bound := make(chan struct{})
	done := make(chan struct{})
	c.trapListener = listener
	c.trapBound = bound
	c.trapDone = done

	go func() {
		select {
		case <-listener.Listening():
			close(bound)
			raiseTrapReadBuffer(listener, TrapReadBuffer)
		case <-done:
			// Listen returned without ever binding. There is no socket to
			// enlarge, and `bound` stays open on purpose: a stop must be able
			// to tell "not yet" from "never", and `done` is what says never.
			log.Printf("trap listener: never bound; leaving the default read buffer")
		}
	}()

	go func() {
		defer func() {
			// Clear it only if it is still OURS: a Stop followed by a Start can
			// have installed a new listener before this goroutine unwinds, and
			// setting nil unconditionally would strand that one with nothing
			// able to close it.
			c.trapMu.Lock()
			if c.trapListener == listener {
				c.trapListener = nil
				c.trapBound = nil
				c.trapDone = nil
			}
			c.trapMu.Unlock()
			close(done)
		}()
		log.Printf("Starting trap listener on port %d", port)
		err := listener.Listen(netaddr.ListenAddress(port))
		if err != nil && !strings.Contains(err.Error(), "closed") {
			log.Printf("Error in trap listener: %v", err)
			runtime.EventsEmit(c.ctx, "trapError", fmt.Sprintf("Error in listener: %v", err))
		}
	}()
	c.trapPort = port
	c.trapUsers = sec.info.accepted
	return sec.info, nil
}

// SetTrapEngineID gives the trap listener the snmpEngineID it stands for, from
// its next start.
//
// The receiver of an SNMPv3 INFORM is the authoritative engine for it (RFC 3414
// 1.5.1), and gosnmp's listener never answers the discovery a sender would learn
// the ID from: a message naming no engine names no user either, and the table
// of users drops it before anything could report. So a sender has to be TOLD
// the ID, and one that changed at every start is one nobody could configure —
// which is why the application keeps this one and shows it.
//
// gosnmp does not hold a sender to it. An INFORM localised to any other valid
// engine ID is read all the same, its listener taking RFC 3414 3.2.3a's
// "continue processing" literally; a sender configured before this existed
// keeps working.
func (c *Client) SetTrapEngineID(id []byte) {
	c.trapMu.Lock()
	defer c.trapMu.Unlock()
	c.trapEngineID = string(id)
}

// StopTrapListener stops the active trap listener.
func (c *Client) StopTrapListener() {
	c.trapLife.Lock()
	defer c.trapLife.Unlock()
	c.stopTrapListener()
}

func (c *Client) stopTrapListener() {
	c.trapMu.Lock()
	listener := c.trapListener
	bound := c.trapBound
	done := c.trapDone
	c.trapMu.Unlock()

	if listener == nil {
		log.Println("Trap listener is not running, cannot stop.")
		return
	}
	// Wait for the socket to exist before closing it.
	//
	// gosnmp's Close returns EARLY and does nothing when `conn` is still nil —
	// and it has already set `finish`, so it will never do anything on a second
	// call either. A stop landing in the window between StartTrapListener
	// returning and the socket being bound therefore reported success and left
	// a listener running with nothing able to stop it. Measured: still running
	// 2.1 s after Close returned in 73 ms.
	//
	// THIS WAS A TIMEOUT AND THAT WAS THE BUG. It waited on Listening()
	// directly, which delivers a single VALUE that the read-buffer goroutine
	// had normally already taken, so the wait here always ran to its two
	// seconds and then closed anyway — with no happens-before edge to
	// listenUDP's unlocked write of `conn`. `go test -race` caught it on CI
	// (twice, in TestStartingTwiceIsRefused and TestStartStopStart) and it is
	// not merely a detector artefact: with no edge, Close can read the nil it
	// returns early on, having already burned `finish`, which is exactly the
	// failure the paragraph above says was fixed.
	//
	// Waiting on the two facts instead needs no timeout, because Listen does
	// exactly one of them: it binds, or it returns.
	select {
	case <-bound:
	case <-done:
		// Never bound. There is no socket, and calling Close would only spend
		// gosnmp's one-shot `finish` on nothing.
		log.Println("Trap listener never bound; nothing to stop.")
		return
	}

	// Close OUTSIDE the lock: it waits for the listen goroutine to unwind, and
	// that goroutine takes the same lock to clear the field.
	log.Println("Stopping trap listener...")
	listener.Close()

	// And wait for the listen goroutine to let go of it. Close returns once
	// gosnmp's loop has ended, but the field is cleared by OUR goroutine after
	// Listen returns, so a start straight after a stop could still be refused
	// as "already running". Harmless for a person pressing a button; not for
	// UpdateTrapUsers, which is a stop and a start back to back.
	select {
	case <-done:
	case <-time.After(trapStopGrace):
		log.Println("trap listener: still unwinding after close")
	}
}

// UpdateTrapUsers changes which USM users a running listener accepts.
//
// gosnmp's table can be ADDED to while the listener reads it and never removed
// from, so changing it in place would leave a deleted user authenticating until
// the next restart, and a rotated passphrase accepted in its old form as well as
// its new one. The listener is restarted instead, on the port it holds — and
// only when the accepted set actually changed, so renaming a profile or saving
// settings that touch no v3 user costs nothing. The restart closes the port for
// the few milliseconds between the two calls, and a datagram arriving then is
// refused by the kernel: the price of never accepting a credential that was
// taken away, paid only when one was.
//
// With nothing listening it only answers what the users WOULD be, so the caller
// can remember them for the next start.
func (c *Client) UpdateTrapUsers(users []V3Params) (TrapListenerInfo, error) {
	c.trapLife.Lock()
	defer c.trapLife.Unlock()

	next := newTrapSecurity(users, gosnmp.Logger{}).info
	c.trapMu.Lock()
	running := c.trapListener != nil
	port := c.trapPort
	same := sameTrapUsers(c.trapUsers, next.accepted)
	c.trapMu.Unlock()

	if !running || same {
		return next, nil
	}
	c.stopTrapListener()
	info, err := c.startTrapListener(port, users)
	if err != nil {
		return info, fmt.Errorf("the trap listener stopped to change its SNMPv3 users and could not start again: %w", err)
	}
	info.Restarted = true
	return info, nil
}

// TrapListenerRunning reports whether a listener is currently bound.
func (c *Client) TrapListenerRunning() bool {
	c.trapMu.Lock()
	defer c.trapMu.Unlock()
	return c.trapListener != nil
}

// recoverTrapHandler contains a panic raised while handling one datagram.
//
// This is the only place in pkg/snmp where a panic can be reached by an
// unauthenticated remote party, and it was the only background goroutine in
// the application without a guard. gosnmp recovers inside its DECODER
// (marshal.go and v3.go) and nowhere around OnNewTrap, and its receive loop is
// one goroutine, so a panic anywhere below this line unwinds through
// listenUDP and ends the process.
//
// Measured, with a handler that dereferences a nil PDU on the first trap:
// unguarded the process died with "exit status 2" and never saw the second
// trap; guarded it recovered and handled it. The cost of that crash is not one
// datagram — it is every monitoring session, every threshold, the notification
// dispatcher and the outbox drain, from a UDP packet nobody authenticated.
//
// Losing the trap is the right outcome here, and the reason it is acceptable
// is that the panic happened before anything durable was written. What is NOT
// acceptable is losing it silently, so the event says which source produced
// it. DedupKey is per source: a device sending the same malformed varbind ten
// thousand times must not become ten thousand major events.
//
// The journal write gets a guard of its own. A recover that panics is a
// process kill with extra steps, and this one runs when the process is already
// in a state nobody predicted.
func (c *Client) recoverTrapHandler(addr *net.UDPAddr) {
	r := recover()
	if r == nil {
		return
	}
	source := "unknown"
	if addr != nil && addr.IP != nil {
		source = addr.IP.String()
	}
	log.Printf("PANIC while handling a trap from %s: %v\n%s", source, r, debug.Stack())

	defer func() { _ = recover() }()
	_ = c.recorder.Record(events.Event{
		Ts:       time.Now().UTC().Format(time.RFC3339),
		Category: events.CategorySystem,
		Kind:     events.KindSystemListenerError,
		Severity: events.SevMajor.String(),
		State:    events.StateOneshot,
		Source:   source,
		DedupKey: "trap.panic|" + source,
		TitleKey: "events.kind." + events.KindSystemListenerError,
		Summary:  fmt.Sprintf("Dropped a trap from %s: the handler panicked (%v)", source, r),
		Params:   map[string]any{"source": source},
	}, "")
}

// declineToAcknowledge stops gosnmp sending an INFORM's acknowledgement.
//
// The whole reason the journal insert is SYNCHRONOUS is that gosnmp sends that
// acknowledgement after this handler returns, so acknowledging a confirmed
// notification before it is durably journalled would be a lie. When the insert
// FAILED, the acknowledgement was sent anyway — the same lie, told at the one
// moment it matters, and the sender then has no reason to retry.
//
// gosnmp decides by reading trap.PDUType after OnNewTrap returns, and
// deliberately passes the packet rather than a copy ("we don't pass a copy
// because the SnmpPacket type is somewhat large"), asking handlers not to
// alter it. This alters it, which is acceptable on the same terms as the
// socket-buffer reach in trapbuf.go and no others: it is FAIL-SOFT — a gosnmp
// that starts passing a copy makes this a no-op and restores today's
// behaviour, nothing breaks — and it is PINNED BY A TEST that drives a real
// sender against a real listener, so an upgrade that changes it fails CI
// rather than quietly resuming the lie.
//
// Measured before it was written: with the field left alone the sender was
// acknowledged in 1 ms; with it changed the sender timed out after 900 ms and
// would have retried. Retrying is the point — an INFORM exists so the sender
// can know, and a duplicate journal entry after a transient failure costs
// nothing next to a lost alert.
//
// A plain trap has no acknowledgement to withhold, so this only applies to an
// INFORM; there the log line is all there is.
func (c *Client) declineToAcknowledge(packet *gosnmp.SnmpPacket, source string, cause error) {
	if packet == nil || packet.PDUType != gosnmp.InformRequest {
		return
	}
	packet.PDUType = gosnmp.SNMPv2Trap
	log.Printf("Not acknowledging the INFORM from %s: it could not be journalled (%v). "+
		"The sender will retry.", source, cause)
}

func (c *Client) handleTrap(packet *gosnmp.SnmpPacket, addr *net.UDPAddr) {
	defer c.recoverTrapHandler(addr)

	log.Printf("Received trap from %s (Version: %s)", addr.IP.String(), packet.Version)

	vars := make([]Result, 0)

	if packet.Version == gosnmp.Version1 && packet.PDUType == gosnmp.Trap {
		vars = append(vars, Result{Oid: "snmpTrapOID.0", Type: "SNMPv1 Trap", Value: packet.Enterprise})
		vars = append(vars, Result{Oid: "genericTrap", Type: "INTEGER", Value: packet.GenericTrap})
		vars = append(vars, Result{Oid: "specificTrap", Type: "INTEGER", Value: packet.SpecificTrap})
		vars = append(vars, Result{Oid: "timestamp", Type: "TimeTicks", Value: packet.Timestamp})
		vars = append(vars, Result{Oid: "agentAddress", Type: "IPAddress", Value: packet.AgentAddress})
	}

	for _, variable := range packet.Variables {
		vars = append(vars, Result{
			Oid:   variable.Name,
			Type:  variable.Type.String(),
			Value: formatSnmpValue(variable),
		})
	}

	pduType := "Trap"
	if packet.PDUType == gosnmp.InformRequest {
		pduType = "Inform"
	}

	source := addr.IP.String()
	ts := time.Now().UTC().Format(time.RFC3339)

	trapData := map[string]interface{}{
		"source":    source,
		"version":   packet.Version.String(),
		"variables": vars,
		"pduType":   pduType,
		"timestamp": ts,
	}

	// Journal FIRST, emit second. EventsEmit only reaches a live webview: with
	// the window closed — or simply before the frontend has subscribed — every
	// received trap used to disappear with no error and no trace. Persisting
	// first is what makes background trap collection possible at all.
	if err := c.recordTrap(source, ts, pduType, packet, vars); err != nil {
		c.declineToAcknowledge(packet, source, err)
	}

	// Guarded, the same way recordEvent guards its own emit: the runtime refuses
	// a context it did not issue and takes the process with it. A client built
	// without one — a test, or a headless run — must still receive traps.
	if c.ctx != nil {
		runtime.EventsEmit(c.ctx, "newTrap", trapData)
	}
}

// snmpTrapOIDInstance is snmpTrapOID.0, the second varbind RFC 3416 requires in
// every v2c/v3 notification.
const snmpTrapOIDInstance = "1.3.6.1.6.3.1.1.4.1.0"

// trapOIDFrom picks the trap OID out of a notification's varbinds.
//
// This used to look for the TEXT "snmpTrapOID" in each varbind name, and gosnmp
// does no MIB translation — a varbind name is always numeric. Measured:
// Name=".1.3.6.1.6.3.1.1.4.1.0" contains snmpTrapOID = false. So EVERY v2c and
// v3 trap was journalled with an empty OID: an OID-prefix rule could not match
// one, {{trapOid}} rendered nothing, and every trap from a host shared a single
// dedup key regardless of what it reported.
//
// The name form is still accepted, because handleTrap synthesises exactly one
// varbind called "snmpTrapOID.0" for v1, which has no such varbind on the wire.
func trapOIDFrom(vars []Result) string {
	for _, v := range vars {
		name := strings.TrimLeft(v.Oid, ".")
		if name == snmpTrapOIDInstance || strings.HasPrefix(v.Oid, "snmpTrapOID") {
			return fmt.Sprintf("%v", v.Value)
		}
	}
	return ""
}

// recordTrap writes a received trap to the event journal. The full varbind list
// goes to the payload side so listing the journal never reads it.
//
// It REPORTS failure, because the caller has something to do about it: an
// INFORM that was not journalled must not be acknowledged.
func (c *Client) recordTrap(source, ts, pduType string, packet *gosnmp.SnmpPacket, vars []Result) error {
	kind := events.KindTrapReceived
	if pduType == "Inform" {
		kind = events.KindTrapInform
	}

	// A trap's identity — for deduplication, for an OID-prefix route rule and
	// for {{trapOid}} in a message template — is its source plus its trap OID.
	trapOID := trapOIDFrom(vars)

	payload, err := json.Marshal(vars)
	if err != nil {
		payload = []byte("[]")
	}

	summary := fmt.Sprintf("%s from %s (%s, %d varbinds)", pduType, source,
		packet.Version.String(), len(vars))

	ev := events.Event{
		Ts:       ts,
		Category: events.CategoryTrap,
		Kind:     kind,
		Severity: events.SevMinor.String(),
		State:    events.StateOneshot,
		Source:   source,
		OID:      trapOID,
		DedupKey: "trap|" + source + "|" + trapOID,
		TitleKey: "events.kind." + kind,
		Params: map[string]any{
			"source":   source,
			"version":  packet.Version.String(),
			"pduType":  pduType,
			"varbinds": len(vars),
			"trapOid":  trapOID,
		},
		Summary: summary,
	}

	if err := c.recorder.Record(ev, string(payload)); err != nil {
		log.Printf("Failed to journal trap from %s: %v", source, err)
		return err
	}
	return nil
}

// InformResult reports what came back from an INFORM.
//
// A trap is fire-and-forget; an INFORM is acknowledged, and the acknowledgement
// is the entire reason to send one. Reporting only "no error" would throw away
// the one thing that distinguishes it from a trap.
type InformResult struct {
	Acknowledged   bool   `json:"acknowledged"`
	ResponseTimeMs int64  `json:"responseTimeMs"`
	Error          string `json:"error,omitempty"`
}

// SendTrap sends an SNMP trap to a target.
func (c *Client) SendTrap(target string, port int, community, version, trapOid string, variables []TrapVariable) error {
	_, err := c.sendNotification(target, port, community, version, trapOid, variables, false)
	return err
}

// SendInform sends an INFORM and waits for the receiver to acknowledge it.
//
// v1 is refused rather than silently downgraded to a trap: RFC 1157 has no
// InformRequest PDU at all, so there is nothing to send, and a caller who
// asked for a confirmed notification must not be told one was delivered.
//
// The error names v2c and nothing else. It used to offer v3 as well, which
// sendNotification then rejects with "supports v1 and v2c only" — sending a
// caller who followed the advice straight into a contradicting error. The
// sender has never carried V3Params; when it does, this message can grow.
func (c *Client) SendInform(target string, port int, community, version, trapOid string, variables []TrapVariable) InformResult {
	start := time.Now()
	if version == "v1" {
		return InformResult{Error: "SNMPv1 has no INFORM: use v2c, or send a trap"}
	}
	acked, err := c.sendNotification(target, port, community, version, trapOid, variables, true)
	res := InformResult{Acknowledged: acked, ResponseTimeMs: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func (c *Client) sendNotification(target string, port int, community, version, trapOid string, variables []TrapVariable, inform bool) (bool, error) {
	// A destination may name its own port, which then wins over the port field,
	// as it does for every request (netaddr.SplitTarget).
	host, port := netaddr.SplitTarget(target, port)
	g := &gosnmp.GoSNMP{
		Target:    host,
		Port:      normalisePort(port, DefaultTrapPort),
		Community: community,
		Timeout:   5 * time.Second,
	}

	switch version {
	case "v1":
		g.Version = gosnmp.Version1
	case "v2c":
		g.Version = gosnmp.Version2c
	default:
		return false, fmt.Errorf("trap sending supports v1 and v2c only")
	}

	if err := g.Connect(); err != nil {
		return false, fmt.Errorf("connect failed: %v", err)
	}
	defer g.Conn.Close()

	trap := gosnmp.SnmpTrap{
		Variables: []gosnmp.SnmpPDU{},
		IsInform:  inform,
	}

	if inform && g.Version == gosnmp.Version1 {
		return false, fmt.Errorf("SNMPv1 has no INFORM")
	}

	if g.Version == gosnmp.Version2c {
		trap.Variables = append(trap.Variables, gosnmp.SnmpPDU{
			Name:  ".1.3.6.1.6.3.1.1.4.1.0",
			Type:  gosnmp.ObjectIdentifier,
			Value: trapOid,
		})
	} else {
		trap.Enterprise = trapOid
		// agent-addr is a NetworkAddress in RFC1157, which is four octets and
		// has no IPv6 form at all. The wildcard says "look at the source
		// address of the datagram", which is the only honest answer over IPv6.
		trap.AgentAddress = "0.0.0.0"
		trap.GenericTrap = 6
		trap.SpecificTrap = 0
	}

	for _, v := range variables {
		pdu := gosnmp.SnmpPDU{Name: v.Oid}
		switch strings.ToLower(v.Type) {
		case "integer":
			val, _ := strconv.Atoi(v.Value)
			pdu.Type = gosnmp.Integer
			pdu.Value = val
		case "octetstring", "string":
			pdu.Type = gosnmp.OctetString
			pdu.Value = []byte(v.Value)
		case "oid", "objectidentifier":
			pdu.Type = gosnmp.ObjectIdentifier
			pdu.Value = v.Value
		case "timeticks":
			val, _ := strconv.ParseUint(v.Value, 10, 32)
			pdu.Type = gosnmp.TimeTicks
			pdu.Value = uint32(val)
		default:
			pdu.Type = gosnmp.OctetString
			pdu.Value = []byte(v.Value)
		}
		trap.Variables = append(trap.Variables, pdu)
	}

	packet, err := g.SendTrap(trap)
	if err != nil {
		return false, err
	}
	if !inform {
		return false, nil
	}
	// A response arrived. Whether it says yes is a separate question: an
	// unknown trap OID or a refused varbind comes back as an error status, and
	// treating that as delivered is exactly the mistake an INFORM exists to
	// prevent.
	if packet == nil {
		return false, fmt.Errorf("no response to the inform")
	}
	if packet.Error != gosnmp.NoError {
		return false, fmt.Errorf("the receiver refused the inform: %s", packet.Error.String())
	}
	return true, nil
}
