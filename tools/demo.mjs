/**
 * Build the browser demo into docs/demo/.
 *
 *   node tools/demo.mjs
 *
 * The real application with fixtures instead of a backend — the same bundle the
 * screenshots are taken from, so it cannot show an interface the product does
 * not actually produce. See frontend/vite.demo.config.js for what differs.
 *
 * This is a separate command rather than part of tools/build.mjs on purpose: the
 * demo is part of the SITE, not of the product, and a release should not be able
 * to fail because a page under docs/ did not compile.
 */
import { spawnSync } from 'node:child_process';
import { existsSync, readdirSync, statSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..');
const frontend = join(repo, 'frontend');
const out = join(repo, 'docs', 'demo');

const seeds = join(frontend, 'screenshots', 'bridge', 'seeds.json');
if (!existsSync(seeds)) {
  console.error('No fixtures yet. Run: node tools/genbridge.mjs tools/screenshot-spec.json');
  process.exit(1);
}

const viteBin = join(frontend, 'node_modules', 'vite', 'bin', 'vite.js');
if (!existsSync(viteBin)) {
  console.error('Vite is not installed. Run: cd frontend && npm install');
  process.exit(1);
}

console.log('Building the browser demo…');
const build = spawnSync(
  process.execPath,
  [viteBin, 'build', '--config', 'vite.demo.config.js', '--logLevel', 'error'],
  { cwd: frontend, stdio: 'inherit' },
);
if (build.status !== 0) {
  console.error('The demo did not build.');
  process.exit(1);
}

function weigh(dir) {
  let bytes = 0;
  let files = 0;
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) {
      const inner = weigh(p);
      bytes += inner.bytes;
      files += inner.files;
    } else {
      bytes += st.size;
      files += 1;
    }
  }
  return { bytes, files };
}

const { bytes, files } = weigh(out);
console.log(`\ndocs/demo/ — ${files} files, ${Math.round(bytes / 1024)} KB.`);
console.log('Serve docs/ and open /demo/ to try it.');
