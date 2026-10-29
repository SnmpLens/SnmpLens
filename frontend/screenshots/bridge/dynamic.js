/**
 * Bindings whose answer has to be COMPUTED rather than listed.
 *
 * Hand-written, unlike App.js. Anything here takes precedence over the
 * generated fixtures, which is how a chart gets six hours of points anchored to
 * the present without six hours of JSON in the repository.
 */

import { series, presetSeries, CPU_SESSION, PRESET_SESSION, PRESET_SESSION_2 } from './series.js';

/**
 * The generated fixture table, handed over by App.js at module init.
 *
 * A computed answer sometimes needs to AMEND a fixture rather than replace it:
 * the history list wants a second walk added to diff against and its other four
 * entries left exactly as they are. Importing App.js here would be a cycle, and
 * copying the entries would leave two versions of them to keep in step.
 */
let FIXTURES = {};
export function setFixtures(table) {
  FIXTURES = table || {};
}

let cpu = null;
let bound = null;
let bound2 = null;

export const dynamic = {
  // Generated once per page load, so every chart on the page agrees with itself
  // and with the statistics computed from it.
  // The ARGUMENT matters here, and it used to be ignored: every session was
  // handed the CPU session's points, under its own OID and its own targets. A
  // dashboard bound to one switch then found nothing that matched its eleven
  // OIDs and drew eleven empty widgets — a picture of the waiting state.
  MonitorLoadSessionData(sessionId) {
    if (sessionId === PRESET_SESSION.sessionId) {
      if (!bound) bound = presetSeries();
      return bound;
    }
    if (sessionId === PRESET_SESSION_2.sessionId) {
      if (!bound2) bound2 = presetSeries(PRESET_SESSION_2);
      return bound2;
    }
    if (!cpu) cpu = series(CPU_SESSION);
    return cpu;
  },

  MonitorLoadHistoricalData() {
    if (!cpu) cpu = series(CPU_SESSION);
    return cpu;
  },
};

/**
 * The event journal, lengthened and anchored to the present.
 *
 * The specification gave eight events — the right MIX, which is the hard part,
 * but eight rows leave the lower half of a 1600x1000 capture empty, and an
 * application that looks unused is the one thing these pictures must not show.
 *
 * So the eight are cycled with fresh identities and descending timestamps. The
 * variety is the specification's; only the quantity is ours.
 */
import { EVENT_SEED } from './eventseed.js';

let journal = null;

function buildJournal() {
  const template = EVENT_SEED.items || [];
  if (!template.length) return EVENT_SEED;

  const items = [];
  const now = Date.now();
  let seq = 18422;

  for (let i = 0; i < 22; i++) {
    const base = template[i % template.length];
    // Irregular gaps: evenly spaced timestamps read as generated, because they
    // are — real events do not arrive on a metronome.
    const minutesBack = i * 7 + ((i * 13) % 11) + (i % 3) * 4;
    // Vary the device as well as the clock. Cycling eight templates unchanged
    // repeats a visible pattern down the column, which reads as generated.
    const fleet = ['10.20.4.11', '10.20.4.1', '10.20.4.77', '10.20.4.23',
                   '10.20.5.8', '192.168.30.5', '10.20.4.42', '10.20.7.3'];
    const source = base.source ? fleet[(i * 3 + 1) % fleet.length] : base.source;

    // `summary` is not what the panel renders. EventsPanel builds its text from
    // `titleKey` and `params`, so changing the source column alone put
    // "10.20.4.1" in one column beside "Trap from 10.20.4.11" in the next —
    // which is precisely the kind of thing a reader who knows SNMP notices
    // first. Every copy of the address travels together.
    const swap = (text) => (base.source ? String(text).split(base.source).join(source) : text);
    const params = {};
    for (const [k, v] of Object.entries(base.params || {})) {
      params[k] = typeof v === 'string' ? swap(v) : v;
    }

    items.push({
      ...base,
      source,
      params,
      summary: swap(base.summary),
      dedupKey: swap(base.dedupKey),
      seq: seq--,
      id: `${base.id.slice(0, 24)}-${String(i).padStart(2, '0')}`,
      ts: new Date(now - minutesBack * 60000).toISOString(),
      acked: i > 15,
    });
  }
  return { ...EVENT_SEED, items };
}

