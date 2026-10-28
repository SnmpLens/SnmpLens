/**
 * The scene catalogue: one entry per screenshot the site needs, in BOTH themes.
 *
 * A scene is a URL, not a script. `?scene=monitor-charts-dark` seeds
 * localStorage before the bundle evaluates, picks the tab, and may override any
 * bridge fixture — so a capture is a plain page load with nothing to click and
 * nothing to time.
 *
 * That matters more than it sounds. Driving a UI by clicking is how screenshot
 * automation becomes flaky: a selector moves, a transition is half-finished, and
 * the image is wrong in a way nobody notices until it is on the front page.
 * Here the only thing that can vary between two runs of the same scene is the
 * clock, and the fixtures pin that too.
 *
 * Every entry is declared ONCE and emitted twice, dark and light. The site shows
 * whichever matches the reader's own theme, so a missing counterpart is not a
 * missing picture — it is a picture of the wrong application on half the screens
 * that load the page. Declaring the pair by hand is how one of the two silently
 * drifts from the other.
 */

import { ACME_CRAC_ICON } from './simulatorIcon.js';

/** The seven tabs, as App.svelte names them. */
export const TABS = {
  operations: 'operations',
  traps: 'traps',
  history: 'history',
  monitor: 'monitor',
  discovery: 'discovery',
  events: 'events',
  mibeditor: 'mibeditor',
  dashboard: 'dashboard',
};

/** The two themes every scene is captured in. */
export const THEMES = ['dark', 'light'];

/**
 * Seeds every scene starts from: a configured, used installation.
 *
 * English is forced rather than detected. setupI18n reads `settings.locale`
 * straight out of localStorage before anything is decrypted, so a plaintext blob
 * is read correctly — and without this the shots come out in whatever language
 * the capturing machine happens to be set to, which for this one is French.
 */
function baseSeeds(seeds) {
  return {
    ...seeds,
    snmplens_panel_collapsed: '0',
    snmplens_panel_width: '400',
  };
}

/** Merge a settings patch into the seeded settings blob. */
function withSettings(base, patch) {
  let obj = {};
  try {
    obj = JSON.parse(base.settings || '{}');
  } catch {
    obj = {};
  }
  return JSON.stringify({ ...obj, locale: 'en', ...patch });
}

/** One scene. `tab` picks the workspace; `theme` picks dark or light. */
function scene(base, name, { tab, theme = 'dark', width = 1600, height = 1000, settings = {}, seeds: extra = {}, bindings = {}, latency = {}, events = [], feed = null, act = [], describe }) {
  return {
    name,
    width,
    height,
    theme,
    describe,
    seeds: {
      ...base,
      ...extra,
      settings: withSettings(base, { theme, ...settings }),
      snmplens_active_tab: tab,
    },
    bindings,
    // How long each call should APPEAR to take. A still spends these instantly
    // under virtual time; only a clip ever sees them.
    latency,
    events,
    // A repeating event source, for the screens whose subject is that the
    // numbers keep arriving. See screenshots/bridge/feed.js.
    feed,
    act,
  };
}

/**
 * The estate the target and credential scenes share, so the profile named in
 * one picture is the profile used in the other: the two edge routers migrated
 * to an SNMPv3 user of their own, a UPS that only speaks v1 on a port of its
 * own, and a lab still on MD5 and DES — which the list flags — that is only
 * polled, so it is kept out of the trap listener. Passphrases are blank for the
 * reason given on the settings scene.
 */
