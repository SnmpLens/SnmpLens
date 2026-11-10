package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"sync"
	"time"
)

// eventQueue is how many frames a window may fall behind by before it is
// disconnected. Samples arrive once a poll round and a journalled event when
// it happens, so this is a window that stopped reading: a frozen one, which
// reconnects and reloads when it recovers. Waiting for it instead would hold
// up the engine that emits.
const eventQueue = 512

// errorType is the type of a method's trailing error result.
var errorType = reflect.TypeOf((*error)(nil)).Elem()

// Server answers the methods of a Host to the windows connected to it, and
// broadcasts what the host's engine emits to all of them. It is the engine's
// Emitter: hand it to the engine, and every window sees what a window in the
// same process would have.
type Server struct {
	methods map[string]reflect.Value
	hello   Hello

	mu        sync.Mutex
	conns     map[*serverConn]struct{}
	listeners map[net.Listener]struct{}
	closed    bool
	// done is closed by Close, to wake Serve from its wait between retries.
	done chan struct{}
}

// NewServer serves host's methods — those of the Host interface, and no other
// — reporting version in its hello.
func NewServer(host Host, version string) *Server {
	s := &Server{
		methods:   map[string]reflect.Value{},
		hello:     Hello{Protocol: ProtocolVersion, Version: version, PID: os.Getpid(), Build: BuildID()},
		conns:     map[*serverConn]struct{}{},
		listeners: map[net.Listener]struct{}{},
		done:      make(chan struct{}),
	}
	v := reflect.ValueOf(host)
	t := reflect.TypeOf((*Host)(nil)).Elem()
	for i := 0; i < t.NumMethod(); i++ {
		name := t.Method(i).Name
		s.methods[name] = v.MethodByName(name)
	}
	return s
}

// How long Serve waits after a failed accept, doubling from the first.
const (
	acceptRetryMin = 100 * time.Millisecond
	acceptRetryMax = 30 * time.Second
)

// Serve accepts windows on l until l is closed or the server is.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		l.Close()
		return net.ErrClosed
	}
	s.listeners[l] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.listeners, l)
		s.mu.Unlock()
	}()

	// A failed accept is not the end of the listener — a named pipe reports an
	// instance it could not open and carries on. Giving up would leave the
	// service supervising with no window able to reach it until it restarts, so
	// it says why and tries again, less and less often.
	delay := acceptRetryMin
	for {
		c, err := l.Accept()
		if err == nil {
			delay = acceptRetryMin
			go s.ServeConn(c)
			continue
		}
		if errors.Is(err, net.ErrClosed) || s.isClosed() {
			return nil
		}
		log.Printf("remote: could not accept a window, retrying in %v: %v", delay, err)
		select {
		case <-time.After(delay):
		case <-s.done:
			return nil
		}
		if delay *= 2; delay > acceptRetryMax {
			delay = acceptRetryMax
		}
	}
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// ServeConn talks to one window until it disconnects.
func (s *Server) ServeConn(c net.Conn) {
	sc := newServerConn(c)
	// The hello goes first, queued before the connection can receive any
	// event: a window expects it before anything else.
	sc.out <- mustEncode(frame{Hello: &s.hello})
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return
	}
	s.conns[sc] = struct{}{}
	s.mu.Unlock()

	defer func() {
		sc.close()
		s.mu.Lock()
		delete(s.conns, sc)
		s.mu.Unlock()
	}()

	go sc.writeLoop()

	dec := json.NewDecoder(c)
	for {
		var f frame
		if err := dec.Decode(&f); err != nil {
			return
		}
		if f.Method == "" || f.ID == 0 {
			continue
		}
		// Each call on its own goroutine: a long history query must not hold up
		// the counts the same window asks for meanwhile.
		go func(f frame) {
			sc.send(mustEncode(s.call(f)))
		}(f)
	}
}

