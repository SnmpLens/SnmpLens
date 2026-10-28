<script>
  import { onMount, tick } from 'svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import ContextMenu from '../ContextMenu.svelte';
  import ConfirmDialog from '../ConfirmDialog.svelte';
  import MibDiagnosis from '../mib/MibDiagnosis.svelte';
  import Dialog from './Dialog.svelte';
  import NameDialog from './NameDialog.svelte';
  import BackupsDialog from './BackupsDialog.svelte';
  import DiffDialog from './DiffDialog.svelte';
  import DepsDialog from './DepsDialog.svelte';
  import { fileMenu, listMenu, copyName as copyNameFor, nameForModule } from './fileMenu.js';
  import { problemsText, withoutMuted, sortProblems, FIXABLE } from './problems.js';
  import { notificationStore } from '../stores/notifications';
  import { mibPathsStore } from '../stores/mibPathsStore';
  import { mibEditorStore } from '../stores/mibEditorStore';
  import { mutedRules } from '../stores/mutedRules';
  import { requestTreeNode } from '../stores/treeRequest';
  import { copyToClipboard } from '../utils/clipboard';
  import {
    GetPersistentMibDirectory,
    OpenFolder,
    MibDiagnose,
    MibDependencyGraph,
    MibEditorRead,
    MibEditorRepair,
    MibEditorQuickFix,
    MibEditorRestoreBundled,
    MibEditorRevealFile,
    MibEditorRenameFile,
    MibEditorDuplicateFile,
    MibEditorDeleteFiles,
    MibEditorBackups,
    MibEditorDeletedFiles,
    MibEditorReadBackup,
    MibEditorRestoreDeleted,
    MibEditorBundledContent,
    MibEditorModuleRoot,
    MibEditorExportWithDependencies,
  } from '../../wailsjs/go/app/App';

  // What the MIB editor's file list does besides opening a file: its context
  // menu (mibeditor/fileMenu.js decides what it offers), the dialogs it opens,
  // and the actions themselves (internal/app/mibfiles.go does the writing).
  //
  // The panel keeps the list and the selection; what it needs done to the
  // buffer it is asked through these callbacks, since the buffer's undo
  // history is the panel's.

  /** MibEditorList's answer. */
  export let files = [];
  /** verdictMap's answer, for "show in the MIB tree". */
  export let verdicts = {};
  /** Open a file as the user would; resolves false when they kept the one open. */
  export let onOpen = async () => false;
  /** Open a file without asking, after the draft has been carried over. */
  export let onReopen = async () => {};
  /** Replace the buffer with text, as one undoable edit. */
  export let onReplaceBuffer = () => {};
  /** Restore the shipped copy of the file open in the editor (it asks first). */
  export let onRestoreOpen = async () => {};
  /** The directory changed: list it again and rebuild the tree. */
  export let onChanged = async () => {};
  /** List the directory again. */
  export let onRefresh = async () => {};

  let mibDir = '';
  onMount(async () => {
    try {
      mibDir = await GetPersistentMibDirectory();
    } catch (e) {
      mibDir = '';
    }
  });

  $: openName = $mibEditorStore.source && !$mibEditorStore.source.external ? $mibEditorStore.source.name : '';
  $: flags = $mibPathsStore.enabledMibs[mibDir] || {};

  const t = (key, values) => get(_)(key, values ? { values } : undefined);
  const say = (key, values, level = 'success') => notificationStore.add(t('mibEditor.fileActions.' + key, values), level);
  const fail = (e) => notificationStore.add(String(e?.message || e), 'error');

  // --- the menu ------------------------------------------------------------

  let menu = null; // { x, y, names, items }

  function translate(entries) {
    return entries.map((e) => (e.action === '---'
      ? { label: '---' }
      : {
        label: t('mibEditor.fileMenu.' + e.key, e.values),
        icon: e.icon,
        hint: e.hint === 'Del' ? t('mibEditor.fileMenu.delKey') : e.hint,
        action: e.action,
        disabled: !!e.disabled,
        disabledReason: e.reason ? t('mibEditor.fileMenu.' + e.reason) : '',
      }));
  }

  /** Open the menu for names at a point on screen. */
  export function openMenu(x, y, names) {
    const isEnabled = (n) => flags[n] !== false;
    const entries = fileMenu({ names, files, isEnabled, verdicts });
    if (entries.length) menu = { x, y, names, items: translate(entries) };
  }

  /** Open the menu of the list itself. */
  export function openListMenu(x, y) {
    menu = { x, y, names: [], items: translate(listMenu()) };
  }

  function onMenuAction(e) {
    const names = menu?.names || [];
    menu = null;
    run(e.detail.action, names);
  }

  // --- dialogs ---------------------------------------------------------------

  let nameDialog = null; // { mode: 'rename'|'duplicate', name, value, error, busy }
  let confirmDelete = null; // { names }
  let backups = null; // { name, copies, loading, deleted }
  let diff = null; // { title, leftLabel, rightLabel, before, after }
  let deps = null; // { module, nodes, mode, loading }
  let diagnosis = null; // { name, result }

  // --- actions -----------------------------------------------------------------

  const fileOf = (name) => files.find((f) => f.name === name);
  const separator = () => (mibDir.includes('\\') ? '\\' : '/');

  /** Run an action on names; what F2, Delete and the menu all come through. */
  export async function run(action, names) {
    const name = names[0];
    try {
      switch (action) {
        case 'copyName':
          return copyToClipboard(names.join('\n'), names.length > 1
            ? t('mibEditor.fileActions.namesLabel', { count: names.length }) : t('mibEditor.fileActions.nameLabel'));
        case 'copyPath':
          return copyToClipboard(mibDir + separator() + name, t('mibEditor.fileActions.pathLabel'));
        case 'copyModule':
          return copyToClipboard(fileOf(name)?.module || '', t('mibEditor.fileActions.moduleLabel'));
        case 'reveal':
          return await MibEditorRevealFile(name);
        case 'rename':
          if (fileOf(name)?.bundled) return;
          nameDialog = { mode: 'rename', name, value: name, error: '', busy: false };
          return;
        case 'renameToModule':
          return await renameFile(name, nameForModule(fileOf(name)?.module || '', name));
        case 'duplicate':
          nameDialog = { mode: 'duplicate', name, value: copyNameFor(name, files.map((f) => f.name)), error: '', busy: false };
          return;
        case 'delete': {
          const deletable = names.filter((n) => !fileOf(n)?.bundled);
          if (deletable.length) confirmDelete = { names: deletable };
          return;
        }
        case 'compareBundled':
          return await compare(name, await MibEditorBundledContent(name), t('mibEditor.fileActions.shipped'));
        case 'restoreBundled':
          return await restoreBundled(name);
        case 'backups':
          return await showBackups(name);
        case 'enable':
        case 'disable':
          for (const n of names) mibPathsStore.setMibEnabled(mibDir, n, action === 'enable');
          await onChanged();
          return;
        case 'useThisVersion':
          return await useThisVersion(name);
        case 'diagnose':
          diagnosis = { name, result: null };
          diagnosis = { name, result: await MibDiagnose(name) };
          return;
        case 'showInTree':
          return await showInTree(name);
        case 'imports':
        case 'importers':
          return await showDeps(name, action);
        case 'export':
          return await exportWithDeps(names);
        case 'copyProblems':
          return await copyProblemsOf(name);
        case 'openAndFix':
          return await openAndFix(name);
        case 'restoreDeleted':
          return await showDeleted();
        case 'openFolder':
          return await OpenFolder(mibDir);
        case 'refresh':
          return await onRefresh();
      }
    } catch (e) {
      fail(e);
    }
  }

  async function renameFile(from, to) {
    if (!to || to === from) return false;
    const wasOpen = from === openName;
    // The unsaved buffer is written to its draft first, so the rename carries
    // it and the reopened file recovers it.
    if (wasOpen) await mibEditorStore.flushDraft();
    await MibEditorRenameFile(from, to);
    mibPathsStore.renameMib(mibDir, from, to);
    if (wasOpen) await onReopen(to);
    say('renamed', { from, to });
    await onChanged();
    return true;
  }

  async function submitName(e) {
    const d = nameDialog;
    if (!d) return;
    nameDialog = { ...d, busy: true, error: '' };
    try {
      if (d.mode === 'rename') {
        await renameFile(d.name, e.detail.name);
      } else {
        await MibEditorDuplicateFile(d.name, e.detail.name);
        say('duplicated', { name: d.name, copy: e.detail.name });
        await onChanged();
      }
      nameDialog = null;
    } catch (err) {
      nameDialog = { ...d, value: e.detail.name, busy: false, error: String(err?.message || err) };
    }
  }

  async function doDelete() {
    const names = confirmDelete?.names || [];
    confirmDelete = null;
    let moved = [];
    try {
      moved = (await MibEditorDeleteFiles(names)) || [];
    } catch (e) {
      // Some went and some were refused: both are said.
      fail(e);
    }
    if (moved.includes(openName)) mibEditorStore.close();
    if (moved.length) {
      say('deleted', { count: moved.length });
      await onChanged();
    }
  }

  // What a file reads as now: the buffer for the file open in the editor,
  // what is on disk for any other.
  async function currentText(name) {
    if (name === openName) return get(mibEditorStore).buffer;
    return (await MibEditorRead(name)).content;
  }

  async function compare(name, other, otherLabel) {
    const now = await currentText(name);
    diff = {
      title: t('mibEditor.fileActions.compareTitle', { name }),
      leftLabel: otherLabel,
      rightLabel: name === openName ? t('mibEditor.fileActions.inEditor', { name }) : t('mibEditor.fileActions.onDisk', { name }),
      before: other,
      after: now,
    };
  }

  async function restoreBundled(name) {
    if (name === openName) return await onRestoreOpen();
    if (!window.confirm(t('mibEditor.restoreConfirm', { name }))) return;
    await MibEditorRestoreBundled(name);
    say('restoredBundled', { name });
    await onChanged();
  }

  async function showBackups(name) {
    backups = { name, copies: [], loading: true, deleted: false };
    backups = { name, copies: (await MibEditorBackups(name)) || [], loading: false, deleted: false };
  }

  async function showDeleted() {
    backups = { name: '', copies: [], loading: true, deleted: true };
    backups = { name: '', copies: (await MibEditorDeletedFiles()) || [], loading: false, deleted: true };
  }

  function when(iso) {
    try {
      return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
    } catch (e) {
      return iso;
    }
  }

  async function compareBackup(e) {
    const c = e.detail;
    try {
      await compare(backups.name, await MibEditorReadBackup(c.id), t('mibEditor.fileActions.copyOf', { date: when(c.savedAt) }));
    } catch (err) {
      fail(err);
    }
  }

  // A copy of a file goes into the editor, to be looked at and saved; a
  // deleted file goes back into the folder, under the name it had.
  async function restoreBackup(e) {
    const c = e.detail;
    const mode = backups;
    try {
      if (mode.deleted) {
        const name = await MibEditorRestoreDeleted(c.id);
        backups = null;
        say('restoredDeleted', { name });
        await onChanged();
        return;
      }
      const content = await MibEditorReadBackup(c.id);
      backups = null;
      if (mode.name !== openName && !(await onOpen(mode.name))) return;
      await tick();
      onReplaceBuffer(content);
      say('backupRestored', { date: when(c.savedAt) }, 'info');
    } catch (err) {
      fail(err);
    }
  }

  async function useThisVersion(name) {
    const module = (fileOf(name)?.module || '').toUpperCase();
    const others = files.filter((f) => f.name !== name && (f.module || '').toUpperCase() === module);
    for (const o of others) mibPathsStore.setMibEnabled(mibDir, o.name, false);
    mibPathsStore.setMibEnabled(mibDir, name, true);
    say('versionChosen', { name, module: fileOf(name)?.module || name });
    await onChanged();
  }

  async function showInTree(name) {
    const module = fileOf(name)?.module || '';
    const oid = module ? await MibEditorModuleRoot(module) : '';
    if (!oid) {
      say('notInTree', { module: module || name }, 'warning');
      return;
    }
    requestTreeNode(oid, module);
  }

  async function showDeps(name, mode) {
    const module = fileOf(name)?.module || name;
    deps = { module, nodes: [], mode, loading: true };
    deps = { module, nodes: (await MibDependencyGraph()) || [], mode, loading: false };
  }

  async function openDep(e) {
    deps = null;
    if (e.detail) await onOpen(e.detail);
  }

  async function exportWithDeps(names) {
    const r = await MibEditorExportWithDependencies(names);
    if (!r || !r.path) return; // cancelled
    say('exported', { count: (r.files || []).length, path: r.path });
    if ((r.missing || []).length) say('exportMissing', { modules: r.missing.join(', ') }, 'warning');
  }

  async function copyProblemsOf(name) {
    const diags = name === openName ? get(mibEditorStore).diagnostics : (await MibEditorRead(name)).diagnostics;
    const list = sortProblems(withoutMuted(diags || [], get(mutedRules)));
    if (!list.length) {
      say('noProblems', { name }, 'info');
      return;
    }
    const { label, text } = problemsText(name, list, get(_));
    copyToClipboard(text, label);
  }

  // Everything the editor can fix, written into the buffer as one edit to be
  // reviewed and saved: the lenient reading's repairs, then each quick fix.
  // A muted rule is left alone — muting it said it is not to be acted on.
  async function openAndFix(name) {
    if (name !== openName && !(await onOpen(name))) return;
    await tick();
    const before = get(mibEditorStore).buffer;
    let text = before;
    let count = 0;
    const muted = get(mutedRules);
    const repaired = await MibEditorRepair(text);
    if (repaired && repaired.content) {
      text = repaired.content;
      count += (repaired.fixes || []).length;
    }
    for (const code of FIXABLE) {
      if (muted.has(code)) continue;
      const fixed = await MibEditorQuickFix(text, code, []);
      if (fixed && fixed.content) {
        text = fixed.content;
        count += (fixed.fixed || []).length;
      }
    }
    if (text !== before) onReplaceBuffer(text);
    say('fixedAll', { count, name }, count ? 'success' : 'info');
  }