const PROFILED_ESTATE = {
  targets: [
    '10.20.0.1 # core-sw-01',
    '10.20.0.2 # core-sw-02',
    '10.20.4.11 # dist-sw-2f',
    '10.20.4.12 # dist-sw-3f',
    '10.20.4.23 # dist-sw-4f',
    '10.20.8.1 # edge-rtr-01',
    '10.20.8.2 # edge-rtr-02',
    '192.168.30.5 # ups-server-room',
  ].join('\n'),
  credentialProfiles: [
    {
      id: 'p-edge0001', name: 'Edge routers', version: 'v3',
      v3: { user: 'noc-edge', secLevel: 'AuthPriv', authProto: 'SHA256', authPass: '', privProto: 'AES256C', privPass: '', contextName: '' },
    },
    { id: 'p-ups00001', name: 'UPS (legacy v1)', version: 'v1', community: 'ups-ro' },
    {
      id: 'p-lab00001', name: 'Lab', version: 'v3', acceptTraps: false,
      v3: { user: 'lab', secLevel: 'AuthPriv', authProto: 'MD5', authPass: '', privProto: 'DES', privPass: '', contextName: '' },
    },
  ],
  targetOverrides: {
    '10.20.8.1': { profile: 'p-edge0001' },
    '10.20.8.2': { profile: 'p-edge0001' },
    '192.168.30.5': { profile: 'p-ups00001', port: 1161 },
  },
};

/**
 * The catalogue, theme-independent. `base` is the file stem; the emitted scene
 * names — and therefore the file names — are `<base>-dark` and `<base>-light`.
 */
