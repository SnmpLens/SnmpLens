#!/usr/bin/env node
//
//   node tools/actionpins.mjs
//
// Two rules about the way `.github/workflows` pins third-party actions, both of
// which have already been broken once.
//
// ONE ACTION, ONE SHA. `github/codeql-action/init` writes a configuration file
// that `github/codeql-action/analyze` reads back, and the two compare versions:
// with one bumped and not the other the run ends on "Loaded a configuration
// file for version '4.38.1', but running version '4.38.2'". Dependabot sees two
// `uses:` lines, raises two pull requests, and has no way to know they are one
// pin — so each is red on its own and green on everything else, which reads as
// a flaky upgrade rather than a pair that must move together. It happened at
// 4.37.9 to 4.38.0 and again at 4.38.1 to 4.38.2.
//
// PINNED BY SHA. The repository's rule is that actions are pinned by commit,
// not by tag: a tag is a moving pointer the owner can repoint, so `@v4` is a
// promise that whatever that name means on the day CI runs is what runs. The
// rule holds today, and this is what keeps it holding after the next edit.
//
// Deliberately NOT checked: whether a pin is the newest release. That is
// Dependabot's job, and a check that fails because something newer exists is a
// check that fails on a commit which changed nothing.

import { readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dir = join(root, '.github', 'workflows');

// `uses: owner/repo/optional/path@ref` — a local action (`./...`) or a
// container (`docker://...`) is not a pinned third-party action and is skipped.
const USES = /^\s*-?\s*uses:\s*([^\s@]+)@([^\s#]+)/;
const SHA = /^[0-9a-f]{40}$/;

const refs = [];
for (const file of readdirSync(dir).filter((f) => /\.ya?ml$/.test(f))) {
  const lines = readFileSync(join(dir, file), 'utf8').split('\n');
  lines.forEach((line, i) => {
    const m = USES.exec(line);
    if (!m) return;
    const [, path, ref] = m;
    if (path.startsWith('.') || path.startsWith('docker://')) return;
    // The repo is the first two segments; everything after is a subpath of the
    // same repository, and shares its single version.
    const repo = path.split('/').slice(0, 2).join('/');
    refs.push({ file, line: i + 1, path, repo, ref });
  });
}

let failures = 0;
const fail = (msg) => {
  console.error(`actionpins: ${msg}`);
  failures++;
};

if (refs.length === 0) fail('no action references found at all — the pattern or the path is wrong');

const floating = refs.filter((r) => !SHA.test(r.ref));
for (const r of floating) {
  fail(`${r.file}:${r.line} pins ${r.path} to "${r.ref}", which is not a commit SHA`);
}

const byRepo = new Map();
for (const r of refs) {
  if (!byRepo.has(r.repo)) byRepo.set(r.repo, []);
  byRepo.get(r.repo).push(r);
}

for (const [repo, group] of byRepo) {
  const distinct = [...new Set(group.map((r) => r.ref))];
  if (distinct.length === 1) continue;
  fail(
    `${repo} is pinned at ${distinct.length} different commits, and every path under one ` +
      `action shares its version:\n` +
      group.map((r) => `    ${r.file}:${r.line}  ${r.path}@${r.ref}`).join('\n') +
      `\n  Move them together, in one commit.`,
  );
}

if (failures) {
  process.exit(1);
}

console.log(
  `actionpins: ${refs.length} references over ${byRepo.size} actions, each pinned to one commit.`,
);
