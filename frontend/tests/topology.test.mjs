// The network map's renderer side: the request, checked against the Go struct
// it is unmarshalled into, and the layout, which must be the same every time it
// is drawn and keep every device on the page.
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

const dir = mkdtempSync(join(tmpdir(), 'snmplens-topology-'));
writeFileSync(join(dir, 'stub.js'), ['SettingsKeyStatus', 'SettingsSeal', 'SettingsOpen', 'SettingsAdoptKey',
  'SettingsForgetKey', 'EventsOn'].map((n) => `export const ${n} = async () => {};`).join('\n'));
const out = join(dir, 'topology.mjs');
await esbuild.build({
  entryPoints: [join(process.cwd(), 'src/utils/topology.js')],
  bundle: true, format: 'esm', outfile: out, platform: 'node', logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /wailsjs[/](go[/]app[/]App|runtime[/]runtime)$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});
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
const T = await import(pathToFileURL(out).href);

// --- the request, against the Go struct -------------------------------------
const go = readFileSync(join(process.cwd(), '..', 'internal', 'app', 'topology.go'), 'utf8');
const body = go.match(/type TopologyRequest struct \{([\s\S]*?)\n\}/)[1];
const declared = [...body.matchAll(/json:"([^",]+)/g)].map((m) => m[1]).sort();
const settings = {
  targets: '10.0.0.1\n10.0.0.2\n// 10.0.0.3', community: 'public', snmpVersion: 'v2c', port: 161, timeout: 2, retries: 1,
  v3: { user: '', authProto: 'SHA', authPass: '', privProto: 'AES', privPass: '', secLevel: 'NoAuthNoPriv', contextName: '' },
  targetOverrides: { '10.0.0.2': { community: 'core-only' } },
};
const req = T.buildTopologyRequest(settings, ['10.0.0.1'], 9);
check('the request carries exactly the fields Go declares',
  JSON.stringify(Object.keys(req).sort()) === JSON.stringify(declared), `${Object.keys(req)} vs ${declared}`);
check('the depth is bounded', req.depth === 3);
check('a neighbour that is a target is asked with its own identifiers',
  req.known.find((r) => r.targets[0] === '10.0.0.2')?.community === 'core-only' && req.known.length === 2);
check('anything else, with the defaults', req.template.community === 'public' && req.template.targets.length === 0);

// --- the layout ---------------------------------------------------------------
const graph = {
  nodes: ['core', 'a1', 'a2', 'a3', 'ap', 'lonely'].map((id) => ({ id })),
  links: [{ a: 'core', b: 'a1' }, { a: 'core', b: 'a2' }, { a: 'core', b: 'a3' }, { a: 'a1', b: 'ap' }],
};
const one = T.layout(graph, { width: 800, height: 600 });
const two = T.layout(graph, { width: 800, height: 600 });
check('the same map is drawn the same way', JSON.stringify(one) === JSON.stringify(two));
const pts = Object.values(one);
check('every device is on the page',
  pts.length === 6 && pts.every((p) => Number.isFinite(p.x) && p.x >= 60 && p.x <= 740 && p.y >= 60 && p.y <= 540));
const dist = (a, b) => Math.hypot(one[a].x - one[b].x, one[a].y - one[b].y);
check('no two devices on one spot', pts.every((p, i) => pts.every((q, j) => i === j || Math.hypot(p.x - q.x, p.y - q.y) > 20)));
check('a linked device is drawn nearer than an unlinked one', dist('a1', 'ap') < dist('a3', 'ap'));
check('one device is drawn in the middle', JSON.stringify(T.layout({ nodes: [{ id: 'x' }] }, { width: 100, height: 80 })) === '{"x":{"x":50,"y":40}}');

const links = T.linksOf({ links: [{ a: 'x', aPort: 'p1', b: 'y', bPort: 'p2', protocol: 'lldp' }] }, 'y');
check('a link is described from the end asked about', links[0].other === 'x' && links[0].port === 'p2' && links[0].remotePort === 'p1');

process.exit(failures ? 1 : 0);
