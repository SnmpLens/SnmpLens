import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { fileURLToPath } from 'node:url';
import { readFileSync } from 'node:fs';
import { buildScenes } from './screenshots/scenes.js';
import { demoBindings } from './screenshots/demoBindings.js';

/**
 * The browser demo: the real application, with fixtures instead of a backend.
 *
 *   node ../tools/demo.mjs        builds into docs/demo/
 *
 * It is the same bundle the screenshots are taken from — the same Svelte
 * components, the same stylesheet, the same translations, the same stubbed
 * bridge — because a demo drawn separately drifts from the product the first
 * time anything changes, and then it is worse than no demo at all.
 *
 * What differs is entirely in the director (screenshots/demo.js): it seeds
 * without clearing, it does not pin the language, and it sets the flag that
 * makes the bridge REFUSE the calls that reach for the operating system. See
 * the comments there.
 *
 * `base: './'` matters: GitHub Pages serves this from /SnmpLens/demo/, and the
 * default absolute base would ask for /assets/… at the domain root.
 */
function stubWailsBridge() {
  const bridge = fileURLToPath(new URL('./screenshots/bridge/App.js', import.meta.url));
  const runtime = fileURLToPath(new URL('./screenshots/bridge/runtime.js', import.meta.url));

  return {
    name: 'snmplens-demo-bridge',
    enforce: 'pre',
    resolveId(source) {
      const id = source.replace(/\\/g, '/');
      if (/(^|\/)bridge\/App(\.js)?$/.test(id)) return bridge;
      if (/(^|\/)bridge\/runtime(\.js)?$/.test(id)) return runtime;
      return null;
    },
  };
}

function injectDemo() {
  const seeds = JSON.parse(
    readFileSync(fileURLToPath(new URL('./screenshots/bridge/seeds.json', import.meta.url)), 'utf8'),
  );

  // The opening state is the operations scene's, minus the two things that are
  // right for a photograph and wrong for a person: a pinned language and a
  // pinned theme. Taking it from the catalogue rather than writing it again is
  // what stops the demo and the screenshots drifting apart.
  const scenes = buildScenes(seeds);
  const base = scenes.find((s) => s.name === 'operations-dark');
  // The simulator's answers come from the scenes that photograph it, for the
  // same reason: see screenshots/demoBindings.js.
  const bindings = demoBindings(scenes);
  const demoSeeds = { ...base.seeds };
  try {
    const settings = JSON.parse(demoSeeds.settings || '{}');
    delete settings.locale;
    delete settings.theme;
    demoSeeds.settings = JSON.stringify(settings);
  } catch { /* the raw blob is still better than nothing */ }

  const banner = readFileSync(
    fileURLToPath(new URL('./screenshots/demo-banner.html', import.meta.url)), 'utf8',
  );

  return {
    name: 'snmplens-demo-scene',
    transformIndexHtml() {
      const src = readFileSync(
        fileURLToPath(new URL('./screenshots/demo.js', import.meta.url)), 'utf8',
      )
        .replace('__SEEDS__', () => JSON.stringify(demoSeeds))
        .replace('__BINDINGS__', () => JSON.stringify(bindings));

      for (const placeholder of ['__SEEDS__', '__BINDINGS__']) {
        if (src.includes(placeholder)) {
          throw new Error(`screenshots/demo.js still contains ${placeholder} after substitution.`);
        }
      }

      return {
        // A CLASSIC script in the head, for the same reason the screenshot
        // director is one: settingsStore.js reads localStorage at module
        // evaluation, and a module script is deferred until after that.
        tags: [
          { tag: 'script', injectTo: 'head-prepend', children: src },
          // The application's own index.html has no favicon — it is a desktop
          // window, which has an icon rather than a tab. Served as a page it
          // asks for /favicon.ico and gets a 404.
          {
            tag: 'link',
            injectTo: 'head',
            attrs: { rel: 'icon', type: 'image/png', href: '../assets/img/favicon-48.png' },
          },
          {
            tag: 'meta',
            injectTo: 'head',
            attrs: { name: 'robots', content: 'noindex' },
          },
          { tag: 'div', injectTo: 'body', attrs: { id: 'demo-banner' }, children: banner },
        ],
      };
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [stubWailsBridge(), injectDemo(), svelte()],
  build: {
    outDir: '../docs/demo',
    emptyOutDir: true,
    sourcemap: false,
  },
});
