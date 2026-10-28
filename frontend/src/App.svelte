<script>
  import { onMount, onDestroy } from 'svelte';
  import { onBackdrop } from './utils/modal';
  import { get } from 'svelte/store';
  import { _ } from 'svelte-i18n';
  import { notificationStore } from './stores/notifications';
  import { initNotifications } from './utils/nativeNotify';
  import MibPanel from './MibPanel.svelte';
  import OperationsPanel from './OperationsPanel.svelte';
  import TrapPanel from './TrapPanel.svelte';
  import HistoryPanel from './HistoryPanel.svelte';
  import MonitorPanel from './MonitorPanel.svelte';
  import DashboardPanel from './DashboardPanel.svelte';
  import DiscoveryPanel from './DiscoveryPanel.svelte';
  import EventsPanel from './EventsPanel.svelte';
  import Notifications from './Notifications.svelte';
  import SettingsModal from './SettingsModal.svelte';
  import TargetManager from './TargetManager.svelte';
  import DebugPanel from './DebugPanel.svelte';
  import Icon from './Icon.svelte';
  import UpdateBanner from './UpdateBanner.svelte';
  import SimulatorModal from './SimulatorModal.svelte';
  import MibDiagnosis from './mib/MibDiagnosis.svelte';
  import { trapStore } from './stores/trapStore';
  import { updateStore } from './stores/updateStore';
  import { mibPathsStore } from './stores/mibPathsStore';
  import { mibStore, mibDiagnostics } from './stores/mibStore';
  import { settingsStore } from './stores/settingsStore';
  import { pollingStore } from './stores/pollingStore';
  import { historyStore } from './stores/historyStore';
  import { eventCounts, eventsStore } from './stores/eventsStore';
  import { simulatorStore, runningSimulated } from './stores/simulatorStore';
  import { GetPersistentMibDirectory, ListMibFiles, ImportMibFiles, ImportPresetFiles, TraySetLabels } from '../wailsjs/go/app/App';
  import MibEditorPanel from './MibEditorPanel.svelte';
  import { mibEditorStore } from './stores/mibEditorStore';
  import { tabRequest } from './stores/tabRequest';
  import { settingsRequest } from './stores/settingsRequest';
  import { OnFileDrop, OnFileDropOff } from '../wailsjs/runtime/runtime';

  let activeTab = 'operations'; // 'operations', 'traps', or 'history'
  let historyHighlightId = null; // entry to highlight when opening the History tab

  function goToHistory() {
    activeTab = 'history';
  }

  function goToHistoryEntry(e) {
    historyHighlightId = e.detail?.id ?? null;
    activeTab = 'history';
  }

  // Compute target count from settings
  $: targetCount = $settingsStore.targets
    ? $settingsStore.targets.split('\n').filter(t => t.trim()).length
    : 0;
  let selectedNode = null;
  let showSettings = false;
  // Where Settings opens — `{ section, anchor }` from requestSettings — or null
  // for the first section, which is what the shortcut and the button open.
  let settingsTarget = null;
  let showTargets = false;
  let showSimulator = false;
  let showDebug = false;
  let pendingSnmpAction = null;
  
  // Resizable panel
  let mibPanelWidth = 350; // Default width in pixels
  const MIN_PANEL = 250;
  const MAX_PANEL = 800;
  // Collapsed, the tree gives its width back to whatever tab is open. Worth
  // having on every tab, but the editor is where it earns its keep.
  let mibPanelCollapsed = false;
  // Width of the rail left behind when collapsed: enough for the arrow to stay
  // fully visible and clickable, since it is the only way back.
  const COLLAPSED_RAIL = 12;
  let isResizing = false;
  let startX = 0;
  let startWidth = 0;

  // Load saved width from localStorage
  function loadPanelWidth() {
    try {
      const saved = localStorage.getItem('snmplens_panel_width');
      if (saved) {
        mibPanelWidth = parseInt(saved, 10);
      }
      mibPanelCollapsed = localStorage.getItem('snmplens_panel_collapsed') === '1';
    } catch (e) {
      console.error('Failed to load panel width:', e);
    }
  }

  // Save width to localStorage
  function savePanelWidth() {
    try {
      localStorage.setItem('snmplens_panel_width', mibPanelWidth.toString());
      localStorage.setItem('snmplens_panel_collapsed', mibPanelCollapsed ? '1' : '0');
    } catch (e) {
      console.error('Failed to save panel width:', e);
    }
  }

  function startResize(event) {
    if (mibPanelCollapsed) return; // nothing to resize; the arrow still works
    isResizing = true;
    startX = event.clientX;
    startWidth = mibPanelWidth;
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  }

  function handleMouseMove(event) {
    if (!isResizing) return;
    
    const delta = event.clientX - startX;
    const newWidth = startWidth + delta;
    
    mibPanelWidth = Math.max(MIN_PANEL, Math.min(MAX_PANEL, newWidth));
  }

  // The handle is a `slider`, not a `separator`, and it is focusable. ARIA calls
  // a focusable divider a widget, and a widget only a mouse can reach is not one
  // — so the same bounds are here, driven by the arrow keys. Shift takes bigger
  // steps, Home and End go to the ends.
  function resizeKey(event) {
    if (mibPanelCollapsed) return;
    const step = event.shiftKey ? 50 : 10;
    let width = mibPanelWidth;
    if (event.key === 'ArrowLeft') width -= step;
    else if (event.key === 'ArrowRight') width += step;
    else if (event.key === 'Home') width = MIN_PANEL;
    else if (event.key === 'End') width = MAX_PANEL;
    else return;
    event.preventDefault();
    mibPanelWidth = Math.max(MIN_PANEL, Math.min(MAX_PANEL, width));
  }

  function toggleMibPanel() {
    mibPanelCollapsed = !mibPanelCollapsed;
    savePanelWidth();
  }

  function stopResize() {
    if (!isResizing) return;
    isResizing = false;
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
    savePanelWidth(); // Save on resize stop
  }

  // --- Theme management ---
  const systemDarkQuery = window.matchMedia('(prefers-color-scheme: dark)');

  function applyTheme(themeSetting) {
    let resolved;
    if (themeSetting === 'light' || themeSetting === 'dark') {
      resolved = themeSetting;
    } else {
      // 'system' — use OS preference
      resolved = systemDarkQuery.matches ? 'dark' : 'light';
    }
    if (resolved === 'dark') {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', 'light');
    }
  }

  // React to settings changes
  $: applyTheme($settingsStore.theme);

  // React to OS theme changes in real time (only when set to 'system')
  function onSystemThemeChange() {
    if ($settingsStore.theme === 'system' || !$settingsStore.theme) {
      applyTheme('system');
    }
  }
  systemDarkQuery.addEventListener('change', onSystemThemeChange);
  onDestroy(() => {
    systemDarkQuery.removeEventListener('change', onSystemThemeChange);
    OnFileDropOff();
  });

  // --- Drag-and-drop MIB import ---
  let dragOver = false;
  let dragLeaveTimer = null;

  // Only a drag that carries FILES is an import. Text dragged inside the MIB
  // editor — a selection moved with the mouse — is a drag too, and the
  // overlay offered to import it. Not preventing the default for it also
  // leaves the textarea's own drop, which is how the text gets moved, alone.
  function carriesFiles(event) {
    return Array.from(event?.dataTransfer?.types || []).includes('Files');
  }

  function handleDragOver(event) {
    if (!carriesFiles(event)) return;
    event.preventDefault();
    clearTimeout(dragLeaveTimer);
    dragOver = true;
  }
  function handleDragLeave() {
    // Small delay to avoid flicker when moving between child elements
    dragLeaveTimer = setTimeout(() => { dragOver = false; }, 80);
  }
  function handleDragEnd() {
    dragOver = false;
  }

  // MIB import result modal state
  let importErrors = [];   // [{ fileName, error }]
  let showImportErrors = false;

  // A preset that imports with problems is still imported: the error list with
  // its field paths is what the library is for, and you cannot fix a file you
  // were not allowed to keep. So this reports rather than refuses — one line
  // per file, because a drop is a bulk action nobody reads unless it went
  // wrong, which is exactly when it has to say which file and why.
  async function importDroppedPresets(paths, t) {
    try {
      const results = await ImportPresetFiles(paths);
      for (const r of results || []) {
        if (!r.success) {
          notificationStore.add(`${r.fileName}: ${r.error}`, 'error');
        } else if (r.problems > 0) {
          notificationStore.add(
            t('preset.importedWithProblems', { values: { file: r.fileName, count: r.problems } }),
            'warning',
          );
        } else if (!r.skipped) {
          notificationStore.add(t('preset.imported', { values: { file: r.fileName } }), 'success');
        }
      }
    } catch (e) {
      console.error('ImportPresetFiles failed', e);
      notificationStore.add(String(e), 'error');
    }
  }

  async function handleFileDrop(_x, _y, paths) {
    dragOver = false;
    if (!paths || paths.length === 0) return;

    const t = get(_);

    // One drop, two destinations.
    //
    // There is exactly one OnFileDrop handler for the window, and it used to
    // send everything to the MIB importer. A dashboard preset is JSON and a MIB
    // never is, so the extension decides which GATE a file faces — not whether
    // it is admitted. Both importers decide that by CONTENT, on the Go side,
    // and both refuse with a sentence naming what the file actually is.
    const presetPaths = paths.filter((p) => /\.json$/i.test(p));
    if (presetPaths.length > 0) {
      await importDroppedPresets(presetPaths, t);
    }
    paths = paths.filter((p) => !/\.json$/i.test(p));
    if (paths.length === 0) return;

    const droppedNames = paths.map(p => p.replace(/.*[/\\]/, ''));

    try {
      // 1. Copy files to MIB directory
      const copyResults = await ImportMibFiles(paths);
      const copyFailed = copyResults.filter(r => !r.success);
      const skipped = copyResults.filter(r => r.success && r.skipped);
      const newlyImported = copyResults.filter(r => r.success && !r.skipped);
      const copiedNames = newlyImported.map(r => r.fileName);

      if (newlyImported.length === 0 && copyFailed.length === 0) {
        // Everything was skipped (already exists)
        notificationStore.add(t('app.mibDrop.allSkipped', { values: { count: skipped.length } }), 'info');
        return;
      }

      if (newlyImported.length === 0 && copyFailed.length > 0) {
        // All new files failed, only skips otherwise
        importErrors = copyFailed;
        showImportErrors = true;
        return;
      }

      // 2. Re-scan and reload MIBs (suppress default notification via silent flag)
      const defaultPath = await GetPersistentMibDirectory();
      const mibFiles = await ListMibFiles(defaultPath);
      mibPathsStore.setDetectedMibs(defaultPath, mibFiles);
      await mibStore.loadSilent();

      // 3. Check load diagnostics for the files we just dropped
      const diag = get(mibDiagnostics);
      const loadFailed = diag
        .filter(d => !d.success && copiedNames.includes(d.fileName))
        .map(d => ({
          fileName: d.fileName,
          error: d.error || t('app.mibDrop.parseError'),
          diagnosis: d.diagnosis || null,
        }));

      // 4. Combine copy failures + load/parse failures
      const allErrors = [...copyFailed, ...loadFailed];
      const fullySucceeded = copiedNames.length - loadFailed.length;

      // 5. Build notification message
      let msg = '';
      if (fullySucceeded > 0) {
        msg = t('app.mibDrop.success', { values: { count: fullySucceeded } });
      }
      if (skipped.length > 0) {
        const skipMsg = t('app.mibDrop.skipped', { values: { count: skipped.length } });
        msg = msg ? msg + ' — ' + skipMsg : skipMsg;
      }
      if (msg) {
        notificationStore.add(msg, allErrors.length > 0 ? 'info' : 'success');
      }

      if (allErrors.length > 0) {
        importErrors = allErrors;
        showImportErrors = true;
        if (fullySucceeded === 0 && !msg) {
          notificationStore.add(t('app.mibDrop.noFiles'), 'error');
        }
      }
    } catch (e) {
      console.error('MIB drop import failed:', e);
      importErrors = [{ fileName: droppedNames.join(', '), error: String(e) }];
      showImportErrors = true;
    }
  }

  // Initialize MIB paths store on startup
  // Something elsewhere asked for a tab — the MIB settings' "open in editor"
  // link, today.
  $: if ($tabRequest) {
    activeTab = $tabRequest;
    showSettings = false;
    tabRequest.set(null);
  }

  // Something elsewhere asked for Settings at a given place — the Traps tab's
  // "Manage profiles", today.
  $: if ($settingsRequest) {
    settingsTarget = $settingsRequest;
    showSettings = true;
    settingsRequest.set(null);
  }

  onMount(async () => {
    // Load saved panel width
    loadPanelWidth();

    // The tray menu is built by Go before any locale is known, so push the
    // translated wording now. Go keeps no catalogue of its own: a second one
    // would drift from the five JSON files that are the real source of truth.
    TraySetLabels($_('tray.show'), $_('tray.quit')).catch(() => {});

    // Initialize native OS notifications
    initNotifications();

    // Register Wails file drop handler
    OnFileDrop(handleFileDrop, true);

    mibPathsStore.load();

    // Load persisted query history from SQLite (migrates any legacy
    // localStorage history on first run).
    historyStore.init();

    // The journal keeps recording with the tab closed, so the badge has to
    // start counting immediately rather than on first visit.
    eventsStore.listen();
    eventsStore.refreshCounts();

    // The simulated devices are Go's. Listed now, so the header says how many
    // are running before anyone opens the simulator.
    simulatorStore.init().catch(() => {});

    // Scan default MIB directory
    try {
      const defaultPath = await GetPersistentMibDirectory();
      const mibFiles = await ListMibFiles(defaultPath);
      mibPathsStore.setDetectedMibs(defaultPath, mibFiles);

      // Now load the MIBs after paths are initialized
      await mibStore.load();
    } catch (e) {
      console.error('Failed to scan default MIB directory:', e);
    }

    // Load the current version and, if enabled, check for updates in the
    // background (non-blocking, failures are swallowed).
    updateStore.loadVersion();
    if ($settingsStore.updates?.autoCheck) {
      updateStore.check();
    }
  });

  // Handle SNMP action from context menu
  function handleSnmpAction(event) {
    const { type, oid, name } = event.detail;
    activeTab = 'operations'; // Switch to operations tab
    pendingSnmpAction = { type, oid, name };

    // Clear the pending action after a short delay
    setTimeout(() => {
      pendingSnmpAction = null;
    }, 100);
  }

  // Global keyboard shortcuts
  function handleGlobalKeydown(event) {
    // Don't trigger shortcuts when typing in inputs
    const target = event.target;
    const isInput = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT';

    // Ctrl+1/2/3 to switch tabs
    if (event.ctrlKey && !event.shiftKey && !event.altKey) {
      if (event.key === '1') {
        event.preventDefault();
        activeTab = 'operations';
      } else if (event.key === '2') {
        event.preventDefault();
        activeTab = 'traps';
      } else if (event.key === '3') {
        event.preventDefault();
        activeTab = 'history';
      } else if (event.key === '4') {
        event.preventDefault();
        activeTab = 'monitor';
      } else if (event.key === '5') {
        event.preventDefault();
        activeTab = 'discovery';
      } else if (event.key === '6') {
        event.preventDefault();
        activeTab = 'events';
      } else if (event.key === '7') {
        event.preventDefault();
        activeTab = 'mibeditor';
      } else if (event.key === '8') {
        event.preventDefault();
        activeTab = 'dashboard';
      } else if (event.key === 'b') {
        // The conventional shortcut for a sidebar, and it was free.
        event.preventDefault();
        toggleMibPanel();
      } else if (event.key === ',') {
        // Ctrl+, to open settings (like VS Code)
        event.preventDefault();
        showSettings = true;
      }
    }

    // Ctrl+Shift+A to toggle anonymous mode
    if (event.ctrlKey && event.shiftKey && event.key === 'A') {
      event.preventDefault();
      settingsStore.save({ ...$settingsStore, anonymousMode: !$settingsStore.anonymousMode });
    }

    // Escape closes whichever dialog this component owns. SettingsModal
    // handles its own, so it is deliberately not listed here.
    if (event.key === 'Escape') {
      if (showImportErrors) {
        showImportErrors = false;
        return;
      }
      if (showTargets) {
        showTargets = false;
        return;
      }
    }
  }
