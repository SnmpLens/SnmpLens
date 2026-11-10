package simulator

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

// A custom model is one somebody else wrote: a JSON file saying what a device
// answers, imported into the application and made into devices like any model
// of the catalogue.
//
// It is read the way a dashboard preset is, and for the same reason: it is a
// stranger's file, and it makes this application answer and send SNMP. It PICKS
// from a frozen vocabulary and describes nothing — a type from the SMI's list, a
// behaviour from the ones values.go implements (a constant, the uptime, a
// counter, a gauge), notifications naming objects the model answers. Nothing in
// it is code, a path or an address, and every bound is checked before anything
// is built.

const (
	// CustomModelKind is what a model file says it is, so that a preset or a
	// device file is refused as what it is rather than as a model with problems.
	CustomModelKind = "snmplens-simulator-model"
	// CustomModelFormat is the format version this package reads.
	CustomModelFormat = 1
	// CustomModelPrefix begins the catalogue ID of every custom model, so that
	// one can never take a built-in model's ID, today's or a later one's.
	CustomModelPrefix = "custom:"
)

// The bounds on a custom model: sanity bounds against a malformed file, far
// above what a device needs — IF-MIB for a 48-port switch is about 1300 objects.
const (
	MaxCustomModelBytes    = 1 << 20
	MaxCustomObjects       = 8192
	MaxCustomInstances     = 1024
	MaxCustomInterfaces    = 256
	MaxCustomNotifications = 32
	maxNotificationObjects = 16
	maxCustomDescription   = 600
	// maxCustomValue bounds one OCTET STRING, which travels in every response
	// that carries it.
	maxCustomValue  = 4096
	maxCustomPeriod = 7 * 86400
	maxCustomRate   = 1e12
)

// instancePlaceholder stands for the instance in a templated object's OID and
// string value: the placeholder a preset uses for the same thing.
const instancePlaceholder = "{#}"

// customCategories are the categories a custom model may be filed under: the
// catalogue's, and "other". The interface names each (simulator.category.<id>).
var customCategories = []string{"server", "network", "security", "wireless", "storage", "power", "printing", "environment",
	"telephony", "other"}

// customTypes are the SMI types an object may be sent as, by the names the SMI
// gives them.
var customTypes = map[string]gosnmp.Asn1BER{
	"Integer32":        gosnmp.Integer,
	"Integer":          gosnmp.Integer,
	"OctetString":      gosnmp.OctetString,
	"ObjectIdentifier": gosnmp.ObjectIdentifier,
	"IpAddress":        gosnmp.IPAddress,
	"Counter32":        gosnmp.Counter32,
	"Gauge32":          gosnmp.Gauge32,
	"Unsigned32":       gosnmp.Gauge32,
	"TimeTicks":        gosnmp.TimeTicks,
	"Counter64":        gosnmp.Counter64,
	"Opaque":           gosnmp.Opaque,
}

