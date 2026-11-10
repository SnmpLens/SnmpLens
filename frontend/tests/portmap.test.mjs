// The port mapper's renderer side: the request it builds, checked against the
// Go struct it is unmarshalled into (a key Go does not declare is dropped
// without a word, a field it declares and nobody sends arrives as zero), and the
// table a map is read as.
import * as esbuild from 'esbuild';
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const dir = mkdtempSync(join(tmpdir(), 'snmplens-portmap-'));
writeFileSync(join(dir, 'stub.js'), ['SettingsKeyStatus', 'SettingsSeal', 'SettingsOpen', 'SettingsAdoptKey',
  'SettingsForgetKey', 'EventsOn'].map((n) => `export const ${n} = async () => {};`).join('\n'));
const out = join(dir, 'portMap.mjs');
await esbuild.build({
  entryPoints: [join(process.cwd(), 'src/utils/portMap.js')],
  bundle: true, format: 'esm', outfile: out, platform: 'node', logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /wailsjs[/](go[/]app[/]App|runtime[/]runtime)$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});
// targets.js reaches the settings store through anonymize.js, and the store
// reads localStorage when it loads.
const mem = new Map();
globalThis.localStorage = {
  getItem: (k) => (mem.has(k) ? mem.get(k) : null),
  setItem: (k, v) => mem.set(k, String(v)),
  removeItem: (k) => mem.delete(k),
};
globalThis.window = { addEventListener() {}, matchMedia: () => ({ matches: false, addEventListener() {} }) };
if (!globalThis.navigator) {
  Object.defineProperty(globalThis, 'navigator', { value: { language: 'en' }, configurable: true });
}
const P = await import(pathToFileURL(out).href);

// --- the request, against the Go struct -------------------------------------
const go = readFileSync(join(process.cwd(), '..', 'internal', 'app', 'portmap.go'), 'utf8');
const body = go.match(/type PortMapRequest struct \{([\s\S]*?)\n\}/)[1];
const declared = [...body.matchAll(/json:"([^",]+)/g)].map((m) => m[1]).sort();

const settings = {
  targets: '10.0.0.1\n10.0.0.254', community: 'public', snmpVersion: 'v2c', port: 161, timeout: 2, retries: 1,
  v3: { user: '', authProto: 'SHA', authPass: '', privProto: 'AES', privPass: '', secLevel: 'NoAuthNoPriv', contextName: '' },
  targetOverrides: { '10.0.0.254': { community: 'router-only' } },
};
const req = P.buildPortMapRequest(settings, '10.0.0.1', ['10.0.0.254', '10.0.0.1', ''], true);
check('the request carries exactly the fields Go declares',
  JSON.stringify(Object.keys(req).sort()) === JSON.stringify(declared), `${Object.keys(req)} vs ${declared}`);
check('each device is asked with its own identifiers',
  req.switch.community === 'public' && req.gateways.length === 1 && req.gateways[0].community === 'router-only');
check('the switch is not its own gateway, and a blank is no gateway', req.gateways.every((g) => g.targets[0] === '10.0.0.254'));

// --- the table ----------------------------------------------------------------
const map = {
  ports: [
    { ifIndex: 1, name: 'Gi1/0/1', alias: 'desk 12', up: true, vlan: 10, uplink: false, stations: [
      { mac: '00:11:22:33:44:55', vlan: 10, ips: ['192.0.2.10'], names: ['pc-12.example'] },
      { mac: '00:11:22:33:44:66', vlan: 10, ips: [], names: [] },
    ] },
    { ifIndex: 2, name: 'Gi1/0/2', up: false, vlan: 10, uplink: false, stations: [] },
    { ifIndex: 3, name: 'Gi1/0/3', up: true, vlan: 1, uplink: true, neighbour: { name: 'core', port: 'Te1/1/1', protocol: 'lldp' }, stations: [] },
  ],
  unplaced: [], stations: 2, source: 'bridge', warnings: [],
};
let rows = P.portRows(map);
check('a row per station, and one for a port with none', rows.length === 4, String(rows.length));
check('the port is drawn on its first row only', rows[0].first && !rows[1].first && rows[2].first);
rows = P.portRows(map, { usedOnly: true });
check('the ports in use: a station or a neighbour', rows.map((r) => r.port.ifIndex).join() === '1,1,3');
check('a MAC found the way a Cisco prints it', P.portRows(map, { query: '0011.2233.4466' }).length === 1);
check('an IP, a name, a port, an alias are found',
  P.portRows(map, { query: '192.0.2.10' }).length === 1 && P.portRows(map, { query: 'pc-12' }).length === 1 &&
  P.portRows(map, { query: 'gi1/0/3' }).length === 1 && P.portRows(map, { query: 'desk' }).length === 2);
check('a MAC written with dashes is the same MAC', P.portRows(map, { query: '00-11-22-33-44-55' }).length === 1);
check('every row key is distinct', new Set(P.portRows(map).map((r) => r.key)).size === 4);

const csv = P.csvRows(map);
check('the CSV has a header, a row per station and one per empty port', csv.length === 5 && csv[0][0] === 'port');
check('the CSV says which port is an uplink, and to whom', csv[4][5] === 'core Te1/1/1' && csv[4][6] === 'yes');

process.exit(failures ? 1 : 0);
