<script>
  // The bench's cables: one per line, port to port, and a row to plug another
  // in. Go keeps them, checks each against the ports of the devices at its
  // ends, and recables a running device without restarting it; this lists them
  // and asks.
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import { simulatorStore } from '../stores/simulatorStore';
  import { notificationStore } from '../stores/notifications';
  import { cableRows, freePorts, cableReady, portOrder } from '../utils/simulator';

  export let devices = [];
  export let links = [];

  let draft = { a: { device: '', port: '' }, b: { device: '', port: '' } };
  // The ports of each device, read once it is chosen, under what decides them:
  // a device edited to another model or another number of ports is read again.
  let ports = {};
  let busy = false;

  function portsKey(id, list) {
    const d = (list || []).find((x) => x.id === id);
    return d ? `${id}|${d.model}|${JSON.stringify(d.params || {})}` : '';
  }

  async function loadPorts(id, list) {
    const key = portsKey(id, list);
    if (!key || ports[key]) return;
    try {
      const read = await simulatorStore.ports(id);
      ports = { ...ports, [key]: [...read].sort((x, y) => portOrder(x.name, y.name)) };
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function chooseDevice(end, id) {
    draft = { ...draft, [end]: { device: id, port: '' } };
    loadPorts(id, devices);
  }

  async function connect() {
    busy = true;
    try {
      await simulatorStore.connect(draft);
      const name = (id) => devices.find((d) => d.id === id)?.name || id;
      notificationStore.add($_('simulator.cabling.connected', { values: {
        a: name(draft.a.device), aPort: draft.a.port, b: name(draft.b.device), bPort: draft.b.port,
      } }), 'success');
      draft = { a: { device: draft.a.device, port: '' }, b: { device: '', port: '' } };
      await simulatorStore.refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      busy = false;
    }
  }

  async function disconnect(id) {
    busy = true;
    try {
      await simulatorStore.disconnect(id);
      await simulatorStore.refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      busy = false;
    }
  }
</script>

<section class="cabling" aria-labelledby="sim-cabling-title">
  <h4 class="category-title" id="sim-cabling-title"><Icon name="plug" size={14} /> {$_('simulator.cabling.title')}</h4>
  <p class="hint">{$_('simulator.cabling.hint')}</p>

  {#if cableRows(links, devices).length > 0}
    <ul class="cables">
      {#each cableRows(links, devices) as row (row.id)}
        <li class="cable">
          <span class="end"><span class="name">{row.a.name}</span><code>{row.a.port}</code></span>
          <span class="wire" aria-hidden="true"></span>
          <span class="end"><span class="name">{row.b.name}</span><code>{row.b.port}</code></span>
          <button class="icon-btn danger" disabled={busy} title={$_('simulator.cabling.disconnect')}
            aria-label={$_('simulator.cabling.disconnect')} on:click={() => disconnect(row.id)}>
            <Icon name="x" size={14} />
          </button>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="none">{$_('simulator.cabling.none')}</p>
  {/if}

  <div class="new-cable">
    {#each ['a', 'b'] as end (end)}
      <div class="end-picker">
        <select id="sim-cable-{end}-device" aria-label={$_(`simulator.cabling.${end}Device`)} disabled={busy}
          value={draft[end].device} on:change={(e) => chooseDevice(end, e.currentTarget.value)}>
          <option value="">{$_('simulator.cabling.chooseDevice')}</option>
          {#each devices as d (d.id)}
            <option value={d.id} disabled={d.id === draft[end === 'a' ? 'b' : 'a'].device}>{d.name}</option>
          {/each}
        </select>
        <select id="sim-cable-{end}-port" aria-label={$_(`simulator.cabling.${end}Port`)}
          disabled={busy || !ports[portsKey(draft[end].device, devices)]}
          bind:value={draft[end].port}>
          <option value="">{$_('simulator.cabling.choosePort')}</option>
          {#each freePorts(ports[portsKey(draft[end].device, devices)], links, draft[end].device) as p (p.name)}
            <option value={p.name}>{p.name}{p.descr && p.descr !== p.name ? ` — ${p.descr}` : ''}</option>
          {/each}
        </select>
      </div>
      {#if end === 'a'}<span class="wire" aria-hidden="true"></span>{/if}
    {/each}
    <button class="btn btn-small" disabled={busy || !cableReady(draft)} on:click={connect}>
      <Icon name="plug" size={13} /> {$_('simulator.cabling.connect')}
    </button>
  </div>
</section>

<style>
  .cabling {
    margin-top: 18px;
  }

  .category-title {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0 0 6px;
    font-size: 0.85em;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }

  .hint,
  .none {
    margin: 0 0 10px;
    color: var(--text-muted);
    font-size: 0.85em;
    line-height: 1.45;
  }

  .cables {
    list-style: none;
    margin: 0 0 10px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .cable,
  .new-cable {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .new-cable {
    flex-wrap: wrap;
    border-style: dashed;
  }

  .end {
    display: inline-flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
    flex: 1;
  }

  .name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  code {
    font-size: 0.85em;
    color: var(--text-muted);
  }

  /* The cable between the two ends. */
  .wire {
    flex: 0 0 28px;
    height: 2px;
    border-radius: 1px;
    background-color: var(--accent-color);
  }

  .end-picker {
    display: flex;
    gap: 6px;
    flex: 1;
    min-width: 220px;
  }

  select {
    flex: 1;
    min-width: 0;
    height: 30px;
    padding: 0 8px;
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: inherit;
    font-size: 0.85em;
  }

  .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
  }

  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 28px;
    height: 28px;
    padding: 0 6px;
    background: none;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
  }

  .icon-btn:hover:not(:disabled) {
    color: var(--error-color);
    border-color: var(--error-color);
  }
</style>
