<script>
  import { _ } from 'svelte-i18n';
  import { createEventDispatcher, onMount } from 'svelte';
  import { onBackdrop } from './utils/modal';
  import { simulatorStore } from './stores/simulatorStore';
  import { settingsStore } from './stores/settingsStore';
  import { notificationStore } from './stores/notifications';
  import { anonMode, maskString } from './utils/anonymize';
  import {
    SIM_VERSIONS, MAX_EVERY, MAX_DESTINATIONS, MAX_SCHEDULES, blankDevice, blankV3, blankDestination, blankSchedule,
    editableDevice, deviceProblems, devicePayload, addDeviceAsTarget, notificationsOf, trapActivity, deliveryReport,
    modelOf, modelName, modelDescription, importReport, firstImported, recordRequest, recordableTargets, deviceImportReport,
    deviceGroups, faultChips, categoryIcon, EDITOR_TABS, tabsWithProblems, OVERRIDE_TYPES, MAX_OVERRIDES,
    blankOverride, previewPayload, previewQuery, PREVIEW_COLUMNS,
  } from './utils/simulator.js';
  import UsmFields from './settings/UsmFields.svelte';
  import ModelPicker from './simulator/ModelPicker.svelte';
  import ModelIcon from './simulator/ModelIcon.svelte';
  import FaultsPanel from './simulator/FaultsPanel.svelte';
  import CablingPanel from './simulator/CablingPanel.svelte';
  import SecretInput from './SecretInput.svelte';
  import Icon from './Icon.svelte';

  /**
   * The simulated devices: a list to start, stop and add as targets, and an
   * editor for one device at a time. Go keeps the devices, their secrets and the
   * loopback rule; this shows, and asks.
   */
  const dispatch = createEventDispatcher();

  /** The device being edited, in the editor's shape — or null for the list. */
  let editing = null;
  let saving = false;
  let saveError = '';
  /** Devices with a start, a stop or a removal on the way, by id. */
  let busy = {};
  /** The device whose delete button has been pressed once. */
  let confirmDelete = null;

  // A running device's request count moves and nothing announces it, so the
  // list is re-read while the dialog is open.
  onMount(() => {
    simulatorStore.refresh().catch((e) => notificationStore.add(String(e), 'error'));
    simulatorStore.refreshModels().catch((e) => notificationStore.add(String(e), 'error'));
    const timer = setInterval(() => simulatorStore.refresh().catch(() => {}), 2000);
    // What moves in the preview is read again while the Data tab is open.
    const live = setInterval(() => {
      if (tab === 'data' && editing && !previewBusy) runPreview();
    }, 5000);
    return () => {
      clearInterval(timer);
      clearInterval(live);
      clearTimeout(previewTimer);
    };
  });

  /** The model of the device being edited, whose parameters it may be given. */
  $: editedModel = editing ? modelOf($simulatorStore.models, editing.model) : null;
  /** The device being edited as the list shows it — running, faults and all — once it is saved. */
  $: listed = editing?.id ? $simulatorStore.devices.find((d) => d.id === editing.id) || null : null;
  $: problems = editing ? deviceProblems(editing, editedModel) : {};
  $: usmProblems = usmMessages(problems, $_);

  /** The editor's tab, and the tabs holding something to fix. */
  let tab = 'identity';
  $: flagged = tabsWithProblems(problems);

  /** What the device as edited would answer: the rows the filter matches. */
  let previewFilter = '';
  let previewDynamicOnly = false;
  let preview = null;
  let previewError = '';
  let previewBusy = false;
  let previewTimer = null;
  let previewSeq = 0;
  /** When the Data tab was opened: values are read as if the device had run since. */
  let previewOpenedAt = 0;
  /**
   * The preview's column widths in pixels, which a header's edge is dragged to
   * change. They start at the dialog's own width, so nothing is cut before
   * anyone has asked for a wider column.
   */
  let previewWidths = { oid: 165, name: 185, type: 100, behaviour: 85, value: 185 };
  $: previewTableWidth = PREVIEW_COLUMNS.reduce((sum, c) => sum + previewWidths[c], 0);

  // The preview follows the editor: a parameter, a value of its own or the
  // name changes what the device answers, so it is asked again a moment after.
  $: if (tab === 'data' && editing) schedulePreview(editing, previewFilter, previewDynamicOnly);

  function openTab(id) {
    if (id === 'data' && tab !== 'data') {
      previewOpenedAt = Date.now();
      preview = null;
    }
    tab = id;
  }

  /** The column being widened: which, where the drag started, and how wide it was. */
  let resizing = null;

  function startColumnResize(event, col) {
    resizing = { col, x: event.clientX, width: previewWidths[col] };
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
  }

  function onWindowMouseMove(event) {
    if (!resizing) return;
    previewWidths = { ...previewWidths, [resizing.col]: Math.min(800, Math.max(60, resizing.width + event.clientX - resizing.x)) };
  }

  function onWindowMouseUp() {
    if (!resizing) return;
    resizing = null;
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
  }

  // The same bounds from the keyboard, as the MIB panel's handle: the arrows
  // step, Shift steps further.
  function resizeColumnKey(event, col) {
    const step = event.shiftKey ? 50 : 10;
    const delta = { ArrowLeft: -step, ArrowRight: step }[event.key];
    if (!delta) return;
    event.preventDefault();
    previewWidths = { ...previewWidths, [col]: Math.min(800, Math.max(60, previewWidths[col] + delta)) };
  }

  function schedulePreview() {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(runPreview, 400);
  }

  // Answers overlap while someone types, and nothing orders them: only the
  // last one asked is shown.
  async function runPreview() {
    if (!editing) return;
    const seq = ++previewSeq;
    previewBusy = true;
    try {
      const page = await simulatorStore.preview(previewPayload(editing),
        previewQuery(previewFilter, previewDynamicOnly, previewOpenedAt));
      if (seq === previewSeq) {
        preview = page;
        previewError = '';
      }
    } catch (e) {
      if (seq === previewSeq) previewError = String(e);
    } finally {
      if (seq === previewSeq) previewBusy = false;
    }
  }

  /** The editor's SNMPv3 problems, one set per user, in the shape UsmFields shows them. */
  function usmMessages(p, t) {
    return (p.users || []).map((u) => {
      const out = {};
      for (const key of ['user', 'authPass', 'privPass']) {
        if (u[key]) out[key] = { text: t(`simulator.problem.${u[key]}`), level: 'error' };
      }
      return out;
    });
  }

  function close() {
    dispatch('close');
  }

  // The editor first, the dialog after: Escape backs out one level at a time.
  function onWindowKey(event) {
    if (event.key !== 'Escape') return;
    if (exportIds) {
      exportIds = null;
      return;
    }
    if (editing) {
      editing = null;
      return;
    }
    close();
  }

  /** The name a new device was given, which follows its model until someone types another. */
  let defaultName = '';

  function nameFor(id, devices) {
    return `${modelName(modelOf($simulatorStore.models, id), $_, id)} ${devices.length + 1}`;
  }

  async function newDevice(models, devices) {
    const model = models[0]?.id || 'linux-server';
    const suggestion = await simulatorStore.suggestAddress().catch(() => null);
    editing = blankDevice(model, suggestion);
    defaultName = nameFor(model, devices);
    editing.name = defaultName;
    saveError = '';
    tab = 'identity';
  }

  // A new device's model. Its name follows the model until someone types
  // another, and a schedule naming a notification the new model does not send
  // falls back to one at random, rather than being refused at save.
  function onModel(id) {
    if (!editing || editing.id) return;
    const followsModel = editing.name === defaultName;
    editing.model = id;
    if (followsModel) {
      defaultName = nameFor(id, $simulatorStore.devices);
      editing.name = defaultName;
    }
    const names = notificationsOf($simulatorStore.models, id).map((n) => n.name);
    editing.traps.schedules = editing.traps.schedules.map((s) =>
      (s.notification && !names.includes(s.notification) ? { ...s, notification: '' } : s));
    // A number is its model's to take: another model's does not follow.
    editing.params = {};
    editing = editing;
  }

  let importingModels = false;

  // Each model imported, updated or refused is reported, and the first one
  // imported becomes the new device's model: importing one is how someone says
  // they want it.
  async function importModels() {
    importingModels = true;
    try {
      const results = await simulatorStore.importModels();
      for (const line of importReport(results)) {
        notificationStore.add($_(line.key, { values: line.values }), line.level);
      }
      await simulatorStore.refreshModels();
      const first = firstImported(results);
      if (first) onModel(first);
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      importingModels = false;
    }
  }

  let recordOpen = false;
  let stopRequested = false;

  // A device recorded becomes a model as one imported does, and the new
  // device's model: recording one is how someone says they want it.
  async function recordDevice({ target, name, vendor, category }) {
    stopRequested = false;
    const shown = $anonMode ? maskString(target) : target;
    const description = $_('simulator.record.description', { values: { target, date: new Date().toLocaleDateString() } });
    try {
      const result = await simulatorStore.record(recordRequest($settingsStore, target, { name, vendor, category, description }));
      for (const line of importReport([result])) {
        notificationStore.add($_(line.key, { values: line.values }), line.level);
      }
      await simulatorStore.refreshModels();
      if (result.success) {
        recordOpen = false;
        onModel(result.modelId);
      }
    } catch (e) {
      if (stopRequested) notificationStore.add($_('simulator.record.stopped'), 'info');
      else notificationStore.add($_('simulator.record.failed', { values: { target: shown, error: String(e) } }), 'error');
    }
  }

  function stopRecording() {
    stopRequested = true;
    simulatorStore.stopRecording().catch(() => {});
  }

  async function exportModel(id) {
    const name = modelName(modelOf($simulatorStore.models, id), $_, id);
    try {
      const path = await simulatorStore.exportModel(id);
      if (path) notificationStore.add($_('simulator.models.exported', { values: { name, path } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function deleteModel(id) {
    const name = modelName(modelOf($simulatorStore.models, id), $_, id);
    try {
      await simulatorStore.removeModel(id);
      await simulatorStore.refreshModels();
      notificationStore.add($_('simulator.models.deleted', { values: { name } }), 'success');
      if (editing && editing.model === id) onModel($simulatorStore.models[0]?.id || 'linux-server');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function edit(device) {
    try {
      editing = editableDevice(device, await simulatorStore.credentials(device.id));
      saveError = '';
      tab = 'identity';
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function toggleVersion(version) {
    const versions = editing.versions.includes(version)
      ? editing.versions.filter((v) => v !== version)
      : [...editing.versions, version];
    editing = { ...editing, versions };
  }

  function addUser() {
    editing = { ...editing, users: [...editing.users, blankV3(editing.users.length + 1)] };
  }

  function removeUser(index) {
    editing = { ...editing, users: editing.users.filter((u, i) => i !== index) };
  }

  /** The device's SNMPv3 user names, which a v3 notification is sent as. */
  function userNames(users) {
    return users.map((u) => (u.user || '').trim()).filter(Boolean);
  }

  function setTraps(traps) {
    editing = { ...editing, traps: { ...editing.traps, ...traps } };
  }

  function addDestination() {
    setTraps({ destinations: [...editing.traps.destinations, blankDestination($settingsStore.trapPort)] });
  }

  function removeDestination(index) {
    setTraps({ destinations: editing.traps.destinations.filter((d, i) => i !== index) });
  }

  // v3 is sent as one of the device's users — the first, until another is
  // chosen — and v1 has no INFORM.
  function onDestinationVersion(dest) {
    if (dest.version === 'v3' && !dest.user) dest.user = userNames(editing.users)[0] || '';
    if (dest.version === 'v1') dest.inform = false;
    editing = editing;
  }

  async function useListenerEngine(dest) {
    try {
      dest.engineId = await simulatorStore.listenerEngineId();
      editing = editing;
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  function addSchedule() {
    setTraps({ schedules: [...editing.traps.schedules, blankSchedule()] });
  }

  function removeSchedule(index) {
    setTraps({ schedules: editing.traps.schedules.filter((s, i) => i !== index) });
  }

  function addOverride() {
    editing = { ...editing, overrides: [...editing.overrides, blankOverride()] };
  }

  function removeOverride(index) {
    editing = { ...editing, overrides: editing.overrides.filter((o, i) => i !== index) };
  }

  async function save() {
    saving = true;
    saveError = '';
    try {
      await simulatorStore.save(devicePayload(editing));
      editing = null;
      await simulatorStore.refresh();
    } catch (e) {
      saveError = String(e);
    } finally {
      saving = false;
    }
  }

  async function run(device, action) {
    busy = { ...busy, [device.id]: true };
    try {
      await simulatorStore[action](device.id);
    } catch (e) {
      notificationStore.add($_('simulator.startFailed', { values: { name: device.name, error: String(e) } }), 'error');
    } finally {
      busy = { ...busy, [device.id]: false };
      simulatorStore.refresh().catch(() => {});
    }
  }

  async function remove(device) {
    if (confirmDelete !== device.id) {
      confirmDelete = device.id;
      return;
    }
    confirmDelete = null;
    try {
      await simulatorStore.remove(device.id);
      await simulatorStore.refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  async function addAsTarget(device) {
    try {
      const creds = await simulatorStore.credentials(device.id);
      const { settings, added, version, target } = addDeviceAsTarget($settingsStore, device, creds);
      await settingsStore.save(settings);
      notificationStore.add(
        $_(added ? 'simulator.targetAdded' : 'simulator.targetUpdated', { values: { address: target, version } }),
        'success',
      );
    } catch (e) {
      notificationStore.add(String(e), 'error');
    }
  }

  // The menu is a select that goes back to its prompt once used: a notification
  // is an action, not a setting.
  async function sendTrap(device, event) {
    const name = event.currentTarget.value;
    event.currentTarget.value = '';
    if (!name) return;
    busy = { ...busy, [device.id]: true };
    try {
      const deliveries = await simulatorStore.sendTrap(device.id, name);
      for (const line of deliveryReport(name, deliveries)) {
        const values = $anonMode && line.values.destination
          ? { ...line.values, destination: maskString(line.values.destination) }
          : line.values;
        notificationStore.add($_(line.key, { values }), line.level);
      }
    } catch (e) {
      notificationStore.add($_('simulator.traps.sendFailed', { values: { name, error: String(e) } }), 'error');
    } finally {
      busy = { ...busy, [device.id]: false };
      simulatorStore.refresh().catch(() => {});
    }
  }

  /** The devices whose export is being asked about — every one when empty — or null. */
  let exportIds = null;
  let benchBusy = false;

  // What an export holds is asked every time: the passwords stay in the
  // keychain unless the operator says to put them in the file.
  function askExport(ids) {
    exportIds = ids;
  }

  async function exportDevices(ids, withSecrets) {
    exportIds = null;
    benchBusy = true;
    try {
      const path = await simulatorStore.exportDevices(ids, withSecrets);
      if (path) notificationStore.add($_('simulator.export.done', { values: { path } }), 'success');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      benchBusy = false;
    }
  }

  async function importDevices() {
    benchBusy = true;
    try {
      const results = await simulatorStore.importDevices();
      for (const line of deviceImportReport(results)) {
        notificationStore.add($_(line.key, { values: line.values }), line.level);
      }
      await simulatorStore.refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      benchBusy = false;
    }
  }

  async function duplicate(device) {
    busy = { ...busy, [device.id]: true };
    try {
      const copy = await simulatorStore.duplicate(device.id, $_('simulator.copyName', { values: { name: device.name } }));
      notificationStore.add($_('simulator.duplicated', { values: { name: device.name, copy: copy.name } }), 'success');
      await simulatorStore.refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      busy = { ...busy, [device.id]: false };
    }
  }

  async function restart(device) {
    busy = { ...busy, [device.id]: true };
    try {
      await simulatorStore.restart(device.id);
      notificationStore.add($_('simulator.restarted', { values: { name: device.name } }), 'success');
    } catch (e) {
      notificationStore.add($_('simulator.startFailed', { values: { name: device.name, error: String(e) } }), 'error');
    } finally {
      busy = { ...busy, [device.id]: false };
      simulatorStore.refresh().catch(() => {});
    }
  }

  /** The device whose faults are open, or null. */
  let faultsOpen = null;

  // Applied at once, without a restart: the device keeps its uptime and its
  // counters, which are what a fault is tested against.
  async function applyFaults(device, faults) {
    busy = { ...busy, [device.id]: true };
    try {
      await simulatorStore.setFaults(device.id, faults);
      const key = faultChips(faults).length ? 'simulator.faults.applied' : 'simulator.faults.cleared';
      notificationStore.add($_(key, { values: { name: device.name } }), 'success');
      await simulatorStore.refresh();
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      busy = { ...busy, [device.id]: false };
    }
  }
</script>

<svelte:window on:keydown={onWindowKey} on:mousemove={onWindowMouseMove} on:mouseup={onWindowMouseUp} />

<div class="sim-backdrop" on:mousedown={onBackdrop(close)} role="presentation">
  <div class="sim-modal" role="dialog" aria-modal="true" aria-labelledby="sim-title" tabindex="-1">
    <header class="sim-header">
      <h2 id="sim-title"><Icon name="server-cog" size={20} /> {$_('simulator.title')}</h2>
      <button class="sim-close" on:click={close} aria-label={$_('common.close')}>&times;</button>
    </header>

    {#if editing}
      <div class="sim-body">
        <h3 class="editor-title">{editing.id ? $_('simulator.editTitle') : $_('simulator.newTitle')}</h3>
        <div class="tabs" role="tablist">
          {#each EDITOR_TABS as t (t.id)}
            <button type="button" role="tab" id="sim-tab-{t.id}" class="tab" class:active={tab === t.id}
              aria-selected={tab === t.id} aria-controls="sim-tab-panel" on:click={() => openTab(t.id)}>
              {$_(`simulator.tab.${t.id}`)}
              {#if flagged.includes(t.id)}
                <span class="tab-flag" title={$_('simulator.tabProblem')} aria-label={$_('simulator.tabProblem')}></span>
              {/if}
            </button>
          {/each}
        </div>
        <div id="sim-tab-panel" role="tabpanel" aria-labelledby="sim-tab-{tab}">
        {#if tab === 'identity'}
        {#if editing.id}
          <!-- A device keeps its model: its engine ID carries the model's vendor. -->
          {@const model = modelOf($simulatorStore.models, editing.model)}
          <div class="model-card">
            <ModelIcon category={model?.category || 'other'} icon={$simulatorStore.icons[editing.model] || ''} size={44} />
            <div class="model-card-text">
              <span class="model-card-name">{modelName(model, $_, editing.model)}</span>
              <span class="model-card-description">{modelDescription(model, $_)}</span>
            </div>
          </div>
        {:else}
          <h4 class="section-title first">{$_('simulator.field.model')}</h4>
          <ModelPicker models={$simulatorStore.models} icons={$simulatorStore.icons} devices={$simulatorStore.devices}
            value={editing.model} busy={importingModels || !!$simulatorStore.recording}
            targets={recordableTargets($settingsStore)} recording={$simulatorStore.recording} bind:recordOpen
            on:change={(e) => onModel(e.detail)} on:import={importModels} on:delete={(e) => deleteModel(e.detail)}
            on:record={(e) => recordDevice(e.detail)} on:stop={stopRecording} on:export={(e) => exportModel(e.detail)} />
        {/if}
        <div class="grid endpoint">
          <div class="form-group name-field">
            <label for="sim-name">{$_('simulator.field.name')}</label>
            <input id="sim-name" type="text" maxlength="64" spellcheck="false" bind:value={editing.name} />
            {#if problems.name}<span class="problem">{$_(`simulator.problem.${problems.name}`)}</span>{/if}
          </div>
          <div class="form-group">
            <label for="sim-address">{$_('simulator.field.address')}</label>
            <input id="sim-address" type="text" spellcheck="false" bind:value={editing.address} />
          </div>
          <div class="form-group">
            <label for="sim-port">{$_('simulator.field.port')}</label>
            <input id="sim-port" type="number" min="1" max="65535" bind:value={editing.port} />
            {#if problems.port}<span class="problem">{$_(`simulator.problem.${problems.port}`)}</span>{/if}
          </div>
        </div>
        <p class="hint"><Icon name="shield-check" size={14} /> {$_('simulator.loopbackHint')}</p>
        <div class="grid identity-fields">
          <div class="form-group">
            <label for="sim-location">{$_('simulator.field.location')}</label>
            <input id="sim-location" type="text" maxlength="255" placeholder={$_('simulator.field.modelDefault')}
              bind:value={editing.location} />
          </div>
          <div class="form-group">
            <label for="sim-contact">{$_('simulator.field.contact')}</label>
            <input id="sim-contact" type="text" maxlength="255" placeholder={$_('simulator.field.modelDefault')}
              bind:value={editing.contact} />
          </div>
        </div>
        <label class="check autostart"><input type="checkbox" bind:checked={editing.autoStart} /> {$_('simulator.field.autoStart')}</label>
        {#if editing.id && editing.engineId}
          <div class="engine-line">
            <span class="engine-label">{$_('simulator.field.engineId')}</span>
            <code>{editing.engineId}</code>
            <span class="engine-boots">{$_('simulator.engineBoots', { values: { count: editing.engineBoots } })}</span>
          </div>
          <p class="hint"><Icon name="key-round" size={14} /> {$_('simulator.engineHint')}</p>
        {/if}
        {/if}

        {#if tab === 'access'}
        <fieldset class="versions">
          <legend>{$_('simulator.field.versions')}</legend>
          {#each SIM_VERSIONS as version (version)}
            <label class="check">
              <input type="checkbox" checked={editing.versions.includes(version)} on:change={() => toggleVersion(version)} />
              {version}
            </label>
          {/each}
          {#if problems.versions}<span class="problem">{$_(`simulator.problem.${problems.versions}`)}</span>{/if}
        </fieldset>

        {#if editing.versions.includes('v1') || editing.versions.includes('v2c')}
          <div class="grid communities">
            <div class="form-group">
              <label for="sim-community">{$_('simulator.field.community')}</label>
              <SecretInput id="sim-community" bind:value={editing.community} />
              {#if problems.community}<span class="problem">{$_(`simulator.problem.${problems.community}`)}</span>{/if}
            </div>
            <div class="form-group">
              <label for="sim-write-community">{$_('simulator.field.writeCommunity')}</label>
              <SecretInput id="sim-write-community" placeholder={$_('simulator.field.writeCommunityNone')}
                bind:value={editing.writeCommunity} />
              {#if problems.writeCommunity}<span class="problem">{$_(`simulator.problem.${problems.writeCommunity}`)}</span>{/if}
            </div>
          </div>
        {/if}

        {#if editing.versions.includes('v3')}
          <h4 class="section-title">{$_('simulator.field.v3')}</h4>
          {#each editing.users as u, i (i)}
            <div class="user-block">
              <div class="user-head">
                <span class="user-title">{$_('simulator.userN', { values: { n: i + 1 } })}</span>
                {#if editing.users.length > 1}
                  <button class="icon-btn danger" title={$_('simulator.removeUser')} aria-label={$_('simulator.removeUser')}
                    on:click={() => removeUser(i)}>
                    <Icon name="trash-2" size={14} />
                  </button>
                {/if}
              </div>
              <!-- bind:, as the settings do: UsmFields edits the object in place, and
                   without it the checks above never see a passphrase being typed. -->
              <UsmFields bind:v3={u} idPrefix="sim-v3-{i}" problems={usmProblems[i] || {}} showContext={false} revealable />
              <label class="check user-write"><input type="checkbox" bind:checked={u.write} /> {$_('simulator.field.userWrite')}</label>
            </div>
          {/each}
          {#if problems.noUser}<span class="problem">{$_(`simulator.problem.${problems.noUser}`)}</span>{/if}
          <div class="users-foot">
            <button class="btn tertiary btn-small" on:click={addUser}>
              <Icon name="plus" size={13} /> {$_('simulator.addUser')}
            </button>
            {#if editing.users.length > 1}<span class="users-note">{$_('simulator.firstUserTarget')}</span>{/if}
          </div>
        {/if}
        <p class="hint"><Icon name="pencil" size={14} /> {$_('simulator.writeHint')}</p>
        {/if}

        {#if tab === 'traps'}
        <p class="hint first"><Icon name="radio" size={14} /> {$_('simulator.traps.hint')}</p>
        {#each editing.traps.destinations as dest, i (i)}
          <div class="user-block">
            <div class="user-head">
              <span class="user-title">{$_('simulator.traps.destinationN', { values: { n: i + 1 } })}</span>
              <button class="icon-btn danger" title={$_('simulator.traps.removeDestination')}
                aria-label={$_('simulator.traps.removeDestination')} on:click={() => removeDestination(i)}>
                <Icon name="trash-2" size={14} />
              </button>
            </div>
            <div class="dest-grid">
              <div class="form-group">
                <label for="sim-dest-{i}-host">{$_('simulator.traps.host')}</label>
                <input id="sim-dest-{i}-host" type="text" spellcheck="false" bind:value={dest.host} />
                {#if problems.destinations?.[i]?.host}<span class="problem">{$_(`simulator.problem.${problems.destinations[i].host}`)}</span>{/if}
              </div>
              <div class="form-group">
                <label for="sim-dest-{i}-port">{$_('simulator.traps.port')}</label>
                <input id="sim-dest-{i}-port" type="number" min="1" max="65535" bind:value={dest.port} />
                {#if problems.destinations?.[i]?.port}<span class="problem">{$_(`simulator.problem.${problems.destinations[i].port}`)}</span>{/if}
              </div>
              <div class="form-group">
                <label for="sim-dest-{i}-version">{$_('simulator.traps.version')}</label>
                <select id="sim-dest-{i}-version" bind:value={dest.version} on:change={() => onDestinationVersion(dest)}>
                  {#each SIM_VERSIONS as version (version)}<option value={version}>{version}</option>{/each}
                </select>
              </div>
              {#if dest.version === 'v3'}
                <div class="form-group">
                  <label for="sim-dest-{i}-user">{$_('simulator.traps.user')}</label>
                  <select id="sim-dest-{i}-user" bind:value={dest.user}>
                    {#each userNames(editing.users) as userName (userName)}<option value={userName}>{userName}</option>{/each}
                  </select>
                  {#if problems.destinations?.[i]?.user}<span class="problem">{$_(`simulator.problem.${problems.destinations[i].user}`)}</span>{/if}
                </div>
              {:else}
                <div class="form-group">
                  <label for="sim-dest-{i}-community">{$_('simulator.traps.community')}</label>
                  <SecretInput id="sim-dest-{i}-community" bind:value={dest.community} />
                  {#if problems.destinations?.[i]?.community}<span class="problem">{$_(`simulator.problem.${problems.destinations[i].community}`)}</span>{/if}
                </div>
              {/if}
            </div>
            <label class="check inform">
              <input type="checkbox" bind:checked={dest.inform} disabled={dest.version === 'v1'} />
              {$_('simulator.traps.inform')}
            </label>
            {#if dest.version === 'v3' && dest.inform}
              <div class="form-group engine">
                <label for="sim-dest-{i}-engine">{$_('simulator.traps.engineId')}</label>
                <div class="engine-row">
                  <input id="sim-dest-{i}-engine" type="text" spellcheck="false"
                    placeholder={$_('simulator.traps.engineIdPlaceholder')} bind:value={dest.engineId} />
                  <button class="btn tertiary btn-small" on:click={() => useListenerEngine(dest)}>
                    {$_('simulator.traps.useListener')}
                  </button>
                </div>
                <span class="field-hint">{$_('simulator.traps.engineIdHint')}</span>
                {#if problems.destinations?.[i]?.engineId}<span class="problem">{$_(`simulator.problem.${problems.destinations[i].engineId}`)}</span>{/if}
              </div>
            {/if}
          </div>
        {/each}
        <div class="users-foot">
          <button class="btn tertiary btn-small" disabled={editing.traps.destinations.length >= MAX_DESTINATIONS}
            on:click={addDestination}>
            <Icon name="plus" size={13} /> {$_('simulator.traps.addDestination')}
          </button>
          <span class="users-note">{$_('simulator.traps.newDestinationNote')}</span>
        </div>

        {#if editing.traps.destinations.length}
          <div class="triggers">
            <label class="check"><input type="checkbox" bind:checked={editing.traps.onStart} /> {$_('simulator.traps.onStart')}</label>
            <label class="check"><input type="checkbox" bind:checked={editing.traps.onAuthFailure} /> {$_('simulator.traps.onAuthFailure')}</label>
          </div>
          {#each editing.traps.schedules as s, i (i)}
            <div class="schedule">
              <select aria-label={$_('simulator.traps.notification')} bind:value={s.notification}>
                <option value="">{$_('simulator.traps.any')}</option>
                {#each notificationsOf($simulatorStore.models, editing.model) as n (n.name)}
                  <option value={n.name}>{n.name}</option>
                {/each}
              </select>
              <span>{$_('simulator.traps.every')}</span>
              <input type="number" min="1" max={MAX_EVERY} aria-label={$_('simulator.traps.seconds')} bind:value={s.every} />
              <span>{$_('simulator.traps.seconds')}</span>
              <label class="check"><input type="checkbox" bind:checked={s.irregular} /> {$_('simulator.traps.irregular')}</label>
              <button class="icon-btn danger" title={$_('simulator.traps.removeSchedule')}
                aria-label={$_('simulator.traps.removeSchedule')} on:click={() => removeSchedule(i)}>
                <Icon name="trash-2" size={14} />
              </button>
              {#if problems.schedules?.[i]?.every}<span class="problem">{$_('simulator.problem.every')}</span>{/if}
            </div>
          {/each}
          <div class="users-foot">
            <button class="btn tertiary btn-small" disabled={editing.traps.schedules.length >= MAX_SCHEDULES} on:click={addSchedule}>
              <Icon name="plus" size={13} /> {$_('simulator.traps.addSchedule')}
            </button>
          </div>
        {/if}
        {/if}

        {#if tab === 'data'}
        {#if editedModel?.params?.length}
          <h4 class="section-title first">{$_('simulator.data.params')}</h4>
          <div class="grid params">
            {#each editedModel.params as p (p.name)}
              <div class="form-group">
                <label for="sim-param-{p.name}">{$_(`simulator.param.${p.name}`)}</label>
                <input id="sim-param-{p.name}" type="number" min={p.min} max={p.max} placeholder={String(p.default)}
                  bind:value={editing.params[p.name]} />
                <span class="field-hint">{$_('simulator.data.range', { values: { min: p.min, max: p.max, default: p.default } })}</span>
                {#if problems.params?.[p.name]}
                  <span class="problem">{$_(`simulator.problem.${problems.params[p.name]}`, { values: { min: p.min, max: p.max } })}</span>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
        <h4 class="section-title" class:first={!editedModel?.params?.length}>{$_('simulator.data.overrides')}</h4>
        <p class="hint first"><Icon name="pencil" size={14} /> {$_('simulator.data.overridesHint')}</p>
        {#each editing.overrides as ov, i (i)}
          <div class="override">
            <input class="ov-oid" type="text" spellcheck="false" aria-label={$_('simulator.data.oid')}
              placeholder="1.3.6.1.2.1.1.1.0" bind:value={ov.oid} />
            <select aria-label={$_('simulator.data.type')} bind:value={ov.type}>
              {#each OVERRIDE_TYPES as t (t)}<option value={t}>{t}</option>{/each}
            </select>
            <input class="ov-value" type="text" spellcheck="false" aria-label={$_('simulator.data.value')} bind:value={ov.value} />
            {#if ov.type === 'OctetString'}
              <label class="check" title={$_('simulator.data.hexTitle')}><input type="checkbox" bind:checked={ov.hex} /> hex</label>
            {/if}
            <button class="icon-btn danger" title={$_('simulator.data.removeOverride')} aria-label={$_('simulator.data.removeOverride')}
              on:click={() => removeOverride(i)}>
              <Icon name="trash-2" size={14} />
            </button>
            {#if problems.overrides?.[i]?.oid}<span class="problem">{$_(`simulator.problem.${problems.overrides[i].oid}`)}</span>{/if}
            {#if problems.overrides?.[i]?.value}<span class="problem">{$_(`simulator.problem.${problems.overrides[i].value}`)}</span>{/if}
          </div>
        {/each}
        <div class="users-foot">
          <button class="btn tertiary btn-small" disabled={editing.overrides.length >= MAX_OVERRIDES} on:click={addOverride}>
            <Icon name="plus" size={13} /> {$_('simulator.data.addOverride')}
          </button>
        </div>

        <h4 class="section-title">{$_('simulator.data.preview')}</h4>
        <p class="hint first">{$_('simulator.data.previewHint')}</p>
        <div class="preview-bar">
          <input type="text" spellcheck="false" aria-label={$_('simulator.data.filter')}
            placeholder={$_('simulator.data.filterPlaceholder')} bind:value={previewFilter} />
          <label class="check"><input type="checkbox" bind:checked={previewDynamicOnly} /> {$_('simulator.data.dynamicOnly')}</label>
          <button class="icon-btn" disabled={previewBusy} title={$_('simulator.data.refresh')} aria-label={$_('simulator.data.refresh')}
            on:click={runPreview}>
            <Icon name="refresh-cw" size={14} />
          </button>
          {#if preview}
            <span class="preview-count">
              {$_('simulator.data.previewCount', { values: { shown: preview.rows.length, total: preview.total } })}
            </span>
          {/if}
        </div>
        {#if previewError}<span class="problem">{previewError}</span>{/if}
        {#if preview?.rows.length}
          <div class="preview-wrap">
            <table class="preview" style:width="{previewTableWidth}px">
              <colgroup>
                {#each PREVIEW_COLUMNS as c (c)}<col style:width="{previewWidths[c]}px" />{/each}
              </colgroup>
              <thead>
                <tr>
                  {#each PREVIEW_COLUMNS as c (c)}
                    <th scope="col">
                      {$_(`simulator.data.column.${c}`)}
                      <span class="col-resize" role="slider" tabindex="0" aria-orientation="horizontal"
                        aria-valuemin="60" aria-valuemax="800" aria-valuenow={previewWidths[c]}
                        aria-label={$_('simulator.data.resizeColumn', { values: { column: $_(`simulator.data.column.${c}`) } })}
                        on:mousedown={(e) => startColumnResize(e, c)} on:keydown={(e) => resizeColumnKey(e, c)}></span>
                    </th>
                  {/each}
                </tr>
              </thead>
              <tbody>
                {#each preview.rows as r (r.oid)}
                  <tr class:moving={r.behaviour !== 'static'}>
                    <td title={r.oid}><code>{r.oid}</code></td>
                    <td title={r.name}>{r.name}</td>
                    <td>{r.type}</td>
                    <td><span class="behaviour {r.behaviour}">{$_(`simulator.data.behaviour.${r.behaviour}`)}</span></td>
                    <td class="value" title={r.value}>{r.value}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else if preview}
          <p class="hint">{$_('simulator.data.previewEmpty')}</p>
        {/if}
        {/if}

        {#if tab === 'faults'}
        {#if listed}
          <FaultsPanel faults={listed.faults} idPrefix="sim-edit-faults" busy={busy[listed.id]}
            on:apply={(e) => applyFaults(listed, e.detail)} on:close={() => (tab = 'identity')} />
        {:else}
          <p class="hint first"><Icon name="zap" size={14} /> {$_('simulator.faults.saveFirst')}</p>
        {/if}
        {/if}
        </div>
      </div>
      <footer class="sim-footer">
        {#if saveError}<span class="save-error">{saveError}</span>{/if}
        <button class="btn secondary" on:click={() => (editing = null)}>{$_('common.cancel')}</button>
        <button class="btn" disabled={saving || Object.keys(problems).length > 0} on:click={save}>
          {$_('common.save')}
        </button>
      </footer>
    {:else}
      <div class="sim-body">
        {#if exportIds}
          <div class="export-choice" role="alertdialog" aria-labelledby="sim-export-title" aria-describedby="sim-export-hint">
            <h3 id="sim-export-title">
              <Icon name="download" size={16} />
              {$_('simulator.export.title', { values: { count: exportIds.length || $simulatorStore.devices.length } })}
            </h3>
            <p id="sim-export-hint"><Icon name="key-round" size={14} /> {$_('simulator.export.hint')}</p>
            <div class="export-actions">
              <button class="btn secondary btn-small" on:click={() => (exportIds = null)}>{$_('common.cancel')}</button>
              <button class="btn secondary btn-small warn" on:click={() => exportDevices(exportIds, true)}>
                <Icon name="triangle-alert" size={13} /> {$_('simulator.export.with')}
              </button>
              <button class="btn btn-small" on:click={() => exportDevices(exportIds, false)}>{$_('simulator.export.without')}</button>
            </div>
          </div>
        {/if}
        <p class="intro">{$_('simulator.intro')}</p>
        {#if $simulatorStore.devices.length === 0}
          <div class="empty">
            <Icon name="server-cog" size={32} strokeWidth={1.5} />
            <p>{$_('simulator.empty')}</p>
          </div>
        {:else}
          {#each deviceGroups($simulatorStore.devices, $simulatorStore.models) as group (group.category)}
          <h4 class="category-title">
            <Icon name={categoryIcon(group.category)} size={14} /> {$_(`simulator.category.${group.category}`)}
          </h4>
          <ul class="devices">
            {#each group.devices as d (d.id)}
              {@const activity = trapActivity(d.traps)}
              {@const model = modelOf($simulatorStore.models, d.model)}
              <li class="device" class:running={d.running}>
                <span class="state" title={d.running ? $_('simulator.running') : $_('simulator.stopped')}></span>
                <ModelIcon category={model?.category || 'other'} icon={$simulatorStore.icons[d.model] || ''} size={30} />
                <div class="identity">
                  <span class="name">{d.name}</span>
                  <span class="meta">
                    {modelName(model, $_, d.model)} · <code>{d.address}:{d.port}</code>
                  </span>
                  <span class="meta">
                    {#each d.versions as version (version)}<span class="chip">{version}</span>{/each}
                    {#if d.users?.length}
                      <span class="users">
                        <Icon name="key-round" size={12} />
                        {$anonMode ? maskString(d.users[0].name) : d.users.map((u) => u.name).join(', ')}
                      </span>
                    {/if}
                    <span class="activity">
                      {d.running ? $_('simulator.packets', { values: { count: d.packets } }) : $_('simulator.stopped')}
                    </span>
                    {#if d.running && d.traps?.destinations?.length}
                      <span class="traps-activity" title={activity.lastError}>
                        <Icon name="radio" size={12} />
                        {$_('simulator.traps.activity', { values: { sent: activity.sent } })}
                        {#if activity.failed}
                          · <span class="failed">{$_('simulator.traps.activityFailed', { values: { failed: activity.failed } })}</span>
                        {/if}
                      </span>
                    {/if}
                    {#each faultChips(d.faults) as c (c.key)}
                      <span class="chip fault">{$_(`simulator.faults.chip.${c.key}`, { values: c.values })}</span>
                    {/each}
                    {#if d.autoStart}<span class="chip">{$_('simulator.autoStartChip')}</span>{/if}
                  </span>
                </div>
                <div class="actions">
                  {#if d.running}
                    <button class="btn secondary btn-small" disabled={busy[d.id]} on:click={() => run(d, 'stop')}>
                      <Icon name="square" size={13} /> {$_('common.stop')}
                    </button>
                  {:else}
                    <button class="btn btn-small" disabled={busy[d.id]} on:click={() => run(d, 'start')}>
                      <Icon name="play" size={13} /> {$_('common.start')}
                    </button>
                  {/if}
                  {#if d.running && d.traps?.destinations?.length}
                    <select class="send-trap" title={$_('simulator.traps.sendTitle')} aria-label={$_('simulator.traps.sendTitle')}
                      disabled={busy[d.id]} on:change={(e) => sendTrap(d, e)}>
                      <option value="">{$_('simulator.traps.send')}</option>
                      {#each notificationsOf($simulatorStore.models, d.model) as n (n.name)}
                        <option value={n.name}>{n.name}</option>
                      {/each}
                    </select>
                  {/if}
                  <button class="btn tertiary btn-small" title={$_('simulator.addAsTargetTitle')} on:click={() => addAsTarget(d)}>
                    <Icon name="target" size={13} /> {$_('simulator.addAsTarget')}
                  </button>
                  <button class="icon-btn push" class:active={faultChips(d.faults).length > 0} aria-expanded={faultsOpen === d.id}
                    title={$_('simulator.faults.button')} aria-label={$_('simulator.faults.button')}
                    on:click={() => (faultsOpen = faultsOpen === d.id ? null : d.id)}>
                    <Icon name="zap" size={14} />
                  </button>
                  {#if d.running}
                    <button class="icon-btn" disabled={busy[d.id]} title={$_('simulator.restart')} aria-label={$_('simulator.restart')}
                      on:click={() => restart(d)}>
                      <Icon name="refresh-cw" size={14} />
                    </button>
                  {/if}
                  <button class="icon-btn" disabled={busy[d.id]} title={$_('simulator.duplicate')} aria-label={$_('simulator.duplicate')}
                    on:click={() => duplicate(d)}>
                    <Icon name="copy" size={14} />
                  </button>
                  <button class="icon-btn" title={$_('simulator.exportDevice')} aria-label={$_('simulator.exportDevice')}
                    on:click={() => askExport([d.id])}>
                    <Icon name="download" size={14} />
                  </button>
                  <button class="icon-btn" title={$_('common.edit')} aria-label={$_('common.edit')} on:click={() => edit(d)}>
                    <Icon name="pencil" size={14} />
                  </button>
                  <button class="icon-btn danger" class:confirming={confirmDelete === d.id}
                    title={$_('simulator.deleteTitle')} aria-label={$_('simulator.deleteTitle')} on:click={() => remove(d)}>
                    {#if confirmDelete === d.id}{$_('simulator.confirmDelete')}{:else}<Icon name="trash-2" size={14} />{/if}
                  </button>
                </div>
                {#if faultsOpen === d.id}
                  <FaultsPanel faults={d.faults} idPrefix="sim-faults-{d.id}" busy={busy[d.id]}
                    on:apply={(e) => applyFaults(d, e.detail)} on:close={() => (faultsOpen = null)} />
                {/if}
              </li>
            {/each}
          </ul>
          {/each}
          {#if $simulatorStore.devices.length > 1}
            <CablingPanel devices={$simulatorStore.devices} links={$simulatorStore.links} />
          {/if}
        {/if}
      </div>
      <footer class="sim-footer">
        <button class="btn tertiary" disabled={benchBusy} title={$_('simulator.importDevicesTitle')} on:click={importDevices}>
          <Icon name="upload" size={14} /> {$_('simulator.importDevices')}
        </button>
        <button class="btn tertiary" disabled={benchBusy || $simulatorStore.devices.length === 0}
          title={$_('simulator.exportAllTitle')} on:click={() => askExport([])}>
          <Icon name="download" size={14} /> {$_('simulator.exportAll')}
        </button>
        <span class="footer-gap"></span>
        <button class="btn" on:click={() => newDevice($simulatorStore.models, $simulatorStore.devices)}>
          <Icon name="plus" size={14} /> {$_('simulator.new')}
        </button>
      </footer>
    {/if}
  </div>
</div>

<style>
  .sim-backdrop {
    position: fixed;
    inset: 0;
    background-color: var(--backdrop-color);
    display: flex;
    justify-content: center;
    align-items: center;
    z-index: 100;
  }

  .sim-modal {
    background-color: var(--bg-light-color);
    border-radius: 8px;
    width: 92%;
    max-width: 760px;
    max-height: 85vh;
    box-shadow: 0 5px 15px var(--shadow-color-strong);
    display: flex;
    flex-direction: column;
  }

  .sim-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 15px 20px;
    border-bottom: 1px solid var(--border-color);
  }

  .sim-header h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 1.2em;
  }

  .sim-close {
    background: none;
    border: none;
    color: var(--text-color);
    font-size: 1.5rem;
    cursor: pointer;
  }

  .sim-body {
    padding: 16px 20px;
    overflow-y: auto;
    flex: 1;
  }

  .sim-footer {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 10px;
    padding: 12px 20px;
    border-top: 1px solid var(--border-color);
  }

  .intro {
    margin: 0 0 14px;
    color: var(--text-muted);
    font-size: 0.9em;
    line-height: 1.45;
  }

  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 28px 0;
    color: var(--text-dimmed);
  }

  .empty p {
    margin: 0;
  }

  .devices {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  /* Two lines: what the device is and does, then what can be done with it. On
     one line the actions a running device gains squeezed its name and its
     request count into an ellipsis the moment it started. */
  .device {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    padding: 10px 12px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .state {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    flex-shrink: 0;
    background-color: var(--text-dimmed);
  }

  .device.running .state {
    background-color: var(--success-color);
    box-shadow: 0 0 0 3px var(--success-subtle-strong);
  }

  .identity {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
    flex: 1;
  }

  .name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    font-size: 0.82em;
    color: var(--text-muted);
  }

  .meta code {
    color: var(--text-color);
  }

  .chip {
    padding: 0 6px;
    border-radius: 4px;
    font-size: 0.92em;
    font-weight: 600;
    background-color: var(--hover-overlay-medium);
    color: var(--text-muted);
  }

  .activity {
    color: var(--text-dimmed);
  }

  .device.running .activity {
    color: var(--success-color);
  }

  /* A line of its own, lined up under the name: past the state dot, the
     30 px icon and the two gaps between them. */
  .actions {
    display: flex;
    flex: 1 1 100%;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    padding-left: calc(9px + 30px + 2 * 12px);
  }

  /* The buttons that act on the device itself start the right-hand group. */
  .actions .push {
    margin-left: auto;
  }

  .icon-btn:disabled {
    cursor: not-allowed;
    opacity: 0.5;
  }

  .icon-btn.active {
    color: var(--warning-color);
    border-color: var(--warning-border);
  }

  .category-title {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 14px 0 8px;
    font-size: 0.85em;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-muted);
  }

  .chip.fault {
    background-color: var(--warning-subtle);
    color: var(--warning-color);
  }

  .identity-fields {
    margin-top: 12px;
  }

  .autostart {
    margin-top: 10px;
    font-size: 0.9em;
  }

  .export-choice {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 14px;
    padding: 12px 14px;
    background-color: var(--bg-color);
    border: 1px solid var(--accent-border);
    border-radius: 6px;
  }

  .export-choice h3 {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: 0.95em;
  }

  .export-choice p {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    margin: 0;
    font-size: 0.85em;
    line-height: 1.45;
    color: var(--text-muted);
  }

  .export-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 8px;
  }

  .export-actions .btn,
  .sim-footer .btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .export-actions .warn {
    color: var(--warning-color);
    border-color: var(--warning-border);
  }

  .footer-gap {
    flex: 1;
  }

  .actions .btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    white-space: nowrap;
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

  .icon-btn:hover {
    color: var(--text-color);
    background-color: var(--hover-overlay-medium);
  }

  .icon-btn.danger:hover,
  .icon-btn.confirming {
    color: var(--error-color);
    border-color: var(--error-color);
  }

  .icon-btn.confirming {
    font-size: 0.82em;
    font-weight: 600;
  }

  .editor-title {
    margin: 0 0 12px;
    font-size: 1em;
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px 20px;
    /* A field with a message under it must not stretch its neighbour. */
    align-items: start;
  }

  /* The name and where the device answers, on one line: a port is five digits
     and needs no half of the dialog. */
  .endpoint {
    grid-template-columns: minmax(0, 3fr) minmax(0, 2fr) minmax(5.5rem, 1fr);
  }

  /* stretch, not the global .form-group's centring: labels line up on the left,
     as they do in UsmFields beside them. */
  .form-group {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    min-width: 0;
  }

  .form-group label {
    margin-bottom: 5px;
    font-size: 0.9em;
    color: var(--text-light);
  }

  .form-group input,
  .form-group select {
    width: 100%;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .form-group select:disabled {
    opacity: 0.7;
  }

  .model-card {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 14px;
    padding: 10px 12px;
    background-color: var(--bg-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .model-card-text {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }

  .model-card-name {
    font-weight: 600;
  }

  .model-card-description {
    font-size: 0.82em;
    line-height: 1.4;
    color: var(--text-muted);
  }

  .hint {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    margin: 10px 0 0;
    font-size: 0.82em;
    color: var(--text-muted);
    line-height: 1.4;
  }

  .versions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 16px;
    margin: 14px 0 0;
    padding: 8px 12px 10px;
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .versions legend {
    padding: 0 4px;
    font-size: 0.9em;
    color: var(--text-light);
  }

  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
  }

  .communities {
    margin-top: 14px;
  }

  .user-write {
    margin-top: 10px;
    font-size: 0.9em;
  }

  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: 2px;
    margin: 0 0 14px;
    border-bottom: 1px solid var(--border-color);
  }

  .tab {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin-bottom: -1px;
    padding: 7px 14px;
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    font-size: 0.9em;
    cursor: pointer;
  }

  .tab:hover {
    color: var(--text-color);
  }

  .tab.active {
    border-bottom-color: var(--accent-color);
    color: var(--text-color);
    font-weight: 600;
  }

  .tab-flag {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background-color: var(--error-color);
  }

  .hint.first {
    margin-top: 0;
  }

  .engine-line {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 8px;
    margin-top: 14px;
    font-size: 0.9em;
  }

  .engine-label {
    color: var(--text-light);
  }

  .engine-line code {
    overflow-wrap: anywhere;
  }

  .engine-boots {
    font-size: 0.92em;
    color: var(--text-muted);
  }

  .params {
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    margin-top: 10px;
  }

  .override {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    margin-top: 8px;
  }

  .override input[type='text'],
  .override select,
  .preview-bar input {
    padding: 6px 8px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .override .ov-oid {
    flex: 2 1 180px;
    min-width: 0;
  }

  .override .ov-value {
    flex: 3 1 160px;
    min-width: 0;
  }

  .override .problem {
    flex-basis: 100%;
  }

  .preview-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
  }

  .preview-bar .check {
    font-size: 0.85em;
    white-space: nowrap;
  }

  .preview-bar input {
    flex: 1;
    min-width: 0;
  }

  .preview-count {
    font-size: 0.82em;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .preview-wrap {
    max-height: 320px;
    overflow: auto;
    margin-top: 8px;
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  /* Fixed layout: the columns are as wide as someone dragged them, and what
     does not fit ends in an ellipsis, the whole of it in the cell's title. */
  .preview {
    table-layout: fixed;
    min-width: 100%;
    border-collapse: collapse;
    font-size: 0.82em;
  }

  .preview th,
  .preview td {
    padding: 4px 8px;
    border-bottom: 1px solid var(--border-color);
    text-align: left;
    vertical-align: top;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .preview th {
    position: sticky;
    top: 0;
    background-color: var(--bg-color);
    color: var(--text-light);
    font-weight: 600;
  }

  .col-resize {
    position: absolute;
    top: 0;
    right: 0;
    width: 7px;
    height: 100%;
    cursor: col-resize;
  }

  .col-resize:hover,
  .col-resize:focus-visible {
    background-color: var(--accent-border);
    outline: none;
  }

  .behaviour {
    padding: 0 6px;
    border-radius: 4px;
    font-weight: 600;
    background-color: var(--hover-overlay-medium);
    color: var(--text-muted);
  }

  .behaviour:not(.static) {
    background-color: var(--accent-border);
    color: var(--accent-color);
  }

  .preview tr.moving .value {
    color: var(--accent-color);
  }

  .section-title {
    margin: 16px 0 0;
    font-size: 0.95em;
  }

  .section-title.first {
    margin-top: 0;
  }

  .user-block {
    margin-top: 10px;
    padding: 10px 12px 12px;
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .user-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    min-height: 28px;
  }

  .user-title {
    font-size: 0.88em;
    font-weight: 600;
    color: var(--text-muted);
  }

  .users-foot {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 10px;
    margin-top: 10px;
  }

  .users-foot .btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .users-note {
    font-size: 0.82em;
    color: var(--text-muted);
  }

  .users {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .problem {
    margin-top: 4px;
    font-size: 0.78em;
    color: var(--error-color);
  }

  .save-error {
    flex: 1;
    font-size: 0.85em;
    color: var(--error-color);
  }

  .traps-activity {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }

  .traps-activity .failed {
    color: var(--error-color);
  }

  .send-trap {
    height: 28px;
    padding: 0 6px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-size: 0.85em;
  }

  .dest-grid {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr 2fr;
    gap: 10px 14px;
    align-items: start;
    margin-top: 6px;
  }

  .inform {
    margin-top: 10px;
    font-size: 0.9em;
  }

  .engine {
    margin-top: 10px;
  }

  .engine-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .engine-row input {
    flex: 1;
    min-width: 0;
  }

  .engine-row .btn {
    white-space: nowrap;
  }

  .field-hint {
    margin-top: 4px;
    font-size: 0.78em;
    color: var(--text-muted);
  }

  .triggers {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 14px;
    font-size: 0.9em;
  }

  .schedule {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-top: 10px;
    font-size: 0.9em;
  }

  .schedule select,
  .schedule input[type='number'] {
    padding: 6px 8px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .schedule input[type='number'] {
    width: 90px;
  }

  .schedule .problem {
    flex-basis: 100%;
    margin-top: 0;
  }

  @media (max-width: 640px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .dest-grid {
      grid-template-columns: 1fr 1fr;
    }
    .actions {
      padding-left: 0;
    }
    /* Narrow, the name takes a line and the address keeps its port beside it. */
    .endpoint {
      grid-template-columns: minmax(0, 1fr) 6rem;
    }
    .endpoint .name-field {
      grid-column: 1 / -1;
    }
  }
</style>
