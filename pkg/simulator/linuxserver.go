package simulator

import (
	"fmt"
	"net/netip"
	"time"
)

// linuxServer is an Ubuntu 24.04 host running net-snmp, as a walk of one finds
// it: the system group and sysORTable; three interfaces — the loopback, one
// busy, one cabled and down, which is what an interface wall looks like on a
// real server; its address, ARP cache, routes and sockets; its devices, file
// systems, the processes running — kernel threads included, as net-snmp lists
// them — and the packages installed; and UCD-SNMP-MIB's memory, load and disks.
//
// It answers everything the bundled "Interfaces" and "Host resources" presets
// poll, and TestEveryModelFeedsItsPresets holds it to that: binding a preset to
// a simulated server and getting empty widgets would be a demonstration of the
// wrong thing.
//
// It has two processors unless it is given another number.
var linuxServer = model{
	ModelInfo: ModelInfo{ID: "linux-server", Category: "server",
		Params: []ModelParam{{Name: "cpus", Min: 1, Max: 64, Default: 2}}},
	enterprise: 8072, // net-snmp
	build:      buildLinuxServer,
	notifications: []Notification{
		linkNotification(false, 3), // eth1, cabled and down
		linkNotification(true, 2),  // eth0, the uplink
		// NET-SNMP-AGENT-MIB: what snmpd sends as it stops, and when it
		// restarts on a SIGHUP.
		{Name: "nsNotifyShutdown", OID: ".1.3.6.1.4.1.8072.4.0.2"},
		{Name: "nsNotifyRestart", OID: ".1.3.6.1.4.1.8072.4.0.3"},
	},
}