const CATALOGUE = [
  {
    base: 'operations',
    tab: TABS.operations,
    // A walk's results are the component's own state, so they cannot be seeded
    // — the walk has to be run. This is the one place the harness presses
    // buttons, and it presses them by their label.
    // 'Table View', not 'Table'. Two buttons in the results toolbar start with
    // "Table ", the exporter comes first in the document, and the loose match
    // takes the earliest — so this pressed "Table CSV" and the only visible
    // effect was a toast saying the table had been exported. It looked like it
    // worked because Table View is already the default.
    act: ['WALK', 'Execute WALK', 'Table View'],
    // The fixture already claims 412 ms in its own responseTimeMs, and answering
    // instantly contradicted it: 155 varbinds landed in the same frame as the
    // click, so the button never showed that it was working.
    latency: { SnmpWalk: 900 },
    describe: 'A walk of ifTable rendered as a real table, split by INDEX.',
  },
  {
    base: 'mib-browser',
    tab: TABS.operations,
    // The picture had nothing selected, so the detail panel its caption promised
    // was not in it. The node is addressed by OID rather than by name because
    // "ifOperStatus" is ALSO the label of a favourite in the panel above, which
    // is what a text search finds first.
    //
    // Deliberately not filtering first, though that was the obvious move:
    // measured, typing into the search box replaces the tree with COMPACTED
    // ancestor paths — three rows reading "mgmt .mib-2 .interfaces .ifTable
    // .ifEntry" — and the leaf, which is the thing being selected, is no longer
    // in the document at all.
    act: ['sel:[data-oid="1.3.6.1.2.1.2.2.1.8"] .node-label'],
    describe: 'The MIB tree, searched, with a node selected and its detail shown.',
  },
  {
    base: 'monitor-charts',
    tab: TABS.monitor,
    // Six hours of history are seeded, and then the samples keep coming: for the
    // still this changes nothing that virtual time does not spend instantly, and
    // it is what makes the clip of this screen worth recording at all.
    feed: { from: 'monitorSamples', everyMs: 420 },
    // Tall enough for the response-time chart below the main one; at 1100 it
    // was sliced through its legend, which reads as a broken layout.
    height: 1320,
    describe: 'Polling sessions charted over several hours, with a threshold crossed.',
  },
  {
    base: 'dashboard-preset',
    tab: TABS.dashboard,
    // The same feed the monitor scene uses. A dashboard with no samples in it
    // renders every tile as an em dash and every chart as an empty grid, which
    // is a picture of the empty state rather than of the feature.
    feed: { from: 'monitorSamples', everyMs: 420 },
    // Tall enough for the eight-port grid under the chart; the widgets stack
    // into one column below roughly 1100.
    height: 1200,
    describe: 'A dashboard drawn from a community preset bound to one switch.',
  },
  {
    base: 'dashboard-group',
    tab: TABS.dashboard,
    feed: { from: 'monitorSamples', everyMs: 420 },
    // The picker's first entry is the group; the individual equipments follow
    // it. By index because a group's value is its preset's whole widget
    // signature, which is not a thing to paste into a scene.
    act: ['pick:.picker select|0'],
    height: 1200,
    describe: 'One preset bound to two switches of different sizes, drawn as one dashboard.',
  },
  {
    base: 'trap-listener',
    tab: TABS.traps,
    // Who the listener hears is named above the traps: the default user, the
    // edge routers' own, and the lab kept out.
    settings: { ...PROFILED_ESTATE, v3: { user: 'noc-ro', secLevel: 'AuthPriv', authProto: 'SHA256', privProto: 'AES' } },
    // "with their varbinds, the listener running" was true of neither: every
    // row was collapsed behind its chevron and the header said the listener was
    // stopped, next to a Start Listening button nobody had pressed.
    act: ['Start Listening', 'sel:.trap-summary|0'],
    describe: 'Received traps with their varbinds, the listener running.',
  },
  {
    base: 'events-journal',
    tab: TABS.events,
    describe: 'The journal: traps, thresholds, a reachability loss, a system event.',
  },
  {
    base: 'history-diff',
    tab: TABS.history,
    // Nothing was ever diffed here — the scene had no steps at all, so the
    // picture was the plain history list with Diff Mode un-pressed, under a
    // caption about a comparison. Diff mode wants two entries chosen, A then B,
    // before it will offer Compare.
    // The OLDER walk is chosen first: diff mode labels them A then B in the
    // order they are picked, and A being the later of the two reads backwards.
    act: ['Diff Mode', 'sel:.entry-header|1', 'sel:.entry-header|0', 'Compare'],
    // The modal is taller than the list behind it.
    height: 1150,
    describe: 'Two walks of the same device, diffed side by side.',
  },
  {
    base: 'network-discovery',
    tab: TABS.discovery,
    height: 1150,
    // The sweep has to be RUN: its results are the component's own state, so
    // without this the picture is of an empty form saying "no scan results".
    act: ['Scan'],
    describe: 'A CIDR sweep that found a real estate, with each device named.',
  },
  {
    base: 'mib-editor',
    tab: TABS.mibeditor,
    height: 1100,
    // Without this the editor shows its empty state — "pick a MIB on the left"
    // — which is a picture of the file list, not of the editor.
    act: ['ACME-POE-MIB'],
    describe: 'A MIB open, highlighted, with the analysis pointing at the line.',
  },
  {
    base: 'target-manager',
    tab: TABS.operations,
    // Framed to the dialog. At 1100 the lower half was empty backdrop, which
    // makes a documentation image about a dialog mostly about nothing.
    height: 940,
    // A real estate rather than two devices: three groups, the two edge
    // routers migrated to a v3 credential profile while the rest were not, and
    // a UPS that only speaks v1 on a port of its own — the situation profiles
    // and overrides exist for.
    settings: {
      ...PROFILED_ESTATE,
      targetGroups: [
        { id: 'default', name: 'Default' },
        { id: 'core', name: 'Core switches' },
        { id: 'dist', name: 'Distribution' },
        { id: 'edge', name: 'Edge routers' },
      ],
      targetGroupAssignments: {
        '10.20.0.1': 'core', '10.20.0.2': 'core',
        '10.20.4.11': 'dist', '10.20.4.12': 'dist', '10.20.4.23': 'dist',
        '10.20.8.1': 'edge', '10.20.8.2': 'edge',
      },
    },
    act: ['Target'],
    describe: 'Managing targets: groups, credential profiles, per-device overrides, reachability.',
  },
  {
    base: 'settings-snmp',
    tab: TABS.operations,
    height: 1500,
    // A named v3 user, with the passphrases BLANK — which is not an oversight.
    // The interface never receives a stored credential back from the backend,
    // so an empty field beside a filled username is what a configured
    // installation actually looks like.
    //
    // The values are the ones the selects hold, not their labels: this said
    // 'authPriv', 'SHA-256' and 'AES-256', which match no option, so the
    // picture showed three EMPTY selects under a comment describing a
    // configured user.
    settings: {
      ...PROFILED_ESTATE,
      v3: { user: 'noc-ro', secLevel: 'AuthPriv', authProto: 'SHA256', privProto: 'AES' },
    },
    act: ['key:,', 'SNMP'],
    describe: 'The default identifiers, the credential profiles, and the store named.',
  },
  {
    base: 'settings-profile',
    tab: TABS.operations,
    height: 1100,
    settings: PROFILED_ESTATE,
    act: ['key:,', 'SNMP', 'sel:.profiles .list li .btn-copy-small|0'],
    describe: 'One credential profile: its SNMPv3 user, the targets given it, and a test.',
  },
  {
    base: 'settings-service',
    tab: TABS.operations,
    height: 1000,
    act: ['key:,', 'Service'],
    describe: 'Background mode, the trap listener at startup, and the login entry.',
  },
  {
    base: 'settings-notifications',
    tab: TABS.operations,
    height: 1250,
    act: ['key:,', 'Notifications'],
    describe: 'Destinations, routing rules, and the delivery log.',
  },
  {
    base: 'anonymous-mode',
    tab: TABS.operations,
    // NOT `settings: { anonymousMode: true }`, which is what this used to say —
    // and it silently produced an UNMASKED screenshot. settingsStore.js forces
    // the flag to false on load, deliberately, so that closing the application
    // can never leave someone's install in a masked state. Seeding it is
    // therefore impossible by design, and the only way in is the application's
    // own Ctrl+Shift+A — pressed after the walk, so there is something on
    // screen for it to mask.
    latency: { SnmpWalk: 900 },
    act: ['WALK', 'Execute WALK', 'key:shift+A'],
    describe: 'The same screen with every address replaced by a stable alias.',
  },
  {
    base: 'simulator',
    tab: TABS.operations,
    height: 900,
    bindings: {
      ListSimulatorModels: simulatorModels(),
      ListSimulatedDevices: [
        {
          id: 'c0ffee01', name: 'srv-web-01', model: 'linux-server', address: '127.0.0.2', port: 161,
          versions: ['v2c', 'v3'], users: [{ name: 'ops', secLevel: 'AuthPriv', authProto: 'SHA256', privProto: 'AES' }],
          engineId: '80001f880302a1b2c3d4e5', engineBoots: 3, running: true, packets: 1284,
          traps: {
            destinations: [
              { id: 'd1', host: '127.0.0.1', port: 162, version: 'v2c', inform: false, user: '', engineId: '',
                sent: 42, failed: 0, dropped: 0, lastError: '' },
              { id: 'd2', host: '192.0.2.50', port: 162, version: 'v3', inform: true, user: 'ops', engineId: '8000000005a1b2c3d4e5f60718',
                sent: 17, failed: 1, dropped: 0, lastError: 'the INFORM was not acknowledged' },
            ],
            onStart: true, onAuthFailure: true, schedules: [{ notification: 'linkDown', every: 300, irregular: true }], suppressed: 0,
          },
        },
        {
          id: 'c0ffee02', name: 'sw-floor-2', model: 'cisco-catalyst-48', address: '127.0.0.3', port: 161,
          versions: ['v2c'], users: [], engineId: '800000090302f6e5d4c3b2', engineBoots: 7, running: true, packets: 5310,
          traps: { destinations: [], onStart: false, onAuthFailure: false, schedules: [], suppressed: 0 },
          // Made to misbehave while it runs, which the list says on the device.
          faults: { latencyMs: 250, jitterMs: 50, lossPercent: 5, mute: false, error: '', errorPercent: 0, counterSpeed: 100 },
          autoStart: true,
        },
        {
          id: 'c0ffee04', name: 'ups-01', model: 'apc-smart-ups', address: '127.0.0.5', port: 161,
          versions: ['v1', 'v2c'], users: [], engineId: '8000013e0302b7a6c5d4e3', engineBoots: 1, running: false, packets: 0,
          traps: { destinations: [], onStart: false, onAuthFailure: false, schedules: [], suppressed: 0 },
        },
      ],
    },
    act: ['sel:.status-item.simulator|0'],
    describe: 'The simulator: a Linux server sending traps, a Catalyst switch made to misbehave and a UPS, each one click from being a target.',
  },
  {
    base: 'simulator-traps',
    tab: TABS.operations,
    height: 1600,
    bindings: {
      ListSimulatorModels: simulatorModels(),
      ListSimulatedDevices: [
        {
          id: 'c0ffee03', name: 'srv-edge-01', model: 'linux-server', address: '127.0.0.4', port: 161,
          versions: ['v2c'], users: [], engineId: '80001f880302c3b2a1f6e5', engineBoots: 2, running: false, packets: 0,
          traps: {
            destinations: [
              { id: 'd3', host: '127.0.0.1', port: 162, version: 'v2c', inform: false, user: '', engineId: '',
                sent: 0, failed: 0, dropped: 0, lastError: '' },
              { id: 'd4', host: '192.0.2.50', port: 162, version: 'v2c', inform: true, user: '', engineId: '',
                sent: 0, failed: 0, dropped: 0, lastError: '' },
            ],
            onStart: true,
            onAuthFailure: true,
            schedules: [{ notification: 'linkDown', every: 300, irregular: true }, { notification: '', every: 60, irregular: false }],
            suppressed: 0,
          },
        },
      ],
      SimulatorDeviceCredentials: { community: 'public', users: {}, destinations: { d3: 'public', d4: 'noc-traps' } },
    },
    // By its label: the first of a device's icon buttons is its faults, and the
    // editor opens on the device's own tab, not on what it sends.
    act: ['sel:.status-item.simulator|0', 'sel:.device .icon-btn[aria-label="Edit"]|0', 'sel:#sim-tab-traps'],
    describe: 'What a simulated device sends: traps to SnmpLens here and INFORMs to a collector on the network, at boot, on a refused request, and on a schedule.',
  },
  {
    base: 'simulator-editor',
    tab: TABS.operations,
    height: 1300,
    bindings: {
      ListSimulatorModels: simulatorModels(),
      ListSimulatorModelIcons: [{ id: 'custom:acme-crac', icon: ACME_CRAC_ICON }],
      ListSimulatedDevices: [],
      SimulatorSuggestAddress: { address: '127.0.0.2', port: 161 },
    },
    // The model picker, a custom model with its icon among the catalogue, on the
    // editor's first tab: what the device is and where it answers.
    act: ['sel:.status-item.simulator|0', 'New device'],
    describe: 'A new simulated device: its model — a custom one among the catalogue, with its icon — its name and its loopback address.',
  },
  {
    base: 'simulator-access',
    tab: TABS.operations,
    height: 1250,
    bindings: {
      ListSimulatorModels: simulatorModels(),
      ListSimulatorModelIcons: [{ id: 'custom:acme-crac', icon: ACME_CRAC_ICON }],
      ListSimulatedDevices: [],
      SimulatorSuggestAddress: { address: '127.0.0.2', port: 161 },
    },
    // v3 ticked beside v2c, a write community, a second user allowed to SET,
    // and every passphrase typed, so the tab has nothing left to fix.
    act: [
      'sel:.status-item.simulator|0', 'New device', 'sel:#sim-tab-access', 'sel:.versions input|2', 'Add a user',
      'type:#sim-write-community|private-rw',
      'type:#sim-v3-0-authPass|auth-pass-one', 'type:#sim-v3-0-privPass|priv-pass-one',
      'type:#sim-v3-1-authPass|auth-pass-two', 'type:#sim-v3-1-privPass|priv-pass-two',
      'sel:.user-write input|1',
    ],
    describe: 'Who may ask a simulated device, and who may write to it: a read and a write community, and two SNMPv3 users — the second allowed to SET.',
  },
  {
    base: 'simulator-data',
    tab: TABS.operations,
    height: 1650,
    bindings: {
      ListSimulatorModels: simulatorModels(),
      ListSimulatedDevices: [
        {
          id: 'c0ffee05', name: 'sw-lab-12', model: 'cisco-catalyst-24', address: '127.0.0.6', port: 161,
          versions: ['v2c'], users: [], engineId: '800000090302a7b6c5d4e3', engineBoots: 4, running: true, packets: 912,
          traps: { destinations: [], onStart: false, onAuthFailure: false, schedules: [], suppressed: 0 },
          params: { ports: 12 },
          overrides: [
            { oid: '.1.3.6.1.2.1.1.4.0', type: 'OctetString', value: 'noc@example.net', hex: false },
            { oid: '.1.3.6.1.4.1.9.2.1.58.0', type: 'Integer', value: '37', hex: false },
          ],
        },
      ],
      SimulatorDeviceCredentials: { community: 'public', writeCommunity: 'private-rw', users: {}, destinations: {} },
      SimulatorPreview: simulatorPreview(),
    },
    // Twelve ports on a 24-port model, a contact and a CPU figure of its own,
    // and below them what the device answers — named from the loaded MIBs,
    // each value saying whether it moves.
    act: ['sel:.status-item.simulator|0', 'sel:.device .icon-btn[aria-label="Edit"]|0', 'sel:#sim-tab-data'],
    describe: 'A simulated switch given twelve ports and values of its own, and a preview of what it answers — each object named from the MIBs, each value saying whether it moves.',
  },
];

