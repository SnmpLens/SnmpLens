#!/usr/bin/env node
//
//   node tools/actionpins.mjs
//
// Three rules about the way `.github/workflows` pins third-party actions, each
// of which has already been broken.
//
// ONE ACTION, ONE SHA. `github/codeql-action/init` writes a configuration file
// that `github/codeql-action/analyze` reads back, and the two compare versions:
// with one bumped and not the other the run ends on "Loaded a configuration
// file for version '4.38.1', but running version '4.38.2'". Dependabot sees two
// `uses:` lines and raises two pull requests — so each is red on its own and
// green on everything else, which reads as a flaky upgrade rather than a pair
// that must move together. It happened at 4.37.9 to 4.38.0 and again at 4.38.1
// to 4.38.2.
//
// ONE ACTION, ONE DEPENDABOT GROUP. The rule above catches the split; it does
// not stop the next one, and it kept recurring — 4.38.3, then 4.39.0, each
// fixed by a third pull request written by hand. Dependabot treats every path
// as a separate dependency unless a `groups` pattern in .github/dependabot.yml
// covers them all, and then it raises one pull request that moves them
// together. So an action used under more than one path must be covered by a
// single pattern there; this fails on the commit that adds the second path,
// not on the release that splits them weeks later.
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

// The group patterns of the github-actions entry in .github/dependabot.yml,
// read without a YAML parser: the repository has no dependency to parse it
// with, and the file is ours. A list item under `patterns:` is collected only
// inside that ecosystem's entry, since a Go module group cannot move an action.
function groupPatterns() {
  const lines = readFileSync(join(root, '.github', 'dependabot.yml'), 'utf8').split(/\r?\n/);
  const patterns = [];
  let ecosystem = '';
  let indent = -1;
  for (const line of lines) {
    const eco = /^\s*-?\s*package-ecosystem:\s*["']?([\w-]+)/.exec(line);
    if (eco) {
      ecosystem = eco[1];
      indent = -1;
      continue;
    }
    const key = /^(\s*)patterns:\s*$/.exec(line);
    if (key) {
      indent = key[1].length;
      continue;
    }
    if (indent < 0 || line.trim() === '' || line.trim().startsWith('#')) continue;
    // YAML allows the items at the key's own indentation, hence `>=`.
    const item = /^(\s*)-\s*["']?([^"'\s#]+)["']?\s*(#.*)?$/.exec(line);
    if (item && item[1].length >= indent) {
      if (ecosystem === 'github-actions') patterns.push(item[2]);
      continue;
    }
    indent = -1;
  }
  return patterns;
}

// Dependabot's patterns know one wildcard, `*`, for any run of characters.
const glob = (p) =>
  new RegExp('^' + p.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*') + '$');

const patterns = groupPatterns().map((p) => ({ p, re: glob(p) }));
let split = 0;
for (const [repo, group] of byRepo) {
  const paths = [...new Set(group.map((r) => r.path))];
  if (paths.length === 1) continue;
  split++;
  if (patterns.some(({ re }) => paths.every((path) => re.test(path)))) continue;
  fail(
    `${repo} is used under ${paths.length} paths (${paths.join(', ')}) and no single group ` +
      `pattern in .github/dependabot.yml covers them all, so each release arrives as ` +
      `${paths.length} pull requests that cannot pass one at a time.\n` +
      `  Add a group to the github-actions entry with a pattern such as "${repo}*".`,
  );
}

if (failures) {
  process.exit(1);
}

console.log(
  `actionpins: ${refs.length} references over ${byRepo.size} actions, each pinned to one commit; ` +
    `${split} used under several paths, each moved by one Dependabot group.`,
);
