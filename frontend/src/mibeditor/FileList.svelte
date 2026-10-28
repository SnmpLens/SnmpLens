<script>
  import { createEventDispatcher } from 'svelte';
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import { countVerdicts, isProblem, ICONS as VERDICT_ICONS, FAILED, ERROR, WARNING } from '../utils/mibVerdict';
  import { select as selectFiles } from './fileMenu.js';

  // The MIB editor's file list: a mark beside every file that does not load,
  // loads with errors or with warnings — the one verdict the MIB settings and
  // the import report show too, decided in Go (pkg/mib/verdict.go) — and a
  // switch to list only those: in a directory of two hundred MIBs the broken
  // ones were found by opening them one by one.
  //
  // It selects and asks; the panel opens, and the file actions
  // (mibeditor/FileActions.svelte) act on what is selected.

  /** MibEditorList's answer. */
  export let files = [];
  /** verdictMap's answer. */
  export let statuses = {};
  /** The rail's filter, by name. */
  export let filter = '';
  /** The directory file open in the editor. */
  export let activeName = '';
  export let onlyProblems = false;
  /** The files selected, and the one a Shift-click ranges from. */
  export let selected = [];
  export let anchor = '';

  const dispatch = createEventDispatcher();

  $: problemCounts = countVerdicts(statuses);
  $: shown = files.filter((f) => (!filter || f.name.toLowerCase().includes(filter.toLowerCase()))
    && (!onlyProblems || isProblem(statuses[f.name]?.level)));

  // A plain click opens the file and selects it alone; Ctrl (Cmd) and Shift
  // select without opening, as in a file manager.
  function clickFile(e, name) {
    const toggle = e.ctrlKey || e.metaKey;
    const range = e.shiftKey;
    ({ selected, anchor } = selectFiles(selected, anchor, name, shown.map((f) => f.name), { toggle, range }));
    if (!toggle && !range) dispatch('open', name);
  }

  // A right-click on a file outside the selection selects that file alone,
  // without opening it; inside, the menu acts on the whole selection.
  function namesFor(name) {
    if (selected.includes(name)) return selected;
    selected = [name];
    anchor = name;
    return selected;
  }

  function fileContextMenu(e, name) {
    dispatch('menu', { x: e.clientX, y: e.clientY, names: namesFor(name) });
  }

  function listContextMenu(e) {
    if (e.target.closest('.file')) return;
    e.preventDefault();
    dispatch('listmenu', { x: e.clientX, y: e.clientY });
  }

  function fileKeydown(e, name) {
    if (e.key === 'F2') {
      e.preventDefault();
      dispatch('rename', name);
    } else if (e.key === 'Delete') {
      e.preventDefault();
      dispatch('delete', namesFor(name));
    } else if (e.key === 'ContextMenu' || (e.key === 'F10' && e.shiftKey)) {
      e.preventDefault();
      const r = e.currentTarget.getBoundingClientRect();
      dispatch('menu', { x: r.left + 16, y: r.bottom, names: namesFor(name) });
    }
  }
</script>