// What SimulatorPreview answers for the Catalyst above: the system group and
// its first ports, as the loaded MIBs name them — and the two Cisco objects no
// bundled MIB describes, which keep their OID alone.
function simulatorPreview() {
  const row = (oid, name, type, value, behaviour = 'static') => ({ oid, name, type, value, behaviour });
  const rows = [
    row('.1.3.6.1.2.1.1.1.0', 'SNMPv2-MIB::sysDescr.0', 'OctetString',
      'Cisco IOS Software, C2960 Software (C2960-LANBASEK9-M), Version 15.0(2)SE11, RELEASE SOFTWARE (fc3)'),
    row('.1.3.6.1.2.1.1.2.0', 'SNMPv2-MIB::sysObjectID.0', 'ObjectIdentifier', '.1.3.6.1.4.1.9.1.716'),
    row('.1.3.6.1.2.1.1.3.0', 'SNMPv2-MIB::sysUpTime.0', 'TimeTicks', '1843', 'uptime'),
    row('.1.3.6.1.2.1.1.4.0', 'SNMPv2-MIB::sysContact.0', 'OctetString', 'noc@example.net'),
    row('.1.3.6.1.2.1.1.5.0', 'SNMPv2-MIB::sysName.0', 'OctetString', 'sw-lab-12'),
    row('.1.3.6.1.2.1.1.6.0', 'SNMPv2-MIB::sysLocation.0', 'OctetString', 'Wiring closet, floor 2'),
    row('.1.3.6.1.2.1.2.1.0', 'IF-MIB::ifNumber.0', 'Integer', '15'),
  ];
  for (const [i, descr, speed, up] of [[1, 'FastEthernet0/1', 100000000, true], [2, 'FastEthernet0/2', 100000000, true],
    [3, 'FastEthernet0/3', 100000000, false], [25, 'GigabitEthernet0/1', 1000000000, true]]) {
    rows.push(
      row(`.1.3.6.1.2.1.2.2.1.2.${i}`, `IF-MIB::ifDescr.${i}`, 'OctetString', descr),
      row(`.1.3.6.1.2.1.2.2.1.5.${i}`, `IF-MIB::ifSpeed.${i}`, 'Gauge32', String(speed)),
      row(`.1.3.6.1.2.1.2.2.1.8.${i}`, `IF-MIB::ifOperStatus.${i}`, 'Integer', up ? '1' : '2'),
      row(`.1.3.6.1.2.1.2.2.1.10.${i}`, `IF-MIB::ifInOctets.${i}`, 'Counter32', up ? String(1662931 * i) : '0', up ? 'counter' : 'static'),
    );
  }
  rows.push(
    row('.1.3.6.1.4.1.9.2.1.58.0', '', 'Integer', '37'),
    row('.1.3.6.1.4.1.9.9.109.1.1.1.1.8.1', '', 'Gauge32', '23', 'gauge'),
  );
  return { rows, total: 3894 };
}

