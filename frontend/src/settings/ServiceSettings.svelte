<script>
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import { notificationStore } from '../stores/notifications';
  import { ServiceGetStatus, ServiceSetConfig, AutostartGet, AutostartSet } from '../bridge/App';

  // These preferences do NOT live in the settings store: they are read by Go
  // before a window exists, so they have their own file. Keeping them in
  // localStorage would make them unreadable at the moment they are needed.
  let status = null;
  let autostart = null;
  let saving = false;

  onMount(async () => {
    try {
      status = await ServiceGetStatus();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
    await refreshAutostart();
  });

  // Always ask the OS. The entry can be removed through Task Manager or System
  // Settings without us being told, and a remembered "yes" would be a lie this
  // screen keeps repeating.
  async function refreshAutostart() {
    try {
      autostart = await AutostartGet();
    } catch (e) {
      autostart = null;
    }
  }

  async function toggleAutostart(enabled) {
    saving = true;
    try {
      autostart = await AutostartSet(enabled);
      notificationStore.add(
        get(_)(enabled ? 'service.loginRegistered' : 'service.loginRemoved'),
        'success',
      );
    } catch (e) {
      notificationStore.add(String(e), 'error');
      await refreshAutostart();
    } finally {
      saving = false;
    }
  }

  async function apply(changes) {
    if (!status) return;
    saving = true;
    const next = { ...status.config, ...changes };
    try {
      status = await ServiceSetConfig(next);
      notificationStore.add(get(_)('service.saved'), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      saving = false;
    }
  }

  // Background mode without a tray icon would leave an app that cannot be
  // reached or quit, so Go refuses to hide the window in that case. Say so
  // instead of letting the checkbox look like it did nothing.
  $: trayMissing = status && status.config.runInBackground && !status.trayAvailable;
</script>

<div class="service-settings">
  {#if !status}
    <p class="empty-state">{$_('common.loading')}</p>
  {:else}
    <section>
      <h4>{$_('service.backgroundTitle')}</h4>
      <p class="hint">{$_('service.backgroundHint')}</p>

      <label class="toggle">
        <input type="checkbox" checked={status.config.runInBackground} disabled={saving}
          on:change={(e) => apply({ runInBackground: e.currentTarget.checked })} />
        <span>{$_('service.runInBackground')}</span>
      </label>

      {#if trayMissing}
        <p class="note warn">
          <Icon name="triangle-alert" size={14} />
          {$_('service.noTray', { values: { platform: status.platform } })}
        </p>
      {/if}

      <label class="toggle" class:disabled={!status.config.runInBackground}>
        <input type="checkbox" checked={status.config.startHidden}
          disabled={saving || !status.config.runInBackground}
          on:change={(e) => apply({ startHidden: e.currentTarget.checked })} />
        <span>{$_('service.startHidden')}</span>
      </label>
      <p class="sub">{$_('service.startHiddenHint')}</p>
    </section>

    <section>
      <h4>{$_('service.loginTitle')}</h4>
      <p class="hint">{$_('service.loginHint')}</p>

      {#if autostart && autostart.supported}
        <label class="toggle">
          <input type="checkbox" checked={autostart.enabled} disabled={saving}
            on:change={(e) => toggleAutostart(e.currentTarget.checked)} />
          <span>{$_('service.startAtLogin')}</span>
        </label>
        <p class="sub">{$_('service.loginLocation', { values: { location: autostart.location } })}</p>
        {#if autostart.error}
          <p class="note warn">
            <Icon name="triangle-alert" size={14} /> {autostart.error}
          </p>
        {/if}
        {#if autostart.enabled && !status.config.startHidden}
          <p class="note">{$_('service.loginWindowNote')}</p>
        {/if}
      {:else}
        <p class="note">{$_('service.loginUnsupported')}</p>
      {/if}
    </section>

    <section>
      <h4>{$_('service.unattendedTitle')}</h4>
      <p class="hint">{$_('service.unattendedHint')}</p>

      <label class="toggle">
        <input type="checkbox" checked={status.config.autoResumeMonitors} disabled={saving}
          on:change={(e) => apply({ autoResumeMonitors: e.currentTarget.checked })} />
        <span>{$_('service.autoResume')}</span>
      </label>

      <label class="toggle">
        <input type="checkbox" checked={status.config.autoStartTrapListener} disabled={saving}
          on:change={(e) => apply({ autoStartTrapListener: e.currentTarget.checked })} />
        <span>{$_('service.autoStartTraps')}</span>
      </label>

      <label class="fld" class:disabled={!status.config.autoStartTrapListener}>
        <span>{$_('service.trapPort')}</span>
        <input type="number" min="1" max="65535" value={status.config.trapPort}
          disabled={saving || !status.config.autoStartTrapListener}
          on:change={(e) => apply({ trapPort: Number(e.currentTarget.value) })} />
        <span class="sub">{$_('service.trapPortHint')}</span>
      </label>
    </section>

    <section>
      <h4>{$_('service.auditTitle')}</h4>
      <p class="hint">{$_('service.auditHint')}</p>
      <label class="toggle">
        <input type="checkbox" checked={status.config.auditFailedSets} disabled={saving}
          on:change={(e) => apply({ auditFailedSets: e.currentTarget.checked })} />
        <span>{$_('service.auditFailedSets')}</span>
      </label>
    </section>

    <section>
      <h4>{$_('service.stateTitle')}</h4>
      <ul class="state">
        <li>
          <span class="dot" class:on={status.trayAvailable}></span>
          {$_('service.stateTray')}
          <strong>{status.trayAvailable ? $_('service.active') : $_('service.inactive')}</strong>
        </li>
        <li>
          <span class="dot" class:on={status.trapListenerRunning}></span>
          {$_('service.stateTraps')}
          <strong>{status.trapListenerRunning ? $_('service.active') : $_('service.inactive')}</strong>
        </li>
      </ul>
      <p class="note">{$_('service.restartNote')}</p>
    </section>
  {/if}
</div>

<style>
  .service-settings {
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

  .toggle.disabled,
  .fld.disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .fld {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    font-size: 0.82rem;
    color: var(--text-color);
    max-width: 220px;
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

  .state {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    font-size: 0.8rem;
    color: var(--text-color);
  }

  .state li {
    display: flex;
    align-items: center;
    gap: 0.45rem;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }

  .dot.on {
    background: var(--success-color);
  }

  .empty-state {
    font-size: 0.8rem;
    color: var(--text-muted);
  }
</style>