func buildLinuxServer(id Identity) []Object {
	var o objects
	addSystem(&o, id, fmt.Sprintf("Linux %s 6.8.0-45-generic #45-Ubuntu SMP PREEMPT_DYNAMIC x86_64", id.Name),
		".1.3.6.1.4.1.8072.3.2.10", "root@"+id.Name, "Server room, rack A1", 72)
	lan := lanPrefix(id.Seed)
	peer := func(n int, port uint16) netip.AddrPort { return netip.AddrPortFrom(hostIn(lan, n), port) }
	addStack(&o, id, stack{
		seed: id.Seed,
		ifs: []iface{
			{index: 1, descr: "lo", ifType: ifTypeSoftwareLoopback, mtu: 65536, speed: 10_000_000,
				up: true, inRate: 20_000, outRate: 20_000},
			{index: 2, descr: "eth0", alias: "uplink", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
				mac: deviceMAC(id.Seed, 1), up: true, inRate: 1_250_000, outRate: 310_000, errorRate: 0.004},
			{index: 3, descr: "eth1", alias: "spare", ifType: ifTypeEthernet, mtu: 1500, speed: 1_000_000_000,
				mac: deviceMAC(id.Seed, 2)},
		},
		addrs:   []ifAddr{{ifIndex: 2, prefix: addrIn(lan, 10+int(id.Seed%40)), neighbours: 6}},
		gateway: hostIn(lan, 1),
		pps:     1400,
		listen:  []uint16{22, 80, 443, 5432, 9100},
		udp:     []uint16{123, 161},
		sessions: []session{
			{22, peer(50, 51844)},   // someone logged in
			{5432, peer(21, 43210)}, // the application server
			{5432, peer(21, 43212)},
			{443, peer(77, 55012)},
		},
		modules: []sysOR{moduleHostResources},
	})

	const mem, swap = 8388608, 2097152 // KiB: 8 GiB, and 2 GiB of swap
	// One row per CPU, indexed as net-snmp indexes hrDeviceTable.
	cpus := id.count("cpus", 2)
	processors := make([]int, cpus)
	for i := range processors {
		processors[i] = 196608 + i
	}
	addHostResources(&o, id.Seed, hostResources{
		memoryKiB: mem,
		users:     [2]float64{1, 3},
		storage: []storageArea{
			{1, hrStorageRam, "Physical memory", 1024, mem, 0.55, 0.75, 4 * time.Minute},
			{3, hrStorageVirtualMemory, "Virtual memory", 1024, mem + swap, 0.48, 0.62, 5 * time.Minute},
			{6, hrStorageOther, "Memory buffers", 1024, mem, 0.02, 0.03, 30 * time.Minute},
			{7, hrStorageOther, "Cached memory", 1024, mem, 0.18, 0.24, 25 * time.Minute},
			{8, hrStorageOther, "Shared memory", 1024, mem, 0.01, 0.015, 30 * time.Minute},
			{10, hrStorageVirtualMemory, "Swap space", 1024, swap, 0, 0.02, time.Hour},
			{31, hrStorageFixedDisk, "/", 4096, 25600000, 0.41, 0.43, 6 * time.Hour},
			{32, hrStorageFixedDisk, "/boot", 4096, 499712, 0.18, 0.19, 24 * time.Hour},
			{35, hrStorageFixedDisk, "/run", 4096, 204800, 0.01, 0.02, time.Hour},
			{36, hrStorageFixedDisk, "/home", 4096, 51200000, 0.63, 0.64, 9 * time.Hour},
		},
		processors: processors,
		cpuLo:      4,
		cpuHi:      55,
		cpu:        "GenuineIntel: Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz",
		boot:       393216,
		bootParams: "BOOT_IMAGE=/vmlinuz-6.8.0-45-generic root=/dev/mapper/ubuntu--vg-ubuntu--lv ro",
		devices: []hrDevice{
			{index: 262145, kind: hrDeviceNetwork, descr: "network interface lo", ifIndex: 1},
			{index: 262146, kind: hrDeviceNetwork, descr: "network interface eth0", ifIndex: 2},
			{index: 262147, kind: hrDeviceNetwork, descr: "network interface eth1", ifIndex: 3},
			{index: 393216, kind: hrDeviceDiskStorage, descr: "SCSI disk (/dev/sda)", diskKB: 104857600},
			{index: 393232, kind: hrDeviceDiskStorage, descr: "SCSI disk (/dev/sdb)", diskKB: 209715200},
		},
		fs: []hrFS{
			{"/", hrFSLinuxExt2, 31, true},
			{"/boot", hrFSLinuxExt2, 32, false},
			{"/run", hrFSOther, 35, false},
			{"/home", hrFSLinuxExt2, 36, false},
		},
		procs:    linuxProcesses(cpus),
		software: ubuntuPackages(),
	})
	addUCD(&o, id.Seed, ucdInfo{
		memKB: mem, swapKB: swap, memUsed: [2]float64{0.3, 0.45}, cpus: cpus,
		load: [2]float64{0.15, 1.4}, user: [2]float64{3, 38}, system: [2]float64{1, 11},
		disks: []ucdDisk{
			{"/", "/dev/mapper/ubuntu--vg-ubuntu--lv", 102400000, [2]float64{0.41, 0.43}},
			{"/boot", "/dev/sda2", 1998848, [2]float64{0.18, 0.19}},
			{"/home", "/dev/sdb1", 204800000, [2]float64{0.63, 0.64}},
		},
	})
	return o
}