dynamic.EventsQuery = () => {
  if (!journal) journal = buildJournal();
  return journal;
};

/**
 * A walk of ifTable, twelve interfaces deep.
 *
 * The specification's example carried two, which renders a table with two rows —
 * enough to prove the pivot works and not enough to show what it is FOR. The
 * point of the table view is that a walk of two hundred varbinds becomes
 * something readable, so the picture has to have two hundred varbinds in it.
 *
 * The columns and their types are the specification's; only the row count and
 * the plausibility of each device are ours.
 */
const IF_BASE = '.1.3.6.1.2.1.2.2.1';

const INTERFACES = [
  { descr: 'Loopback0', type: 24, mtu: 1500, speed: 0, mac: '', admin: 1, oper: 1 },
  { descr: 'GigabitEthernet0/0/1 - uplink to core-rtr-01 Te0/0/3, 1000BASE-LX single-mode', type: 6, mtu: 1500, speed: 1000000000, mac: '00:1B:21:3C:4D:5E', admin: 1, oper: 1 },
  { descr: 'GigabitEthernet0/0/2 - uplink to core-rtr-02 Te0/0/3', type: 6, mtu: 1500, speed: 1000000000, mac: '00:1B:21:3C:4D:5F', admin: 1, oper: 1 },
  { descr: 'GigabitEthernet0/1/1 - access, floor 2 north', type: 6, mtu: 1500, speed: 1000000000, mac: '00:1B:21:3C:4D:60', admin: 1, oper: 1 },
  { descr: 'GigabitEthernet0/1/2 - access, floor 2 south', type: 6, mtu: 1500, speed: 1000000000, mac: '00:1B:21:3C:4D:61', admin: 1, oper: 2 },
  { descr: 'GigabitEthernet0/1/3 - access, floor 3 north', type: 6, mtu: 1500, speed: 100000000, mac: '00:1B:21:3C:4D:62', admin: 1, oper: 1 },
  { descr: 'GigabitEthernet0/1/4 - spare', type: 6, mtu: 1500, speed: 0, mac: '00:1B:21:3C:4D:63', admin: 2, oper: 2 },
  { descr: 'TenGigabitEthernet1/0/1 - to spine-01', type: 6, mtu: 9216, speed: 10000000000, mac: '00:1B:21:3C:4D:70', admin: 1, oper: 1 },
  { descr: 'TenGigabitEthernet1/0/2 - to spine-02', type: 6, mtu: 9216, speed: 10000000000, mac: '00:1B:21:3C:4D:71', admin: 1, oper: 1 },
  { descr: 'Port-channel1 - LACP to spine pair', type: 161, mtu: 9216, speed: 20000000000, mac: '00:1B:21:3C:4D:72', admin: 1, oper: 1 },
  { descr: 'Vlan10 - management', type: 53, mtu: 1500, speed: 1000000000, mac: '00:1B:21:3C:4D:80', admin: 1, oper: 1 },
  { descr: 'Vlan240 - guest wireless', type: 53, mtu: 1500, speed: 1000000000, mac: '00:1B:21:3C:4D:81', admin: 1, oper: 1 },
];

function ifTableWalk(after = 0) {
  const vars = [];
  const add = (col, index, type, value) =>
    vars.push({ oid: `${IF_BASE}.${col}.${index}`, type, value });

  INTERFACES.forEach((iface, i) => {
    const n = i + 1;
    add(1, n, 'Integer', n);
    add(2, n, 'OctetString', iface.descr);
    add(3, n, 'Integer', iface.type);
    add(4, n, 'Integer', iface.mtu);
    add(5, n, 'Gauge32', iface.speed);
    if (iface.mac) add(6, n, 'OctetString', iface.mac);
    add(7, n, 'Integer', iface.admin);

    // `after` is minutes elapsed since the first walk. The later walk moves
    // exactly what a later walk would: the access port on floor 2 south was
    // down and has come back up, its counters start moving with it, and the
    // rest of the table is byte-for-byte identical. That last part is what
    // makes the diff READABLE — a picture in which every row changed says
    // nothing about what the feature is for, and this one reports 36 modified
    // against 119 identical.
    const recovered = after > 0 && iface.descr.indexOf('floor 2 south') > 0;
    const up = iface.oper === 1 || recovered;
    const secs = after * 60;

    // The row the whole picture is about: 2 (down) becoming 1 (up). It is
    // column 8, so the diff lists it FIRST, above the counters it caused.
    add(8, n, 'Integer', up ? 1 : iface.oper);
    add(9, n, 'TimeTicks', recovered ? 41207 : 106238300 + secs * 100 - i * 41207);
    // The counters, which are what anyone actually walks ifTable for.
    add(10, n, 'Counter32', up ? 418923400 + i * 9137711 + secs * 121400 : 0);
    add(16, n, 'Counter32', up ? 391004822 + i * 8412093 + secs * 98750 : 0);
    add(14, n, 'Counter32', up ? i * 3 : 0);
    add(20, n, 'Counter32', up ? i : 0);
  });

  return [{
    target: '10.20.0.1',
    responseTimeMs: after ? 388 : 412,
    result: { oid: '1.3.6.1.2.1.2.2', type: 'WalkResponse', value: vars },
  }];
}

