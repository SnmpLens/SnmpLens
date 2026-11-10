import './style.css'
import './styles/shared.css'
import { setupI18n } from './i18n/index.js'
import { mount } from 'svelte'
import App from './App.svelte'
import { EventsOn } from '../wailsjs/runtime/runtime'

// `new App({ target })` was the Svelte 3 and 4 way and Svelte 5 refuses it
// outright (`component_api_invalid_new`). A component is no longer a class, so
// there is nothing to construct; `mount` is the replacement and this is the only
// call site in the app.
//
// setupI18n() must still resolve BEFORE this runs. svelte-i18n throws on a
// `$_(...)` evaluated before a locale is loaded, and App.svelte reads one in its
// first render — so mounting early does not show untranslated text, it shows
// nothing at all.
setupI18n().then(() => {
  mount(App, {
    target: document.getElementById('app')
  })
})

// The window moved to another supervision — the system service's, or its own
// once the service stopped. Every store read from the one before, so the
// interface starts again and reads everything afresh from the one it drives now.
EventsOn('backend:changed', () => window.location.reload())
