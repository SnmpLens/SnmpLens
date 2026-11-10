<script>
  // The system service: the supervision — polling, thresholds, traps and
  // notifications — run by the operating system from boot, with nobody signed
  // in, and driven from this window while it runs. Go decides everything here
  // (internal/app/systemservice.go); this says what it answered and asks.
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import Icon from '../Icon.svelte';
  import { notificationStore } from '../stores/notifications';
  import {
    SystemServiceStatus,
    SystemServiceInstall,
    SystemServiceUninstall,
    SystemServiceStart,
    SystemServiceStop,
    SystemServiceAllow,
  } from '../../wailsjs/go/app/App';

  let link = null;
  let busy = false;
  let removing = false;
  let bringBack = true;
  let purge = false;

  onMount(refresh);

  async function refresh() {
    try {
      link = await SystemServiceStatus();
    } catch (e) {
      link = null;
    }
  }

  // Each of these goes through the administrator prompt; the window reloads by
  // itself when it moves to the service or back (backend:changed).
  async function run(action, done) {
    busy = true;
    try {
      link = await action();
      if (done) notificationStore.add(get(_)(done), 'success');
      removing = false;
    } catch (e) {
      notificationStore.add(String(e), 'error');
      await refresh();
    } finally {
      busy = false;
    }
  }
</script>

{#if link && link.supported}
  <section class="system-service">
    <h4>{$_('service.system.title')}</h4>
    <p class="hint">{$_('service.system.hint')}</p>

    <p class="state">
      <span class="dot" class:on={link.installed && link.state === 'running'}></span>
      {#if !link.installed}
        {$_('service.system.notInstalled')}
      {:else if link.driving}
        {$_('service.system.driving', { values: { version: link.serviceVersion || '' } })}
      {:else if link.state === 'running'}
        {$_('service.system.runningElsewhere')}
      {:else}
        {$_('service.system.stopped')}
      {/if}
    </p>

    {#if link.installed && !link.driving && link.reason}
      <p class="note warn">
        <Icon name="triangle-alert" size={14} />
        {$_(`service.system.reason.${link.reason}`)}{link.detail ? ` — ${link.detail}` : ''}
      </p>
    {/if}
    {#if link.location && (!link.installed || link.driving)}
      <p class="note warn"><Icon name="triangle-alert" size={14} /> {$_('service.system.location')}</p>
    {/if}

    <div class="actions">
      {#if !link.installed}
        <button class="btn" disabled={busy || !!link.location}
          on:click={() => run(SystemServiceInstall, 'service.system.installed')}>
          <Icon name="shield-check" size={14} /> {$_('service.system.install')}
        </button>
      {:else}
        {#if link.reason === 'denied'}
          <button class="btn" disabled={busy} on:click={() => run(SystemServiceAllow, 'service.system.allowed')}>
            {$_('service.system.allow')}
          </button>
        {/if}
        {#if link.state === 'running'}
          <button class="btn btn-secondary" disabled={busy} on:click={() => run(SystemServiceStop, 'service.system.stoppedDone')}>
            {$_('service.system.stop')}
          </button>
        {:else}
          <button class="btn" disabled={busy} on:click={() => run(SystemServiceStart, 'service.system.started')}>
            {$_('service.system.start')}
          </button>
        {/if}
        {#if link.driving}
          <button class="btn btn-secondary" disabled={busy || !!link.location}
            on:click={() => run(SystemServiceInstall, 'service.system.updated')}>
            {$_('service.system.update')}
          </button>
        {/if}
        <button class="btn btn-secondary danger" disabled={busy} on:click={() => (removing = !removing)}>
          {$_('service.system.uninstall')}
        </button>
      {/if}
    </div>

    {#if !link.installed}
      <p class="sub">{$_('service.system.installHint')}</p>
    {/if}

    {#if removing && link.installed}
      <div class="remove">
        <label class="toggle">
          <input id="system-service-bring-back" type="checkbox" bind:checked={bringBack} disabled={busy} />
          <span>{$_('service.system.bringBack')}</span>
        </label>
        <label class="toggle">
          <input id="system-service-purge" type="checkbox" bind:checked={purge} disabled={busy} />
          <span>{$_('service.system.purge')}</span>
        </label>
        <button class="btn danger" disabled={busy}
          on:click={() => run(() => SystemServiceUninstall(bringBack, purge), 'service.system.uninstalled')}>
          {$_('service.system.uninstallConfirm')}
        </button>
      </div>
    {/if}
  </section>
{/if}

<style>
  .system-service {
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
    font-size: 0.8rem;
    color: var(--text-muted);
  }

  .state {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
    font-size: 0.85rem;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }

  .dot.on {
    background: var(--success-color, #3fb950);
  }

  .note {
    display: flex;
    align-items: flex-start;
    gap: 0.4rem;
    margin: 0;
    font-size: 0.8rem;
  }

  .note.warn {
    color: var(--warning-color, #d29922);
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .remove {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    padding: 0.6rem 0.75rem;
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .remove .btn {
    align-self: flex-start;
  }

  .toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.85rem;
  }
</style>
