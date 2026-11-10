package simulator

import (
	"bytes"
	"errors"
	"maps"
	"math"
	"slices"

	"github.com/gosnmp/gosnmp"
)

// A device written to. A SET from a manager allowed to write — the write
// community, or a v3 user with write access — changes what the device answers
// until it restarts, as a running agent's configuration does until it is saved.
// RFC 3416 4.2.5: every varbind is checked before any is applied, and a SET is
// applied whole or not at all.
//
// Everything a manager reads may be written, except what the agent answers
// itself — its counters, its engine, the USM and the VACM — and an object only
// a notification carries. A varbind naming an instance nothing answers creates
// it, when it would sit in the tree the way an instance does: a leaf, under no
// other instance and with none under it. That is what lets a table be given a
// row.
//
// Rows proper need the MIB: which column is a RowStatus (RFC 2579). The agent
// has no MIB, so it is told (Config.RowStatus), and it then does what RFC 2579
// has an agent do — createAndGo makes the row active, createAndWait
// notInService, destroy removes every instance of the row — and refuses what the
// RFC refuses.

// The values of a RowStatus (RFC 2579).
const (
	rowActive        = 1
	rowNotInService  = 2
	rowCreateAndGo   = 4
	rowCreateAndWait = 5
	rowDestroy       = 6
)

// change is one varbind of a SET, checked and ready to apply.
type change struct {
	id  oid
	typ gosnmp.Asn1BER
	val any
	// destroy removes a row — every instance under entry whose index is index
	// — rather than writing a value.
	destroy      bool
	entry, index oid
}

// set answers a SET from a manager allowed to write. Requests are handled on
// the receive goroutine alone, so no other SET comes between the Load and the
// Store, and notifications only read the tree; a rewire (Agent.Rewire) replaces
// it from another goroutine, which swap keeps out of that window.
func (a *Agent) set(ver gosnmp.SnmpVersion, req *gosnmp.SnmpPacket) answer {
	a.swap.Lock()
	defer a.swap.Unlock()
	t := a.tree.Load()
	fail := func(i int, status gosnmp.SNMPError) answer {
		if ver == gosnmp.Version1 {
			status = v1Status(status)
		}
		return answer{vars: req.Variables, status: status, index: errIndex(i, len(req.Variables))}
	}
	changes := make([]change, 0, len(req.Variables))
	for i, v := range req.Variables {
		id, err := parseArcs(v.Name)
		if err != nil || len(id) < 2 {
			return fail(i, gosnmp.NoCreation)
		}
		if slices.ContainsFunc(agentSubtrees, id.hasPrefix) || t.notifyOnly[id.String()] != nil {
			return fail(i, gosnmp.NotWritable)
		}
		e := t.get(id)
		switch {
		case e != nil && v.Type != e.typ:
			return fail(i, gosnmp.WrongType)
		case e == nil && !t.placeable(id):
			return fail(i, gosnmp.NoCreation)
		}
		val, status := setValue(v.Type, v.Value)
		if status != gosnmp.NoError {
			return fail(i, status)
		}
		ch := change{id: id, typ: v.Type, val: val}
		if column, ok := a.rowStatusColumn(id); ok {
			asked, isInt := val.(int)
			if !isInt {
				return fail(i, gosnmp.WrongType)
			}
			next, status := rowStatus(e != nil, asked)
			switch {
			case status != gosnmp.NoError:
				return fail(i, status)
			case next == rowDestroy:
				ch = change{id: id, destroy: true, entry: column[:len(column)-1], index: id[len(column):]}
			default:
				ch.val = next
			}
		}
		changes = append(changes, ch)
	}
	next, err := t.with(changes)
	if err != nil {
		// Two instances of one SET, one under the other.
		return fail(0, gosnmp.NoCreation)
	}
	a.tree.Store(next)
	return answer{vars: req.Variables}
}

// rowStatusColumn is the RowStatus column id is an instance of, when the agent
// has been told of one.
func (a *Agent) rowStatusColumn(id oid) (oid, bool) {
	if a.rowStatus == nil {
		return nil, false
	}
	name, ok := a.rowStatus(id.String())
	if !ok {
		return nil, false
	}
	column, err := parseOID(name)
	if err != nil || len(id) <= len(column) || !id.hasPrefix(column) {
		return nil, false
	}
	return column, true
}