var (
	customIDPattern         = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`)
	notificationNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
	instancePattern         = regexp.MustCompile(`^[0-9]{1,10}(?:\.[0-9]{1,10}){0,31}$`)
)

// customFile is a model file as it is written.
type customFile struct {
	Kind          string               `json:"kind"`
	FormatVersion int                  `json:"formatVersion"`
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Vendor        string               `json:"vendor"`
	Category      string               `json:"category"`
	Enterprise    *int64               `json:"enterprise"`
	Icon          string               `json:"icon"`
	System        *customSystem        `json:"system"`
	Interfaces    []customInterface    `json:"interfaces"`
	Objects       []customObject       `json:"objects"`
	Notifications []customNotification `json:"notifications"`
}

type customSystem struct {
	Descr    string `json:"descr"`
	ObjectID string `json:"objectId"`
	Contact  string `json:"contact"`
	Location string `json:"location"`
	Services *int   `json:"services"`
}

type customInterface struct {
	Index           int      `json:"index"`
	Descr           string   `json:"descr"`
	Name            string   `json:"name"`
	Alias           string   `json:"alias"`
	Type            int      `json:"type"`
	MTU             int      `json:"mtu"`
	SpeedMbps       *float64 `json:"speedMbps"`
	Up              bool     `json:"up"`
	AdminDown       bool     `json:"adminDown"`
	InOctetsPerSec  float64  `json:"inOctetsPerSec"`
	OutOctetsPerSec float64  `json:"outOctetsPerSec"`
	ErrorsPerSec    float64  `json:"errorsPerSec"`
}

type customObject struct {
	OID        string          `json:"oid"`
	Type       string          `json:"type"`
	Instances  []instance      `json:"instances"`
	Value      json.RawMessage `json:"value"`
	Hex        *string         `json:"hex"`
	Uptime     bool            `json:"uptime"`
	SecondsUp  bool            `json:"secondsUp"`
	Gauge      *customGauge    `json:"gauge"`
	Counter    *customCounter  `json:"counter"`
	NotifyOnly bool            `json:"notifyOnly"`
}

type customGauge struct {
	Min       *float64 `json:"min"`
	Max       *float64 `json:"max"`
	PeriodSec float64  `json:"periodSec"`
}

type customCounter struct {
	PerSecond *float64 `json:"perSecond"`
	Start     uint64   `json:"start"`
	Swing     float64  `json:"swing"`
	PeriodSec float64  `json:"periodSec"`
}

type customNotification struct {
	Name    string   `json:"name"`
	OID     string   `json:"oid"`
	Objects []string `json:"objects"`
}

// instance is one instance a templated object is made for, written as a string
// ("1.2") or, for a one-arc index, as a number.
type instance string

func (i *instance) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*i = instance(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New("an instance is a string or a number")
	}
	*i = instance(n.String())
	return nil
}

// CustomModel is a custom model that has been read and checked.
type CustomModel struct {
	m    model
	icon string
}

// ID is the model's catalogue ID: CustomModelPrefix, then its slug.
func (c CustomModel) ID() string { return c.m.ID }

// Slug is the file's own "id", which the application files the model under.
func (c CustomModel) Slug() string { return strings.TrimPrefix(c.m.ID, CustomModelPrefix) }

// Name is the name the file gives the model.
func (c CustomModel) Name() string { return c.m.Name }

// Icon is the file the model names as its icon — a name in the archive it came
// in, never a path — or empty.
func (c CustomModel) Icon() string { return c.icon }

// CustomModelSlug is the slug of a custom model's catalogue ID, and false for
// anything else: a built-in model's ID, or a string no model file could give —
// which is what makes a slug safe to name a file after.
func CustomModelSlug(id string) (string, bool) {
	slug, ok := strings.CutPrefix(id, CustomModelPrefix)
	return slug, ok && customIDPattern.MatchString(slug)
}

// ParseCustomModel reads a model file and checks all of it, building it once to
// do so: what is refused is refused naming the field or the OID that caused it,
// and what passes will start.
func ParseCustomModel(raw []byte) (CustomModel, error) {
	if len(raw) > MaxCustomModelBytes {
		return CustomModel{}, fmt.Errorf("a model file is at most %d KB", MaxCustomModelBytes>>10)
	}
	f, err := decodeModel(raw)
	if err != nil {
		return CustomModel{}, err
	}
	return f.compile(packageParts{})
}

// decodeModel reads a model file, all of it and strictly, once what it says it
// is has been read.
func decodeModel(raw []byte) (customFile, error) {
	// What the file says it is comes first, and is read leniently — a preset
	// refused for its "widgets" field would be told the wrong thing — and from
	// the first value alone, so that what follows it is reported as that.
	var head struct {
		Kind          string `json:"kind"`
		FormatVersion int    `json:"formatVersion"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&head); err != nil {
		return customFile{}, jsonProblem(raw, err)
	}
	if head.Kind != CustomModelKind {
		return customFile{}, fmt.Errorf("this is not a simulator model: its \"kind\" is %q, and a model's is %q",
			head.Kind, CustomModelKind)
	}
	if head.FormatVersion != CustomModelFormat {
		return customFile{}, fmt.Errorf("format version %d: this version of SnmpLens reads version %d",
			head.FormatVersion, CustomModelFormat)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	// A field the format does not define is refused rather than ignored:
	// "vaule" for "value" would otherwise leave an object with no value, and be
	// reported as that.
	dec.DisallowUnknownFields()
	var f customFile
	if err := dec.Decode(&f); err != nil {
		return customFile{}, jsonProblem(raw, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return customFile{}, errors.New("there is more after the model: a file holds one")
	}
	return f, nil
}

// jsonProblem says what is wrong with a file JSON cannot read, and where.
func jsonProblem(raw []byte, err error) error {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		line, col := position(raw, syntax.Offset)
		return fmt.Errorf("line %d, column %d: %s", line, col, syntax.Error())
	case errors.As(err, &typ):
		line, col := position(raw, typ.Offset)
		return fmt.Errorf("line %d, column %d: %q is a JSON %s where the format has %s",
			line, col, typ.Field, typ.Value, jsonKind(typ.Type.String()))
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("the file ends before the model does")
	}
	return errors.New(strings.TrimPrefix(err.Error(), "json: "))
}

