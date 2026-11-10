package simulator

import (
	"fmt"
	"net/netip"
	"time"
)

// windowsServer is a Windows Server 2022 host with the SNMP service, as a walk
// of one finds it: the system group as Windows answers it; the loopback, two
// adapters and the pseudo-interfaces Windows lists beside them, down; its
// address, routes and sockets — RDP, SMB, WinRM; and the host resources: its
// drives by letter, its memory, four processors numbered as Windows numbers
// them, its devices, the processes running and the programs installed. It fits
// the "Interfaces" and "Host resources" presets.
var windowsServer = model{
	ModelInfo:     ModelInfo{ID: "windows-server", Category: "server"},
	enterprise:    311, // Microsoft
	build:         buildWindowsServer,
	notifications: []Notification{linkNotification(false, 7), linkNotification(true, 6)},
}

func buildWindowsServer(id Identity) []Object {
	var o objects
	addSystem(&o, id,
		"Hardware: Intel64 Family 6 Model 85 Stepping 7 AT/AT COMPATIBLE - Software: Windows Version 6.3 (Build 20348 Multiprocessor Free)",
		".1.3.6.1.4.1.311.1.1.3.1.2", // a Windows server
		"Administrator", "Server room, rack B2", 76)
	lan := lanPrefix(id.Seed)
	peer := func(n int, port uint16) netip.AddrPort { return netip.AddrPortFrom(hostIn(lan, n), port) }
	ifs := []iface{
		{index: 1, descr: "Software Loopback Interface 1", name: "loopback_0", ifType: ifTypeSoftwareLoopback,
			mtu: 1500, speed: 1_073_741_824, up: true},
		{index: 6, descr: "Intel(R) Ethernet Server Adapter I350-T2", name: "ethernet_32768", ifType: ifTypeEthernet,
			mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, 1), up: true,
			inRate: 2_100_000, outRate: 4_600_000, errorRate: 0.001},
		{index: 7, descr: "Intel(R) Ethernet Server Adapter I350-T2 #2", name: "ethernet_32769", ifType: ifTypeEthernet,
			mtu: 1500, speed: 1_000_000_000, mac: deviceMAC(id.Seed, 2)},
	}
	// The miniports and tunnels Windows always lists, none of them carrying
	// anything.
	for i, p := range []struct {
		descr, name string
		kind        int
	}{
		{"WAN Miniport (SSTP)", "wman_0", ifTypePPP},
		{"WAN Miniport (IKEv2)", "wman_1", ifTypePPP},
		{"WAN Miniport (L2TP)", "wman_2", ifTypePPP},
		{"WAN Miniport (PPTP)", "wman_3", ifTypePPP},
		{"WAN Miniport (IP)", "ethernet_0", ifTypeEthernet},
		{"WAN Miniport (IPv6)", "ethernet_1", ifTypeEthernet},
		{"WAN Miniport (Network Monitor)", "ethernet_2", ifTypeEthernet},
		{"Microsoft Kernel Debug Network Adapter", "ethernet_3", ifTypeEthernet},
		{"Teredo Tunneling Pseudo-Interface", "tunnel_0", ifTypeTunnel},
		{"Microsoft IP-HTTPS Platform Interface", "tunnel_1", ifTypeTunnel},
	} {
		index := []int{2, 3, 4, 5, 8, 9, 10, 11, 12, 13}[i]
		ifs = append(ifs, iface{index: index, descr: p.descr, name: p.name, ifType: p.kind, mtu: 1500})
	}
	addStack(&o, id, stack{
		seed:    id.Seed,
		ifs:     ifs,
		addrs:   []ifAddr{{ifIndex: 6, prefix: addrIn(lan, 12+int(id.Seed%40)), neighbours: 5}},
		gateway: hostIn(lan, 1),
		ttl:     128,
		pps:     2600,
		listen:  []uint16{80, 135, 139, 445, 3389, 5985, 47001, 49664, 49665, 49666, 49667, 49668},
		udp:     []uint16{123, 137, 138, 161, 500, 3389, 4500, 5353},
		sessions: []session{
			{3389, peer(50, 52877)}, // an administrator's remote desktop
			{445, peer(64, 50412)},  // a file share in use
			{445, peer(71, 61023)},
			{80, peer(88, 58113)},
		},
		modules: []sysOR{moduleHostResources},
	})

	// A volume serial number, as Windows writes one into hrStorageDescr.
	serial := func(n uint64) string { return fmt.Sprintf("%08x", uint32(mix(id.Seed+n))) }
	addHostResources(&o, id.Seed, hostResources{
		memoryKiB: 16777216, // 16 GiB
		users:     [2]float64{1, 3},
		storage: []storageArea{
			{1, hrStorageFixedDisk, `C:\ Label:  Serial Number ` + serial(1), 4096, 32768000, 0.46, 0.52, 3 * time.Hour},
			{2, hrStorageFixedDisk, `D:\ Label:Data  Serial Number ` + serial(2), 4096, 131072000, 0.61, 0.62, 12 * time.Hour},
			{3, hrStorageVirtualMemory, "Virtual Memory", 65536, 327680, 0.35, 0.55, 6 * time.Minute},
			{4, hrStorageRam, "Physical Memory", 65536, 262144, 0.5, 0.7, 4 * time.Minute},
		},
		processors: []int{5, 6, 7, 8},
		cpuLo:      3,
		cpuHi:      45,
		cpu:        "Intel(R) Xeon(R) Gold 6230 CPU @ 2.10GHz",
		boot:       3,
		devices: []hrDevice{
			{index: 1, kind: hrDevicePrinter, descr: "Microsoft Print To PDF"},
			{index: 2, kind: hrDevicePrinter, descr: "Microsoft XPS Document Writer v4"},
			{index: 3, kind: hrDeviceDiskStorage, descr: "Fixed Disk", diskKB: 134217728},
			{index: 4, kind: hrDeviceDiskStorage, descr: "Fixed Disk", diskKB: 536870912},
			{index: 9, kind: hrDeviceNetwork, descr: "Software Loopback Interface 1", ifIndex: 1},
			{index: 10, kind: hrDeviceNetwork, descr: "Intel(R) Ethernet Server Adapter I350-T2", ifIndex: 6},
			{index: 11, kind: hrDeviceNetwork, descr: "Intel(R) Ethernet Server Adapter I350-T2 #2", ifIndex: 7},
		},
		fs: []hrFS{
			{`C:\`, hrFSNTFS, 1, true},
			{`D:\`, hrFSNTFS, 2, false},
		},
		procs:    windowsProcesses(),
		software: windowsPrograms(),
	})
	return o
}

// windowsProcesses is what Windows Server runs with IIS and remote desktop in
// use, named and pathed as the SNMP service reports them.
func windowsProcesses() []process {
	const sys = `C:\Windows\system32\`
	type p struct {
		pid        int
		name, args string
		cpu        float64
		memKB      int
	}
	procs := []process{{pid: 4, name: "System", kind: 2, cpu: 0.02, memKB: 144}}
	for _, x := range []p{
		{92, "Registry", "", 0, 72000},
		{316, "smss.exe", "", 0, 1200},
		{440, "csrss.exe", "", 0.001, 5400},
		{516, "wininit.exe", "", 0, 6800},
		{528, "csrss.exe", "", 0.001, 5900},
		{600, "winlogon.exe", "", 0, 11200},
		{640, "services.exe", "", 0.001, 10400},
		{656, "lsass.exe", "", 0.003, 24100},
		{760, "svchost.exe", "-k DcomLaunch -p", 0.002, 31500},
		{784, "fontdrvhost.exe", "", 0, 3900},
		{812, "svchost.exe", "-k RPCSS -p", 0.001, 12700},
		{868, "svchost.exe", "-k LocalServiceNoNetwork -p", 0, 7200},
		{932, "dwm.exe", "", 0.004, 61000},
		{968, "svchost.exe", "-k netsvcs -p -s Schedule", 0.001, 21400},
		{1004, "svchost.exe", "-k LocalServiceNetworkRestricted -p", 0.001, 14800},
		{1072, "svchost.exe", "-k NetworkService -p", 0.002, 18900},
		{1120, "svchost.exe", "-k LocalSystemNetworkRestricted -p", 0.001, 16300},
		{1236, "svchost.exe", "-k netsvcs -p -s Winmgmt", 0.004, 19700},
		{1392, "spoolsv.exe", "", 0, 16900},
		{1476, "svchost.exe", "-k appmodel -p -s StateRepository", 0.001, 12100},
		{1528, "svchost.exe", "-k utcsvc -p", 0.001, 24300},
		{1600, "MsMpEng.exe", "", 0.012, 212000},
		{1648, "snmp.exe", "", 0.003, 8900},
		{1692, "svchost.exe", "-k iissvcs", 0.001, 20100},
		{1736, "svchost.exe", "-k termsvcs -s TermService", 0.002, 18400},
		{1804, "vmtoolsd.exe", "", 0.002, 25400},
		{1860, "svchost.exe", "-k NetworkServiceNetworkRestricted -p -s PolicyAgent", 0, 8800},
		{2012, "WmiPrvSE.exe", "", 0.003, 19600},
		{2196, "dllhost.exe", "/Processid:{02D4B3F1-FD88-11D1-960D-00805FC79235}", 0, 12300},
		{2308, "msdtc.exe", "", 0, 9200},
		{2544, "NisSrv.exe", "", 0.001, 11300},
		{2780, "w3wp.exe", `-ap "DefaultAppPool" -v "v4.0" -l "webengine4.dll" -a \\.\pipe\iisipm`, 0.03, 148000},
		{2904, "w3wp.exe", `-ap "Intranet" -v "v4.0" -l "webengine4.dll" -a \\.\pipe\iisipm`, 0.02, 121000},
		{3120, "svchost.exe", "-k UnistackSvcGroup", 0, 9700},
		{3368, "csrss.exe", "", 0.001, 6200},
		{3412, "winlogon.exe", "", 0, 10600},
		{3500, "rdpclip.exe", "", 0, 9800},
		{3544, "sihost.exe", "", 0, 21800},
		{3600, "svchost.exe", "-k UserSvcGroup", 0.001, 14700},
		{3648, "taskhostw.exe", "", 0, 11700},
		{3780, "explorer.exe", "", 0.003, 96000},
		{3912, "ctfmon.exe", "", 0, 12900},
		{4080, "ServerManager.exe", "", 0.002, 118000},
		{4216, "RuntimeBroker.exe", "", 0, 18200},
		{4388, "conhost.exe", "0x4", 0, 6400},
		{4452, "powershell.exe", "", 0.001, 71000},
	} {
		procs = append(procs, process{pid: x.pid, name: x.name, path: sys + x.name, args: x.args, kind: 4, cpu: x.cpu, memKB: x.memKB})
	}
	return procs
}

// windowsPrograms are what the server lists as installed.
func windowsPrograms() []software {
	base := time.Date(2023, 11, 8, 10, 4, 0, 0, time.UTC)
	var out []software
	for i, name := range []string{
		"Microsoft Visual C++ 2015-2022 Redistributable (x64) - 14.38.33135",
		"Microsoft Visual C++ 2015-2022 Redistributable (x86) - 14.38.33135",
		"VMware Tools",
		"Microsoft Edge",
		"Microsoft Edge Update",
		"7-Zip 23.01 (x64)",
		"Notepad++ (64-bit x64)",
		"PowerShell 7-x64",
		"Microsoft Web Deploy 4.0",
		"IIS URL Rewrite Module 2",
		"Microsoft .NET Runtime - 8.0.8 (x64)",
		"Microsoft ASP.NET Core 8.0.8 - Shared Framework (x64)",
	} {
		out = append(out, software{name: name, installed: base.Add(time.Duration(i) * 97 * time.Hour)})
	}
	return out
}
