package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"SnmpLens/pkg/secrets"
	"SnmpLens/pkg/simulator"

)

// simulatorFile holds the simulated devices, beside monitoring.db, and not one
// secret: communities and passphrases go to pkg/secrets under
// SimulatorDeviceRef. It is the file a person copies to move their devices to
// another machine, which is exactly why.
const simulatorFile = "simulator.json"

const simulatorFileVersion = 1

type simulatorFileContent struct {
	FormatVersion int                `json:"formatVersion"`
	Devices       []simulator.Device `json:"devices"`
}

// simulatorService holds the simulated devices and runs them. devices never
// holds a secret: they are read from the credential store when a device starts
// and when the renderer asks for them by name (SimulatorDeviceCredentials).
type simulatorService struct {
	mu      sync.Mutex
	path    string
	devices []simulator.Device
	fleet   *simulator.Fleet
	// modelDir holds the custom models (simmodels.go), and models is what
	// it held when it was last read.
	modelDir string
	models   []simulator.CustomModel
	// rec is the recording under way, if one is (simrecord.go).
	rec recorder
}

func newSimulatorService(dir string) *simulatorService {
	s := &simulatorService{
		path:     filepath.Join(dir, simulatorFile),
		modelDir: filepath.Join(dir, simulatorModelSubdir),
		fleet:    simulator.NewFleet(),
	}
	s.load()
	// The custom models before any device is validated or started: a device
	// names its model by ID.
	s.loadModels()
	return s
}

func (s *simulatorService) load() {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("simulator: cannot read %s: %v", s.path, err)
		return
	}
	var content simulatorFileContent
	if err := json.Unmarshal(raw, &content); err != nil || content.FormatVersion != simulatorFileVersion {
		// Set aside rather than overwritten by the next save: a file this version
		// cannot read may be one a later version wrote, or one a person can mend.
		aside := s.path + ".unreadable"
		if rerr := os.Rename(s.path, aside); rerr != nil {
			log.Printf("simulator: %s is unreadable and could not be set aside: %v", s.path, rerr)
		} else {
			log.Printf("simulator: %s is unreadable (%v); set aside as %s", s.path, err, aside)
		}
		return
	}
	// WithoutSecrets even here: a secret someone typed into the file by hand is
	// not one this application will send, or keep.
	for _, d := range content.Devices {
		s.devices = append(s.devices, d.WithoutSecrets())
	}
}

// write replaces the file with devices, through a temporary file so that a
// crash mid-write cannot leave half of one behind.
func (s *simulatorService) write(devices []simulator.Device) error {
	bare := make([]simulator.Device, len(devices))
	for i, d := range devices {
		bare[i] = d.WithoutSecrets()
	}
	raw, err := json.MarshalIndent(simulatorFileContent{FormatVersion: simulatorFileVersion, Devices: bare}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *simulatorService) index(id string) int {
	return slices.IndexFunc(s.devices, func(d simulator.Device) bool { return d.ID == id })
}

// SimulatedDevice is a simulated device as the interface lists it. It has no
// field that could hold a secret — rather than a Device with them blanked, so a
// secret field added to Device later cannot reach the renderer by default.
type SimulatedDevice struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Model       string          `json:"model"`
	Address     string          `json:"address"`
	Port        int             `json:"port"`
	Versions    []string        `json:"versions"`
	Users       []SimulatedUser `json:"users"`
	EngineID    string          `json:"engineId"`
	EngineBoots uint32          `json:"engineBoots"`
	Running     bool            `json:"running"`
	// Packets is what the device has received since it started.
	Packets uint32         `json:"packets"`
	Traps   SimulatedTraps `json:"traps"`
	// Location, Contact and AutoStart are the device's own settings, and
	// Faults what it is made to do wrong. None of it is a secret.
	Location  string           `json:"location"`
	Contact   string           `json:"contact"`
	AutoStart bool             `json:"autoStart"`
	Faults    simulator.Faults `json:"faults"`
	// Params and Overrides are the numbers its model was given and the
	// values it answers in place of its model's.
	Params    map[string]int       `json:"params"`
	Overrides []simulator.Override `json:"overrides"`
}

// SimulatedTraps is what a simulated device sends, as the list shows it: the
// destinations without their communities, each with what it has been sent since
// the device started.
type SimulatedTraps struct {
	Destinations  []SimulatedDestination `json:"destinations"`
	OnStart       bool                   `json:"onStart"`
	OnAuthFailure bool                   `json:"onAuthFailure"`
	Schedules     []simulator.Schedule   `json:"schedules"`
	// Suppressed counts what the device's cap held back.
	Suppressed uint32 `json:"suppressed"`
}