// linuxProcesses is what a Linux server with cpus processors runs: the kernel's
// threads, as net-snmp lists them, then the services a web and database host
// has.
func linuxProcesses(cpus int) []process {
	procs := []process{{pid: 1, name: "systemd", path: "/sbin/init", args: "splash", kind: 4, cpu: 0.001, memKB: 12400}}
	kernel := []string{"kthreadd", "pool_workqueue_release", "kworker/R-rcu_g", "kworker/R-rcu_p",
		"kworker/R-slub_", "kworker/R-netns", "kworker/0:0H-events_highpri", "kworker/R-mm_pe",
		"rcu_tasks_kthread", "rcu_tasks_rude_kthread", "rcu_tasks_trace_kthread", "rcu_preempt"}
	for c := 0; c < cpus; c++ {
		kernel = append(kernel, fmt.Sprintf("ksoftirqd/%d", c), fmt.Sprintf("migration/%d", c),
			fmt.Sprintf("idle_inject/%d", c), fmt.Sprintf("cpuhp/%d", c))
	}
	kernel = append(kernel, "kdevtmpfs", "kworker/R-inet_", "kauditd", "khungtaskd", "oom_reaper",
		"kworker/R-write", "kcompactd0", "ksmd", "khugepaged", "kworker/R-kinte", "kworker/R-kbloc",
		"kworker/R-blkcg", "irq/9-acpi", "kworker/R-tpm_d", "kworker/R-ata_s", "kworker/R-md",
		"kworker/R-md_bi", "kworker/R-edac-", "kworker/R-devfr", "watchdogd", "kswapd0",
		"ecryptfs-kthread", "kworker/R-kthro", "kworker/R-acpi_", "scsi_eh_0", "kworker/R-scsi_",
		"scsi_eh_1", "kworker/R-mld", "kworker/R-ipv6_", "kworker/R-kstrp", "kworker/R-crypt",
		"jbd2/dm-0-8", "kworker/R-ext4-", "jbd2/sda2-8", "jbd2/sdb1-8", "kworker/0:1-events",
		"kworker/1:2-mm_percpu_wq", "kworker/u4:3-events_unbound", "psimon")
	for i, name := range kernel {
		procs = append(procs, process{pid: 2 + i, name: name, kind: 2})
	}
	type svc struct {
		name, path, args string
		cpu              float64
		memKB            int
	}
	pid := 380
	for _, s := range []svc{
		{"systemd-journald", "/usr/lib/systemd/systemd-journald", "", 0.002, 61200},
		{"multipathd", "/sbin/multipathd", "-d -s", 0.001, 27100},
		{"systemd-udevd", "/usr/lib/systemd/systemd-udevd", "", 0.0005, 7400},
		{"systemd-networkd", "/usr/lib/systemd/systemd-networkd", "", 0.0005, 8800},
		{"systemd-resolved", "/usr/lib/systemd/systemd-resolved", "", 0.001, 12800},
		{"systemd-timesyncd", "/usr/lib/systemd/systemd-timesyncd", "", 0, 7200},
		{"cron", "/usr/sbin/cron", "-f -P", 0, 2700},
		{"dbus-daemon", "@dbus-daemon", "--system --address=systemd: --nofork --nopidfile --systemd-activation --syslog-only", 0.0005, 5100},
		{"networkd-dispatcher", "/usr/bin/python3", "/usr/bin/networkd-dispatcher --run-startup-triggers", 0, 20400},
		{"rsyslogd", "/usr/sbin/rsyslogd", "-n -iNONE", 0.001, 6300},
		{"snapd", "/usr/lib/snapd/snapd", "", 0.002, 31700},
		{"systemd-logind", "/usr/lib/systemd/systemd-logind", "", 0, 7700},
		{"udisksd", "/usr/libexec/udisks2/udisksd", "", 0.0005, 13200},
		{"polkitd", "/usr/lib/polkit-1/polkitd", "--no-debug", 0, 10100},
		{"agetty", "/sbin/agetty", "-o -p -- \\u --noclear - linux", 0, 1900},
		{"unattended-upgr", "/usr/bin/python3", "/usr/share/unattended-upgrades/unattended-upgrade-shutdown --wait-for-signal", 0, 23900},
		{"snmpd", "/usr/sbin/snmpd", "-LOw -u Debian-snmp -g Debian-snmp -I -smux mteTrigger mteTriggerConf -f -p /run/snmpd.pid", 0.004, 11300},
		{"sshd", "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", "", 0, 9100},
		{"chronyd", "/usr/sbin/chronyd", "-F 1", 0, 3400},
		{"node_exporter", "/usr/bin/prometheus-node-exporter", "", 0.006, 21800},
		{"nginx", "nginx: master process /usr/sbin/nginx", "-g daemon on; master_process on;", 0, 1600},
		{"nginx", "nginx: worker process", "", 0.02, 5300},
		{"nginx", "nginx: worker process", "", 0.02, 5100},
		{"postgres", "/usr/lib/postgresql/16/bin/postgres", "-D /var/lib/postgresql/16/main -c config_file=/etc/postgresql/16/main/postgresql.conf", 0.002, 31200},
		{"postgres", "postgres: 16/main: checkpointer", "", 0.001, 142000},
		{"postgres", "postgres: 16/main: background writer", "", 0.001, 61000},
		{"postgres", "postgres: 16/main: walwriter", "", 0.001, 21400},
		{"postgres", "postgres: 16/main: autovacuum launcher", "", 0, 11800},
		{"postgres", "postgres: 16/main: logical replication launcher", "", 0, 10200},
		{"postgres", "postgres: 16/main: app appdb client idle", "", 0.06, 188000},
		{"postgres", "postgres: 16/main: app appdb client idle", "", 0.05, 176000},
		{"sshd", "sshd: ops [priv]", "", 0, 11200},
		{"sshd", "sshd: ops@pts/0", "", 0.001, 7300},
		{"bash", "-bash", "", 0, 5200},
		{"systemd", "/usr/lib/systemd/systemd", "--user", 0, 10900},
		{"(sd-pam)", "(sd-pam)", "", 0, 4100},
	} {
		procs = append(procs, process{pid: pid, name: s.name, path: s.path, args: s.args, kind: 4, cpu: s.cpu, memKB: s.memKB})
		pid += 7 + pid%11
	}
	return procs
}