let walk = null;
dynamic.SnmpWalk = () => {
  if (!walk) walk = ifTableWalk();
  return walk;
};

/**
 * The history list, with a second walk to diff the first against.
 *
 * The fixture carried one WALK, and `isDiffEligible` accepts only WALK and
 * GETBULK — so Diff Mode could select exactly one entry and Compare never
 * appeared. `DiffModal` reads `entry.results` straight off each entry, which the
 * fixture's walk did not have either.
 *
 * The pair is forty minutes apart on the same OID and the same target, because
 * that is the question the feature answers: what changed on this device since I
 * last looked.
 */
dynamic.LoadHistory = () => {
  const base = FIXTURES.LoadHistory || [];
  const first = base.find((e) => e.operation === 'WALK');
  if (!first) return undefined;

  const withResults = (entry, minutes, id, when) => ({
    ...entry,
    id,
    timestamp: when,
    targets: ['10.20.0.1'],
    duration: minutes ? 388 : 412,
    results: ifTableWalk(minutes),
    totalResults: 155,
  });

  const t0 = new Date(first.timestamp).getTime();
  return [
    withResults(first, 40, `${first.id}-b`, new Date(t0 + 40 * 60000).toISOString()),
    withResults(first, 0, first.id, first.timestamp),
    ...base.filter((e) => e !== first),
  ];
};

/**
 * The MIB the editor screenshot has open.
 *
 * The specification's `content` was a DESCRIPTION of the file rather than the
 * file — "<<the 90-line ACME-POE-MIB printed verbatim in visualDirection>>" —
 * so opening it showed that sentence in a syntax highlighter. The text is here
 * for real, imported rather than pasted into a JS string, because the diagnostic
 * positions are derived from it: line 18 column 17 is exactly where `Integerr32`
 * starts, and line 32 is the second `::= { acmePoeObjects 1 }`. A stray edit to
 * the text moves the squiggles off the thing they are pointing at.
 */
import ACME_POE_MIB_FILE from './acme-poe-mib.txt?raw';

// In LF, as Go hands every buffer to the editor (pkg/mib DecodeSource). A
// checkout on Windows gives the file CRLF, which the editor would hold as a
// text nobody typed: CodeMirror splits lines at a CR too and gives the text
// back in LF. (The hand-made surface before it drew a line too many instead.)
const ACME_POE_MIB = ACME_POE_MIB_FILE.replace(/\r\n?/g, '\n');

// The copy ON DISK is sound: `SYNTAX Gauge32`. The unsaved draft below is what
// introduced `Integerr32`, and that difference is what makes the editor recover
// the draft. So one picture shows two things: work surviving a closed window,
// and the semantic pass catching what loading alone would not.
const ON_DISK = ACME_POE_MIB.split('\n')
  .map((line, i) => (i === 17 ? '    SYNTAX      Gauge32' : line))
  .join('\n');

dynamic.MibEditorReadDraft = () => ACME_POE_MIB;

dynamic.MibEditorRead = () => ({
  name: 'ACME-POE-MIB',
  // Escaped: unescaped, JavaScript drops each backslash it does not recognise
  // and the path renders as "C:Usersops...".
  path: 'C:\\Users\\ops\\AppData\\Roaming\\SnmpLens\\mibs\\ACME-POE-MIB',
  content: ON_DISK,
  eol: 'lf',
  bundled: false,
  external: false,
  sha256: 'cfbca6d794927294d050e9b093af1dc70fd065f671855aceece1f0e5797c3562',
  diagnostics: [],
});