// SimulatedDestination is a destination without its community.
type SimulatedDestination struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Version   string `json:"version"`
	Inform    bool   `json:"inform"`
	User      string `json:"user"`
	EngineID  string `json:"engineId"`
	Sent      uint32 `json:"sent"`
	Failed    uint32 `json:"failed"`
	Dropped   uint32 `json:"dropped"`
	LastError string `json:"lastError"`
}

// SimulatedUser is an SNMPv3 user of a simulated device, without passphrases.
type SimulatedUser struct {
	Name      string `json:"name"`
	SecLevel  string `json:"secLevel"`
	AuthProto string `json:"authProto"`
	PrivProto string `json:"privProto"`
	Write     bool   `json:"write"`
}

func (s *simulatorService) view(d simulator.Device) SimulatedDevice {
	users := make([]SimulatedUser, len(d.Users))
	for i, u := range d.Users {
		users[i] = SimulatedUser{Name: u.Name, SecLevel: u.SecLevel, AuthProto: u.AuthProto, PrivProto: u.PrivProto,
			Write: u.Write}
	}
	stats, running := s.fleet.Status(d.ID)
	traps := SimulatedTraps{
		Destinations:  make([]SimulatedDestination, len(d.Traps.Destinations)),
		OnStart:       d.Traps.OnStart,
		OnAuthFailure: d.Traps.OnAuthFailure,
		Schedules:     append([]simulator.Schedule{}, d.Traps.Schedules...),
		Suppressed:    stats.Suppressed,
	}
	for i, dest := range d.Traps.Destinations {
		v := SimulatedDestination{ID: dest.ID, Host: dest.Host, Port: dest.Port, Version: dest.Version,
			Inform: dest.Inform, User: dest.User, EngineID: dest.EngineID}
		for _, st := range stats.Notifications {
			if st.ID == dest.ID {
				v.Sent, v.Failed, v.Dropped, v.LastError = st.Sent, st.Failed, st.Dropped, st.LastError
			}
		}
		traps.Destinations[i] = v
	}
	return SimulatedDevice{
		ID: d.ID, Name: d.Name, Model: d.Model, Address: d.Address, Port: d.Port,
		Versions: slices.Clone(d.Versions), Users: users,
		EngineID: d.EngineID, EngineBoots: d.EngineBoots,
		Running: running, Packets: stats.Packets, Traps: traps,
		Location: d.Location, Contact: d.Contact, AutoStart: d.AutoStart, Faults: d.Faults,
		Params: maps.Clone(d.Params), Overrides: append([]simulator.Override{}, d.Overrides...),
	}
}

// emitSimulatorChanged tells the renderer to list the devices again.
func (a *App) emitSimulatorChanged() {
	// No context is a test's App, and the Wails runtime answers a nil context
	// by ending the process.
	a.emit("simulator:changed")
}

var errNoSimulator = errors.New("the simulator is not running in this session")

// ListSimulatorModels lists the models a simulated device can be made from.
func (a *App) ListSimulatorModels() []simulator.ModelInfo {
	return simulator.Models()
}

// ListSimulatedDevices lists the simulated devices, and which are running.
//
// Named List… for tools/genbridge.mjs, which gives such a binding an empty
// ARRAY as its screenshot fixture and anything else null.
func (a *App) ListSimulatedDevices() []SimulatedDevice {
	out := []SimulatedDevice{}
	s := a.sim
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		out = append(out, s.view(d))
	}
	return out
}

// SimulatorAddress is where a new simulated device could answer.
type SimulatorAddress struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// SimulatorSuggestAddress proposes where a new device answers: an address and
// port no other device is configured on, that nothing holds right now, and that
// an ordinary user may bind.
//
// An address of its own first, so the device is a target without a port — and,
// once devices send traps, tells its traps apart by their source. Windows and
// Linux answer on the whole of 127.0.0.0/8; macOS on 127.0.0.1 and on the aliases
// someone added, and trying to bind is how to find out which. Failing that,
// 127.0.0.1 at the next free port: a target names its port ("127.0.0.1:1162"),
// so each device is still a target of its own. A port below 1024 needs
// privileges everywhere but Windows.
func (a *App) SimulatorSuggestAddress() SimulatorAddress {
	s := a.sim
	var taken []string
	if s != nil {
		s.mu.Lock()
		for _, d := range s.devices {
			taken = append(taken, d.Listen())
		}
		s.mu.Unlock()
	}
	return suggestAddress(goruntime.GOOS, taken, canBindUDP)
}