// ubuntuPackages are some of what dpkg says an Ubuntu 24.04 server has, named
// as net-snmp names them, and when they were installed: most with the system,
// the rest with the last updates.
func ubuntuPackages() []software {
	installed := time.Date(2024, 4, 25, 14, 12, 7, 0, time.UTC)
	updated := time.Date(2024, 9, 3, 6, 25, 41, 0, time.UTC)
	var out []software
	for i, name := range []string{
		"adduser-3.137ubuntu1", "apt-2.7.14build2", "base-files-13ubuntu10.1", "bash-5.2.21-2ubuntu4",
		"bsdutils-1:2.39.3-9ubuntu6.1", "ca-certificates-20240203", "chrony-4.5-1ubuntu4.1",
		"coreutils-9.4-3ubuntu6", "cron-3.0pl1-184ubuntu2", "curl-8.5.0-2ubuntu10.4",
		"dbus-1.14.10-4ubuntu4.1", "dpkg-1.22.6ubuntu6.1", "e2fsprogs-1.47.0-2.4~exp1ubuntu4.1",
		"gzip-1.12-1ubuntu3", "iproute2-6.1.0-1ubuntu6", "iputils-ping-3:20240117-1build1",
		"libc6-2.39-0ubuntu8.3", "libsnmp40t64-5.9.4+dfsg-1.1ubuntu3", "libssl3t64-3.0.13-0ubuntu3.4",
		"linux-image-6.8.0-45-generic-6.8.0-45.45", "login-1:4.13+dfsg1-4ubuntu3.2",
		"logrotate-3.21.0-2build1", "multipath-tools-0.9.4-5ubuntu8", "nginx-1.24.0-2ubuntu7.1",
		"openssh-server-1:9.6p1-3ubuntu13.5", "openssl-3.0.13-0ubuntu3.4", "passwd-1:4.13+dfsg1-4ubuntu3.2",
		"perl-5.38.2-3.2build2", "postgresql-16-16.4-0ubuntu0.24.04.2", "procps-2:4.0.4-4ubuntu3.2",
		"prometheus-node-exporter-1.7.0-1ubuntu0.2", "python3-3.12.3-0ubuntu2", "rsyslog-8.2312.0-3ubuntu9",
		"snapd-2.63+24.04", "snmpd-5.9.4+dfsg-1.1ubuntu3", "sudo-1.9.15p5-3ubuntu5",
		"systemd-255.4-1ubuntu8.4", "tar-1.35+dfsg-3build1", "tzdata-2024a-3ubuntu1.1",
		"ubuntu-minimal-1.539.2", "ufw-0.36.2-6", "unattended-upgrades-2.9.1+nmu4ubuntu1",
		"util-linux-2.39.3-9ubuntu6.1", "vim-2:9.1.0016-1ubuntu7.2", "wget-1.21.4-1ubuntu4.1",
		"zlib1g-1:1.3.dfsg-3.1ubuntu2.1",
	} {
		when := installed.Add(time.Duration(i) * 11 * time.Second)
		if i%5 == 3 {
			when = updated.Add(time.Duration(i) * 7 * time.Second)
		}
		out = append(out, software{name: name, installed: when})
	}
	return out
}
