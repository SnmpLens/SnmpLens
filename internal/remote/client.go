package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"SnmpLens/internal/engine"
)

// helloTimeout bounds the wait for the service's first frame. A service that
// accepts and then says nothing is not one this window can use.
const helloTimeout = 5 * time.Second

// Client drives a service's engine. It satisfies Host, and so Backend: the
// generated methods in client_gen.go turn each call into a request and wait for
// its answer.
//
// A method with no error result cannot report a lost connection; it returns its
// zero value, and Done says why soon enough — the window watches it and goes
// back to its own engine.
type Client struct {
	conn    net.Conn
	hello   Hello
	onEvent func(name string, data []json.RawMessage)

	writeMu sync.Mutex

	// events holds what the service broadcast until onEvent takes it.
	events chan frame

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan frame
	err     error
	done    chan struct{}
}

// NewClient takes over conn, reads the service's hello and checks it against
// this build: the version must match exactly, since the two are one
// executable. An empty version accepts any, for tests.
//
// onEvent receives what the service broadcasts, in order, on a goroutine of its
// own, so that it may call the client: the answer is read meanwhile. A handler
// that falls eventQueue events behind loses the connection, as on the
// service's side.
func NewClient(conn net.Conn, version string, onEvent func(name string, data []json.RawMessage)) (*Client, error) {
	dec := json.NewDecoder(conn)
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	var f frame
	if err := dec.Decode(&f); err != nil {
		conn.Close()
		return nil, fmt.Errorf("the service did not introduce itself: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if f.Hello == nil {
		conn.Close()
		return nil, errors.New("the service did not introduce itself")
	}
	h := *f.Hello
	if h.Protocol != ProtocolVersion || (version != "" && h.Version != version) {
		conn.Close()
		return nil, &VersionError{Service: h.Version, Window: version, ServiceProtocol: h.Protocol, WindowProtocol: ProtocolVersion}
	}
	// Two unversioned builds share a name, not their code: a window built today
	// would drive a service built last week, and call methods it does not have.
	if version == devVersion && h.Build != BuildID() {
		conn.Close()
		return nil, &VersionError{
			Service: h.Version + " (" + shortBuild(h.Build) + ")", Window: version + " (" + shortBuild(BuildID()) + ")",
			ServiceProtocol: h.Protocol, WindowProtocol: ProtocolVersion,
		}
	}

	c := &Client{
		conn:    conn,
		hello:   h,
		onEvent: onEvent,
		pending: map[uint64]chan frame{},
		done:    make(chan struct{}),
	}
	if onEvent != nil {
		c.events = make(chan frame, eventQueue)
		go c.eventLoop()
	}
	go c.readLoop(dec)
	return c, nil
}

// Hello is what the service said about itself.
func (c *Client) Hello() Hello { return c.hello }

// Done is closed when the connection is lost or closed.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, once Done is closed.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close disconnects.
func (c *Client) Close() { c.shutdown(ErrClosed) }

func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.done)
	c.mu.Unlock()
	c.conn.Close()
}

func (c *Client) readLoop(dec *json.Decoder) {
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			c.shutdown(fmt.Errorf("%w: %v", ErrClosed, err))
			return
		}
		if f.Event != "" {
			if c.events == nil {
				continue
			}
			select {
			case c.events <- f:
			default:
				c.shutdown(fmt.Errorf("%w: the window fell too far behind the service's events", ErrClosed))
				return
			}
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[f.ID]
		delete(c.pending, f.ID)
		c.mu.Unlock()
		if ok {
			ch <- f
		}
	}
}

// eventLoop hands the service's events to onEvent, in the order they came.
// Those still queued when the connection ends are dropped: the window reloads
// from whichever engine it drives next.
func (c *Client) eventLoop() {
	for {
		select {
		case f := <-c.events:
			c.onEvent(f.Event, f.Data)
		case <-c.done:
			return
		}
	}
}

// call sends a request and decodes the answer into results, which point at the
// method's non-error results in order.
func (c *Client) call(method string, params []any, results ...any) error {
	raw := make([]json.RawMessage, len(params))
	for i, p := range params {
		b, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("%s: parameter %d: %w", method, i+1, err)
		}
		raw[i] = b
	}

	ch := make(chan frame, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	b, err := json.Marshal(frame{ID: id, Method: method, Params: raw})
	if err == nil {
		c.writeMu.Lock()
		_, err = c.conn.Write(append(b, '\n'))
		c.writeMu.Unlock()
	}
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		c.shutdown(fmt.Errorf("%w: %v", ErrClosed, err))
		return ErrClosed
	}

	var f frame
	select {
	case f = <-ch:
	case <-c.done:
		return ErrClosed
	}
	if f.Error != "" {
		return &RemoteError{Message: f.Error}
	}
	for i, r := range results {
		if i >= len(f.Result) {
			break
		}
		if err := json.Unmarshal(f.Result[i], r); err != nil {
			return fmt.Errorf("%s: result %d: %w", method, i+1, err)
		}
	}
	return nil
}

// logLost records a failed call whose method cannot return the error. The lost
// connection itself is reported once, by whoever watches Done.
func (c *Client) logLost(method string, err error) {
	if err != nil && !errors.Is(err, ErrClosed) {
		log.Printf("remote: a call to the service failed: %s: %v", method, err)
	}
}

// The in-process engine answers everything a service does, a Client asks for
// all of it, and a Switch forwards the window's part.
var (
	_ Host = (*engine.Engine)(nil)
	_ Host = (*Client)(nil)
	_ Host = (*Switch)(nil)
)