// The simulator's catalogue as ListSimulatorModels serves it, in Go's order, and
// one custom model after it. A function declaration, so the catalogue above can
// call it before this line is reached.
function simulatorModels() {
  const generic = (own = []) => [
    { name: 'coldStart', oid: '.1.3.6.1.6.3.1.1.5.1' },
    { name: 'warmStart', oid: '.1.3.6.1.6.3.1.1.5.2' },
    ...own,
    { name: 'authenticationFailure', oid: '.1.3.6.1.6.3.1.1.5.5' },
  ];
  const link = [
    { name: 'linkDown', oid: '.1.3.6.1.6.3.1.1.5.3' },
    { name: 'linkUp', oid: '.1.3.6.1.6.3.1.1.5.4' },
  ];
  const config = { name: 'ciscoConfigManEvent', oid: '.1.3.6.1.4.1.9.9.43.2.0.1' };
  return [
    { id: 'linux-server', category: 'server', params: [{ name: 'cpus', min: 1, max: 64, default: 2 }],
      notifications: generic([...link,
        { name: 'nsNotifyShutdown', oid: '.1.3.6.1.4.1.8072.4.0.2' },
        { name: 'nsNotifyRestart', oid: '.1.3.6.1.4.1.8072.4.0.3' }]) },
    { id: 'windows-server', category: 'server', notifications: generic(link) },
    { id: 'dell-idrac9', category: 'server', notifications: generic([
      { name: 'alertTemperatureProbeWarning', oid: '.1.3.6.1.4.1.674.10892.5.3.2.1.0.2162' }]) },
    { id: 'cisco-catalyst-24', category: 'network', params: [{ name: 'ports', min: 2, max: 24, default: 24 }],
      notifications: generic([...link, config]) },
    { id: 'cisco-catalyst-48', category: 'network', params: [{ name: 'ports', min: 2, max: 48, default: 48 }],
      notifications: generic([...link, config]) },
    { id: 'cisco-isr-4331', category: 'network', notifications: generic([...link,
      { name: 'bgpEstablishedNotification', oid: '.1.3.6.1.2.1.15.0.1' },
      { name: 'bgpBackwardTransNotification', oid: '.1.3.6.1.2.1.15.0.2' }, config]) },
    { id: 'mikrotik-rb4011', category: 'network', notifications: generic([...link,
      { name: 'mtxrTemperatureException', oid: '.1.3.6.1.4.1.14988.1.1.9.0.2' }]) },
    { id: 'fortigate-60f', category: 'security', notifications: generic([...link,
      { name: 'fnTrapCpuThreshold', oid: '.1.3.6.1.4.1.12356.100.1.3.0.101' },
      { name: 'fnTrapMemThreshold', oid: '.1.3.6.1.4.1.12356.100.1.3.0.102' }]) },
    { id: 'unifi-u6-pro', category: 'wireless', notifications: generic([link[1]]) },
    { id: 'synology-nas', category: 'storage', params: [{ name: 'disks', min: 1, max: 9, default: 4 }],
      notifications: generic(link) },
    { id: 'apc-smart-ups', category: 'power', notifications: generic([{ name: 'upsTrapOnBattery', oid: '.1.3.6.1.2.1.33.2.1' }]) },
    { id: 'apc-rack-pdu', category: 'power', params: [{ name: 'outlets', min: 8, max: 24, default: 8 }], notifications: generic([
      { name: 'rPDUOutletOff', oid: '.1.3.6.1.4.1.318.0.269' },
      { name: 'rPDUNearOverload', oid: '.1.3.6.1.4.1.318.0.274' }]) },
    { id: 'hp-laserjet', category: 'printing', notifications: generic([{ name: 'printerV2Alert', oid: '.1.3.6.1.2.1.43.18.2.0.1' }]) },
    { id: 'environment-probe', category: 'environment', notifications: generic() },
    { id: 'custom:acme-crac', category: 'environment', custom: true, name: 'Acme CRAC-40 cooling unit', vendor: 'Acme',
      description: 'A computer room air conditioner: three temperature sensors, the fan speed, the compressor’s running time, and an alarm when the return air is too warm.',
      notifications: generic([{ name: 'acmeHighTemperature', oid: '.1.3.6.1.4.1.32473.0.1' }]) },
  ];
}