{#if problemCounts[FAILED] + problemCounts[ERROR] + problemCounts[WARNING] > 0 || onlyProblems}
  <button class="only-problems" aria-pressed={onlyProblems} on:click={() => (onlyProblems = !onlyProblems)}
    title={onlyProblems ? $_('mibEditor.files.showAll') : $_('mibEditor.files.onlyProblems')}>
    <Icon name="list-filter" size={13} />
    <span class="label">{onlyProblems ? $_('mibEditor.files.showAll') : $_('mibEditor.files.onlyProblems')}</span>
    {#each [FAILED, ERROR, WARNING] as level}
      {#if problemCounts[level] > 0}
        <span class="count {level}" title={$_('mibVerdict.short.' + level)}>
          <Icon name={VERDICT_ICONS[level]} size={12} /> {problemCounts[level]}
        </span>
      {/if}
    {/each}
  </button>
{/if}

<!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
<ul class="files" on:contextmenu={listContextMenu}>
  {#each shown as f (f.name)}
    <li>
      <button class="file" class:active={activeName === f.name}
        class:selected={selected.length > 1 && selected.includes(f.name)}
        on:click={(e) => clickFile(e, f.name)}
        on:contextmenu|preventDefault={(e) => fileContextMenu(e, f.name)}
        on:keydown={(e) => fileKeydown(e, f.name)}
        title={isProblem(statuses[f.name]?.level)
          ? $_('mibVerdict.' + statuses[f.name].level, { values: { count: statuses[f.name].count } })
            + (statuses[f.name].live ? ' (' + $_('mibVerdict.live') + ')' : '')
            + (statuses[f.name].detail ? ' — ' + statuses[f.name].detail : '')
          : f.name}>
        <!-- One element whatever the verdict, its icon chosen by name:
             swapping {#if} branches is a destroy and a create, and showed
             the previous icon for a frame on every hover. -->
        <span class="file-mark {statuses[f.name]?.level || ''}" aria-hidden="true">
          {#if isProblem(statuses[f.name]?.level)}<Icon name={VERDICT_ICONS[statuses[f.name].level]} size={12} />{/if}
        </span>
        <span class="fname">{f.name}</span>
        {#if f.bundled}
          <span class="tag" class:changed={f.modified}
            title={f.modified ? $_('mibEditor.bundledModified') : $_('mibEditor.bundled')}>
            {f.modified ? '±' : '◆'}
          </span>
        {/if}
      </button>
    </li>
  {/each}
  {#if shown.length === 0}
    <li class="empty">{onlyProblems ? $_('mibEditor.files.noProblems') : $_('mibEditor.noFiles')}</li>
  {/if}
</ul>

<style>
  /* The rail holds TWO lists and has to divide its height between them.
     `flex: 1` here claimed everything left over, so the outline below rendered
     at zero height: present in the DOM, scrollable, and invisible. With no file
     open there is no outline and this still takes the whole rail. */
  .files {
    list-style: none;
    margin: 0;
    padding: 0;
    overflow-y: auto;
    min-height: 4rem;
    flex: 1 1 40%;
  }

  .file {
    display: flex;
    align-items: center;
    gap: 5px;
    width: 100%;
    padding: 4px 6px;
    background: none;
    border: none;
    border-radius: 3px;
    color: var(--text-color);
    font-size: 0.8em;
    text-align: left;
    cursor: pointer;
  }

  .file:hover {
    background-color: var(--hover-overlay);
  }

  .file.active {
    background-color: var(--accent-subtle);
    color: var(--accent-color);
  }

  /* Several selected: marked apart from the file open in the editor. */
  .file.selected {
    box-shadow: inset 2px 0 0 var(--accent-color);
    background-color: var(--hover-overlay);
  }

  .fname {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Always there, empty for a file without a problem, so the names line up.
     Not `.mark`: that is the editor's squiggle, absolutely positioned and
     underlined, and sharing the name put both on every file in the list. */
  .file-mark {
    flex: 0 0 14px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 14px;
  }

  .file-mark.failed { color: var(--error-color); }
  .file-mark.error { color: var(--error-color); }
  .file-mark.warning { color: var(--warning-color); }

  .only-problems {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 3px 6px;
    background: none;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-muted);
    font: inherit;
    font-size: 0.78em;
    cursor: pointer;
  }

  .only-problems[aria-pressed='true'] {
    border-color: var(--accent-color);
    color: var(--accent-color);
    background: var(--accent-subtle);
  }

  .only-problems .label {
    flex: 1;
    min-width: 0;
    text-align: left;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .only-problems .count {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }

  .only-problems .count.failed,
  .only-problems .count.error { color: var(--error-color); }
  .only-problems .count.warning { color: var(--warning-color); }

  .tag {
    color: var(--text-muted);
    font-size: 0.9em;
  }

  .tag.changed {
    color: var(--warning-color);
  }

  .empty {
    padding: 8px 6px;
    font-size: 0.78em;
    color: var(--text-muted);
  }
</style>