/**
 * A CIDR sweep that found something.
 *
 * Two things were wrong with the specification's set. The scan is never RUN —
 * the results are the component's own state, like a walk's — and the devices
 * were on 10.20.x.x while the form's default range is 192.168.1.0/24, so the
 * picture would have shown a scan of one subnet returning addresses from
 * another. A reader who knows SNMP is exactly the reader who would notice.
 *
 * The estate is the kind a small site actually has: routers and switches from
 * different vendors, the wireless, and the three things people forget are on
 * the network until they answer an SNMP sweep — the printer, the UPS and the
 * NAS. The sysDescr strings are the real shapes those devices return, because
 * a screenshot of a discovery tool is partly a screenshot of sysDescr.
 */
const DISCOVERED = [
  ['192.168.1.1', 'edge-rtr-01', 'Cisco IOS Software, ISR4331 Software (X86_64_LINUX_IOSD-UNIVERSALK9-M), Version 17.09.04a, RELEASE SOFTWARE (fc3)', 1099992310, 4],
  ['192.168.1.2', 'core-sw-01', 'Cisco IOS Software, C9300 Software (CAT9K_IOSXE), Version 17.12.03, RELEASE SOFTWARE (fc2)', 1043118800, 3],
  ['192.168.1.3', 'core-sw-02', 'Cisco IOS Software, C9300 Software (CAT9K_IOSXE), Version 17.12.03, RELEASE SOFTWARE (fc2)', 1043092140, 3],
  ['192.168.1.10', 'dist-sw-2f', 'Aruba JL255A 2930F-24G-4SFP+ Switch, revision WC.16.11.0013, ROM WC.16.01.0006', 884419770, 6],
  ['192.168.1.11', 'dist-sw-3f', 'Aruba JL255A 2930F-24G-4SFP+ Switch, revision WC.16.11.0013, ROM WC.16.01.0006', 884401200, 7],
  ['192.168.1.20', 'wifi-ctrl', 'MikroTik RouterOS 7.15.3 (stable) CRS328-24P-4S+', 612334880, 5],
  ['192.168.1.21', 'ap-floor2-n', 'Ubiquiti UniFi U6-Pro, firmware 6.6.77.15402', 388120450, 11],
  ['192.168.1.22', 'ap-floor3-n', 'Ubiquiti UniFi U6-Pro, firmware 6.6.77.15402', 388102190, 12],
  ['192.168.1.40', 'prn-acct-01', 'HP LaserJet MFP M528dn, Firmware 2409074_000587, Serial CNBRQ1234X', 219044100, 24],
  ['192.168.1.50', 'ups-server-room', 'APC Smart-UPS SRT 5000 RM, Network Management Card AOS v7.1.2', 447882300, 9],
  ['192.168.1.60', 'nas-backup-01', 'Linux nas-backup-01 4.4.302+ #72806 SMP x86_64 DiskStation DS1821+', 302118940, 8],
  ['192.168.1.80', 'esxi-host-02', 'VMware ESXi 8.0.2 build-23305546 VMware, Inc. x86_64', 154099220, 14],
  ['192.168.1.90', 'mon-collector', 'Linux mon-collector 6.8.0-45-generic #45-Ubuntu SMP x86_64', 88220110, 2],
];

dynamic.SnmpDiscover = () =>
  DISCOVERED.map(([ip, sysName, sysDescr, sysUpTime, responseTime]) => ({
    ip, sysName, sysDescr,
    sysUpTime: String(sysUpTime),
    responseTime,
    reachable: true,
  }));

/**
 * OID → name, for the varbind tables.
 *
 * `Trap.svelte` calls `GetOidDetails` once per varbind and puts the answer in
 * the Name column. Every binding used to be generated with an empty parameter
 * list, so all six calls got the same fixture back and the trap screenshot read
 * `linkDown` six times beside six plainly different OIDs — the one thing a
 * picture of an SNMP tool must not get wrong, because its whole claim is that it
 * resolves OIDs through your MIBs.
 *
 * Only the OIDs the fixtures actually contain are listed. An unknown OID falls
 * back to the static fixture, which is what the trap-OID varbind wants anyway.
 */