// rowStatus is what a RowStatus is set to, from what a manager asked for and
// whether the row exists (RFC 2579): the row made active or notInService, kept
// as it is asked to be, or destroyed — or the error the RFC answers instead.
func rowStatus(exists bool, asked int) (int, gosnmp.SNMPError) {
	switch asked {
	case rowCreateAndGo, rowCreateAndWait:
		if exists {
			return 0, gosnmp.InconsistentValue
		}
		if asked == rowCreateAndGo {
			return rowActive, gosnmp.NoError
		}
		return rowNotInService, gosnmp.NoError
	case rowActive, rowNotInService:
		if !exists {
			return 0, gosnmp.InconsistentValue
		}
		return asked, gosnmp.NoError
	case rowDestroy:
		return rowDestroy, gosnmp.NoError
	}
	// notReady(3) is the agent's to say, never a manager's to set.
	return 0, gosnmp.WrongValue
}

// setValue is a varbind's value in the Go type Const holds for its type, or
// the error a SET carrying it earns.
func setValue(t gosnmp.Asn1BER, v any) (any, gosnmp.SNMPError) {
	var out any
	switch t {
	case gosnmp.Integer:
		n := gosnmp.ToBigInt(v)
		if !n.IsInt64() || n.Int64() < math.MinInt32 || n.Int64() > math.MaxInt32 {
			return nil, gosnmp.WrongValue
		}
		out = int(n.Int64())
	case gosnmp.OctetString, gosnmp.Opaque:
		switch b := v.(type) {
		case []byte:
			out = bytes.Clone(b)
		case string:
			out = []byte(b)
		default:
			return nil, gosnmp.WrongValue
		}
	case gosnmp.ObjectIdentifier, gosnmp.IPAddress:
		s, ok := v.(string)
		if !ok {
			return nil, gosnmp.WrongValue
		}
		out = s
	case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks:
		n := gosnmp.ToBigInt(v)
		if !n.IsUint64() || n.Uint64() > math.MaxUint32 {
			return nil, gosnmp.WrongValue
		}
		out = uint32(n.Uint64())
	case gosnmp.Counter64:
		n := gosnmp.ToBigInt(v)
		if !n.IsUint64() {
			return nil, gosnmp.WrongValue
		}
		out = n.Uint64()
	default:
		return nil, gosnmp.WrongType
	}
	if err := checkValue(t, out); err != nil {
		return nil, gosnmp.WrongValue
	}
	return out, gosnmp.NoError
}

// v1Status is an SNMPv2 error as an SNMPv1 agent answers it (RFC 3584 4.4).
func v1Status(s gosnmp.SNMPError) gosnmp.SNMPError {
	switch s {
	case gosnmp.WrongValue, gosnmp.WrongEncoding, gosnmp.WrongType, gosnmp.WrongLength, gosnmp.InconsistentValue:
		return gosnmp.BadValue
	case gosnmp.NoAccess, gosnmp.NotWritable, gosnmp.NoCreation, gosnmp.InconsistentName, gosnmp.AuthorizationError:
		return gosnmp.NoSuchName
	case gosnmp.ResourceUnavailable, gosnmp.CommitFailed, gosnmp.UndoFailed:
		return gosnmp.GenErr
	}
	return s
}

// placeable reports whether an instance could be made at o: a leaf, lying
// under no instance and with none under it.
func (t *tree) placeable(o oid) bool {
	i, found := t.find(o)
	switch {
	case found:
		return false
	case i < len(t.entries) && t.entries[i].oid.hasPrefix(o):
		return false
	case i > 0 && o.hasPrefix(t.entries[i-1].oid):
		return false
	}
	return true
}

// with is t with changes applied. t is left as it is — a request reading it
// meanwhile sees it whole — and the tree returned shares none of its entries.
func (t *tree) with(changes []change) (*tree, error) {
	next := &tree{entries: slices.Clone(t.entries), objects: t.objects, notifyOnly: t.notifyOnly}
	var made []oid
	for _, ch := range changes {
		if ch.destroy {
			next.entries = slices.DeleteFunc(next.entries, func(e entry) bool { return inRow(e.oid, ch.entry, ch.index) })
			continue
		}
		e := entry{oid: ch.id, name: ch.id.String(), typ: ch.typ, val: Const(ch.val)}
		i, found := next.find(ch.id)
		if found {
			next.entries[i] = e
			continue
		}
		next.entries = slices.Insert(next.entries, i, e)
		made = append(made, ch.id)
	}
	for i := 1; i < len(next.entries); i++ {
		if next.entries[i].oid.hasPrefix(next.entries[i-1].oid) {
			return nil, errors.New("an instance lies under another")
		}
	}
	if len(made) > 0 {
		next.objects = maps.Clone(t.objects)
		for _, id := range made {
			next.objects[id[:len(id)-1].String()] = true
		}
	}
	return next, nil
}

// inRow reports whether o is an instance of the row index names under entry:
// entry, then a column, then the index.
func inRow(o, entry, index oid) bool {
	return len(o) == len(entry)+1+len(index) && o.hasPrefix(entry) && slices.Equal(o[len(entry)+1:], index)
}