</script>

<svelte:window
  on:focus={() => trapStore.setWindowFocus(true)}
  on:blur={() => trapStore.setWindowFocus(false)}
  on:keydown={handleGlobalKeydown}
  on:mousemove={handleMouseMove}
  on:mouseup={stopResize}
  on:dragover={handleDragOver}
  on:dragleave={handleDragLeave}
  on:drop={handleDragEnd}
  on:dragend={handleDragEnd}
/>

{#if dragOver}
  <div class="drop-overlay">
    <div class="drop-content">
      <div class="drop-icon"><Icon name="file-plus" size={48} strokeWidth={1.5} /></div>
      <div class="drop-text">{$_('app.mibDrop.overlay')}</div>
      <div class="drop-hint">{$_('app.mibDrop.overlayHint')}</div>
    </div>
  </div>
{/if}

<main style="--wails-drop-target:drop">
  <Notifications />
  {#if showSettings}
    <SettingsModal {showDebug} initial={settingsTarget} on:toggleDebug={() => showDebug = !showDebug}
      on:close={() => { showSettings = false; settingsTarget = null; }} />
  {/if}

  {#if showSimulator}
    <SimulatorModal on:close={() => (showSimulator = false)} />
  {/if}

  {#if showImportErrors}
    <div class="modal-backdrop" on:click={onBackdrop(() => showImportErrors = false)} role="presentation">
      <div class="import-error-modal" role="dialog" aria-modal="true" tabindex="-1">
        <div class="import-error-header">
          <span class="import-error-title">{$_('app.mibDrop.errorTitle')}</span>
          <button class="btn btn-small" on:click={() => showImportErrors = false}>&times;</button>
        </div>
        <div class="import-error-body">
          <p class="import-error-desc">{$_('app.mibDrop.errorDesc')}</p>
          {#each importErrors as err}
            <MibDiagnosis
              diagnosis={err.diagnosis || { stage: "", summary: err.error }}
              fileName={err.fileName}
              summary={err.error} />
          {/each}
        </div>
      </div>
    </div>
  {/if}

  {#if showTargets}
    <div class="modal-backdrop" on:mousedown={onBackdrop(() => showTargets = false)} role="presentation">
      <div class="targets-modal" role="dialog" aria-modal="true" tabindex="-1">
        <div class="targets-modal-header">
          <h2><Icon name="target" size={20} /> {$_('targets.title', { values: { count: targetCount } })}</h2>
          <button class="close-btn" on:click={() => showTargets = false}>&times;</button>
        </div>
        <div class="targets-modal-body">
          <TargetManager />
        </div>
      </div>
    </div>
  {/if}

  <UpdateBanner />

  <div class="top-bar">
    <div class="top-bar-left">
      <button class="targets-btn" on:click={() => showTargets = true} title={$_('app.status.targetsTitle')}>
        <Icon name="target" size={15} /> {$_('common.target')}
        <span class="targets-badge">{targetCount}</span>
      </button>
    </div>
    <div class="status-bar">
      <div
        class="status-item"
        class:active={$trapStore.isListening}
        title={$trapStore.isListening ? $_('app.status.trapListening', { values: { port: $settingsStore.trapPort } }) : $_('app.status.trapInactive')}
      >
        <span class="status-icon"><Icon name="radio" size={14} /></span>
        <span class="status-label">{$_('app.status.trap')}</span>
        <span class="status-indicator" class:listening={$trapStore.isListening}>
          {$trapStore.isListening ? $settingsStore.trapPort : $_('app.status.trapOff')}
        </span>
      </div>
      <div class="status-separator"></div>
      <button
        class="status-item simulator"
        class:active={$runningSimulated > 0}
        on:click={() => (showSimulator = true)}
        title={$_('app.status.simulatorTitle', { values: { running: $runningSimulated, total: $simulatorStore.devices.length } })}
      >
        <span class="status-icon"><Icon name="server-cog" size={14} /></span>
        <span class="status-label">{$_('app.status.simulator')}</span>
        <span class="status-indicator" class:listening={$runningSimulated > 0}>{$runningSimulated}</span>
      </button>
      <div class="status-separator"></div>
      <div class="version-switcher" title={$_('app.status.snmpVersionTitle')}>
        {#each ['v1', 'v2c', 'v3'] as ver}
          <button
            class="version-btn"
            class:active={$settingsStore.snmpVersion === ver}
            on:click={() => settingsStore.save({ ...$settingsStore, snmpVersion: ver })}
          >
            {ver}
          </button>
        {/each}
      </div>
    </div>
    <button class="btn settings-btn" on:click={() => showSettings = true} title={$_('app.settingsTooltip')}>
      <Icon name="settings" size={15} /> {$_('app.settings')}
    </button>
  </div>

  <div class="container">
    <div class="mib-panel-container" class:collapsed={mibPanelCollapsed}
      style="width: {mibPanelCollapsed ? COLLAPSED_RAIL : mibPanelWidth}px;">
      <!-- Hidden rather than unmounted: the tree keeps its scroll position and
           its expanded nodes, so collapsing and reopening returns you to where
           you were instead of to the top. -->
      <div class="mib-panel-inner" class:hidden={mibPanelCollapsed}>
        <MibPanel 
          on:select={(e) => selectedNode = e.detail.node}
          on:snmpAction={handleSnmpAction}
        />
      </div>
      <div
        class="resize-handle"
        class:disabled={mibPanelCollapsed}
        on:mousedown={startResize}
        on:keydown={resizeKey}
        role="slider"
        tabindex={mibPanelCollapsed ? -1 : 0}
        aria-orientation="vertical"
        aria-valuenow={mibPanelWidth}
        aria-valuemin={MIN_PANEL}
        aria-valuemax={MAX_PANEL}
        aria-disabled={mibPanelCollapsed}
        aria-label={$_('app.panel.resize')}
      >
        <div class="resize-handle-inner"></div>
        <button
          class="panel-toggle"
          on:mousedown|stopPropagation
          on:click|stopPropagation={toggleMibPanel}
          aria-expanded={!mibPanelCollapsed}
          title="{mibPanelCollapsed ? $_('app.panel.show') : $_('app.panel.hide')} (Ctrl+B)"
        >
          <Icon name={mibPanelCollapsed ? 'chevron-right' : 'chevron-left'} size={13} />
        </button>
      </div>
    </div>

    <div class="main-content" class:editor-host={activeTab === 'mibeditor'}>
      <div class="tabs">
        <button class="tab-btn" class:active={activeTab === 'operations'} on:click={() => activeTab = 'operations'} title="Ctrl+1">
          {$_('app.tabs.operations')}
          <span class="shortcut-hint">1</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'traps'} on:click={() => activeTab = 'traps'} title="Ctrl+2">
          {$_('app.tabs.traps')}
          {#if $trapStore.traps.length > 0}
            <span class="badge">{$trapStore.traps.length}</span>
          {/if}
          {#if $trapStore.isListening}
            <span class="listening-indicator" title={$_('app.status.trapListening', { values: { port: $settingsStore.trapPort } })}>●</span>
          {/if}
          <span class="shortcut-hint">2</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'history'} on:click={() => activeTab = 'history'} title="Ctrl+3">
          {$_('app.tabs.history')}
          <span class="shortcut-hint">3</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'monitor'} on:click={() => activeTab = 'monitor'} title="Ctrl+4">
          {$_('app.tabs.monitor')}
          {#if $pollingStore.some(s => s.running)}
            <span class="listening-indicator" title={$_('app.status.trapListening', { values: { port: $settingsStore.trapPort } })}>●</span>
          {/if}
          <span class="shortcut-hint">4</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'dashboard'} on:click={() => activeTab = 'dashboard'} title="Ctrl+8">
          {$_('app.tabs.dashboard')}
          <span class="shortcut-hint">8</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'discovery'} on:click={() => activeTab = 'discovery'} title="Ctrl+5">
          {$_('app.tabs.network')}
          <span class="shortcut-hint">5</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'events'} on:click={() => activeTab = 'events'} title="Ctrl+6">
          {$_('app.tabs.events')}
          {#if $eventCounts.unacked > 0}
            <span class="events-badge" title={$_('events.unackedCount', { values: { count: $eventCounts.unacked } })}>
              {$eventCounts.unacked > 99 ? '99+' : $eventCounts.unacked}
            </span>
          {/if}
          <span class="shortcut-hint">6</span>
        </button>
        <button class="tab-btn" class:active={activeTab === 'mibeditor'} on:click={() => activeTab = 'mibeditor'} title="Ctrl+7">
          <Icon name="book-marked" size={15} /> {$_('app.tabs.mibEditor')}
          {#if $mibEditorStore.source && $mibEditorStore.buffer !== $mibEditorStore.source.content}
            <span class="unsaved-dot" title={$_('mibEditor.unsaved')}></span>
          {/if}
        </button>
        {#if $settingsStore.anonymousMode}
          <span class="anon-badge" title={$_('settings.general.anonymousMode') + ' (Ctrl+Shift+A)'}>ANON</span>
        {/if}
      </div>

      {#if activeTab === 'mibeditor'}
        <MibEditorPanel />
      {/if}

      {#if activeTab === 'operations'}
        <OperationsPanel
          selectedNode={selectedNode}
          pendingAction={pendingSnmpAction}
          on:openHistory={goToHistory}
          on:viewInHistory={goToHistoryEntry}
        />
      {/if}

      {#if activeTab === 'traps'}
        <TrapPanel />
      {/if}

      {#if activeTab === 'history'}
        <HistoryPanel highlightId={historyHighlightId} on:highlightApplied={() => historyHighlightId = null} />
      {/if}

      {#if activeTab === 'monitor'}
        <MonitorPanel />
      {/if}

      {#if activeTab === 'dashboard'}
        <DashboardPanel />
      {/if}

      {#if activeTab === 'discovery'}
        <DiscoveryPanel />
      {/if}

      {#if activeTab === 'events'}
        <EventsPanel />
      {/if}

      {#if showDebug}
        <DebugPanel />
      {/if}
    </div>
  </div>
</main>

<style>
  main {
    display: flex;
    flex-direction: column;
    height: 100vh;
    overflow: hidden;
  }

  .top-bar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0 20px;
    background-color: var(--bg-lighter-color);
    color: white;
    height: 50px;
    flex-shrink: 0;
    border-bottom: 1px solid var(--border-color);
  }

  .top-bar-left {
    display: flex;
    align-items: center;
    gap: 16px;
  }

  .targets-btn {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 14px;
    background-color: var(--accent-subtle-strong);
    border: 1px solid var(--accent-border-strong);
    border-radius: 6px;
    color: var(--text-color);
    font-size: 0.88em;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s;
    white-space: nowrap;
  }

  .targets-btn:hover {
    background-color: var(--accent-border);
    border-color: var(--accent-color);
  }

  .targets-badge {
    background-color: var(--accent-color);
    color: white;
    font-size: 0.8em;
    font-weight: 700;
    padding: 1px 7px;
    border-radius: 10px;
    min-width: 20px;
    text-align: center;
  }

  /* Version Switcher */
  .version-switcher {
    display: flex;
    border: 1px solid var(--border-color);
    border-radius: 6px;
    overflow: hidden;
  }

  .version-btn {
    padding: 5px 12px;
    background: transparent;
    border: none;
    border-right: 1px solid var(--border-color);
    color: var(--text-muted);
    font-size: 0.85em;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.15s;
  }

  .version-btn:last-child {
    border-right: none;
  }

  .version-btn:hover {
    background-color: var(--hover-overlay);
    color: var(--text-color);
  }

  .version-btn.active {
    background-color: var(--accent-color);
    color: white;
  }

  /* Status bar styles */
  .status-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    background-color: var(--bg-color);
    padding: 6px 12px;
    border-radius: 6px;
    font-size: 0.85em;
  }

  .status-item {
    display: flex;
    align-items: center;
    gap: 5px;
    color: var(--text-dimmed);
    background: none;
    border: none;
    font: inherit;
    padding: 0;
    cursor: default;
  }

  .status-item.active {
    color: var(--success-color);
  }

  /* The one status item that opens something. */
  .status-item.simulator {
    cursor: pointer;
    padding: 2px 5px;
    border-radius: 4px;
  }

  .status-item.simulator:hover {
    background-color: var(--hover-overlay-medium);
  }

  .status-icon {
    font-size: 1em;
  }

  .status-label {
    color: var(--text-muted);
  }

  .status-separator {
    width: 1px;
    height: 20px;
    background-color: var(--border-color);
    margin: 0 4px;
  }

  .status-indicator {
    padding: 2px 6px;
    border-radius: 4px;
    font-size: 0.85em;
    font-weight: 600;
    background-color: var(--hover-overlay-medium);
    color: var(--text-muted);
  }

  .status-indicator.listening {
    background-color: var(--success-subtle-strong);
    color: var(--success-color);
    animation: pulse 2s infinite;
  }

  .settings-btn {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .container {
    display: flex;
    flex: 1;
    min-height: 0;
    position: relative;
  }

  .mib-panel-container {
    position: relative;
    flex-shrink: 0;
    display: flex;
    height: 100%;
    min-width: 250px;
    max-width: 800px;
  }

  /* The min-width has to go, or collapsing would stop at 250px. */
  .mib-panel-container.collapsed {
    min-width: 0;
  }

  .mib-panel-inner {
    display: flex;
    min-width: 0;
    width: 100%;
    height: 100%;
  }

  .mib-panel-inner.hidden {
    display: none;
  }

  .resize-handle.disabled {
    cursor: default;
  }

  /* The arrow sits on the divider, vertically centred: the same place the eye
     already goes to drag it. When collapsed it is the only way back, so it
     stays fully on-screen rather than half under the edge. */
  .panel-toggle {
    position: absolute;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: 18px;
    height: 44px;
    padding: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
    opacity: 0.55;
    transition: opacity 0.15s, color 0.15s, border-color 0.15s;
    z-index: 11;
  }

  .panel-toggle:hover,
  .panel-toggle:focus-visible {
    opacity: 1;
    color: var(--accent-color);
    border-color: var(--accent-color);
  }

  .collapsed .panel-toggle {
    opacity: 1;
  }

  /* Resize handle */
  .resize-handle {
    position: absolute;
    top: 0;
    right: -5px;
    width: 10px;
    height: 100%;
    cursor: col-resize;
    z-index: 10;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .resize-handle:hover .resize-handle-inner {
    background-color: var(--accent-color);
    opacity: 1;
  }

  .resize-handle-inner {
    width: 3px;
    height: 60px;
    background-color: var(--border-color);
    border-radius: 2px;
    opacity: 0.5;
    transition: all 0.2s;
  }

  .resize-handle:active .resize-handle-inner {
    background-color: var(--accent-hover-color);
    opacity: 1;
    height: 100px;
  }

  /* Every other panel just grows; the editor needs to own the height so its
     textarea can scroll instead of the page. */
  /* Unsaved work must be visible from another tab, since the editor panel is
     unmounted while you are not looking at it. */
  .unsaved-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background-color: var(--accent-color);
    display: inline-block;
    margin-left: 2px;
  }

  .main-content.editor-host {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    min-height: 0;
  }

  .main-content {
    flex-grow: 1;
    padding: 10px;
    overflow-y: auto;
  }

  .tabs {
    margin-bottom: 10px;
  }

  .tab-btn {
    padding: 10px 15px;
    border: none;
    background-color: transparent;
    color: var(--text-color);
    cursor: pointer;
    border-bottom: 2px solid transparent;
  }

  .tab-btn.active {
    border-bottom: 2px solid var(--accent-color);
    font-weight: bold;
  }

  .badge {
    background-color: var(--accent-color);
    color: white;
    font-size: 0.75em;
    padding: 2px 6px;
    border-radius: 10px;
    margin-left: 6px;
    font-weight: 600;
  }

  .listening-indicator {
    color: var(--success-color);
    margin-left: 4px;
    animation: pulse 1.5s infinite;
  }

  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }

  .events-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 17px;
    height: 17px;
    padding: 0 4px;
    margin-left: 5px;
    border-radius: 9px;
    font-size: 0.68em;
    font-weight: 700;
    color: var(--text-on-accent);
    background-color: var(--error-color);
  }

  .shortcut-hint {
    font-size: 0.7em;
    opacity: 0.4;
    margin-left: 4px;
    padding: 1px 4px;
    border: 1px solid currentColor;
    border-radius: 3px;
    font-weight: normal;
  }

  .tab-btn:hover .shortcut-hint {
    opacity: 0.7;
  }

  .anon-badge {
    margin-left: auto;
    padding: 2px 10px;
    font-size: 0.75em;
    font-weight: 700;
    letter-spacing: 0.05em;
    color: var(--text-on-accent);
    background-color: var(--warning-color);
    border-radius: var(--radius-full);
    cursor: default;
    user-select: none;
    animation: anon-pulse 2s ease-in-out infinite;
  }

  @keyframes anon-pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.7; }
  }

  /* Drop overlay */
  :global(.drop-overlay) {
    position: fixed;
    inset: 0;
    z-index: 9999;
    background: var(--backdrop-color-strong);
    display: flex;
    align-items: center;
    justify-content: center;
    pointer-events: none;
  }

  :global(.drop-content) {
    text-align: center;
    padding: 40px 60px;
    border: 3px dashed var(--accent-color);
    border-radius: 16px;
    background: var(--bg-lighter-color);
  }

  :global(.drop-icon) {
    font-size: 3em;
    margin-bottom: 12px;
  }

  :global(.drop-text) {
    font-size: 1.3em;
    font-weight: 700;
    color: var(--text-color);
  }

  :global(.drop-hint) {
    font-size: 0.9em;
    color: var(--text-muted);
    margin-top: 6px;
  }

  /* Import error modal */
  .import-error-modal {
    background: var(--bg-light-color);
    border: 1px solid var(--error-border);
    border-radius: var(--radius-lg);
    width: 520px;
    max-width: 90vw;
    max-height: 70vh;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .import-error-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-color);
    background: var(--error-subtle);
  }

  .import-error-title {
    font-weight: 700;
    color: var(--error-color);
  }

  .import-error-body {
    padding: 16px;
    overflow-y: auto;
  }

  .import-error-desc {
    margin: 0 0 12px;
    font-size: 0.9em;
    color: var(--text-muted);
  }

  /* Targets Modal */
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

  .targets-modal {
    background-color: var(--bg-light-color);
    border-radius: 8px;
    width: 90%;
    max-width: 600px;
    max-height: 80vh;
    box-shadow: 0 5px 15px var(--shadow-color-strong);
    display: flex;
    flex-direction: column;
  }

  .targets-modal-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 15px 20px;
    border-bottom: 1px solid var(--border-color);
  }

  .targets-modal-header h2 {
    margin: 0;
    font-size: 1.2em;
  }

  .close-btn {
    background: none;
    border: none;
    color: var(--text-color);
    font-size: 1.5rem;
    cursor: pointer;
  }

  .targets-modal-body {
    padding: 20px;
    overflow-y: auto;
  }
</style>