const OID_NAMES = {
  '1.3.6.1.2.1.1.3.0': ['sysUpTime', 'The time since the network management portion of the system was last re-initialized.'],
  '1.3.6.1.6.3.1.1.4.1.0': ['snmpTrapOID', 'The authoritative identification of the notification currently being sent.'],
  '1.3.6.1.2.1.2.2.1.1.3': ['ifIndex', 'A unique value, greater than zero, for each interface.'],
  '1.3.6.1.2.1.2.2.1.2.3': ['ifDescr', 'A textual string containing information about the interface.'],
  '1.3.6.1.2.1.2.2.1.7.3': ['ifAdminStatus', 'The desired state of the interface.'],
  '1.3.6.1.2.1.2.2.1.8.3': ['ifOperStatus', 'The current operational state of the interface.'],
  '1.3.6.1.4.1.318.2.3.3.0': ['upsAdvStateSummary', 'A summary of the UPS state, as a display string.'],
  '1.3.6.1.4.1.318.1.1.1.2.2.1.0': ['upsAdvBatteryCapacity', 'The remaining battery capacity, as a percentage of full capacity.'],
  '1.3.6.1.4.1.318.1.1.1.2.2.3.0': ['upsAdvBatteryRunTimeRemaining', 'The UPS battery run time remaining before battery exhaustion.'],
  '1.3.6.1.2.1.1.1.0': ['sysDescr', 'A textual description of the entity.'],
  '1.3.6.1.2.1.1.5.0': ['sysName', 'An administratively-assigned name for this managed node.'],

  // The NOTIFICATION OIDs — the values of the snmpTrapOID varbind, resolved a
  // second time to fill the Message column. Without these all five rows read
  // "linkDown", including the one from a UPS, which is a picture of an SNMP
  // tool failing at the one thing it claims to do.
  '1.3.6.1.6.3.1.1.5.1': ['coldStart', 'The agent is reinitialising and its configuration may have altered.'],
  '1.3.6.1.6.3.1.1.5.3': ['linkDown', 'A communication link is about to enter the down state.'],
  '1.3.6.1.6.3.1.1.5.4': ['linkUp', 'A communication link has come up.'],
  '1.3.6.1.4.1.318.0.5': ['upsOnBattery', 'The UPS is operating on battery: utility power has failed.'],
  '1.3.6.1.4.1.9': ['ciscoEnterprise', 'An enterprise-specific notification from a Cisco agent.'],
};

dynamic.GetOidDetails = (oid) => {
  const key = String(oid || '').replace(/^\./, '');
  const hit = OID_NAMES[key];
  if (!hit) return undefined; // no opinion: answer() falls through to the fixture
  return { name: hit[0], description: hit[1] };
};

/**
 * The session list, with its target list kept in step with the series.
 *
 * The fixture names three targets and the chart now draws five. The panel takes
 * its chips, its "N target(s)" label and its per-series buffer cap from THIS
 * list, so a chart with more curves than the session admits to is a chart whose
 * legend and header disagree with it.
 */
dynamic.MonitorLoadSessions = () => {
  const base = FIXTURES.MonitorLoadSessions;
  if (!Array.isArray(base)) return undefined;
  return base.map((s) => (s.id === CPU_SESSION.sessionId
    ? { ...s, targets: CPU_SESSION.targets.map((t) => t.address) }
    : s));
};

/* ------------------------------------------------------------------------- *
 * Demo guards
 *
 * The screenshot bundle and the browser demo are the same build with the same
 * fixtures, and for a still that is exactly right: nothing is clicked, so
 * nothing has to be honest about what it would have done.
 *
 * A demo is different. Someone will press "Enable autostart", "Install service",
 * "Check for updates" and "Send test notification", and a fixture answering
 * "ok" to those is a lie about what the application did — worse, it is a lie
 * about what a piece of software just did to their machine. Each of these asks
 * the operating system for something a web page cannot have, so each says so,
 * as an error the application already knows how to show.
 *
 * Active ONLY when the demo director sets the flag, so no screenshot changes.
 * ------------------------------------------------------------------------- */