// position is the line and column of a byte offset into raw.
func position(raw []byte, offset int64) (line, col int) {
	offset = min(max(offset, 0), int64(len(raw)))
	before := raw[:offset]
	line = 1 + bytes.Count(before, []byte{'\n'})
	col = int(offset) - bytes.LastIndexByte(before, '\n')
	return line, col
}

// jsonKind names a Go type the way a JSON author would.
func jsonKind(goType string) string {
	switch {
	case strings.Contains(goType, "int"), strings.Contains(goType, "float"):
		return "a number"
	case goType == "string" || goType == "simulator.instance":
		return "a string"
	case goType == "bool":
		return "true or false"
	case strings.HasPrefix(goType, "[]"):
		return "a list"
	}
	return "an object"
}

// systemGroup is a custom model's system group, checked.
type systemGroup struct {
	descr, objectID, contact, location string
	services                           int
}

// compiledObject is one object of a custom model, ready to be made for a device.
type compiledObject struct {
	oid        string // with the placeholder, when there are instances
	typ        gosnmp.Asn1BER
	instances  []string
	notifyOnly bool
	reading    func(instance string, s Swing) Reading
}

// compile checks a model file, with what the rest of its package adds to it —
// nothing, for a lone file — and makes the model.
func (f customFile) compile(parts packageParts) (CustomModel, error) {
	if !customIDPattern.MatchString(f.ID) {
		return CustomModel{}, fmt.Errorf("\"id\" is 1 to 48 lower-case letters, digits and inner hyphens, not %q", f.ID)
	}
	name := strings.TrimSpace(f.Name)
	if name == "" || utf8.RuneCountInString(name) > MaxDeviceName {
		return CustomModel{}, fmt.Errorf("\"name\" is 1 to %d characters", MaxDeviceName)
	}
	if utf8.RuneCountInString(f.Description) > maxCustomDescription {
		return CustomModel{}, fmt.Errorf("\"description\" is at most %d characters", maxCustomDescription)
	}
	if utf8.RuneCountInString(f.Vendor) > MaxDeviceName {
		return CustomModel{}, fmt.Errorf("\"vendor\" is at most %d characters", MaxDeviceName)
	}
	category := cmp.Or(f.Category, "other")
	if !slices.Contains(customCategories, category) {
		return CustomModel{}, fmt.Errorf("\"category\" is one of %s, not %q", strings.Join(customCategories, ", "), f.Category)
	}
	icon := strings.TrimSpace(f.Icon)
	if icon != "" && (path.Base(icon) != icon || strings.ContainsAny(icon, `/\:`) || strings.HasPrefix(icon, ".") || len(icon) > 128) {
		return CustomModel{}, fmt.Errorf("\"icon\" names a file in the archive beside the model, not a path: %q", icon)
	}
	sys, err := f.systemOf(parts)
	if err != nil {
		return CustomModel{}, err
	}
	enterprise, err := f.enterpriseNumber(sys.objectID)
	if err != nil {
		return CustomModel{}, err
	}
	ifs, err := checkInterfaces(f.Interfaces)
	if err != nil {
		return CustomModel{}, err
	}
	objs, err := compileObjects(slices.Concat([]objectSource{{list: f.Objects}}, parts.objects))
	if err != nil {
		return CustomModel{}, err
	}
	notes, err := checkNotifications(slices.Concat([]notificationSource{{list: f.Notifications}}, parts.notifications))
	if err != nil {
		return CustomModel{}, err
	}
	recorded := parts.unwritten(buildCustom(Identity{Name: "model-check", Seed: 1}, sys, ifs, objs), len(ifs) > 0)
	uptime := parts.uptime

	m := model{
		ModelInfo: ModelInfo{ID: CustomModelPrefix + f.ID, Category: category, Custom: true,
			Name: name, Description: strings.TrimSpace(f.Description), Vendor: strings.TrimSpace(f.Vendor)},
		enterprise:    enterprise,
		notifications: notes,
		build: func(id Identity) []Object {
			return appendRecorded(buildCustom(id, sys, ifs, objs), id, recorded, uptime)
		},
	}

	// Built once, as a device answering v3 will be, with what the agent adds:
	// the tree catches what no field check can — an OID given twice, one under
	// another, a value its type cannot carry — and names the OID.
	tr, err := newTree(slices.Concat(m.build(Identity{Name: "model-check", Seed: 1}),
		agentObjects(true, []byte{0x80, 0, 0, 0, 0}, 1, false)))
	if err != nil {
		hint := ""
		if strings.Contains(err.Error(), "declared twice") {
			hint = ` ("system" makes the system group, "interfaces" makes IF-MIB's, and the snmp group, snmpEngine and the USM statistics are the agent's own)`
		}
		return CustomModel{}, fmt.Errorf("the model's objects: %w%s", err, hint)
	}
	for _, n := range notes {
		for _, name := range n.Objects {
			id, _ := parseOID(name)
			if tr.notification(id) == nil {
				return CustomModel{}, fmt.Errorf("notification %q carries %s, which the model does not answer", n.Name, name)
			}
		}
	}
	return CustomModel{m: m, icon: icon}, nil
}

