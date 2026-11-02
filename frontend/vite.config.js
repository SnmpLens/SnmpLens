import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

/**
 * Reload the page when the generated Wails bindings change.
 *
 * `wails3 dev` regenerates frontend/bindings whenever a Go method is added or its
 * signature changes. That is a change to the module's EXPORTS, which hot module
 * replacement cannot patch — the page keeps the version it imported and fails
 * with "does not provide an export named X", which reads like a missing method
 * rather than a stale tab. A full reload is the correct response, and the only
 * one that leaves the running page agreeing with the running backend.
 */
function reloadOnBindings() {
  return {
    name: 'snmplens-reload-on-bindings',
    apply: 'serve',
    handleHotUpdate({file, server}) {
      if (file.replace(/\\/g, '/').includes('/bindings/')) {
        server.ws.send({type: 'full-reload', path: '*'})
        return []
      }
    },
  }
}

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [svelte(), reloadOnBindings()],
  server: {
    watch: {
      // Generated, and outside src: name it explicitly rather than rely on it
      // being reached through the module graph.
      ignored: ['!**/bindings/**'],
    },
  },
  build: {
    // Vite's 500 kB notice measures a download. This bundle is not downloaded:
    // `//go:embed all:frontend/dist` puts it inside the executable, and the
    // webview reads it from there — so splitting it would buy a second request
    // against a filesystem, and cost the code-splitting machinery to arrange it.
    //
    // The limit is raised rather than switched off, and only to just above what
    // the app currently weighs, so a real jump still says so.
    chunkSizeWarningLimit: 1000,
  },
})