func suggestAddress(goos string, taken []string, free func(listen string) bool) SimulatorAddress {
	usable := func(address string, port int) bool {
		listen := net.JoinHostPort(address, strconv.Itoa(port))
		return !slices.Contains(taken, listen) && free(listen)
	}
	port := 1161
	if goos == "windows" {
		port = 161
	}
	for host := 2; host < 255; host++ {
		if address := "127.0.0." + strconv.Itoa(host); usable(address, port) {
			return SimulatorAddress{address, port}
		}
	}
	for p := port; p < port+100; p++ {
		if usable("127.0.0.1", p) {
			return SimulatorAddress{"127.0.0.1", p}
		}
	}
	return SimulatorAddress{"127.0.0.1", port}
}

func canBindUDP(listen string) bool {
	c, err := net.ListenPacket("udp", listen)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// SimulatorSaveDevice creates or updates a simulated device, and returns it as
// the list shows it.
//
// A new device is given its ID and engine ID here, never by the renderer; an
// existing one keeps both, and its boot count. One that is running restarts, so
// it answers as configured.
func (a *App) SimulatorSaveDevice(d simulator.Device) (SimulatedDevice, error) {
	s := a.sim
	if s == nil {
		return SimulatedDevice{}, errNoSimulator
	}
	if a.secrets == nil {
		return SimulatedDevice{}, errors.New("the credential store is unavailable, so a simulated device cannot keep its community and passphrases")
	}
	d.Name = strings.TrimSpace(d.Name)
	d.Address = strings.TrimSpace(d.Address)
	// A destination is given its ID here, as a device is: its community is kept
	// under it.
	d.Traps.Destinations = slices.Clone(d.Traps.Destinations)
	for i := range d.Traps.Destinations {
		dest := &d.Traps.Destinations[i]
		dest.Host = strings.TrimSpace(dest.Host)
		if dest.ID == "" {
			dest.ID = simulator.NewDestinationID()
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	i := -1
	if d.ID == "" {
		d.ID = simulator.NewDeviceID()
		d.EngineBoots = 0
		engineID, err := simulator.NewEngineID(d.Model, d.ID)
		if err != nil {
			return SimulatedDevice{}, err
		}
		d.EngineID = engineID
	} else if i = s.index(d.ID); i < 0 {
		return SimulatedDevice{}, fmt.Errorf("there is no simulated device %q", d.ID)
	} else {
		// The faults too: they are set while the device runs, never in the editor.
		d.EngineID, d.EngineBoots, d.Faults = s.devices[i].EngineID, s.devices[i].EngineBoots, s.devices[i].Faults
	}
	if err := d.Validate(); err != nil {
		return SimulatedDevice{}, err
	}
	for j, other := range s.devices {
		if j != i && other.Listen() == d.Listen() {
			return SimulatedDevice{}, fmt.Errorf("%q already answers on %s", other.Name, d.Listen())
		}
	}

	// The secrets FIRST: a file naming a device whose secrets were never kept
	// would start it with none.
	raw, err := json.Marshal(d.Secrets())
	if err != nil {
		return SimulatedDevice{}, err
	}
	ref := secrets.SimulatorDeviceRef(d.ID)
	if err := a.secrets.Set(ref, string(raw)); err != nil {
		return SimulatedDevice{}, fmt.Errorf("keeping the device's credentials: %w", err)
	}
	next := slices.Clone(s.devices)
	if i < 0 {
		next = append(next, d.WithoutSecrets())
	} else {
		next[i] = d.WithoutSecrets()
	}
	if err := s.write(next); err != nil {
		if i < 0 {
			_ = a.secrets.Delete(ref)
		}
		return SimulatedDevice{}, fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.devices = next
	if i < 0 {
		i = len(next) - 1
	}

	var restartErr error
	if s.fleet.Stop(d.ID) {
		restartErr = a.startSimulatedLocked(s, i)
	}
	a.emitSimulatorChanged()
	return s.view(s.devices[i]), restartErr
}

// SimulatorDeleteDevice stops a simulated device and forgets it, secrets
// included.
func (a *App) SimulatorDeleteDevice(id string) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return nil
	}
	s.fleet.Stop(id)
	next := slices.Delete(slices.Clone(s.devices), i, i+1)
	if err := s.write(next); err != nil {
		return fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.devices = next
	if a.secrets != nil {
		if err := a.secrets.Delete(secrets.SimulatorDeviceRef(id)); err != nil {
			log.Printf("simulator: the credentials of deleted device %s remain: %v", id, err)
		}
	}
	a.emitSimulatorChanged()
	return nil
}

// SimulatorStartDevice starts a simulated device.
func (a *App) SimulatorStartDevice(id string) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("there is no simulated device %q", id)
	}
	err := a.startSimulatedLocked(s, i)
	a.emitSimulatorChanged()
	return err
}

// SimulatorStopDevice stops a simulated device.
func (a *App) SimulatorStopDevice(id string) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	if s.fleet.Stop(id) {
		a.emitSimulatorChanged()
	}
	return nil
}

