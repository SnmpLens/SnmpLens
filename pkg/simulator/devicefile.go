package simulator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// A file of simulated devices is how a bench moves from one machine to another,
// or is passed on: one device or several, with or without their secrets — and
// saying which. It is read as a model file is, because somebody else may have
// written it: what it says it is first, then all of it and strictly. Each device
// in it is then checked as one typed in is (Device.Validate), the loopback rule
// included, by whoever gives it an ID and an engine.

const (
	// DeviceFileKind is what a file of simulated devices says it is.
	DeviceFileKind = "snmplens-simulated-devices"
	// DeviceFileFormat is the format version this package reads.
	DeviceFileFormat = 1
	// MaxDeviceFileBytes and MaxFileDevices bound a file: a bench is a few
	// dozen devices at most.
	MaxDeviceFileBytes = 1 << 20
	MaxFileDevices     = 64
)

// What a file of devices says of their secrets.
const (
	SecretsIncluded = "included"
	SecretsOmitted  = "omitted"
)

// DeviceFile is simulated devices as a file holds them.
type DeviceFile struct {
	Kind          string `json:"kind"`
	FormatVersion int    `json:"formatVersion"`
	// Secrets says whether the communities and passphrases are in the file
	// (SecretsIncluded) or were left out (SecretsOmitted), so that nobody passes
	// a file on believing it holds none when it does.
	Secrets string       `json:"secrets"`
	Devices []FileDevice `json:"devices"`
	// Cables are the cables between the devices of the file, each end naming
	// its device by its place in Devices: a device imported is given a new ID.
	Cables []FileCable `json:"cables,omitempty"`
}

// FileCable is a cable between two devices of a file.
type FileCable struct {
	A FileCableEnd `json:"a"`
	B FileCableEnd `json:"b"`
}

// FileCableEnd is one end of a cable: a device, by its place in the file from 0,
// and its port.
type FileCableEnd struct {
	Device int    `json:"device"`
	Port   string `json:"port"`
}

// FileDevice is a device as a file holds it: what makes it the device it is,
// and nothing of what makes it this machine's — its ID, its engine and its
// boots, which a device imported is given anew. Two imports of one file are
// two devices, with two engines no manager confuses.
type FileDevice struct {
	Name           string         `json:"name"`
	Model          string         `json:"model"`
	Address        string         `json:"address"`
	Port           int            `json:"port"`
	Versions       []string       `json:"versions"`
	Community      string         `json:"community,omitempty"`
	WriteCommunity string         `json:"writeCommunity,omitempty"`
	Users          []User         `json:"users,omitempty"`
	Traps          Traps          `json:"traps"`
	Location       string         `json:"location,omitempty"`
	Contact        string         `json:"contact,omitempty"`
	Params         map[string]int `json:"params,omitempty"`
	Overrides      []Override     `json:"overrides,omitempty"`
}

