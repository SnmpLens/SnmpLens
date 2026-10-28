package mib

// Imports gosmi satisfies without the module they name.
//
// gosmi rewrites some imports from the SMIv1 modules to the modules that
// replaced them (smi/internal/import.go, importConversions, gosmi v0.4.4):
// `DisplayString FROM RFC1213-MIB` is read as SNMPv2-TC's DisplayString, and
// RFC1213-MIB need not exist. And the SMIv1 macros — OBJECT-TYPE from RFC-1212
// or RFC1155-SMI, TRAP-TYPE from RFC-1215 — are keywords of its grammar, never
// looked up. SnmpLens's own checks knew neither, so a vendor MIB that loaded
// with every type resolved was reported by the editor as importing three
// modules that were "not loaded; add its MIB file" — the loader and the editor
// contradicting each other over the same file.
//
// Only what gosmi actually rewrites. `ifInOctets FROM RFC1213-MIB` is NOT
// rewritten and does need RFC1213-MIB, so a module that is merely "one of the
// SMIv1 ones" is not enough. Nor is NetworkAddress, which RFC1155-SMI defines
// as a CHOICE: gosmi gives a CHOICE no type, with or without the file, and the
// load says so.
var gosmiConverted = map[string]map[string]bool{
	"RFC1155-SMI": set("internet", "directory", "mgmt", "experimental", "private", "enterprises",
		"IpAddress", "Counter", "Gauge", "TimeTicks", "Opaque", "OBJECT-TYPE"),
	"RFC1065-SMI": set("internet", "directory", "mgmt", "experimental", "private", "enterprises",
		"IpAddress", "Counter", "Gauge", "TimeTicks", "Opaque", "OBJECT-TYPE"),
	"RFC-1212": set("OBJECT-TYPE"),
	"RFC-1215": set("TRAP-TYPE"),
	"RFC1213-MIB": set("mib-2", "DisplayString", "PhysAddress",
		"ifAdminStatus", "ifDescr", "ifEntry", "ifInErrors", "ifIndex", "ifOperStatus",
		"ifOutDiscards", "ifOutErrors", "ifPhysAddress", "interfaces",
		"ip", "ipAdEntAddr", "ipAddrEntry", "ipNetToMediaIfIndex", "ipNetToMediaNetAddress",
		"ipNetToMediaPhysAddress", "ipRouteDest",
		"snmp", "sysContact", "sysDescr", "sysLocation", "sysName", "sysObjectID", "sysUpTime", "system",
		"tcpConnLocalAddress", "tcpConnLocalPort", "tcpConnRemAddress", "tcpConnRemPort", "transmission"),
}

// gosmiLost is what gosmi rewrites to a module that does not define it.
// `ipRouteDest FROM RFC1213-MIB` becomes IP-MIB's ipRouteDest, and IP-MIB has
// no route table: RFC 1213 is the only module that ever defined one. The import
// resolves to nothing whichever MIBs are loaded, RFC1213-MIB included, so the
// editor says that rather than asking for a file that would change nothing.
// gosmicontract_test.go fails the day gosmi resolves it.
var gosmiLost = map[string]map[string]bool{
	"RFC1213-MIB": set("ipRouteDest"),
}

func set(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// notConverted is what an import from module still needs module itself for.
// ok is false when gosmi rewrites nothing from it, which makes every name
// count.
func notConverted(module string, names []string) (needed []string, ok bool) {
	converted, ok := gosmiConverted[module]
	if !ok {
		return names, false
	}
	for _, n := range names {
		if !converted[n] {
			needed = append(needed, n)
		}
	}
	return needed, true
}
