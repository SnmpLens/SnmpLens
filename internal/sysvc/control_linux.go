//go:build linux

package sysvc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// controlSocket is where windows connect to drive the service, in its runtime
// directory.
const controlSocket = "control.sock"

// deniedLine is what an account that is no operator is told before the socket
// closes, so that its window can say why rather than "connection reset".
var deniedLine = []byte("{\"denied\":true}\n")

// ListenControl opens the service's socket. Anyone may connect — the socket is
// 0666 — and who may stay is decided per connection, by the peer's uid
// (SO_PEERCRED): root, the service's own account, and the operators, read again
// at each connection, so an account allowed later needs no restart.
func ListenControl() (net.Listener, error) {
	run, err := StatusDir()
	if err != nil {
		return nil, err
	}
	data, err := DataDir()
	if err != nil {
		return nil, err
	}
	return listenControl(filepath.Join(run, controlSocket), data)
}

func listenControl(path, dataDir string) (net.Listener, error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		l.Close()
		return nil, err
	}
	return &operatorListener{Listener: l, dataDir: dataDir, self: os.Getuid()}, nil
}

type operatorListener struct {
	net.Listener
	dataDir string
	self    int
}

// Accept hands on the connections of the accounts allowed, and tells the others
// so before closing theirs.
func (l *operatorListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		cred, err := peerCred(c)
		if err == nil && l.allowed(int(cred.Uid)) {
			return c, nil
		}
		_ = c.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = c.Write(deniedLine)
		c.Close()
	}
}

func (l *operatorListener) allowed(uid int) bool {
	if uid == 0 || uid == l.self {
		return true
	}
	ops, err := loadOperators(l.dataDir)
	if err != nil {
		return false
	}
	for _, op := range ops {
		if op.SID == strconv.Itoa(uid) {
			return true
		}
	}
	return false
}

func peerCred(c net.Conn) (*syscall.Ucred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil, errors.New("not a Unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return cred, serr
}

// DialControl connects to the running service, and checks that what answers is
// the service's process: a window hands the service its credentials.
func DialControl(timeout time.Duration) (net.Conn, error) {
	info, err := Query()
	if err != nil {
		return nil, err
	}
	if !info.Installed || info.State != "running" || info.PID == 0 {
		return nil, fmt.Errorf("the service is not running")
	}
	run, err := StatusDir()
	if err != nil {
		return nil, err
	}
	return dialControl(filepath.Join(run, controlSocket), timeout, info.PID)
}

func dialControl(path string, timeout time.Duration, wantPID uint32) (net.Conn, error) {
	c, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, err
	}
	if wantPID != 0 {
		cred, err := peerCred(c)
		if err != nil || uint32(cred.Pid) != wantPID {
			c.Close()
			return nil, ErrNotService
		}
	}
	pc := &peekedConn{Conn: c, r: bufio.NewReader(c)}
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	first, err := pc.r.Peek(len(deniedLine))
	_ = c.SetReadDeadline(time.Time{})
	if err == nil && bytes.Equal(first, deniedLine) {
		c.Close()
		return nil, ErrAccessDenied
	}
	return pc, nil
}

// peekedConn reads through the buffer the denial was looked for in.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// CurrentUserSID is this account's uid: what names an operator on Linux.
func CurrentUserSID() (string, error) {
	return strconv.Itoa(os.Getuid()), nil
}

// addOperator allows an account, given by uid, to drive the service. added is
// false when it was allowed already.
func addOperator(dir, uid string) (added bool, err error) {
	n, err := strconv.Atoi(uid)
	if err != nil || n < 0 {
		return false, fmt.Errorf("%q is not an account's uid", uid)
	}
	uid = strconv.Itoa(n)
	ops, err := loadOperators(dir)
	if err != nil {
		return false, err
	}
	for _, op := range ops {
		if op.SID == uid {
			return false, nil
		}
	}
	op := Operator{SID: uid}
	if u, err := user.LookupId(uid); err == nil {
		op.Name = u.Username
	}
	if err := saveOperators(dir, append(ops, op)); err != nil {
		return false, err
	}
	return true, nil
}

// Allow lets an account drive the service from a window. The service reads its
// operators at each connection, so no restart is needed. It must run as root.
func Allow(uid string) error {
	if !IsElevated() {
		return fmt.Errorf("allowing an account needs root: run it with sudo")
	}
	dir, err := DataDir()
	if err != nil {
		return err
	}
	if _, err := addOperator(dir, uid); err != nil {
		return err
	}
	// Root wrote the file: give it to the service, like the rest of its
	// directory.
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return os.Chown(filepath.Join(dir, operatorsFile), int(st.Uid), int(st.Gid))
	}
	return nil
}