const DEMO_ONLY = {
  AutostartSet: 'Starting with the session writes a login entry — the HKCU Run key, a LaunchAgent, or an XDG autostart file.',
  ServiceSetConfig: 'Running in the background registers a per-user service.',
  CheckForUpdate: 'The updater asks GitHub for a newer release and verifies its Ed25519 signature.',
  DownloadAndApplyUpdate: 'Applying an update replaces the executable on disk.',
  BrowseDialog: 'Choosing a file opens the operating system’s file dialog.',
  OpenFolder: 'Opening a folder asks the operating system’s file manager to show it.',
  ImportMibFiles: 'Importing MIBs copies files into your configuration directory.',
  MibEditorRevealFile: 'Showing a file asks the operating system’s file manager.',
  MibEditorRenameFile: 'Renaming a MIB renames a file in your configuration directory.',
  MibEditorDuplicateFile: 'Duplicating a MIB writes a file into your configuration directory.',
  MibEditorDeleteFiles: 'Deleting a MIB moves a file out of your configuration directory.',
  MibEditorRestoreDeleted: 'Restoring a deleted MIB writes a file into your configuration directory.',
  MibEditorExportWithDependencies: 'Exporting MIBs opens the operating system’s save dialog and writes an archive.',
  ImportPresetFiles: 'Adding a preset copies the file into your configuration directory.',
  ImportPresetDialog: 'Adding a preset opens the operating system’s file dialog and writes to disk.',
  DeletePreset: 'Removing a preset deletes the file from your configuration directory.',
  ImportHistoryEntries: 'Importing history reads a file from disk.',
  MibEditorSave: 'Saving writes the MIB to your configuration directory.',
  MibEditorSaveDraft: 'Drafts are written to a file so they survive closing the window.',
  MibEditorRestoreBundled: 'Restoring a bundled MIB rewrites it on disk.',
  MibEditorOpenExternal: 'Opening a MIB from elsewhere uses the file dialog.',
  NotifyTestSink: 'A test notification opens a real connection to your syslog relay, webhook or mail server.',
  SimulatorSaveDevice: 'Saving a simulated device writes it to your configuration directory, and its passwords to the system keychain.',
  SimulatorDeleteDevice: 'Removing a simulated device deletes it from your configuration directory and the system keychain.',
  SimulatorStartDevice: 'Starting a simulated device opens a UDP socket on this machine.',
  SimulatorStopDevice: 'Stopping a simulated device closes the socket it answers on, on this machine.',
  SimulatorSendTrap: 'A simulated device sends its notifications over UDP, from this machine.',
  ImportSimulatorModelsDialog: 'Importing a simulator model opens the operating system’s file dialog and writes the model to your configuration directory.',
  SimulatorDeleteModel: 'Deleting a simulator model removes its file from your configuration directory.',
  SimulatorRecordDevice: 'Recording a device walks a real device on your network and writes the model to your configuration directory.',
  SimulatorExportModelDialog: 'Exporting a simulator model opens the operating system’s file dialog and writes a file.',
  ImportSimulatedDevicesDialog: 'Importing simulated devices opens the operating system’s file dialog, and writes the devices to your configuration directory and their passwords to the system keychain.',
  SimulatorExportDevicesDialog: 'Exporting simulated devices opens the operating system’s file dialog and writes a file.',
  SimulatorDuplicateDevice: 'Duplicating a simulated device writes the copy to your configuration directory, and its passwords to the system keychain.',
  SimulatorRestartDevice: 'Restarting a simulated device opens a UDP socket on this machine.',
  SimulatorSetFaults: 'Faults are applied to a simulated device running on this machine, and kept in your configuration directory.',
  // Not a write: worked out in Go, from the model and the device's settings.
  // There is no Go here, and a canned table would show one device's objects
  // under every other device's name.
  SimulatorPreview: 'The preview is worked out by the application itself, from the model and the device’s settings.',
};

for (const [name, why] of Object.entries(DEMO_ONLY)) {
  dynamic[name] = () => {
    if (!globalThis.__SNMPLENS_DEMO__) return undefined; // fall through to the fixture
    return Promise.reject(new Error(`${why} That needs the desktop application — this is a browser demo.`));
  };
}

/**
 * The one native call with a real browser equivalent.
 *
 * Everything else here refuses; opening a link is something a page can actually
 * do, and refusing it would break the documentation links inside the interface
 * for no reason.
 */
dynamic.OpenURL = (url) => {
  if (!globalThis.__SNMPLENS_DEMO__) return undefined;
  try {
    globalThis.open(String(url), '_blank', 'noopener,noreferrer');
  } catch { /* a blocked popup is not worth an error toast */ }
  return null;
};

