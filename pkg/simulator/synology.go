package simulator

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/gosnmp/gosnmp"
)

// synologyNAS is a DS920+ running DSM, whose agent is net-snmp — so its
// sysObjectID is net-snmp's Linux one, as a real DiskStation's is — with
// Synology's own system, disk and RAID tables beside the host resources, UCD's
// memory and disks, and a file server's sockets: SMB, AFP, NFS, DSM.
// It has four disks unless it is given another number: one to nine, which is
// what a DS920+ holds with a DX517 expansion unit beside it.
var synologyNAS = model{
	ModelInfo: ModelInfo{ID: "synology-nas", Category: "storage",
		Params: []ModelParam{{Name: "disks", Min: 1, Max: 9, Default: 4}}},
	enterprise:    8072, // net-snmp
	build:         buildSynologyNAS,
	notifications: []Notification{linkNotification(false, 3), linkNotification(true, 2)},
}

func buildSynologyNAS(id Identity) []Object {
	var o objects
	addSystem(&o, id, fmt.Sprintf("Linux %s 4.4.302+ #69057 SMP Fri Jan 12 17:02:28 CST 2024 x86_64", id.Name),
		".1.3.6.1.4.1.8072.3.2.10", "admin@"+id.Name, "Office, storage shelf", 76)
	lan := lanPrefix(id.Seed)
	peer := func(n int, port uint16) netip.AddrPort { return netip.AddrPortFrom(hostIn(lan, n), port) }
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs: []iface{
			{index: 1, descr: "lo", ifType: ifTypeSoftwareLoopback, mtu: 65536, speed: 10_000_000,
				up: true, inRate: 5_000, outRate: 5_000},
			{index: 2, descr: "eth0", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
				mac: deviceMAC(id.Seed, 1), up: true, inRate: 3_800_000, outRate: 1_100_000, errorRate: 0.0002},
			{index: 3, descr: "eth1", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
				mac: deviceMAC(id.Seed, 2)},
		},
		addrs:   []ifAddr{{ifIndex: 2, prefix: addrIn(lan, 5), neighbours: 8}},
		gateway: hostIn(lan, 1),
		pps:     3100,
		listen:  []uint16{22, 80, 111, 139, 443, 445, 548, 2049, 5000, 5001, 5432},
		udp:     []uint16{111, 123, 137, 138, 161, 1900, 2049, 5353},
		sessions: []session{
			{445, peer(64, 50412)}, // two file shares and a Mac on AFP
			{445, peer(71, 61023)},
			{548, peer(83, 49231)},
			{5001, peer(50, 52102)}, // DSM, open in someone's browser
		},
		modules: []sysOR{moduleHostResources},
	})

	const mem, swap = 4194304, 4194304
	// The volume is SHR over the disks: one disk's worth goes to redundancy
	// from two disks on, and the sizes below are those of four 8 TB disks,
	// scaled to how many hold data.
	disks := id.count("disks", 4)
	scale := float64(max(disks-1, 1)) / 3
	devices := []hrDevice{
		{index: 262145, kind: hrDeviceNetwork, descr: "network interface lo", ifIndex: 1},
		{index: 262146, kind: hrDeviceNetwork, descr: "network interface eth0", ifIndex: 2},
		{index: 262147, kind: hrDeviceNetwork, descr: "network interface eth1", ifIndex: 3},
	}
	for d := range disks {
		devices = append(devices, hrDevice{index: 393216 + 16*d, kind: hrDeviceDiskStorage,
			descr: fmt.Sprintf("SCSI disk (/dev/sata%d)", d+1), diskKB: 7814026584})
	}
	addHostResources(&o, id.Seed, hostResources{
		memoryKiB: mem,
		users:     [2]float64{0, 1},
		storage: []storageArea{
			{1, hrStorageRam, "Physical memory", 1024, mem, 0.4, 0.6, 4 * time.Minute},
			{3, hrStorageVirtualMemory, "Virtual memory", 1024, mem + swap, 0.3, 0.45, 5 * time.Minute},
			{6, hrStorageOther, "Memory buffers", 1024, mem, 0.03, 0.04, 30 * time.Minute},
			{7, hrStorageOther, "Cached memory", 1024, mem, 0.3, 0.38, 25 * time.Minute},
			{10, hrStorageVirtualMemory, "Swap space", 1024, swap, 0.01, 0.03, time.Hour},
			{31, hrStorageFixedDisk, "/", 4096, 614400, 0.55, 0.56, 6 * time.Hour},
			{51, hrStorageFixedDisk, "/volume1", 65536, int(332640000 * scale), 0.37, 0.38, 12 * time.Hour},
		},
		processors: []int{196608, 196609, 196610, 196611},
		cpuLo:      2,
		cpuHi:      30,
		cpu:        "GenuineIntel: Intel(R) Celeron(R) J4125 CPU @ 2.00GHz",
		boot:       393216,
		bootParams: "root=/dev/md0 netif_num=2 syno_hw_version=DS920+ console=ttyS0,115200n8",
		devices:    devices,
		fs: []hrFS{
			{"/", hrFSLinuxExt2, 31, true},
			{"/volume1", hrFSOther, 51, false},
		},
		procs:    synologyProcesses(),
		software: synologyPackages(),
	})
	addUCD(&o, id.Seed, ucdInfo{
		memKB: mem, swapKB: swap, memUsed: [2]float64{0.25, 0.4}, cpus: 4,
		load: [2]float64{0.3, 1.8}, user: [2]float64{2, 22}, system: [2]float64{1, 8},
		disks: []ucdDisk{
			{"/", "/dev/md0", 2455568, [2]float64{0.55, 0.56}},
			{"/volume1", "/dev/mapper/cachedev_0", 21288960000 * scale, [2]float64{0.37, 0.38}},
		},
	})

	// SYNOLOGY-SYSTEM-MIB, -DISK-MIB and -RAID-MIB. "Normal" is 1 throughout.
	const syno = "1.3.6.1.4.1.6574."
	swing := func(n uint64, period time.Duration) Swing { return Swing{Period: period, Seed: id.Seed + 30000 + n} }
	o.add(syno+"1.1.0", gosnmp.Integer, Const(1))                                       // systemStatus
	o.add(syno+"1.2.0", gosnmp.Integer, IntegerGauge(39, 45, swing(0, 40*time.Minute))) // °C
	o.add(syno+"1.3.0", gosnmp.Integer, Const(1))                                       // powerStatus
	o.add(syno+"1.4.1.0", gosnmp.Integer, Const(1))                                     // systemFanStatus
	o.add(syno+"1.4.2.0", gosnmp.Integer, Const(1))                                     // cpuFanStatus
	o.add(syno+"1.5.1.0", gosnmp.OctetString, Const("DS920+"))
	o.add(syno+"1.5.2.0", gosnmp.OctetString, Const(fmt.Sprintf("20B0PDN%06d", int(unit(id.Seed+31)*1e6))))
	o.add(syno+"1.5.3.0", gosnmp.OctetString, Const("DSM 7.2.1-69057 Update 5"))
	o.add(syno+"1.5.4.0", gosnmp.Integer, Const(2)) // upgradeAvailable: unavailable
	o.add(syno+"1.6.0", gosnmp.Integer, Const(0))   // controllerNumber

	// The disks, numbered from 0 as DSM numbers them.
	for disk := range disks {
		col := func(c int) string { return fmt.Sprintf(syno+"2.1.1.%d.%d", c, disk) }
		o.add(col(1), gosnmp.Integer, Const(disk))
		o.add(col(2), gosnmp.OctetString, Const(fmt.Sprintf("Disk %d", disk+1)))
		o.add(col(3), gosnmp.OctetString, Const("ST8000VN004-2M2101"))
		o.add(col(4), gosnmp.OctetString, Const("SATA"))
		o.add(col(5), gosnmp.Integer, Const(1)) // diskStatus
		o.add(col(6), gosnmp.Integer, IntegerGauge(33, 38, swing(10+uint64(disk), time.Hour)))
		o.add(col(7), gosnmp.OctetString, Const("data"))
		o.add(col(8), gosnmp.Integer, Const(0))   // retries
		o.add(col(9), gosnmp.Integer, Const(0))   // bad sectors
		o.add(col(10), gosnmp.Integer, Const(0))  // identify failures
		o.add(col(11), gosnmp.Integer, Const(-1)) // remaining life: a hard disk reports none
		o.add(col(12), gosnmp.OctetString, Const(fmt.Sprintf("Drive %d", disk+1)))
		o.add(col(13), gosnmp.Integer, Const(1)) // diskHealthStatus
	}

	// One volume over the disks, SHR: 21.8 TB over four, 62% free. The sizes
	// are Counter64 in the MIB, so SNMPv1 does not see them.
	raid := func(c int) string { return fmt.Sprintf(syno+"3.1.1.%d.0", c) }
	o.add(raid(1), gosnmp.Integer, Const(0))
	o.add(raid(2), gosnmp.OctetString, Const("Volume 1"))
	o.add(raid(3), gosnmp.Integer, Const(1)) // raidStatus
	o.add(raid(4), gosnmp.Counter64, Const(uint64(13_516_000_000_000*scale)))
	o.add(raid(5), gosnmp.Counter64, Const(uint64(21_800_000_000_000*scale)))
	o.add(raid(6), gosnmp.Integer, Const(0)) // hot spares
	return o
}

