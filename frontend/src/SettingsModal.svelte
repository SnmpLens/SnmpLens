<script>
  import { createEventDispatcher, onMount, tick } from 'svelte';
  import { GetPersistentMibDirectory } from '../wailsjs/go/app/App';
  import { onBackdrop } from './utils/modal';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { settingsStore } from './stores/settingsStore';
  import { pollingStore } from './stores/pollingStore';
  import GeneralSettings from './settings/GeneralSettings.svelte';
  import MibSettings from './settings/MibSettings.svelte';
  import PresetSettings from './settings/PresetSettings.svelte';
  import SnmpSettings from './settings/SnmpSettings.svelte';
  import NotifySettings from './settings/NotifySettings.svelte';
  import ServiceSettings from './settings/ServiceSettings.svelte';
  import BackupSettings from './settings/BackupSettings.svelte';
  import Icon from './Icon.svelte';

  const dispatch = createEventDispatcher();

  export let showDebug = false;

  /**
   * Where to open: `{ section, anchor }`, or null for the first section. Set
   * through requestSettings — the Traps tab's "Manage profiles" opens the SNMP
   * section at the credential profiles.
   */
  export let initial = null;

  // The sections, declared once instead of six times.
  //
  // They were six hand-written buttons, and the seventh was going to be a
  // seventh copy of the same eight lines. The label is looked up in the markup
  // rather than stored here, so the list carries no English at all — the same
  // reason preset.WidgetKinds serves i18n key suffixes.
  const SECTIONS = [
    { id: 'general', icon: 'sliders-horizontal' },
    { id: 'mibs', icon: 'book-marked' },
    { id: 'presets', icon: 'layers' },
    { id: 'snmp', icon: 'globe' },
    { id: 'notify', icon: 'bell' },
    { id: 'service', icon: 'server-cog' },
    { id: 'backup', icon: 'archive' },
  ];

  let activeTab = SECTIONS.some((s) => s.id === initial?.section) ? initial.section : 'general';
  let settings;
  let defaultMibPath = '';

  settingsStore.subscribe(value => {
    settings = JSON.parse(JSON.stringify(value));
  });

  onMount(async () => {
    if (initial?.anchor) {
      await tick();
      document.getElementById(initial.anchor)?.scrollIntoView({ block: 'start' });
    }
    try {
      defaultMibPath = await GetPersistentMibDirectory();
    } catch (e) {
      console.error('Failed to get default MIB path:', e);
    }
  });

  function handleSave() {
    // The identifiers as they were, for the monitoring sessions built from
    // them: a session follows its credential profile, and this dialog is where
    // a profile's credentials — and the default ones — are edited.
    const before = get(settingsStore);
    settingsStore.save(settings);
    pollingStore.followCredentials(before, settings);
    dispatch('close');
  }

  function handleCancel() {
    dispatch('close');
  }
</script>

<!-- Escape closes it, like every other dialog in the app. On the window
     because a keydown starts at whatever has focus — a field deep inside the
     settings — and reaching the backdrop would depend on nothing in between
     stopping it first. -->
<svelte:window on:keydown={(e) => e.key === 'Escape' && handleCancel()} />