func (s *customSystem) check() (systemGroup, error) {
	if s == nil {
		return systemGroup{}, errors.New("\"system\" is missing: every device answers the system group, and a manager reads sysDescr and sysObjectID first")
	}
	descr := strings.TrimSpace(s.Descr)
	if descr == "" || len(descr) > 255 {
		return systemGroup{}, errors.New("system.descr is 1 to 255 octets")
	}
	id, err := parseOID(s.ObjectID)
	if err != nil {
		return systemGroup{}, fmt.Errorf("system.objectId: %w", err)
	}
	if len(s.Contact) > 255 || len(s.Location) > 255 {
		return systemGroup{}, errors.New("system.contact and system.location are at most 255 octets")
	}
	services := 72 // end to end and applications, as a host's agent answers
	if s.Services != nil {
		services = *s.Services
		if services < 0 || services > 127 {
			return systemGroup{}, errors.New("system.services is 0 to 127 (RFC 3418)")
		}
	}
	return systemGroup{descr: descr, objectID: id.String(), contact: s.Contact, location: s.Location, services: services}, nil
}

// enterpriseNumber is the vendor the model's engine ID carries: the one given,
// or else the one its sysObjectID names under enterprises, as a real device's
// does, or else net-snmp's, which most embedded agents are.
func (f customFile) enterpriseNumber(sysObjectID string) (uint32, error) {
	if f.Enterprise != nil {
		if *f.Enterprise < 1 || *f.Enterprise >= 1<<31 {
			return 0, fmt.Errorf("\"enterprise\" is a private enterprise number, 1 to %d", 1<<31-1)
		}
		return uint32(*f.Enterprise), nil
	}
	id, _ := parseOID(sysObjectID)
	if enterprises := (oid{1, 3, 6, 1, 4, 1}); len(id) > len(enterprises) && id.hasPrefix(enterprises) &&
		id[len(enterprises)] > 0 && id[len(enterprises)] < 1<<31 {
		return id[len(enterprises)], nil
	}
	return 8072, nil
}