// synologyProcesses is what DSM runs on a file server: the kernel's threads,
// then DSM's own services and the file-sharing ones.
func synologyProcesses() []process {
	procs := []process{{pid: 1, name: "systemd", path: "/sbin/init", kind: 4, cpu: 0.001, memKB: 7100}}
	for i, name := range []string{"kthreadd", "ksoftirqd/0", "kworker/0:0H", "rcu_sched", "rcu_bh",
		"migration/0", "migration/1", "ksoftirqd/1", "migration/2", "ksoftirqd/2", "migration/3", "ksoftirqd/3",
		"khelper", "kdevtmpfs", "netns", "kblockd", "ata_sff", "md", "kswapd0", "fsnotify_mark",
		"crypto", "scsi_eh_0", "scsi_eh_1", "scsi_eh_2", "scsi_eh_3", "md0_raid1", "md1_raid1",
		"md2_raid5", "jbd2/md0-8", "btrfs-worker", "btrfs-cleaner", "btrfs-transaction", "nfsd", "nfsd", "lockd"} {
		procs = append(procs, process{pid: 2 + i, name: name, kind: 2})
	}
	pid := 3120
	for _, s := range []struct {
		name, path, args string
		cpu              float64
		memKB            int
	}{
		{"systemd-journald", "/lib/systemd/systemd-journald", "", 0.001, 14200},
		{"systemd-udevd", "/lib/systemd/systemd-udevd", "", 0, 4600},
		{"syslog-ng", "/usr/bin/syslog-ng", "-F -p /var/run/syslog-ng.pid", 0.001, 8200},
		{"synoscgi", "synoscgi", "", 0.004, 38000},
		{"synologand", "/usr/syno/sbin/synologand", "", 0.001, 11100},
		{"synostoraged", "/usr/syno/sbin/synostoraged", "", 0.002, 16300},
		{"synoindexd", "/usr/syno/bin/synoindexd", "", 0.001, 21700},
		{"synoelasticd", "/usr/syno/sbin/synoelasticd", "", 0.001, 13200},
		{"pkgctl-SynologyPhotos", "/var/packages/SynologyPhotos/target/usr/bin/synofoto-bin-backend", "", 0.003, 88000},
		{"pkgctl-HyperBackup", "/var/packages/HyperBackup/target/bin/img_backup", "-S", 0.002, 61000},
		{"nginx", "nginx: master process /usr/bin/nginx", "", 0, 14600},
		{"nginx", "nginx: worker process", "", 0.01, 22100},
		{"smbd", "/usr/local/sbin/smbd", "-F --no-process-group", 0.008, 29400},
		{"smbd", "/usr/local/sbin/smbd", "-F --no-process-group", 0.03, 34100},
		{"nmbd", "/usr/local/sbin/nmbd", "-F", 0.001, 8700},
		{"afpd", "/usr/sbin/afpd", "-d -F /etc/afp.conf", 0.004, 13800},
		{"rpcbind", "/sbin/rpcbind", "-w", 0, 2100},
		{"rpc.mountd", "/usr/sbin/rpc.mountd", "-p 892", 0, 2600},
		{"snmpd", "/usr/bin/snmpd", "-f -c /etc/snmp/snmpd.conf -p /run/snmpd.pid", 0.004, 10400},
		{"postgres", "/usr/bin/postgres", "-D /var/services/pgsql", 0.002, 33000},
		{"postgres", "postgres: synofoto synofoto [local] idle", "", 0.01, 61000},
		{"sshd", "/usr/bin/sshd", "", 0, 5200},
		{"crond", "/usr/sbin/crond", "-f", 0, 2300},
		{"ntpd", "/usr/sbin/ntpd", "-p /var/run/ntpd.pid -g", 0, 4100},
		{"avahi-daemon", "avahi-daemon: running [" + "diskstation.local]", "", 0, 3900},
	} {
		procs = append(procs, process{pid: pid, name: s.name, path: s.path, args: s.args, kind: 4, cpu: s.cpu, memKB: s.memKB})
		pid += 13 + pid%17
	}
	return procs
}

// synologyPackages are the DSM packages installed.
func synologyPackages() []software {
	base := time.Date(2024, 2, 19, 21, 40, 0, 0, time.UTC)
	var out []software
	for i, name := range []string{
		"SynologyPhotos-1.6.2-0720", "HyperBackup-4.1.0-3718", "ActiveBackup-Business-2.7.0-13234",
		"SynologyDrive-3.5.0-26085", "StorageAnalyzer-3.1.0-0205", "SMBService-4.15.13-2322",
		"SecureSignIn-1.1.3-0415", "UniversalSearch-1.7.3-0344", "Node.js_v18-18.20.4-1057", "PHP8.2-8.2.21-0105",
	} {
		out = append(out, software{name: name, installed: base.Add(time.Duration(i) * 53 * time.Hour)})
	}
	return out
}
