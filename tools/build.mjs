#!/usr/bin/env node
/**
 * Build SnmpLens for the platform this runs on, into build/bin/.
 *
 *   node tools/build.mjs                     # a production binary
 *   node tools/build.mjs --version v1.2.3    # the same, knowing what it is
 *   node tools/build.mjs --universal         # macOS: Intel and Apple Silicon
 *   node tools/build.mjs --dev [--run]       # what `wails3 dev` rebuilds and runs
 *
 * One script for a developer, CI and the release, so that CI building the way a
 * release builds is true by construction. Wails 3 builds through a Taskfile it
 * generates into the project, a few hundred lines per platform for cross-compiling,
 * packaging formats and mobile targets SnmpLens does not use; what is needed here is
 * this.
 */
import { spawn, spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
const flag = (name) => args.includes(name);
const option = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : '';
};

const dev = flag('--dev');
const version = option('--version');
const platform = process.platform;
const windows = platform === 'win32';

// GTK 3 and WebKitGTK 4.1, not Wails 3's default of GTK 4 and WebKitGTK 6.0. A
// binary replaced in place by the updater starts on whatever the machine has, and
// every machine running SnmpLens has these; an update that needed libraries nothing
// installed would be the last update it ever received. They also keep the web
// storage where it is.
const goTags = [...(dev ? [] : ['production']), ...(platform === 'linux' ? ['gtk3'] : [])];

function run(command, commandArgs, options = {}) {
  const r = spawnSync(command, commandArgs, { cwd: root, stdio: 'inherit', shell: windows, ...options });
  if (r.status !== 0) {
    console.error(`${command} ${commandArgs.join(' ')} failed`);
    process.exit(r.status ?? 1);
  }
}

function bindings() {
  const flags = goTags.includes('gtk3') ? ['-f', windows ? '"-tags gtk3"' : '-tags gtk3'] : [];
  run('wails3', ['generate', 'bindings', '-d', 'frontend/bindings', '-silent', ...flags]);
}

function frontend() {
  if (!existsSync(join(root, 'frontend', 'node_modules'))) {
    run('npm', ['ci'], { cwd: join(root, 'frontend') });
  }
  run('npm', ['run', 'build'], { cwd: join(root, 'frontend') });
}

function goBuild(output, env = {}) {
  const ldflags = [];
  if (!dev) ldflags.push('-w', '-s');
  if (!dev && windows) ldflags.push('-H', 'windowsgui');
  if (version) ldflags.push(`-X SnmpLens/pkg/updater.Version=${version}`);
  const buildArgs = ['build'];
  if (goTags.length) buildArgs.push('-tags', goTags.join(','));
  if (!dev) buildArgs.push('-trimpath');
  if (ldflags.length) buildArgs.push(`-ldflags=${windows ? `"${ldflags.join(' ')}"` : ldflags.join(' ')}`);
  buildArgs.push('-o', output, '.');
  run('go', buildArgs, { env: { ...process.env, ...env } });
}

// The icon, the manifest (the DPI awareness and the common controls the dialogs
// need) and the version block, linked in by being a .syso beside main.go.
function windowsResources() {
  const syso = 'rsrc_windows_amd64.syso';
  run('wails3', ['generate', 'syso', '-arch', 'amd64', '-icon', 'build/windows/icon.ico',
    '-manifest', 'build/windows/wails.exe.manifest', '-info', 'build/windows/info.json', '-out', syso]);
  return join(root, syso);
}

function macBundle(binary) {
  const app = join(root, 'build', 'bin', 'SnmpLens.app');
  rmSync(app, { recursive: true, force: true });
  mkdirSync(join(app, 'Contents', 'MacOS'), { recursive: true });
  mkdirSync(join(app, 'Contents', 'Resources'), { recursive: true });
  copyFileSync(binary, join(app, 'Contents', 'MacOS', 'SnmpLens'));
  rmSync(binary);
  run('wails3', ['generate', 'icons', '-input', 'build/appicon.png',
    '-macfilename', join(app, 'Contents', 'Resources', 'icons.icns')]);
  const plist = readFileSync(join(root, 'build', 'darwin', 'Info.plist'), 'utf8')
    .replaceAll('{{VERSION}}', (version || 'v1.0.0').replace(/^v/, ''));
  writeFileSync(join(app, 'Contents', 'Info.plist'), plist);
  // Apple Silicon refuses to run an unsigned binary, and lipo voids the signature
  // the linker gave each half.
  run('codesign', ['--force', '--deep', '--sign', '-', app]);
  run('codesign', ['--verify', '--deep', '--strict', app]);
}

mkdirSync(join(root, 'build', 'bin'), { recursive: true });
bindings();

if (dev) {
  // The interface comes from Vite, and main.go embeds what is there or fails.
  const dist = join(root, 'frontend', 'dist');
  if (!existsSync(join(dist, 'index.html'))) {
    mkdirSync(dist, { recursive: true });
    writeFileSync(join(dist, 'index.html'), '<!-- development build: the interface comes from Vite -->\n');
  }
  const binary = join('build', 'bin', windows ? 'SnmpLens-dev.exe' : 'SnmpLens-dev');
  goBuild(binary);
  if (flag('--run')) {
    const child = spawn(join(root, binary), { cwd: root, stdio: 'inherit' });
    child.on('exit', (code) => process.exit(code ?? 0));
    for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
  }
} else {
  frontend();
  if (windows) {
    const syso = windowsResources();
    goBuild(join('build', 'bin', 'SnmpLens.exe'));
    rmSync(syso);
  } else if (platform === 'darwin') {
    // What Wails 3 asks of macOS, and what its Taskfile sets.
    const target = { CGO_ENABLED: '1', MACOSX_DEPLOYMENT_TARGET: '12.0',
      CGO_CFLAGS: '-mmacosx-version-min=12.0', CGO_LDFLAGS: '-mmacosx-version-min=12.0' };
    const binary = join('build', 'bin', 'SnmpLens');
    if (flag('--universal')) {
      for (const goarch of ['amd64', 'arm64']) goBuild(`${binary}-${goarch}`, { ...target, GOARCH: goarch });
      run('lipo', ['-create', '-output', binary, `${binary}-amd64`, `${binary}-arm64`]);
      rmSync(join(root, `${binary}-amd64`));
      rmSync(join(root, `${binary}-arm64`));
    } else {
      goBuild(binary, target);
    }
    macBundle(join(root, binary));
  } else {
    goBuild(join('build', 'bin', 'SnmpLens'));
  }
}