export function buildScenes(seeds) {
  const base = baseSeeds(seeds);
  return CATALOGUE.flatMap((entry) =>
    THEMES.map((theme) => scene(base, `${entry.base}-${theme}`, { ...entry, theme })),
  );
}

/**
 * Scenes for CHECKS, not for the site. tools/editorcheck.mjs loads them in a
 * real browser and measures the page; they are in the bundle beside the site's
 * scenes, and tools/screenshots.mjs, which reads buildScenes alone, never
 * captures them.
 */
export function checkScenes(seeds) {
  const base = baseSeeds(seeds);
  const { content, diagnostics } = geometryMib();
  return [
    scene(base, 'check-mib-editor-geometry', {
      tab: TABS.mibeditor,
      theme: 'dark',
      width: 1400,
      height: 900,
      bindings: {
        MibEditorList: [{ name: 'GEOMETRY-MIB', size: content.length, bundled: false, modified: false, problems: 0 }],
        MibEditorRead: {
          name: 'GEOMETRY-MIB', path: '', content, eol: 'lf', encoding: 'utf-8',
          bundled: false, external: false, sha256: 'geometry', diagnostics,
        },
        MibEditorAnalyse: { diagnostics, missing: [], outline: [] },
        MibEditorReadDraft: '',
      },
      act: ['GEOMETRY-MIB'],
      describe: 'A MIB whose problems sit after tabs, accented and wide characters, near the top and far down.',
    }),
  ];
}