/**
 * The notification preview, shaped by the destination.
 *
 * The static fixture answered one Slack-shaped JSON body to all three kinds, so
 * the demo showed a JSON payload for a syslog sink and for a mail sink — which
 * is precisely the thing the real preview was changed to stop doing. A demo that
 * misrepresents a feature is worse than one that omits it.
 *
 * This is NOT the Go renderer; it cannot be, since there is no Go here. It is a
 * faithful shape: an RFC5424 line for syslog, an RFC5322 message for mail, the
 * request body for a webhook — each responding to the format controls, because
 * a control that changes nothing is the same lie one level down.
 */
const PREVIEW_EVENT = {
  severity: 'MAJOR',
  summary: 'ifInOctets reached 912.5, above 900, on 10.0.0.1',
  source: '10.0.0.1',
  oid: '1.3.6.1.2.1.2.2.1.10.1',
  value: '912.5',
  ts: '2026-09-01T09:12:44.000000Z',
  id: '7c41f0a2-4d3e-4a91-9b60-2f8c1d5e7a03',
  kind: 'threshold.opened',
};

function previewSyslog(cfg, ev) {
  const c = cfg.syslog || {};
  // <PRI> is facility*8 + severity; major is 2 (critical) in the RFC5424 scale.
  const pri = (Number.isFinite(c.facility) ? c.facility : 16) * 8 + 2;
  const sd = c.omitStructuredData
    ? '-'
    : `[snmplens@0 id="${ev.id}" category="threshold" severity="major" state="open" `
      + `source="${ev.source}" oid="${ev.oid}"]`;
  return `<${pri}>1 ${ev.ts} ${c.hostname || 'workstation'} ${c.appName || 'SnmpLens'} - `
    + `${ev.kind} ${sd} ﻿${ev.summary}`;
}

function previewEmail(cfg, ev, subject, body) {
  const c = cfg.email || {};
  const type = String(c.format || '').toLowerCase() === 'html' ? 'text/html' : 'text/plain';
  return [
    `From: ${c.from || 'snmplens@example.com'}`,
    `To: ${(c.to || ['noc@example.com']).join(', ')}`,
    `Subject: ${subject}`,
    'Date: Tue, 01 Sep 2026 11:12:44 +0200',
    'MIME-Version: 1.0',
    `Content-Type: ${type}; charset=utf-8`,
    'Content-Transfer-Encoding: 8bit',
    `Message-ID: <${ev.id}@snmplens>`,
    `X-SnmpLens-Event-Id: ${ev.id}`,
    '',
    body,
  ].join('\n');
}

const WEBHOOK_BODIES = {
  json: (ev) => JSON.stringify({
    severity: ev.severity.toLowerCase(), category: 'threshold', kind: ev.kind,
    source: ev.source, oid: ev.oid, value: ev.value, summary: ev.summary,
    time: ev.ts, eventId: ev.id, sender: 'SnmpLens',
  }, null, 2),
  text: (ev) => `[${ev.severity}] ${ev.summary}`,
  xml: (ev) => `<alert severity="${ev.severity.toLowerCase()}">\n`
    + `  <source>${ev.source}</source>\n  <oid>${ev.oid}</oid>\n`
    + `  <value>${ev.value}</value>\n  <summary>${ev.summary}</summary>\n</alert>`,
  form: (ev) => `severity=${ev.severity.toLowerCase()}`
    + `&source=${encodeURIComponent(ev.source)}&oid=${ev.oid}`
    + `&summary=${encodeURIComponent(ev.summary)}`,
};

const WEBHOOK_TYPES = {
  json: 'application/json',
  text: 'text/plain; charset=utf-8',
  xml: 'application/xml; charset=utf-8',
  form: 'application/x-www-form-urlencoded',
};