// call runs one request and builds its response. A panic in the engine is
// answered as an error: one bad call must not take the supervision down.
func (s *Server) call(f frame) (resp frame) {
	resp.ID = f.ID
	defer func() {
		if r := recover(); r != nil {
			log.Printf("remote: a call from a window panicked: %s: %v", f.Method, r)
			resp = frame{ID: f.ID, Error: fmt.Sprintf("%s failed in the service: %v", f.Method, r)}
		}
	}()

	m, ok := s.methods[f.Method]
	if !ok {
		resp.Error = fmt.Sprintf("the service has no method %q", f.Method)
		return resp
	}
	mt := m.Type()
	if len(f.Params) != mt.NumIn() {
		resp.Error = fmt.Sprintf("%s takes %d parameters, not %d", f.Method, mt.NumIn(), len(f.Params))
		return resp
	}
	args := make([]reflect.Value, mt.NumIn())
	for i := range args {
		p := reflect.New(mt.In(i))
		if err := json.Unmarshal(f.Params[i], p.Interface()); err != nil {
			resp.Error = fmt.Sprintf("%s: parameter %d: %v", f.Method, i+1, err)
			return resp
		}
		args[i] = p.Elem()
	}

	out := m.Call(args)
	for i, v := range out {
		if i == len(out)-1 && mt.Out(i) == errorType {
			if !v.IsNil() {
				resp.Error = v.Interface().(error).Error()
			}
			break
		}
		data, err := json.Marshal(v.Interface())
		if err != nil {
			resp.Error = fmt.Sprintf("%s: result %d: %v", f.Method, i+1, err)
			resp.Result = nil
			return resp
		}
		resp.Result = append(resp.Result, data)
	}
	return resp
}

// Emit broadcasts an event to every connected window. It never waits for one:
// a window too far behind is disconnected instead.
func (s *Server) Emit(name string, data ...interface{}) {
	s.mu.Lock()
	if len(s.conns) == 0 {
		s.mu.Unlock()
		return
	}
	conns := make([]*serverConn, 0, len(s.conns))
	for sc := range s.conns {
		conns = append(conns, sc)
	}
	s.mu.Unlock()

	f := frame{Event: name}
	for _, d := range data {
		raw, err := json.Marshal(d)
		if err != nil {
			log.Printf("remote: cannot send %s to the windows: %v", name, err)
			return
		}
		f.Data = append(f.Data, raw)
	}
	b := mustEncode(f)
	for _, sc := range conns {
		if !sc.offer(b) {
			log.Printf("remote: a window stopped reading at %s; disconnecting it", name)
			sc.close()
		}
	}
}

// Windows is how many windows are connected.
func (s *Server) Windows() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Close stops accepting and disconnects every window.
func (s *Server) Close() {
	s.mu.Lock()
	if !s.closed {
		close(s.done)
	}
	s.closed = true
	listeners, conns := s.listeners, s.conns
	s.listeners = map[net.Listener]struct{}{}
	s.conns = map[*serverConn]struct{}{}
	s.mu.Unlock()
	for l := range listeners {
		l.Close()
	}
	for sc := range conns {
		sc.close()
	}
}

// serverConn is one window's connection. Everything sent to it goes through one
// writer, so frames never interleave.
type serverConn struct {
	c    net.Conn
	out  chan []byte
	done chan struct{}
	once sync.Once
}

func newServerConn(c net.Conn) *serverConn {
	return &serverConn{c: c, out: make(chan []byte, eventQueue), done: make(chan struct{})}
}

func (sc *serverConn) writeLoop() {
	for {
		select {
		case b := <-sc.out:
			if _, err := sc.c.Write(b); err != nil {
				sc.close()
				return
			}
		case <-sc.done:
			return
		}
	}
}

// send queues a response, waiting for room: a window gets every answer it asked
// for, or none because it went away.
func (sc *serverConn) send(b []byte) bool {
	select {
	case sc.out <- b:
		return true
	case <-sc.done:
		return false
	}
}

// offer queues an event if there is room.
func (sc *serverConn) offer(b []byte) bool {
	select {
	case sc.out <- b:
		return true
	case <-sc.done:
		return true
	default:
		return false
	}
}

func (sc *serverConn) close() {
	sc.once.Do(func() {
		close(sc.done)
		sc.c.Close()
	})
}

// mustEncode writes a frame as one line. Frames are built from values that
// already marshalled, so this cannot fail.
func mustEncode(f frame) []byte {
	b, err := json.Marshal(f)
	if err != nil {
		panic(fmt.Sprintf("remote: encode frame: %v", err))
	}
	return append(b, '\n')
}