// ExportDevices is the file holding devices, their secrets in it or not, and
// the cables among them — a cable to a device left out of the file is left out
// with it.
func ExportDevices(devices []Device, links []Link, withSecrets bool) ([]byte, error) {
	f := DeviceFile{Kind: DeviceFileKind, FormatVersion: DeviceFileFormat, Secrets: SecretsOmitted,
		Devices: make([]FileDevice, len(devices))}
	if withSecrets {
		f.Secrets = SecretsIncluded
	}
	for i, d := range devices {
		if !withSecrets {
			d = d.WithoutSecrets()
		}
		traps := d.Traps.clone()
		if traps.Destinations == nil {
			traps.Destinations = []Destination{}
		}
		if traps.Schedules == nil {
			traps.Schedules = []Schedule{}
		}
		f.Devices[i] = FileDevice{Name: d.Name, Model: d.Model, Address: d.Address, Port: d.Port,
			Versions: slices.Clone(d.Versions), Community: d.Community, WriteCommunity: d.WriteCommunity,
			Users: slices.Clone(d.Users), Traps: traps,
			Location: d.Location, Contact: d.Contact, Params: maps.Clone(d.Params), Overrides: slices.Clone(d.Overrides)}
	}
	place := make(map[string]int, len(devices))
	for i, d := range devices {
		place[d.ID] = i
	}
	for _, l := range links {
		a, okA := place[l.A.Device]
		b, okB := place[l.B.Device]
		if okA && okB {
			f.Cables = append(f.Cables, FileCable{A: FileCableEnd{a, l.A.Port}, B: FileCableEnd{b, l.B.Port}})
		}
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ParseDeviceFile reads a file of simulated devices: what it says it is, then
// all of it, refusing a field the format does not define.
func ParseDeviceFile(raw []byte) (DeviceFile, error) {
	if len(raw) > MaxDeviceFileBytes {
		return DeviceFile{}, fmt.Errorf("a file of devices is at most %d KB", MaxDeviceFileBytes>>10)
	}
	raw = bytes.TrimPrefix(raw, bom)
	var head struct {
		Kind          string `json:"kind"`
		FormatVersion int    `json:"formatVersion"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&head); err != nil {
		return DeviceFile{}, jsonProblem(raw, err)
	}
	if head.Kind != DeviceFileKind {
		return DeviceFile{}, fmt.Errorf("this is not a file of simulated devices: its \"kind\" is %q, and one's is %q",
			head.Kind, DeviceFileKind)
	}
	if head.FormatVersion != DeviceFileFormat {
		return DeviceFile{}, fmt.Errorf("format version %d: this version of SnmpLens reads version %d",
			head.FormatVersion, DeviceFileFormat)
	}
	var f DeviceFile
	if err := decodeStrict(raw, &f); err != nil {
		return DeviceFile{}, err
	}
	if f.Secrets != SecretsIncluded && f.Secrets != SecretsOmitted {
		return DeviceFile{}, fmt.Errorf("\"secrets\" says whether the passwords are in the file: %q or %q",
			SecretsIncluded, SecretsOmitted)
	}
	switch {
	case len(f.Devices) == 0:
		return DeviceFile{}, errors.New("the file holds no device")
	case len(f.Devices) > MaxFileDevices:
		return DeviceFile{}, fmt.Errorf("the file holds %d devices, and one is read with at most %d", len(f.Devices), MaxFileDevices)
	}
	for i, c := range f.Cables {
		for _, end := range []FileCableEnd{c.A, c.B} {
			if end.Device < 0 || end.Device >= len(f.Devices) || strings.TrimSpace(end.Port) == "" {
				return DeviceFile{}, fmt.Errorf("cable %d names a device the file does not hold, or no port", i+1)
			}
		}
		if c.A.Device == c.B.Device {
			return DeviceFile{}, fmt.Errorf("cable %d joins a device to itself", i+1)
		}
	}
	return f, nil
}

// Device is the device fd describes, yet to be given an ID and an engine.
func (fd FileDevice) Device() Device {
	return Device{Name: strings.TrimSpace(fd.Name), Model: fd.Model, Address: strings.TrimSpace(fd.Address),
		Port: fd.Port, Versions: slices.Clone(fd.Versions), Community: fd.Community, WriteCommunity: fd.WriteCommunity,
		Users: slices.Clone(fd.Users), Traps: fd.Traps.clone(),
		Location: strings.TrimSpace(fd.Location), Contact: strings.TrimSpace(fd.Contact),
		Params: maps.Clone(fd.Params), Overrides: slices.Clone(fd.Overrides)}
}

// ValidateWithoutSecrets is Validate for a device whose secrets are yet to be
// given: everything else about it is checked, and a secret it lacks is not an
// error. A device imported from a file that left its secrets out is checked so,
// and kept; it starts once it is given them.
func (d Device) ValidateWithoutSecrets() error {
	const standIn = "stand-in-secret"
	c := d.WithoutSecrets()
	c.Community = standIn
	for i := range c.Users {
		c.Users[i].AuthPass, c.Users[i].PrivPass = standIn, standIn
	}
	for i := range c.Traps.Destinations {
		c.Traps.Destinations[i].Community = standIn
	}
	return c.Validate()
}