dynamic.NotifyPreviewTemplate = (cfg) => {
  if (!cfg || !cfg.kind) return undefined; // no opinion: fall through to the fixture
  const ev = PREVIEW_EVENT;
  const subject = `[${ev.severity}] ${ev.summary}`;
  const body = (cfg.template && cfg.template.body) || ev.summary;

  switch (cfg.kind) {
    case 'syslog':
      return { subject, body: previewSyslog(cfg, ev), errors: [], format: 'syslog',
               json: false, jsonValid: false, bytes: previewSyslog(cfg, ev).length };
    case 'email': {
      const html = String((cfg.email || {}).format || '').toLowerCase() === 'html';
      const rendered = html
        ? `<p style="margin:0 0 12px"><strong>${ev.severity}</strong> — ${ev.summary}</p>`
        : body;
      const msg = previewEmail(cfg, ev, subject, rendered);
      return { subject, body: msg, errors: [], format: 'email',
               json: false, jsonValid: false, bytes: msg.length };
    }
    case 'webhook': {
      const templated = String((cfg.webhook || {}).payloadMode || '') === 'template';
      const fmt = templated ? ((cfg.webhook || {}).bodyFormat || 'json') : 'json';
      const out = templated
        ? (WEBHOOK_BODIES[fmt] || WEBHOOK_BODIES.json)(ev)
        : JSON.stringify({ event: { severity: 'major', source: ev.source, oid: ev.oid },
                           subject, body, sender: 'SnmpLens' }, null, 2);
      return { subject, body: out, errors: [], format: templated ? fmt : 'json',
               contentType: templated ? WEBHOOK_TYPES[fmt] : 'application/json',
               json: true, jsonValid: true, bytes: out.length };
    }
    default:
      return undefined;
  }
};

/* ------------------------------------------------------------------------- *
 * Getters that GATE a screen
 *
 * Thirty-one of the ninety-three bindings have no fixture and fall through to a
 * shape default, which for most is harmless: they are actions, and nothing reads
 * what a delete returns. A handful are different — a panel renders "Loading…"
 * until they answer, and a null answer is a screen that never arrives. The
 * Service tab did exactly that.
 *
 * These are the ones a person can reach in the demo. Written here rather than in
 * the specification because they describe THIS machine, and the demo runs on
 * someone else's.
 * ------------------------------------------------------------------------- */

dynamic.ServiceGetStatus = () => ({
  config: {
    runInBackground: false,
    startHidden: false,
    autoStartTrapListener: true,
    trapPort: 162,
    autoResumeMonitors: true,
    auditFailedSets: false,
  },
  // A tray icon is what makes background mode real, and the screen explains
  // itself differently when the desktop refused one. True is the ordinary case.
  trayAvailable: true,
  trapListenerRunning: false,
  // Not navigator.platform: the demo shows what the DESKTOP build reports, and
  // the settings text branches on it.
  platform: 'windows',
});

dynamic.AutostartGet = () => ({
  supported: true,
  enabled: false,
  // Escaped: unescaped, JavaScript drops every backslash it does not recognise
  // and the registry path renders as HKCUSoftwareMicrosoftWindowsCurrentVersionRun
  // — which is the one line on this screen whose whole purpose is to let someone
  // go and check what the application did to their machine.
  location: 'HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run',
  command: '"C:\\Program Files\\SnmpLens\\SnmpLens.exe" --hidden',
});

dynamic.GetAppVersion = () => '1.4.1';

/**
 * A connection test, answered per target.
 *
 * Someone will press this in the demo — it is the most obvious button on a
 * target — and a null answer renders as a failure with no message, which reads
 * as a broken application rather than as a demo.
 */
dynamic.TestConnection = (req) => {
  const target = (req && (req.target || (req.targets || [])[0])) || '10.20.0.1';
  return {
    target,
    responseTimeMs: 18 + (String(target).length % 7) * 3,
    result: {
      oid: '1.3.6.1.2.1.1.1.0',
      type: 'OctetString',
      value: 'Cisco IOS Software, C9300 Software (CAT9K_IOSXE), Version 17.12.03',
    },
  };
};

/**
 * Saves ECHO what they were given.
 *
 * The panels replace their list entry with whatever comes back, so returning
 * null empties the row that was just edited — the change appears to have deleted
 * the thing. Echoing is also what the real backend does: it returns the stored
 * record, including any id it assigned.
 */
const echoWithId = (prefix) => (value) => {
  if (!value || typeof value !== 'object') return value;
  return value.id ? value : { ...value, id: `${prefix}-${Math.abs(hashOf(JSON.stringify(value)))}` };
};

// A stable id from the content, so saving the same thing twice does not produce
// two rows — and no Math.random, for the same reason the rest of this harness
// avoids it.
function hashOf(s) {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return h;
}

dynamic.NotifySaveSink = echoWithId('sink');
dynamic.NotifySaveRoute = echoWithId('route');
