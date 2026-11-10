package simulator

import (
	"cmp"
	"net"
	"slices"

	"github.com/gosnmp/gosnmp"
)

// ModelInfo is a device model as the interface lists it. It carries no prose:
// the interface names and describes a model from its ID, in five languages
// (simulator.model.<id>), the way preset.WidgetKinds serves i18n key suffixes
// rather than sentences. A notification is shown by its MIB name, which no
// manager translates.
type ModelInfo struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	// Notifications are what a device of the model can send.
	Notifications []NotificationInfo `json:"notifications"`
	// Custom is a model somebody wrote (ParseCustomModel). No locale knows it,
	// so it names and describes itself, in the words of its file.
	Custom      bool   `json:"custom"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Vendor      string `json:"vendor,omitempty"`
	// Params are the numbers a device of the model may be given (params.go).
	Params []ModelParam `json:"params"`
}

// Identity is what makes one device differ from another of the same model.
type Identity struct {
	// Name is sysName, and the host name wherever the device repeats it.
	Name string
	// Seed decides how the device's values move, so two devices of one model
	// do not move in step.
	Seed uint64
	// Location and Contact are the device's own sysLocation and sysContact,
	// which take the place of its model's when they are given.
	Location, Contact string
	// Params are the numbers the device was given, by ModelParam name; a
	// number it was not given is its model's (Identity.count).
	Params map[string]int
	// Wiring is where the device sits on the bench: what is cabled to its
	// ports (bench.go). Nil is a device cabled to nothing, answering as its
	// model always has.
	Wiring *Wiring
}

type model struct {
	ModelInfo
	// enterprise is the vendor's number, which the engine ID carries.
	enterprise uint32
	build      func(Identity) []Object
	// notifications are the model's own, beside the generic ones every device
	// sends.
	notifications []Notification
	// vlanContexts is an agent answering BRIDGE-MIB one VLAN at a time, as
	// Cisco IOS does (Object.VLAN).
	vlanContexts bool
	// attached are the MACs of what a device bridges for besides itself, by the
	// port they are behind — the
	// computer plugged into a desk phone — which a switch it is cabled to
	// learns on that port (bench.go).
	attached func(Identity) map[string][]net.HardwareAddr
}

// models is the catalogue, in the order the interface offers it; a new device
// is made from the first. Each says which bundled presets it feeds
// (TestEveryModelFeedsItsPresets).
var models = []model{
	linuxServer, windowsServer, dellIDRAC9,
	catalyst24, catalyst48, catalyst9300, aruba2930F, isr4331, mikrotikRB4011,
	fortiGate60F,
	unifiU6Pro,
	ipPhone,
	synologyNAS,
	apcSmartUPS, apcRackPDU,
	hpLaserJet,
	environmentProbe,
}

// Models lists the models a device can be made from: the catalogue, then the
// custom models by name.
func Models() []ModelInfo {
	all := slices.Concat(models, customModels())
	out := make([]ModelInfo, len(all))
	for i, m := range all {
		info := m.ModelInfo
		for _, n := range m.catalogue() {
			info.Notifications = append(info.Notifications, NotificationInfo{Name: n.Name, OID: n.OID})
		}
		out[i] = info
	}
	return out
}

// catalogue is every notification a device of m can send: the generic ones
// every agent has, and m's own.
func (m model) catalogue() []Notification {
	return slices.Concat([]Notification{coldStart, warmStart}, m.notifications, []Notification{authenticationFailure})
}

func findModel(id string) (model, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	for _, m := range customModels() {
		if m.ID == id {
			return m, true
		}
	}
	return model{}, false
}

// objects accumulates a model's objects without restating Object{…} each time.
type objects []Object

func (o *objects) add(oid string, t gosnmp.Asn1BER, r Reading) {
	*o = append(*o, Object{OID: oid, Type: t, Value: r})
}

// addInVLAN adds an object of one VLAN's view (Object.VLAN).
func (o *objects) addInVLAN(vlan int, oid string, t gosnmp.Asn1BER, r Reading) {
	*o = append(*o, Object{OID: oid, Type: t, Value: r, VLAN: vlan})
}

// addForNotify adds an object only a notification carries (Object.NotifyOnly).
func (o *objects) addForNotify(oid string, t gosnmp.Asn1BER, r Reading) {
	*o = append(*o, Object{OID: oid, Type: t, Value: r, NotifyOnly: true})
}

// addSystem adds the system group (RFC 3418) as a model's agent answers it.
// sysServices adds up the layers the device serves: 2 a bridge, 4 a router,
// 8 end to end, 64 applications. The contact and location a device was given
// take the place of its model's.
func addSystem(o *objects, id Identity, descr, sysObjectID, contact, location string, services int) {
	o.add("1.3.6.1.2.1.1.1.0", gosnmp.OctetString, Const(descr))
	o.add("1.3.6.1.2.1.1.2.0", gosnmp.ObjectIdentifier, Const(sysObjectID))
	o.add("1.3.6.1.2.1.1.3.0", gosnmp.TimeTicks, Uptime())
	o.add("1.3.6.1.2.1.1.4.0", gosnmp.OctetString, Const(cmp.Or(id.Contact, contact)))
	o.add("1.3.6.1.2.1.1.5.0", gosnmp.OctetString, Const(id.Name))
	o.add("1.3.6.1.2.1.1.6.0", gosnmp.OctetString, Const(cmp.Or(id.Location, location)))
	o.add("1.3.6.1.2.1.1.7.0", gosnmp.Integer, Const(services))
}
