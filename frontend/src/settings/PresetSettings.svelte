<script>
  import { onMount } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import { notificationStore } from '../stores/notifications';
  import { formatCadence } from '../utils/formatting';
  import {
    ListPresets,
    ReadPreset,
    ImportPresetDialog,
    DeletePreset,
    ListMapAssets,
    ImportMapAssetDialog,
    DeleteMapAsset,
  } from '../bridge/App';

  // The preset library.
  //
  // A preset is a file somebody else wrote, and this screen exists so that
  // choosing one is an informed act: what it polls, how often, what it draws,
  // and what that costs — before it is bound to anything. Nothing here starts
  // a poll.

  let presets = [];
  let openFile = '';
  let detail = null;
  let loading = false;

  onMount(refresh);
  onMount(refreshBackgrounds);

  async function refresh() {
    try {
      presets = await ListPresets();
    } catch (e) {
      console.error('ListPresets failed', e);
      presets = [];
    }
  }

  async function open(file) {
    if (openFile === file) {
      openFile = '';
      detail = null;
      return;
    }
    loading = true;
    openFile = file;
    detail = null;
    try {
      detail = await ReadPreset(file);
    } catch (e) {
      console.error('ReadPreset failed', e);
      notificationStore.add(String(e), 'error');
      openFile = '';
    } finally {
      loading = false;
    }
  }

  async function handleImport() {
    try {
      const results = await ImportPresetDialog();
      reportImport(results || []);
      await refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // One line per file, because an import is a bulk action nobody reads the
  // result of unless something went wrong — which is exactly when it has to be
  // loud about which file and why.
  function reportImport(results) {
    for (const r of results) {
      if (!r.success) {
        notificationStore.add(`${r.fileName}: ${r.error}`, 'error');
      } else if (r.problems > 0) {
        notificationStore.add(
          $_('preset.importedWithProblems', { values: { file: r.fileName, count: r.problems } }),
          'warning',
        );
      } else if (!r.skipped) {
        notificationStore.add($_('preset.imported', { values: { file: r.fileName } }), 'success');
      }
    }
  }

  async function handleDelete(file) {
    try {
      await DeletePreset(file);
      if (openFile === file) {
        openFile = '';
        detail = null;
      }
      await refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // The backgrounds a map widget draws over.
  //
  // Beside the presets rather than in a section of their own, because a
  // background exists only for a preset: a map names one, and this is the
  // screen where a preset is read.
  let backgrounds = [];

  async function refreshBackgrounds() {
    try {
      backgrounds = await ListMapAssets();
    } catch (e) {
      console.error('ListMapAssets failed', e);
      backgrounds = [];
    }
  }

  async function handleImportBackground() {
    try {
      const results = await ImportMapAssetDialog();
      for (const r of results || []) {
        if (r.success) {
          notificationStore.add($_('preset.imported', { values: { file: r.fileName } }), 'success');
        } else {
          // Loud about which file and why: the gate is a DECODE, so "it is a
          // PNG" is not what decided it, and the reason is the only useful
          // part of the answer.
          notificationStore.add(`${r.fileName}: ${r.error}`, 'error');
        }
      }
      await refreshBackgrounds();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function handleDeleteBackground(name) {
    try {
      await DeleteMapAsset(name);
      await refreshBackgrounds();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // Every one of these takes what it reads as an argument. reactive.test.mjs
  // exists for the other shape: Svelte 5 tracks what the EXPRESSION reads, and
  // a read inside a callee is not one.
  const widgetLabel = (kind, t) => t(`preset.widget.${kind}`);

  const problemText = (count, t) =>
    count === 0 ? t('preset.noProblems') : t('preset.problems', { values: { count } });

  const oidsOf = (widget) => (widget.oids || []).join(', ');
</script>

<div class="settings-section">
  <div class="section-head">
    <div>
      <h3>{$_('preset.title')}</h3>
      <p class="hint">{$_('preset.intro')}</p>
    </div>
    <button class="btn primary" on:click={handleImport}>
      <Icon name="file-plus" size={14} /> {$_('preset.add')}
    </button>
  </div>

  {#if presets.length === 0}
    <div class="empty">
      <p>{$_('preset.empty')}</p>
      <p class="hint">{$_('preset.emptyHint')}</p>
    </div>
  {:else}
    <ul class="preset-list">
      {#each presets as p (p.file)}
        <li class="preset-row" class:open={openFile === p.file}>
          <button class="row-head" on:click={() => open(p.file)} aria-expanded={openFile === p.file}>
            <Icon name={openFile === p.file ? 'chevron-down' : 'chevron-right'} size={13} />
            <span class="name">{p.name || p.file}</span>
            {#if p.vendor}<span class="badge">{p.vendor}</span>{/if}
            <span class="meta">
              {$_('preset.summaryLine', {
                values: { widgets: p.widgets, oids: p.oids, cadence: formatCadence(p.intervalSec * 1000) },
              })}
            </span>
            {#if p.problems > 0}
              <span class="badge warn" title={$_('preset.problemsHint')}>
                <Icon name="triangle-alert" size={11} /> {p.problems}
              </span>
            {/if}
          </button>
          <button
            class="btn btn-small btn-danger"
            title={$_('preset.remove')}
            on:click|stopPropagation={() => handleDelete(p.file)}
          >
            <Icon name="trash-2" size={13} />
          </button>
        </li>

        {#if openFile === p.file}
          <li class="detail">
            {#if loading}
              <p class="hint">{$_('common.loading')}</p>
            {:else if detail}
              {#if detail.preset.description}
                <p class="description">{detail.preset.description}</p>
              {/if}

              <!-- The cost, stated before anything is bound. Everything here is
                   arithmetic over the file; nothing is a prediction of how long
                   a round will take, which is measured and belongs to the
                   scheduler's guardrail. -->
              <div class="cost">
                <div class="cost-grid">
                  <div><span class="k">{$_('preset.costOids')}</span><span class="v">{detail.cost.oids}</span></div>
                  <div><span class="k">{$_('preset.costCadence')}</span><span class="v">{formatCadence(detail.cost.intervalSec * 1000)}</span></div>
                  <div><span class="k">{$_('preset.costRequests')}</span><span class="v">{detail.cost.requestsPerDay}</span></div>
                  <div><span class="k">{$_('preset.costVarbinds')}</span><span class="v">{detail.cost.varbindsPerDay}</span></div>
                  {#if detail.cost.watched > 0}
                    <div><span class="k">{$_('preset.costWatched')}</span><span class="v">{detail.cost.watched}</span></div>
                  {/if}
                </div>
                {#if detail.cost.alerting > 0}
                  <!-- The one thing on this screen that is not about this
                       machine: a band that alerts sends mail, or POSTs to a
                       webhook, when it is breached. Said BEFORE binding, beside
                       the request count, because the gate is the same one — the
                       operator sees what the file will do before it does it. -->
                  <p class="hint at-most">
                    {$_('preset.costAlerts', { values: { alerting: detail.cost.alerting } })}
                  </p>
                {/if}
                {#if detail.cost.discovered > 0}
                  <!-- A discovering preset does not know how many instances the
                       equipment has, so every figure above is a CEILING: the
                       widget bounds, not a count of what will be polled. Saying
                       so here is the difference between a number that is wrong
                       and a number that is honest about what it is. -->
                  <p class="hint at-most">
                    {$_('preset.costAtMost', { values: { discovered: detail.cost.discovered } })}
                  </p>
                {/if}
                <p class="hint">
                  {$_('preset.costPerTarget', { values: { chunk: detail.cost.varbindsPerRequest } })}
                </p>
                <p class="hint">{$_('preset.costNotAPrediction')}</p>
              </div>

              {#if detail.errors.length > 0}
                <div class="problems">
                  <strong>{problemText(detail.errors.length, $_)}</strong>
                  <ul>
                    {#each detail.errors as e (e.field + e.message)}
                      <li>
                        <code>{e.field || '—'}</code>
                        <span>{$_(e.message, { values: e.args || {} })}</span>
                      </li>
                    {/each}
                  </ul>
                </div>
              {/if}

              <table class="widgets">
                <thead>
                  <tr>
                    <th scope="col">{$_('preset.widgetTitle')}</th>
                    <th scope="col">{$_('preset.widgetKind')}</th>
                    <th scope="col">{$_('preset.widgetOids')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each detail.preset.widgets || [] as w, i (i)}
                    <tr>
                      <td>{w.title || '—'}</td>
                      <td>{widgetLabel(w.kind, $_)}</td>
                      <td class="oids">{oidsOf(w)}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            {/if}
          </li>
        {/if}
      {/each}
    </ul>
  {/if}

  <div class="section-head backgrounds-head">
    <div>
      <h3>{$_('preset.backgrounds')}</h3>
      <p class="hint">{$_('preset.backgroundsIntro')}</p>
    </div>
    <button class="btn" on:click={handleImportBackground}>
      <Icon name="image" size={14} /> {$_('preset.addBackground')}
    </button>
  </div>

  {#if backgrounds.length === 0}
    <p class="hint">{$_('preset.noBackgrounds')}</p>
  {:else}
    <ul class="asset-list">
      {#each backgrounds as b (b.name)}
        <li class="asset-row">
          <span class="name">{b.name}</span>
          {#if b.error}
            <!-- Listed WITH its problem rather than hidden: the operator put
                 the file there, and needs to know why the map is blank. -->
            <span class="badge warn" title={b.error}>
              <Icon name="triangle-alert" size={11} /> {b.error}
            </span>
          {:else}
            <span class="meta">
              {$_('preset.backgroundMeta', {
                values: { width: b.width, height: b.height, kb: Math.max(1, Math.round(b.bytes / 1024)) },
              })}
            </span>
          {/if}
          <button
            class="btn btn-small btn-danger"
            title={$_('common.delete')}
            on:click={() => handleDeleteBackground(b.name)}
          >
            <Icon name="trash-2" size={12} />
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .backgrounds-head {
    margin-top: 1.2rem;
    padding-top: 1rem;
    border-top: 1px solid var(--border-color);
  }

  .asset-list {
    margin: 0.4rem 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }

  .asset-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.35rem 0.5rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-color);
    font-size: 0.82rem;
  }

  .asset-row .name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .section-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 0.75rem;
  }

  h3 {
    margin: 0 0 0.2rem;
    font-size: 0.95rem;
  }

  .hint {
    margin: 0.2rem 0 0;
    color: var(--text-muted);
    font-size: 0.78rem;
  }

  /* The ceiling notice is the one hint that changes how the figures above it
     are read, so it is not the same grey as the two that merely explain them. */
  .at-most {
    color: var(--text-color);
  }

  .empty {
    padding: 1.5rem;
    text-align: center;
    border: 1px dashed var(--border-color);
    border-radius: 6px;
  }

  .preset-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .preset-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding-right: 0.4rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-light-color);
  }

  .preset-row.open {
    border-bottom-left-radius: 0;
    border-bottom-right-radius: 0;
  }

  .row-head {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
    padding: 0.45rem 0.6rem;
    background: none;
    border: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  .name {
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .meta {
    margin-left: auto;
    color: var(--text-muted);
    font-size: 0.75rem;
    white-space: nowrap;
  }

  .badge {
    padding: 0.05rem 0.35rem;
    border-radius: 3px;
    background-color: var(--bg-lighter-color);
    color: var(--text-muted);
    font-size: 0.7rem;
  }

  .badge.warn {
    display: inline-flex;
    align-items: center;
    gap: 0.2rem;
    background-color: var(--warning-subtle);
    color: var(--warning-color);
  }

  .detail {
    padding: 0.7rem 0.8rem;
    border: 1px solid var(--border-color);
    border-top: none;
    border-radius: 0 0 4px 4px;
    background-color: var(--bg-color);
  }

  .description {
    margin: 0 0 0.6rem;
    font-size: 0.82rem;
  }

  .cost {
    margin-bottom: 0.7rem;
  }

  .cost-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
    gap: 0.4rem;
  }

  .cost-grid > div {
    display: flex;
    flex-direction: column;
    padding: 0.4rem 0.5rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .k {
    color: var(--text-muted);
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }

  .v {
    font-size: 1.05rem;
    font-variant-numeric: tabular-nums;
  }

  .problems {
    margin-bottom: 0.7rem;
    padding: 0.5rem 0.6rem;
    border-radius: 4px;
    background-color: var(--warning-subtle);
    color: var(--warning-color);
    font-size: 0.78rem;
  }

  .problems ul {
    margin: 0.3rem 0 0;
    padding-left: 1rem;
  }

  .problems code {
    margin-right: 0.4rem;
  }

  .widgets {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.78rem;
  }

  .widgets th,
  .widgets td {
    padding: 0.25rem 0.4rem;
    border-bottom: 1px solid var(--border-color);
    text-align: left;
  }

  .widgets th {
    color: var(--text-muted);
    font-weight: 500;
  }

  .oids {
    font-family: var(--font-mono, monospace);
    word-break: break-all;
  }
</style>