/**
 * A MIB written to make underlines hard to place: tab indentation, accented and
 * CJK characters before the word on the same line, and a problem two hundred
 * lines down so the check has to scroll to it. Each diagnostic is located by
 * searching the text, so the positions cannot drift from the content.
 */
export function geometryMib() {
  const lines = [
    'GEOMETRY-MIB DEFINITIONS ::= BEGIN',
    'IMPORTS',
    '\tOBJECT-TYPE, Integer32, enterprises',
    '\t\tFROM SNMPv2-SMI;',
    '',
    'geoRoot OBJECT IDENTIFIER ::= { enterprises 99979 }',
    '',
    'geoTabbed OBJECT-TYPE',
    '\tSYNTAX\tIntegerr32',
    '\tMAX-ACCESS\tread-only',
    '\tSTATUS\tcurrent',
    '\tDESCRIPTION\t"Température — 温度 ±" ::= { geoRot 1 }',
    '',
  ];
  for (let i = 2; i <= 32; i++) {
    lines.push(
      `geoFiller${i} OBJECT-TYPE`,
      '    SYNTAX      Integer32',
      '    MAX-ACCESS  read-only',
      '    STATUS      current',
      `    DESCRIPTION "Filler ${i}, so that the last problem is far down."`,
      `    ::= { geoRoot ${i} }`,
      '',
    );
  }
  lines.push(
    'geoLast OBJECT-TYPE',
    '\t\tSYNTAX\t\tUnknownType',
    '    MAX-ACCESS  read-only',
    '    STATUS      current',
    '    DESCRIPTION "The last one."',
    '    ::= { geoRoot 99 }',
    'END',
    '',
  );
  const content = lines.join('\n');

  const find = (needle, severity, code) => {
    const index = lines.findIndex((l) => l.includes(needle));
    return {
      line: index + 1, column: lines[index].indexOf(needle) + 1,
      severity, code, symbol: needle, message: `${needle} is placed here for the geometry check`,
    };
  };
  return {
    content,
    diagnostics: [
      find('geoTabbed', 'warning', 'no-description'),
      find('Integerr32', 'error', 'unknown-type'),
      find('geoRot', 'error', 'unknown-parent'),
      find('UnknownType', 'error', 'unknown-type'),
    ],
  };
}