// SimulatorSetFaults changes what a simulated device does wrong, at once and
// without restarting it — its uptime and counters are what a fault is tested
// against — and keeps it with the device, for its next start as well.
func (a *App) SimulatorSetFaults(id string, f simulator.Faults) error {
	s := a.sim
	if s == nil {
		return errNoSimulator
	}
	if err := f.Check(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return fmt.Errorf("there is no simulated device %q", id)
	}
	next := slices.Clone(s.devices)
	next[i].Faults = f
	if err := s.write(next); err != nil {
		return fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.devices = next
	if err := s.fleet.SetFaults(id, f); err != nil && !errors.Is(err, simulator.ErrNotRunning) {
		return err
	}
	a.emitSimulatorChanged()
	return nil
}

// startAutoSimulated starts the devices that start with the application, once
// the credential store is open. One that will not start is logged and left
// stopped, and the others start.
func (a *App) startAutoSimulated() {
	s := a.sim
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	started := false
	for i, d := range s.devices {
		if !d.AutoStart {
			continue
		}
		if err := a.startSimulatedLocked(s, i); err != nil {
			log.Printf("simulator: %s did not start with the application: %v", d.Name, err)
			continue
		}
		started = true
	}
	if started {
		a.emitSimulatorChanged()
	}
}

// SimulatorSendTrap has a running simulated device send a notification now to
// each of its destinations, and reports what became of it at each — for an
// INFORM, once the receiver has answered or not.
func (a *App) SimulatorSendTrap(id, notification string) ([]simulator.Delivery, error) {
	s := a.sim
	if s == nil {
		return []simulator.Delivery{}, errNoSimulator
	}
	out, err := s.fleet.Notify(id, notification)
	if out == nil {
		out = []simulator.Delivery{}
	}
	return out, err
}

// SimulatorDeviceCredentials returns a device's community and passphrases: for
// the editor, and for adding the device as a target, which has to reach it with
// them. The one path by which they come back out of the credential store.
func (a *App) SimulatorDeviceCredentials(id string) (simulator.DeviceSecrets, error) {
	s := a.sim
	if s == nil {
		return simulator.DeviceSecrets{}, errNoSimulator
	}
	s.mu.Lock()
	known := s.index(id) >= 0
	s.mu.Unlock()
	if !known {
		return simulator.DeviceSecrets{}, fmt.Errorf("there is no simulated device %q", id)
	}
	return a.simulatorSecrets(id)
}

func (a *App) simulatorSecrets(id string) (simulator.DeviceSecrets, error) {
	out := simulator.DeviceSecrets{Users: map[string]simulator.UserSecrets{}}
	if a.secrets == nil {
		return out, errors.New("the credential store is unavailable")
	}
	raw, err := a.secrets.Get(secrets.SimulatorDeviceRef(id))
	if errors.Is(err, secrets.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("reading the device's credentials: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, fmt.Errorf("reading the device's credentials: %w", err)
	}
	return out, nil
}

// startSimulatedLocked starts device i with its secrets. Its boots are raised
// and KEPT first: a device starting again with the same count would put every
// message recorded during its previous run back inside its time window.
func (a *App) startSimulatedLocked(s *simulatorService, i int) error {
	sec, err := a.simulatorSecrets(s.devices[i].ID)
	if err != nil {
		return err
	}
	next := slices.Clone(s.devices)
	next[i].EngineBoots++
	if err := s.write(next); err != nil {
		return fmt.Errorf("saving the simulated devices: %w", err)
	}
	s.devices = next
	return s.fleet.Start(next[i].WithSecrets(sec))
}