<div class="modal-backdrop" on:mousedown={onBackdrop(handleCancel)} role="presentation">
  <div class="modal" role="dialog" aria-modal="true" tabindex="-1">
    <div class="modal-header">
      <h2><Icon name="settings" size={20} /> {$_('settings.title')}</h2>
      <button class="close-btn" on:click={handleCancel}>&times;</button>
    </div>

    <!-- A NAV, not a tablist.
         `role="tablist"` promises arrow-key navigation between the tabs, and
         declaring the role without implementing it is worse for a screen
         reader than not declaring it. A settings sidebar is a list of places
         to go: `aria-current` says which one you are on, and every browser and
         reader already knows what to do with it. -->
    <div class="settings-body">
      <nav class="nav" aria-label={$_('settings.title')}>
        {#each SECTIONS as section (section.id)}
          <button
            class="nav-item"
            class:active={activeTab === section.id}
            aria-current={activeTab === section.id ? 'true' : undefined}
            on:click={() => (activeTab = section.id)}
          >
            <Icon name={section.icon} size={15} />
            <span>{$_(`settings.tabs.${section.id}`)}</span>
          </button>
        {/each}
      </nav>

      <div class="modal-content">
        {#if activeTab === 'general'}
          <GeneralSettings bind:settings />
        {/if}

        {#if activeTab === 'mibs'}
          <MibSettings {defaultMibPath} />
        {/if}

        {#if activeTab === 'presets'}
          <PresetSettings />
        {/if}

        {#if activeTab === 'snmp'}
          <SnmpSettings bind:settings />
        {/if}

        {#if activeTab === 'notify'}
          <NotifySettings />
        {/if}

        {#if activeTab === 'service'}
          <ServiceSettings />
        {/if}

        {#if activeTab === 'backup'}
          <BackupSettings />
        {/if}
      </div>
    </div>

    <div class="modal-actions">
      <div class="left-actions">
        <button class="btn tertiary" on:click={settingsStore.reset}>{$_('common.reset')}</button>
        <button
          class="btn tertiary debug-toggle"
          class:active={showDebug}
          on:click={() => dispatch('toggleDebug')}
          title={$_('debug.title')}
        >
          <Icon name="bug" size={15} /> Debug
        </button>
      </div>
      <div class="main-actions">
        <button class="btn secondary" on:click={handleCancel}>{$_('common.cancel')}</button>
        <button class="btn" on:click={handleSave}>{$_('common.save')}</button>
      </div>
    </div>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    background-color: var(--backdrop-color);
    display: flex;
    justify-content: center;
    align-items: center;
    z-index: 100;
  }

  .modal {
    background-color: var(--bg-light-color);
    padding: 0;
    border-radius: 8px;
    width: 90%;
    max-width: 880px;
    box-shadow: 0 5px 15px var(--shadow-color-strong);
    display: flex;
    flex-direction: column;
    /* Bounded here rather than on the content, now that two children scroll. */
    max-height: 88vh;
    overflow: hidden;
  }

  .modal-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 15px 20px;
    border-bottom: 1px solid var(--border-color);
  }

  .modal-header h2 {
    margin: 0;
  }

  .close-btn {
    background: none;
    border: none;
    color: var(--text-color);
    font-size: 1.5rem;
    cursor: pointer;
  }

  /* The navigation column, and the row that holds it beside the content. */
  .settings-body {
    display: flex;
    /* min-height: 0 is what lets the content scroll instead of stretching the
       dialog past the viewport: a flex item's default min-height is auto,
       which means "as tall as my content" and defeats every overflow below. */
    min-height: 0;
    flex: 1;
  }

  .nav {
    display: flex;
    flex-direction: column;
    flex: 0 0 190px;
    padding: 8px;
    gap: 2px;
    background-color: var(--bg-lighter-color);
    border-right: 1px solid var(--border-color);
    overflow-y: auto;
  }

  .nav-item {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 9px 12px;
    border: none;
    border-radius: 5px;
    background: transparent;
    color: var(--text-muted);
    font-weight: 500;
    text-align: left;
    cursor: pointer;
    /* No transition. A 150 ms fade on the selection is invisible in use
       and it made the SITE CAPTURES non-deterministic: three of four
       showed the accent still on the item being left and not yet on the
       one being chosen, while the outline — which does not animate —
       was already correct. A selection should look immediate anyway. */
  }

  .nav-item:hover {
    background-color: var(--hover-overlay);
    color: var(--text-color);
  }

  .nav-item.active {
    color: var(--accent-color);
    background-color: var(--accent-subtle-medium);
  }

  .modal-content {
    flex: 1;
    min-width: 0;
    min-height: 0;
    padding: 20px;
    overflow-y: auto;
  }

  /* Narrow enough that a 190 px column is a third of the dialog. The sections
     go back to a row — scrollable rather than squeezed, which is the thing
     this change exists to stop. */
  @media (max-width: 760px) {
    .settings-body {
      flex-direction: column;
    }

    .nav {
      flex: 0 0 auto;
      flex-direction: row;
      overflow-x: auto;
      border-right: none;
      border-bottom: 1px solid var(--border-color);
    }

    .nav-item {
      flex: 0 0 auto;
    }
  }

  .modal-actions {
    padding: 15px 20px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    border-top: 1px solid var(--border-color);
    background-color: var(--bg-lighter-color);
    border-bottom-left-radius: 8px;
    border-bottom-right-radius: 8px;
  }

  .main-actions {
    display: flex;
    gap: 10px;
  }

  .left-actions {
    display: flex;
    gap: 10px;
  }

  .debug-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }

  .debug-toggle.active {
    background-color: var(--success-subtle-medium);
    border-color: var(--success-border);
    color: var(--success-color);
  }

  /* Secondary action button */
  .btn.secondary {
    background-color: var(--bg-disabled);
    color: var(--text-color);
  }
  .btn.secondary:hover {
    background-color: var(--bg-disabled-hover);
  }

  /* Tertiary/ghost action button */
  .btn.tertiary {
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-color);
  }
  .btn.tertiary:hover {
    background-color: var(--bg-lighter-color);
    border-color: var(--border-hover);
  }
</style>
