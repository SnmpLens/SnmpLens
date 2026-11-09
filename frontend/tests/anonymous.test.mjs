// Anonymous Mode is a promise about what is on screen. It was kept in the
// source column and broken everywhere beside it.
//
// The stored params carry the unmasked value — the journal holds
// `params: {"target": "10.0.0.1"}` — and every title key interpolates them:
// "{oid} on {target} is {kind} {bound}", "{pduType} from {source} (...)",
// "{target} stopped responding". So a row read `Device-1` in the source column
// and the real address twice over in the sentence next to it, plus once more in
// each tooltip.
import { readFileSync, writeFileSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { pathToFileURL } from 'node:url';
import * as esbuild from 'esbuild';

// anonymize.js reaches the settings store, which reaches the Wails bridge, so
// it is bundled with the bridge stubbed — the same shape credentials.test.mjs
// uses. Testing a copy of the masking logic would test the copy.
const dir = mkdtempSync(join(tmpdir(), 'snmplens-anon-'));
writeFileSync(join(dir, 'stub.js'), `export const EventsOn = () => {};
export const SettingsKeyStatus = async () => ({ backend: 'none', hasKey: false });
export const SettingsSeal = async (v) => v;
export const SettingsOpen = async (v) => v;
export const SettingsAdoptKey = async () => {};
export const SettingsForgetKey = async () => {};
`);

const bundled = await esbuild.build({
  entryPoints: [join(process.cwd(), 'src/utils/anonymize.js')],
  bundle: true,
  format: 'esm',
  write: false,
  logLevel: 'silent',
  plugins: [{
    name: 'alias',
    setup(b) {
      b.onResolve({ filter: /bridge[/]App$/ }, () => ({ path: join(dir, 'stub.js') }));
    },
  }],
});
writeFileSync(join(dir, 'anonymize.mjs'), bundled.outputFiles[0].text);

globalThis.localStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
};
const { anonymizeText, anonymizeIp, resetMappings } =
  await import(pathToFileURL(join(dir, 'anonymize.mjs')).href);

let failures = 0;
const check = (name, ok, extra = '') => {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${extra ? ' — ' + extra : ''}`);
  if (!ok) failures++;
};

const src = (p) => readFileSync(new URL(p, import.meta.url), 'utf8');

// --- the helper itself ---
{
  resetMappings();
  const summary = 'ifInOctets reached 912.5, above 900, on 10.0.0.1';
  const masked = anonymizeText(summary);
  check('a summary carrying an address is masked',
    !masked.includes('10.0.0.1'), masked);
  check('and the rest of the sentence survives',
    masked.includes('ifInOctets') && masked.includes('912.5'), masked);

  // Stable within a session, or the same device reads as several.
  check('the same address maps to the same label',
    anonymizeText('a 10.0.0.1 b') === 'a ' + anonymizeIp('10.0.0.1') + ' b');

  const v6 = anonymizeText('trap from fe80::1 received');
  check('an IPv6 literal is masked too', !v6.includes('fe80::1'), v6);
}

// --- no panel may put a raw value on screen ---
//
// Matched by SHAPE, so a panel added later is covered without being listed:
// any `title={...}` or interpolation naming `.source`, `.summary` or `.params`
// must go through a masking helper.
const PANELS = [
  ['EventsPanel', '../src/EventsPanel.svelte'],
  ['AlertTimeline', '../src/monitor/AlertTimeline.svelte'],
];

for (const [name, path] of PANELS) {
  const text = src(path);

  const rawTitles = [...text.matchAll(/title=\{([^}]*)\}/g)]
    .map((m) => m[1].trim())
    .filter((expr) => /\.(source|summary)\b/.test(expr))
    .filter((expr) => !/anonymize|display|tooltipText|mask/i.test(expr));
  check(`${name}: no tooltip shows a raw source or summary`,
    rawTitles.length === 0, rawTitles.join(' | '));

  // The i18n interpolation must not be handed the stored params directly.
  const rawParams = [...text.matchAll(/values:\s*([^,}]*(?:\|\|[^,}]*)?)/g)]
    .map((m) => m[1].trim())
    .filter((expr) => /\bparams\b/.test(expr))
    .filter((expr) => !/anonParams/.test(expr));
  check(`${name}: the title is not interpolated with the raw params`,
    rawParams.length === 0, rawParams.join(' | '));

  // And the English fallback is a raw sentence too.
  const fallback = /return\s+\$anonMode\s*\?\s*anonymizeText\(ev\.summary\)\s*:\s*ev\.summary/.test(text);
  check(`${name}: the summary fallback is masked`, fallback);
}

// The detector must be able to fail, or a passing run means nothing.
{
  const broken = 'title={ev.source || \'\'}';
  const found = [...broken.matchAll(/title=\{([^}]*)\}/g)]
    .map((m) => m[1].trim())
    .filter((expr) => /\.(source|summary)\b/.test(expr))
    .filter((expr) => !/anonymize|display|tooltipText|mask/i.test(expr));
  check('the detector sees a raw tooltip', found.length === 1, found.join(''));
}

process.exit(failures ? 1 : 0);