</script>

{#if menu}
  <ContextMenu x={menu.x} y={menu.y} items={menu.items} on:action={onMenuAction} on:close={() => (menu = null)} />
{/if}

{#if nameDialog}
  <NameDialog
    title={$_(nameDialog.mode === 'rename' ? 'mibEditor.fileActions.renameTitle' : 'mibEditor.fileActions.duplicateTitle',
      { values: { name: nameDialog.name } })}
    value={nameDialog.value} error={nameDialog.error} busy={nameDialog.busy}
    confirmLabel={$_(nameDialog.mode === 'rename' ? 'mibEditor.fileActions.renameConfirm' : 'mibEditor.fileActions.duplicateConfirm')}
    on:confirm={submitName} on:close={() => (nameDialog = null)} />
{/if}

{#if confirmDelete}
  <ConfirmDialog
    title={$_('mibEditor.fileActions.deleteTitle', { values: { count: confirmDelete.names.length } })}
    text={$_('mibEditor.fileActions.deleteText', { values: { names: confirmDelete.names.join(', ') } })}
    confirmLabel={$_('mibEditor.fileActions.deleteConfirm')} cancelLabel={$_('common.cancel')}
    confirmIcon="x" danger
    on:confirm={doDelete} on:cancel={() => (confirmDelete = null)} />
{/if}

{#if backups}
  <BackupsDialog
    title={backups.deleted ? $_('mibEditor.fileActions.deletedTitle')
      : $_('mibEditor.fileActions.backupsTitle', { values: { name: backups.name } })}
    copies={backups.copies} loading={backups.loading} deleted={backups.deleted}
    on:compare={compareBackup} on:restore={restoreBackup} on:close={() => (backups = null)} />
{/if}

{#if diff}
  <DiffDialog title={diff.title} leftLabel={diff.leftLabel} rightLabel={diff.rightLabel}
    before={diff.before} after={diff.after} on:close={() => (diff = null)} />
{/if}

{#if deps}
  <DepsDialog module={deps.module} nodes={deps.nodes} mode={deps.mode} loading={deps.loading}
    on:open={openDep} on:close={() => (deps = null)} />
{/if}

{#if diagnosis}
  <Dialog title={$_('mibEditor.fileActions.diagnosisTitle', { values: { name: diagnosis.name } })} icon="bug" width={640}
    on:close={() => (diagnosis = null)}>
    {#if diagnosis.result}
      <MibDiagnosis diagnosis={diagnosis.result} fileName={diagnosis.name} />
    {:else}
      <p class="hint">{$_('mibEditor.fileActions.diagnosing')}</p>
    {/if}
  </Dialog>
{/if}

<style>
  .hint {
    margin: 0;
    color: var(--text-muted);
  }
</style>