func checkInterfaces(in []customInterface) ([]iface, error) {
	if len(in) > MaxCustomInterfaces {
		return nil, fmt.Errorf("a model has at most %d interfaces", MaxCustomInterfaces)
	}
	out := make([]iface, 0, len(in))
	seen := map[int]bool{}
	for i, c := range in {
		at := fmt.Sprintf("interfaces[%d]", i)
		index := c.Index
		if index == 0 {
			index = i + 1
		}
		if index < 1 || index > math.MaxInt32 {
			return nil, fmt.Errorf("%s: \"index\" is 1 to %d", at, math.MaxInt32)
		}
		if seen[index] {
			return nil, fmt.Errorf("%s: index %d is given twice", at, index)
		}
		seen[index] = true
		descr := strings.TrimSpace(c.Descr)
		if descr == "" || len(descr) > 255 {
			return nil, fmt.Errorf("%s: \"descr\" is 1 to 255 octets", at)
		}
		if len(c.Name) > 64 || len(c.Alias) > 64 {
			return nil, fmt.Errorf("%s: \"name\" and \"alias\" are at most 64 octets", at)
		}
		ifType := cmp.Or(c.Type, ifTypeEthernet)
		if ifType < 1 || ifType > math.MaxInt32 {
			return nil, fmt.Errorf("%s: \"type\" is an IANAifType number", at)
		}
		mtu := cmp.Or(c.MTU, 1500)
		if mtu < 0 || mtu > math.MaxInt32 {
			return nil, fmt.Errorf("%s: \"mtu\" is 0 to %d", at, math.MaxInt32)
		}
		speed := 1000.0
		if c.SpeedMbps != nil {
			speed = *c.SpeedMbps
		}
		if !(speed >= 0 && speed <= 1e7) {
			return nil, fmt.Errorf("%s: \"speedMbps\" is 0 to 10000000", at)
		}
		for _, r := range []float64{c.InOctetsPerSec, c.OutOctetsPerSec, c.ErrorsPerSec} {
			if !(r >= 0 && r <= maxCustomRate) {
				return nil, fmt.Errorf("%s: a rate is 0 to %g per second", at, maxCustomRate)
			}
		}
		out = append(out, iface{index: index, descr: descr, name: strings.TrimSpace(c.Name), alias: c.Alias,
			ifType: ifType, mtu: mtu, speed: uint64(math.Round(speed * 1e6)), up: c.Up, adminDown: c.AdminDown,
			inRate: c.InOctetsPerSec, outRate: c.OutOctetsPerSec, errorRate: c.ErrorsPerSec})
	}
	return out, nil
}

// objectSource is a list of objects and the file of a package it was written
// in: none for the model file's own, whose errors read as they always have.
type objectSource struct {
	file string
	list []customObject
}

// notificationSource is the same for notifications.
type notificationSource struct {
	file string
	list []customNotification
}

// within is how an error names the file of a package it is about.
func within(file string) string {
	if file == "" {
		return ""
	}
	return file + ": "
}

func compileObjects(sources []objectSource) ([]compiledObject, error) {
	// Counted before anything is built: a thousand objects of a thousand
	// instances is a million objects, and the bound is the point.
	total := 0
	for _, src := range sources {
		for i, o := range src.list {
			if len(o.Instances) > MaxCustomInstances {
				return nil, fmt.Errorf("%sobjects[%d]: at most %d instances", within(src.file), i, MaxCustomInstances)
			}
			total += max(1, len(o.Instances))
		}
	}
	if total > MaxCustomObjects {
		return nil, fmt.Errorf("the model makes %d objects, and a model makes at most %d", total, MaxCustomObjects)
	}
	var out []compiledObject
	for _, src := range sources {
		for i, o := range src.list {
			c, err := o.compile()
			if err != nil {
				return nil, fmt.Errorf("%sobjects[%d] (%s): %w", within(src.file), i, o.OID, err)
			}
			out = append(out, c)
		}
	}
	return out, nil
}

