<script>
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import { notificationStore } from '../stores/notifications';
  import { settingsStore } from '../stores/settingsStore';
  import { credentialState, encryptSettings } from '../utils/crypto';
  import { collectRendererState, applyRendererState, keepRestoreReport } from '../utils/backupState';
  import {
    BackupExport,
    BackupInspect,
    BackupInspectAutomatic,
    BackupRestore,
    ListAutomaticBackups,
  } from '../../wailsjs/go/app/App';

  // A backup acts at once, like the presets and the background mode: nothing
  // here waits for the dialog's Save. Restoring reloads the window, so that no
  // copy of the old settings held by an open dialog can be saved over it.

  const MIN = 10; // pkg/backup.MinPassphrase
  const KEEP = 5; // internal/app automaticKept
  const ITEMS = ['sinks', 'routes', 'sessions', 'devices', 'models', 'presets', 'assets', 'mibs'];

  let includeSecrets = true;
  let includeMibs = true;
  let passphrase = '';
  let confirm = '';
  let exporting = false;

  let summary = null;
  let restorePassphrase = '';
  let restoring = false;
  let automatic = [];

  $: shortPass = includeSecrets && passphrase.length < MIN;
  $: mismatch = includeSecrets && passphrase !== confirm;
  $: canExport = !exporting && !(includeSecrets && (shortPass || mismatch));

  onMount(refreshAutomatic);

  async function refreshAutomatic() {
    try {
      automatic = await ListAutomaticBackups();
    } catch {
      automatic = [];
    }
  }

  async function doExport() {
    exporting = true;
    try {
      const { state, secrets } = collectRendererState(get(settingsStore), localStorage);
      const res = await BackupExport({
        renderer: state,
        rendererSecrets: includeSecrets ? secrets : {},
        includeSecrets,
        passphrase: includeSecrets ? passphrase : '',
        includeMibs,
      });
      if (res && res.path) {
        notificationStore.add(get(_)('backup.exported', { values: { path: res.path } }), 'success');
        passphrase = '';
        confirm = '';
      }
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      exporting = false;
    }
  }

  async function choose() {
    try {
      const s = await BackupInspect();
      if (s && s.token) {
        summary = s;
        restorePassphrase = '';
      }
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function chooseAutomatic(name) {
    try {
      summary = await BackupInspectAutomatic(name);
      restorePassphrase = '';
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function restore() {
    restoring = true;
    try {
      const current = collectRendererState(get(settingsStore), localStorage);
      const res = await BackupRestore({
        token: summary.token,
        passphrase: restorePassphrase,
        currentRenderer: current.state,
        currentRendererSecrets: current.secrets,
      });
      const problems = [...(res.problems || [])];
      let renderer = [];
      if (res.renderer) {
        renderer = await applyRendererState(res.renderer, res.rendererSecrets, localStorage,
          encryptSettings, get(credentialState) === 'ok');
      }
      keepRestoreReport({ problems, renderer, secrets: res.secretsRestored }, sessionStorage);
      // Every store reads what was written at its next load, and this one is it.
      location.reload();
    } catch (e) {
      notificationStore.add(String(e), 'error');
      restoring = false;
    }
  }

  function when(iso) {
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
  }
</script>

<div class="backup-settings">
  <section>
    <h4><Icon name="download" size={15} /> {$_('backup.exportTitle')}</h4>
    <p class="hint">{$_('backup.exportHint')}</p>

    <label class="toggle">
      <input type="checkbox" bind:checked={includeSecrets} />
      <span>{$_('backup.includeSecrets')}</span>
    </label>
    {#if includeSecrets}
      <p class="sub">{$_('backup.secretsHint', { values: { min: MIN } })}</p>
      <div class="pass">
        <label class="fld">
          <span>{$_('backup.passphrase')}</span>
          <input type="password" autocomplete="new-password" bind:value={passphrase} />
        </label>
        <label class="fld">
          <span>{$_('backup.passphraseConfirm')}</span>
          <input type="password" autocomplete="new-password" bind:value={confirm} />
        </label>
      </div>
      {#if passphrase && shortPass}
        <p class="note warn">{$_('backup.passphraseShort', { values: { min: MIN } })}</p>
      {:else if confirm && mismatch}
        <p class="note warn">{$_('backup.passphraseMismatch')}</p>
      {/if}
    {/if}

    <label class="toggle">
      <input type="checkbox" bind:checked={includeMibs} />
      <span>{$_('backup.includeMibs')}</span>
    </label>

    <div class="actions">
      <button class="btn" disabled={!canExport} on:click={doExport}>
        <Icon name="archive" size={14} /> {$_('backup.export')}
      </button>
    </div>
  </section>

  <section>
    <h4><Icon name="upload" size={15} /> {$_('backup.restoreTitle')}</h4>
    <p class="hint">{$_('backup.restoreHint')}</p>

    {#if !summary}
      <div class="actions">
        <button class="btn secondary" on:click={choose}>{$_('backup.choose')}</button>
      </div>
    {:else}
      <div class="summary">
        <p class="from">
          <strong>{summary.fileName}</strong><br />
          {$_('backup.from', { values: { date: when(summary.createdAt), version: summary.appVersion || '?' } })}
        </p>
        <ul class="contents">
          {#if summary.contents.settings}<li>{$_('backup.c.settings')}</li>{/if}
          {#each ITEMS as item (item)}
            {#if summary.contents[item] > 0}
              <li>{$_(`backup.c.${item}`, { values: { count: summary.contents[item] } })}</li>
            {/if}
          {/each}
          {#if summary.contents.service}<li>{$_('backup.c.service')}</li>{/if}
          {#if summary.contents.trapEngine}<li>{$_('backup.c.trapEngine')}</li>{/if}
        </ul>

        {#if !summary.secrets}
          <p class="note">{$_('backup.noSecrets')}</p>
        {:else if summary.automatic}
          <p class="note"><Icon name="shield-check" size={14} /> {$_('backup.automaticSecrets')}</p>
        {:else}
          <p class="note"><Icon name="key-round" size={14} /> {$_('backup.hasSecrets')}</p>
          <label class="fld">
            <span>{$_('backup.passphraseOptional')}</span>
            <input type="password" autocomplete="current-password" bind:value={restorePassphrase} />
          </label>
        {/if}

        <p class="note warn"><Icon name="triangle-alert" size={14} /> {$_('backup.restoreWarning')}</p>
        <div class="actions">
          <button class="btn secondary" disabled={restoring} on:click={() => (summary = null)}>{$_('common.cancel')}</button>
          <button class="btn" disabled={restoring} on:click={restore}>
            {restoring ? $_('backup.restoring') : $_('backup.restore')}
          </button>
        </div>
      </div>
    {/if}
  </section>

  <section>
    <h4><Icon name="history" size={15} /> {$_('backup.automaticTitle')}</h4>
    <p class="hint">{$_('backup.automaticHint', { values: { count: KEEP } })}</p>
    {#if automatic.length === 0}
      <p class="note">{$_('backup.automaticEmpty')}</p>
    {:else}
      <ul class="points">
        {#each automatic as point (point.name)}
          <li>
            <span>{when(point.createdAt)}</span>
            <span class="size">{Math.max(1, Math.round(point.size / 1024))} KB</span>
            <button class="btn tertiary" disabled={restoring} on:click={() => chooseAutomatic(point.name)}>
              {$_('backup.undo')}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .backup-settings {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }

  section {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  h4 {
    margin: 0;
    font-size: 0.9rem;
    color: var(--text-color);
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  .hint,
  .sub {
    margin: 0;
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.82rem;
    color: var(--text-color);
    cursor: pointer;
  }

  .pass {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
  }

  .fld {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.82rem;
    color: var(--text-color);
    min-width: 200px;
  }

  .fld input {
    padding: 0.35rem 0.5rem;
    background: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.82rem;
  }

  .note {
    margin: 0;
    font-size: 0.75rem;
    color: var(--text-muted);
    display: flex;
    align-items: center;
    gap: 0.35rem;
  }

  .note.warn {
    color: var(--warning-color);
  }

  .actions {
    display: flex;
    gap: 0.5rem;
  }

  .actions .btn {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }

  .summary {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.75rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    background: var(--bg-lighter-color);
  }

  .from {
    margin: 0;
    font-size: 0.8rem;
    color: var(--text-color);
    overflow-wrap: anywhere;
  }

  .contents,
  .points {
    margin: 0;
    padding-left: 1.1rem;
    font-size: 0.8rem;
    color: var(--text-color);
  }

  .points {
    list-style: none;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }

  .points li {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .size {
    color: var(--text-muted);
    font-size: 0.75rem;
  }
</style>