func (o customObject) compile() (compiledObject, error) {
	typ, ok := customTypes[o.Type]
	if !ok {
		names := make([]string, 0, len(customTypes))
		for name := range customTypes {
			names = append(names, name)
		}
		slices.Sort(names)
		return compiledObject{}, fmt.Errorf("%q is not a type: %s", o.Type, strings.Join(names, ", "))
	}
	templated := strings.Contains(o.OID, instancePlaceholder)
	switch {
	case templated && len(o.Instances) == 0:
		return compiledObject{}, errors.New("the OID has {#}, and \"instances\" gives none to put there")
	case !templated && len(o.Instances) > 0:
		return compiledObject{}, errors.New("\"instances\" needs {#} in the OID, where each instance goes")
	}
	instances := make([]string, len(o.Instances))
	for i, inst := range o.Instances {
		s := string(inst)
		if !instancePattern.MatchString(s) {
			return compiledObject{}, fmt.Errorf("%q is not an instance: sub-identifiers, separated by dots", s)
		}
		if slices.Contains(instances[:i], s) {
			return compiledObject{}, fmt.Errorf("instance %s is given twice", s)
		}
		instances[i] = s
	}
	if _, err := parseOID(strings.ReplaceAll(o.OID, instancePlaceholder, "1")); err != nil {
		return compiledObject{}, err
	}

	given := 0
	for _, set := range []bool{o.Value != nil, o.Hex != nil, o.Uptime, o.SecondsUp, o.Gauge != nil, o.Counter != nil} {
		if set {
			given++
		}
	}
	if given != 1 {
		return compiledObject{}, errors.New("give exactly one of \"value\", \"hex\", \"uptime\", \"secondsUp\", \"gauge\" and \"counter\"")
	}
	var reading func(string, Swing) Reading
	var err error
	switch {
	case o.Value != nil:
		reading, err = constantOf(typ, o.Value, instances)
	case o.Hex != nil:
		reading, err = hexOf(typ, *o.Hex)
	case o.Uptime:
		if typ != gosnmp.TimeTicks {
			return compiledObject{}, errors.New("an uptime is TimeTicks")
		}
		reading = func(string, Swing) Reading { return Uptime() }
	case o.SecondsUp:
		if typ != gosnmp.Gauge32 {
			return compiledObject{}, errors.New("seconds up are a Gauge32")
		}
		reading = func(string, Swing) Reading { return SecondsUp() }
	case o.Gauge != nil:
		reading, err = o.Gauge.compile(typ)
	case o.Counter != nil:
		reading, err = o.Counter.compile(typ)
	}
	if err != nil {
		return compiledObject{}, err
	}
	return compiledObject{oid: o.OID, typ: typ, instances: instances, notifyOnly: o.NotifyOnly, reading: reading}, nil
}

// constantOf is a value that never changes, in the Go type Const holds for t. A
// string may carry the placeholder, and each instance gets its own; a number
// written as the placeholder alone is the instance itself, which is what a
// table's index column holds.
func constantOf(t gosnmp.Asn1BER, raw json.RawMessage, instances []string) (func(string, Swing) Reading, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("\"value\": %w", err)
	}
	fixed := func(x any) func(string, Swing) Reading {
		r := Const(x)
		return func(string, Swing) Reading { return r }
	}
	switch t {
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64:
		if v == any(instancePlaceholder) {
			if len(instances) == 0 {
				return nil, errors.New("\"{#}\" as a value is the instance, and the object has none")
			}
			values := make(map[string]any, len(instances))
			for _, inst := range instances {
				x, err := numberOf(t, inst)
				if err != nil {
					return nil, fmt.Errorf("instance %s: %w", inst, err)
				}
				values[inst] = x
			}
			return func(inst string, _ Swing) Reading { return Const(values[inst]) }, nil
		}
		number, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("a %v's value is a number, or \"{#}\" for the instance itself", t)
		}
		x, err := numberOf(t, number.String())
		if err != nil {
			return nil, err
		}
		return fixed(x), nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("a %v's value is a string", t)
	}
	if len(s) > maxCustomValue {
		return nil, fmt.Errorf("a value is at most %d octets", maxCustomValue)
	}
	if !strings.Contains(s, instancePlaceholder) {
		return fixed(s), nil
	}
	return func(inst string, _ Swing) Reading { return Const(strings.ReplaceAll(s, instancePlaceholder, inst)) }, nil
}

// numberOf reads s as a value of the numeric type t, in the Go type Const
// holds for it.
func numberOf(t gosnmp.Asn1BER, s string) (any, error) {
	switch t {
	case gosnmp.Integer:
		n, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s is not an Integer32: a whole number from %d to %d", s, math.MinInt32, math.MaxInt32)
		}
		return int(n), nil
	case gosnmp.Counter64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s is not a Counter64: a whole number from 0 to %d", s, uint64(math.MaxUint64))
		}
		return n, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("%s is not a %v: a whole number from 0 to %d", s, t, uint32(math.MaxUint32))
	}
	return uint32(n), nil
}

// hexOf is an OCTET STRING given as its octets — a MAC address, a bit string —
// with colons and spaces between them allowed.
func hexOf(t gosnmp.Asn1BER, s string) (func(string, Swing) Reading, error) {
	if t != gosnmp.OctetString && t != gosnmp.Opaque {
		return nil, errors.New("\"hex\" is for an OctetString or an Opaque")
	}
	b, err := hex.DecodeString(strings.NewReplacer(":", "", " ", "").Replace(s))
	if err != nil {
		return nil, fmt.Errorf("\"hex\": %w", err)
	}
	if len(b) > maxCustomValue {
		return nil, fmt.Errorf("a value is at most %d octets", maxCustomValue)
	}
	r := Const(b)
	return func(string, Swing) Reading { return r }, nil
}

// periodOf is how slowly a value moves, 300 s when not said.
func periodOf(seconds float64) (time.Duration, error) {
	if seconds == 0 {
		return 5 * time.Minute, nil
	}
	if !(seconds >= 1 && seconds <= maxCustomPeriod) {
		return 0, fmt.Errorf("\"periodSec\" is 1 to %d", maxCustomPeriod)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (g customGauge) compile(t gosnmp.Asn1BER) (func(string, Swing) Reading, error) {
	if g.Min == nil || g.Max == nil {
		return nil, errors.New("a gauge swings between a \"min\" and a \"max\"")
	}
	period, err := periodOf(g.PeriodSec)
	if err != nil {
		return nil, err
	}
	lo, hi := *g.Min, *g.Max
	switch t {
	case gosnmp.Gauge32:
		return func(_ string, s Swing) Reading { s.Period = period; return Gauge(lo, hi, s) }, nil
	case gosnmp.Integer:
		return func(_ string, s Swing) Reading { s.Period = period; return IntegerGauge(lo, hi, s) }, nil
	}
	return nil, fmt.Errorf("a gauge is a Gauge32 or an Integer32, not a %v", t)
}

func (c customCounter) compile(t gosnmp.Asn1BER) (func(string, Swing) Reading, error) {
	if c.PerSecond == nil {
		return nil, errors.New("a counter says how fast it grows: \"perSecond\"")
	}
	rate := *c.PerSecond
	if !(rate >= 0 && rate <= maxCustomRate) {
		return nil, fmt.Errorf("\"perSecond\" is 0 to %g", maxCustomRate)
	}
	if !(c.Swing >= 0 && c.Swing <= 0.95) {
		return nil, errors.New("\"swing\" is 0 to 0.95: the share of its rate a counter swings by, never enough to go back")
	}
	period, err := periodOf(c.PeriodSec)
	if err != nil {
		return nil, err
	}
	switch t {
	case gosnmp.Counter32:
		return func(_ string, s Swing) Reading {
			s.Depth, s.Period = c.Swing, period
			return Counter(rate, c.Start, s)
		}, nil
	case gosnmp.Counter64:
		return func(_ string, s Swing) Reading {
			s.Depth, s.Period = c.Swing, period
			return Counter64(rate, c.Start, s)
		}, nil
	}
	return nil, fmt.Errorf("a counter is a Counter32 or a Counter64, not a %v", t)
}

func checkNotifications(sources []notificationSource) ([]Notification, error) {
	type located struct {
		at string
		n  customNotification
	}
	var in []located
	for _, src := range sources {
		for i, n := range src.list {
			in = append(in, located{fmt.Sprintf("%snotifications[%d]", within(src.file), i), n})
		}
	}
	if len(in) > MaxCustomNotifications {
		return nil, fmt.Errorf("a model has at most %d notifications of its own", MaxCustomNotifications)
	}
	// Every device's own, which the catalogue finds by name.
	seen := map[string]bool{coldStart.Name: true, warmStart.Name: true, authenticationFailure.Name: true}
	out := make([]Notification, 0, len(in))
	for _, l := range in {
		at, n := l.at, l.n
		if !notificationNamePattern.MatchString(n.Name) {
			return nil, fmt.Errorf("%s: \"name\" is the notification's name in its MIB, a letter and then letters, digits or hyphens", at)
		}
		if seen[n.Name] {
			return nil, fmt.Errorf("%s: %s is already one of the model's (coldStart, warmStart and authenticationFailure are every device's)", at, n.Name)
		}
		seen[n.Name] = true
		id, err := parseOID(n.OID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		if len(n.Objects) > maxNotificationObjects {
			return nil, fmt.Errorf("%s: a notification carries at most %d objects", at, maxNotificationObjects)
		}
		objects := make([]string, len(n.Objects))
		for j, name := range n.Objects {
			o, err := parseOID(name)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", at, err)
			}
			objects[j] = o.String()
		}
		out = append(out, Notification{Name: n.Name, OID: id.String(), Objects: objects})
	}
	return out, nil
}

// buildCustom makes a custom model's objects for one device. The device's seed
// moves its values and draws its MAC addresses, as a built-in model's.
func buildCustom(id Identity, sys systemGroup, ifs []iface, objs []compiledObject) []Object {
	var o objects
	addSystem(&o, id, sys.descr, sys.objectID, sys.contact, sys.location, sys.services)
	if len(ifs) > 0 {
		own := slices.Clone(ifs)
		for i := range own {
			if own[i].ifType != ifTypeSoftwareLoopback {
				own[i].mac = deviceMAC(id.Seed, uint64(own[i].index))
			}
		}
		addInterfaces(&o, id.Seed, own)
	}
	for k, c := range objs {
		swing := func(j int) Swing { return Swing{Seed: id.Seed + 1<<20 + uint64(k)<<10 + uint64(j)} }
		if len(c.instances) == 0 {
			o = append(o, Object{OID: c.oid, Type: c.typ, Value: c.reading("", swing(0)), NotifyOnly: c.notifyOnly})
			continue
		}
		for j, inst := range c.instances {
			o = append(o, Object{OID: strings.ReplaceAll(c.oid, instancePlaceholder, inst), Type: c.typ,
				Value: c.reading(inst, swing(j)), NotifyOnly: c.notifyOnly})
		}
	}
	return o
}

// custom holds the custom models every device may be made from.
var custom struct {
	sync.RWMutex
	models []model
}

// SetCustomModels makes ms the custom models, in place of those before.
//
// Process-wide, because a device names its model by ID and everything that
// resolves one — Validate, NewEngineID, a start — is a function of the device
// alone: an application runs one simulator, and so has one catalogue. A device
// whose model is no longer set does not start, and says which model it lacks.
func SetCustomModels(ms []CustomModel) error {
	list := make([]model, 0, len(ms))
	for _, c := range ms {
		if c.m.build == nil {
			return errors.New("a custom model is made by ParseCustomModel")
		}
		if slices.ContainsFunc(list, func(m model) bool { return m.ID == c.m.ID }) {
			return fmt.Errorf("two custom models are %s", c.m.ID)
		}
		list = append(list, c.m)
	}
	slices.SortStableFunc(list, func(a, b model) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	custom.Lock()
	custom.models = list
	custom.Unlock()
	return nil
}

// customModels are the custom models, by name. The slice is replaced, never
// changed, so it is read without holding the lock.
func customModels() []model {
	custom.RLock()
	defer custom.RUnlock()
	return custom.models
}
