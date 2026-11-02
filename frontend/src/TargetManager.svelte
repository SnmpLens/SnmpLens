<script>
  import { _ } from 'svelte-i18n';
  import { onBackdrop } from './utils/modal';
  import { get } from 'svelte/store';
  import { createEventDispatcher } from 'svelte';
  import { settingsStore } from './stores/settingsStore';
  import { notificationStore } from './stores/notifications';
  import { onMount } from 'svelte';
  import { TestConnection, ListPresets, IdentifyDevice } from './bridge/App';
  import { pollingStore } from './stores/pollingStore';
  import { requestTab } from './stores/tabRequest';
  import { buildTestRequest } from './utils/snmpParams';
  import { getEffectiveSettings } from './utils/targets';
  import { assignProfile, findProfile, withProfile } from './utils/credentialProfiles.js';
  import { boundPresetsFor } from './utils/presetBindings';
  import TargetOverrideForm from './TargetOverrideForm.svelte';
  import { anonMode, anonymizeIp } from './utils/anonymize';
  import Icon from './Icon.svelte';

  const dispatch = createEventDispatcher();

  function parseTargets(targetsString) {
    if (!targetsString) return [];
    return targetsString.split('\n')
      .map((line, index) => {
        const trimmed = line.trim();
        if (!trimmed) return null;
        const isDisabled = trimmed.startsWith('//');
        const withoutPrefix = isDisabled ? trimmed.substring(2).trim() : trimmed;
        const parts = withoutPrefix.split('#');
        const address = parts[0].trim();
        const label = parts[1]?.trim() || '';
        return { id: index, address, label, enabled: !isDisabled, testing: false, status: null };
      })
      .filter(t => t !== null);
  }

  function serializeTargets(targets) {
    return targets.map(t => {
      let line = t.enabled ? t.address : '//' + t.address;
      if (t.label) line += ' # ' + t.label;
      return line;
    }).join('\n');
  }

  let targets = parseTargets($settingsStore.targets);
  let newAddress = '';
  let newLabel = '';
  // The identifiers a new target starts with: '' for the defaults, or a
  // credential profile's id. Kept between adds on purpose — equipment is
  // usually added a batch of one kind at a time.
  let newProfile = '';
  let showAddForm = false;
  let expandedOverrideId = null;
  let selectedGroupId = 'all';
  let showGroupMenu = false;
  let newGroupName = '';
  let editingId = null;
  let editAddressValue = '';
  let showImport = false;
  let importText = '';

  // Focus + select an input when it mounts (avoids the autofocus a11y warning)
  function focusOnMount(node) {
    node.focus();
    node.select?.();
  }

  $: {
    const parsed = parseTargets($settingsStore.targets);
    if (JSON.stringify(parsed.map(t => ({a: t.address, l: t.label, e: t.enabled}))) !==
        JSON.stringify(targets.map(t => ({a: t.address, l: t.label, e: t.enabled})))) {
      targets = parsed;
    }
  }

  $: groups = $settingsStore.targetGroups || [{ id: 'default', name: 'Default' }];
  $: assignments = $settingsStore.targetGroupAssignments || {};

  function getGroupForTarget(address) {
    return assignments[address] || 'default';
  }

  // `list` is a parameter rather than a read of `targets` because this is called
  // from the markup: an expression that does not name what it depends on is not
  // re-evaluated when that changes, so the tab kept the count it was first given.
  function getGroupTargetCount(groupId, list) {
    if (groupId === 'all') return list.length;
    return list.filter(t => getGroupForTarget(t.address) === groupId).length;
  }

  $: filteredTargets = selectedGroupId === 'all'
    ? targets
    : targets.filter(t => getGroupForTarget(t.address) === selectedGroupId);

  $: enabledTargets = targets.filter(t => t.enabled);
  $: enabledCount = enabledTargets.length;

  function saveTargets() {
    settingsStore.save({ ...$settingsStore, targets: serializeTargets(targets) });
  }

  // The preset library, for the selector on the add form. Loaded once: this
  // modal is opened to add an equipment, and a list that refetched on every
  // keystroke would be a bridge call per character.
  let presets = [];
  let newPreset = '';
  let binding = false;
  let identifying = false;
  // Binding a preset to an equipment that ALREADY exists. The add form covers
  // the moment somebody knows what the equipment is; this covers every moment
  // after it, which is most of them — a target added before there were presets
  // at all had no other way to get one.
  let expandedPresetId = null;
  let rowPreset = {};
  let rowBinding = null;
  // What the device said about itself, or null. Reset whenever the address
  // changes, because it describes an address and not a form.
  let identity = null;

  // Nothing in this application read sysObjectID until now, so a preset's
  // `match` had no data source at all — the rule was written and never wired.
  // This is the wiring, and it ORDERS the list rather than filtering it: match
  // is advice to the person binding, and a preset that says nothing about which
  // device it is for is still one they downloaded for this one.
  async function identify() {
    const address = newAddress.trim();
    if (!address) return;
    identifying = true;
    identity = null;
    try {
      // With the profile picked for it, when there is one: the device may
      // answer to nothing else, and identifying it with the defaults would
      // report it unreachable.
      const settings = newProfile
        ? withProfile($settingsStore, newProfile)
        : getEffectiveSettings($settingsStore, address);
      const result = await IdentifyDevice(buildTestRequest(settings, address));
      identity = result;
      if (Array.isArray(result.presets) && result.presets.length) {
        presets = result.presets;
      }
      // Offer the best match, without choosing it: the operator still has to
      // see which preset is selected before pressing Add.
      if (result.matched > 0 && !newPreset) {
        const best = (result.presets || []).find((p) => p.problems === 0);
        if (best) newPreset = best.file;
      }
    } catch (e) {
      identity = { error: String(e) };
    } finally {
      identifying = false;
    }
  }

  onMount(async () => {
    try {
      presets = await ListPresets();
    } catch (e) {
      console.error('ListPresets failed', e);
      presets = [];
    }
  });

  // A preset with problems is in the library and cannot be bound, so it is not
  // offered — the reason is in Settings, next to the error list that explains
  // it, rather than as a disabled row here with nothing saying why.
  $: bindablePresets = presets.filter((p) => p.problems === 0);

  function togglePresetRow(id) {
    expandedPresetId = expandedPresetId === id ? null : id;
  }

  async function bindToExisting(target) {
    const file = rowPreset[target.id];
    if (!file) return;
    rowBinding = target.id;
    try {
      await pollingStore.bindPreset(file, target.address, get(settingsStore));
      notificationStore.add($_('targets.presetBound', { values: { address: target.address } }), 'success');
      expandedPresetId = null;
      rowPreset = { ...rowPreset, [target.id]: '' };
      requestTab('dashboard');
      dispatch('close');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      rowBinding = null;
    }
  }

  async function addTarget() {
    if (!newAddress.trim()) return;
    const address = newAddress.trim();
    const file = newPreset;
    targets = [...targets, {
      id: Date.now(), address, label: newLabel.trim(),
      enabled: true, testing: false, status: null
    }];
    // Assign to current group (or default)
    const groupId = selectedGroupId === 'all' ? 'default' : selectedGroupId;
    const newAssignments = { ...($settingsStore.targetGroupAssignments || {}), [address]: groupId };
    const overrides = newProfile
      ? assignProfile($settingsStore.targetOverrides, address, newProfile)
      : $settingsStore.targetOverrides;
    newAddress = '';
    newLabel = '';
    newPreset = '';
    showAddForm = false;
    settingsStore.save({
      ...$settingsStore,
      targets: serializeTargets(targets),
      targetGroupAssignments: newAssignments,
      targetOverrides: overrides,
    });

    // The target is saved FIRST, and the binding is attempted afterwards. A
    // bind that fails must not cost the operator the equipment they just
    // typed in — and the session, if it is created, is durable Go-side state
    // that outlives this modal either way.
    if (!file) return;
    binding = true;
    try {
      await pollingStore.bindPreset(file, address, get(settingsStore));
      notificationStore.add($_('targets.presetBound', { values: { address } }), 'success');
      requestTab('dashboard');
      dispatch('close');
    } catch (e) {
      notificationStore.add(String(e), 'error');
    } finally {
      binding = false;
    }
  }

  function removeTarget(id) {
    const target = targets.find(t => t.id === id);
    targets = targets.filter(t => t.id !== id);
    const overrides = { ...($settingsStore.targetOverrides || {}) };
    const assigns = { ...($settingsStore.targetGroupAssignments || {}) };
    if (target) {
      delete overrides[target.address];
      delete assigns[target.address];
    }
    settingsStore.save({ ...$settingsStore, targets: serializeTargets(targets), targetOverrides: overrides, targetGroupAssignments: assigns });
  }

  function toggleTarget(id) {
    targets = targets.map(t => t.id === id ? { ...t, enabled: !t.enabled } : t);
    saveTargets();
  }

  function updateLabel(id, label) {
    targets = targets.map(t => t.id === id ? { ...t, label } : t);
    saveTargets();
  }

  function startEditAddress(target) {
    editingId = target.id;
    editAddressValue = target.address;
  }

  function cancelEditAddress() {
    editingId = null;
    editAddressValue = '';
  }

  function saveEditAddress(target) {
    const newAddr = editAddressValue.trim();
    if (!newAddr || newAddr === target.address) {
      cancelEditAddress();
      return;
    }
    if (targets.some(t => t.id !== target.id && t.address === newAddr)) {
      notificationStore.add(get(_)('targets.duplicateAddress', { values: { address: newAddr } }), 'error');
      return;
    }
    const oldAddr = target.address;
    targets = targets.map(t => t.id === target.id ? { ...t, address: newAddr, status: null } : t);
    // Migrate overrides and group assignment (both keyed by address)
    const overrides = { ...($settingsStore.targetOverrides || {}) };
    const assigns = { ...($settingsStore.targetGroupAssignments || {}) };
    if (overrides[oldAddr] !== undefined) { overrides[newAddr] = overrides[oldAddr]; delete overrides[oldAddr]; }
    if (assigns[oldAddr] !== undefined) { assigns[newAddr] = assigns[oldAddr]; delete assigns[oldAddr]; }
    settingsStore.save({ ...$settingsStore, targets: serializeTargets(targets), targetOverrides: overrides, targetGroupAssignments: assigns });
    cancelEditAddress();
  }

  // Delete every target currently shown (respects the selected group filter).
  function deleteAllInGroup() {
    const toDelete = filteredTargets;
    if (toDelete.length === 0) return;
    const t = get(_);
    if (!confirm(t('targets.deleteAllConfirm', { values: { count: toDelete.length } }))) return;
    const idsToDelete = new Set(toDelete.map(x => x.id));
    const addrsToDelete = new Set(toDelete.map(x => x.address));
    targets = targets.filter(x => !idsToDelete.has(x.id));
    const overrides = { ...($settingsStore.targetOverrides || {}) };
    const assigns = { ...($settingsStore.targetGroupAssignments || {}) };
    for (const addr of addrsToDelete) { delete overrides[addr]; delete assigns[addr]; }
    settingsStore.save({ ...$settingsStore, targets: serializeTargets(targets), targetOverrides: overrides, targetGroupAssignments: assigns });
  }

  function openImport() {
    importText = '';
    showImport = true;
  }

  // Import a newline-separated list of addresses (optional "address # label" per line).
  // Duplicates are skipped; new targets are assigned to the currently selected group.
  function importTargets() {
    const lines = importText.split('\n').map(l => l.trim()).filter(Boolean);
    if (lines.length === 0) { showImport = false; return; }
    const existing = new Set(targets.map(t => t.address));
    const groupId = selectedGroupId === 'all' ? 'default' : selectedGroupId;
    const assigns = { ...($settingsStore.targetGroupAssignments || {}) };
    const newTargets = [...targets];
    let added = 0, skipped = 0;
    lines.forEach((line, i) => {
      const parts = line.split('#');
      const address = parts[0].trim();
      const label = parts[1]?.trim() || '';
      if (!address) return;
      if (existing.has(address)) { skipped++; return; }
      existing.add(address);
      newTargets.push({ id: Date.now() + i, address, label, enabled: true, testing: false, status: null });
      assigns[address] = groupId;
      added++;
    });
    targets = newTargets;
    settingsStore.save({ ...$settingsStore, targets: serializeTargets(targets), targetGroupAssignments: assigns });
    notificationStore.add(get(_)('targets.import.result', { values: { added, skipped } }), added > 0 ? 'success' : 'info');
    importText = '';
    showImport = false;
  }

  async function testTarget(id) {
    const target = targets.find(t => t.id === id);
    if (!target) return;
    targets = targets.map(t => t.id === id ? { ...t, testing: true, status: null } : t);
    try {
      const effectiveSettings = getEffectiveSettings($settingsStore, target.address);
      const result = await TestConnection(buildTestRequest(effectiveSettings, target.address));
      const success = !result.error;
      targets = targets.map(t => t.id === id ? { ...t, testing: false, status: success ? 'success' : 'error' } : t);
      const t = get(_);
      if (success) {
        notificationStore.add(t('targets.connectionOk', { values: { address: target.address } }), 'success');
      } else {
        notificationStore.add(t('targets.connectionFailed', { values: { address: target.address, error: result.error } }), 'error');
      }
    } catch (err) {
      targets = targets.map(t => t.id === id ? { ...t, testing: false, status: 'error' } : t);
      const t = get(_);
      notificationStore.add(t('targets.connectionFailed', { values: { address: target.address, error: String(err) } }), 'error');
    }
  }

  async function testAllTargets() {
    for (const target of enabledTargets) {
      await testTarget(target.id);
    }
  }

  // Overrides OTHER than a credential profile, which has a chip of its own.
  function hasOverrides(settings, address) {
    const ov = settings.targetOverrides?.[address];
    return !!ov && Object.keys(ov).some((k) => k !== 'profile');
  }

  function profileOf(settings, address) {
    return findProfile(settings, settings.targetOverrides?.[address]?.profile);
  }

  function toggleOverrides(id) {
    expandedOverrideId = expandedOverrideId === id ? null : id;
  }

  function saveOverride(address, overrideData) {
    const overrides = { ...($settingsStore.targetOverrides || {}) };
    const cleaned = Object.fromEntries(Object.entries(overrideData).filter(([, v]) => v !== undefined && v !== null && v !== ''));
    if (Object.keys(cleaned).length > 0) overrides[address] = cleaned;
    else delete overrides[address];
    settingsStore.save({ ...$settingsStore, targetOverrides: overrides });
    expandedOverrideId = null;
  }

  function clearOverride(address) {
    const overrides = { ...($settingsStore.targetOverrides || {}) };
    delete overrides[address];
    settingsStore.save({ ...$settingsStore, targetOverrides: overrides });
    expandedOverrideId = null;
  }

  // "Save as a profile" from a target's override form: the profile joins the
  // library and the target is given it in ONE save — two would briefly leave
  // the target naming a profile that did not exist yet.
  function saveOverrideAsProfile(address, { profile, overrides: own }) {
    settingsStore.save({
      ...$settingsStore,
      credentialProfiles: [...($settingsStore.credentialProfiles || []), profile],
      targetOverrides: { ...($settingsStore.targetOverrides || {}), [address]: own },
    });
    expandedOverrideId = null;
    notificationStore.add(get(_)('profiles.savedFromTarget', { values: { name: profile.name } }), 'success');
  }

  // --- Group management ---

  function addGroup() {
    if (!newGroupName.trim()) return;
    const id = 'grp-' + Date.now();
    const updatedGroups = [...groups, { id, name: newGroupName.trim() }];
    settingsStore.save({ ...$settingsStore, targetGroups: updatedGroups });
    newGroupName = '';
    showGroupMenu = false;
    selectedGroupId = id;
  }

  function renameGroup(groupId, newName) {
    if (!newName.trim() || groupId === 'default') return;
    const updatedGroups = groups.map(g => g.id === groupId ? { ...g, name: newName.trim() } : g);
    settingsStore.save({ ...$settingsStore, targetGroups: updatedGroups });
  }

  function deleteGroup(groupId) {
    if (groupId === 'default') return;
    const t = get(_);
    if (!confirm(t('targets.groups.deleteConfirm'))) return;
    // Move targets to default
    const assigns = { ...($settingsStore.targetGroupAssignments || {}) };
    for (const [addr, gid] of Object.entries(assigns)) {
      if (gid === groupId) assigns[addr] = 'default';
    }
    const updatedGroups = groups.filter(g => g.id !== groupId);
    settingsStore.save({ ...$settingsStore, targetGroups: updatedGroups, targetGroupAssignments: assigns });
    if (selectedGroupId === groupId) selectedGroupId = 'all';
  }

  function moveTargetToGroup(address, groupId) {
    const assigns = { ...($settingsStore.targetGroupAssignments || {}), [address]: groupId };
    settingsStore.save({ ...$settingsStore, targetGroupAssignments: assigns });
  }
</script>

<div class="target-manager">
  <!-- Group tabs -->
  <div class="group-tabs">
    <button class="group-tab" class:active={selectedGroupId === 'all'} on:click={() => selectedGroupId = 'all'}>
      {$_('targets.groups.all')} <span class="group-count">{targets.length}</span>
    </button>
    <!-- The tab is a DIV wrapping two sibling buttons, not one button inside
         another. Nested interactive content is invalid HTML, and the practical
         cost is that the delete control cannot be reached at all without a
         mouse: focus stops at the outer button and never enters it. -->
    {#each groups as group (group.id)}
      <div class="group-tab" class:active={selectedGroupId === group.id}>
        <button
          class="group-tab-label"
          on:click={() => selectedGroupId = group.id}
          on:dblclick={() => {
            if (group.id !== 'default') {
              const newName = prompt($_('targets.groups.renamePrompt'), group.name);
              if (newName) renameGroup(group.id, newName);
            }
          }}
          title={group.id !== 'default' ? $_('targets.groups.dblClickRename') : ''}
        >
          {group.name} <span class="group-count">{getGroupTargetCount(group.id, targets)}</span>
        </button>
        {#if group.id !== 'default'}
          <button class="group-delete" on:click={() => deleteGroup(group.id)} title={$_('common.delete')}><Icon name="x" size={12} /></button>
        {/if}
      </div>
    {/each}
    <button class="group-tab group-add" on:click={() => showGroupMenu = !showGroupMenu} title={$_('targets.groups.addGroup')}>+</button>
  </div>

  {#if showGroupMenu}
    <div class="group-add-form">
      <input
        type="text"
        bind:value={newGroupName}
        placeholder={$_('targets.groups.namePlaceholder')}
        on:keydown={(e) => e.key === 'Enter' && addGroup()}
      />
      <button class="btn-sm primary" on:click={addGroup} disabled={!newGroupName.trim()}>{$_('common.add')}</button>
      <button class="btn-sm" on:click={() => { showGroupMenu = false; newGroupName = ''; }}>{$_('common.cancel')}</button>
    </div>
  {/if}

  <!-- Header actions -->
  <div class="target-header">
    <div class="target-actions">
      <button class="btn-sm" on:click={testAllTargets} disabled={enabledCount === 0} title={$_('targets.testAllTooltip')}>
        <Icon name="plug" size={14} /> {$_('targets.testAll')}
      </button>
      <button class="btn-sm" on:click={openImport} title={$_('targets.import.tooltip')}>
        <Icon name="clipboard-list" size={14} /> {$_('targets.import.button')}
      </button>
      <button class="btn-sm danger" on:click={deleteAllInGroup} disabled={filteredTargets.length === 0} title={$_('targets.deleteAllTooltip')}>
        <Icon name="trash-2" size={14} /> {$_('targets.deleteAll')}
      </button>
      <button class="btn-sm primary" on:click={() => showAddForm = !showAddForm}>
        {#if showAddForm}<Icon name="x" size={14} />{:else}{$_('targets.addButton')}{/if}
      </button>
    </div>
  </div>

  {#if showAddForm}
    <div class="add-form">
      <input type="text" bind:value={newAddress}
        placeholder={$_('targets.addressPlaceholder')}
        on:input={() => (identity = null)}
        on:keydown={(e) => e.key === 'Enter' && addTarget()} />
      <button class="btn-sm" on:click={identify} disabled={!newAddress.trim() || identifying}
        title={$_('targets.presetDetectHint')}>
        {identifying ? $_('common.working') : $_('targets.presetDetect')}
      </button>
      <input type="text" bind:value={newLabel} placeholder={$_('targets.labelPlaceholder')} on:keydown={(e) => e.key === 'Enter' && addTarget()} />
      {#if ($settingsStore.credentialProfiles || []).length > 0}
        <select bind:value={newProfile} title={$_('profiles.identifiers')} on:change={() => (identity = null)}>
          <option value="">{$_('profiles.useDefault')}</option>
          {#each $settingsStore.credentialProfiles as p (p.id)}
            <option value={p.id}>{p.name} · {p.version}</option>
          {/each}
        </select>
      {/if}
      <!-- The preset is chosen HERE, when the equipment is added, because that
           is the only moment somebody knows what the equipment is. -->
      <select bind:value={newPreset} title={$_('targets.presetHint')} disabled={bindablePresets.length === 0}>
        <option value="">{bindablePresets.length === 0 ? $_('targets.presetNone') : $_('targets.presetPick')}</option>
        {#each bindablePresets as p (p.file)}
          <option value={p.file}>{p.name || p.file}</option>
        {/each}
      </select>
      <button class="btn-sm primary" on:click={addTarget} disabled={!newAddress.trim() || binding}>
        {binding ? $_('common.working') : $_('common.add')}
      </button>
    </div>

    {#if identity}
      <p class="identity" class:failed={!!identity.error}>
        {#if identity.error}
          {identity.error}
        {:else}
          <!-- sysObjectID names a vendor and a model as plainly as sysDescr
               does, so Anonymous Mode masks it with everything else. -->
          {$anonMode ? $_('targets.presetDeviceMasked') : (identity.sysDescr || identity.sysObjectId || '')}
          <span class="matched">
            {identity.matched > 0
              ? $_('targets.presetMatched', { values: { count: identity.matched } })
              : $_('targets.presetNoMatch')}
          </span>
        {/if}
      </p>
    {/if}
  {/if}

  <div class="target-list">
    {#if filteredTargets.length === 0}
      <div class="empty-state">{$_('targets.empty')}</div>
    {:else}
      {#each filteredTargets as target (target.id)}
        <div class="target-entry">
          <div class="target-item" class:disabled={!target.enabled}>
            <label class="target-checkbox">
              <input type="checkbox" checked={target.enabled} on:change={() => toggleTarget(target.id)} />
            </label>
            <div class="target-info">
              {#if editingId === target.id}
                <input
                  type="text" class="target-address-input"
                  bind:value={editAddressValue}
                  use:focusOnMount
                  on:keydown={(e) => {
                    if (e.key === 'Enter') saveEditAddress(target);
                    else if (e.key === 'Escape') cancelEditAddress();
                  }}
                />
                <button class="btn-icon" on:click={() => saveEditAddress(target)} title={$_('common.save')}><Icon name="check" class="icon-success" size={15} /></button>
                <button class="btn-icon" on:click={cancelEditAddress} title={$_('common.cancel')}><Icon name="x" size={15} /></button>
              {:else}
                <span class="target-address" class:disabled={!target.enabled}>
                  {$anonMode ? anonymizeIp(target.address) : target.address}
                  {#if hasOverrides($settingsStore, target.address)}
                    <span class="override-badge" title={$_('targets.overrides.badge')}><Icon name="sliders-horizontal" size={11} /></span>
                  {/if}
                  <!-- A badge like the one above, with the name in its title.
                       Written out, the name took the room the operator's own
                       label needs: this dialog is six hundred pixels wide, and
                       both came out cut to a letter or two. -->
                  {#if profileOf($settingsStore, target.address)}
                    <span class="override-badge" role="img"
                      title={$_('profiles.chip', { values: { name: profileOf($settingsStore, target.address).name } })}
                      aria-label={$_('profiles.chip', { values: { name: profileOf($settingsStore, target.address).name } })}>
                      <Icon name="key-round" size={11} />
                    </span>
                  {/if}
                </span>
                <input
                  type="text" class="target-label-input" value={target.label}
                  placeholder={$_('targets.labelPlaceholder')}
                  on:blur={(e) => updateLabel(target.id, e.target.value)}
                  on:keydown={(e) => e.key === 'Enter' && e.target.blur()}
                />
              {/if}
            </div>
            <div class="target-status">
              {#if target.testing}
                <span class="status-icon testing"><Icon name="loader-circle" class="icon-spin" size={15} /></span>
              {:else if target.status === 'success'}
                <span class="status-icon success" title={$_('targets.statusSuccess')}><Icon name="circle-check" class="icon-success" size={15} /></span>
              {:else if target.status === 'error'}
                <span class="status-icon error" title={$_('targets.statusError')}><Icon name="circle-x" class="icon-error" size={15} /></span>
              {/if}
            </div>
            <div class="target-buttons">
              {#if groups.length > 1}
                <select
                  class="group-select"
                  value={getGroupForTarget(target.address)}
                  on:change={(e) => moveTargetToGroup(target.address, e.target.value)}
                  title={$_('targets.groups.moveToGroup')}
                >
                  {#each groups as g}
                    <option value={g.id}>{g.name}</option>
                  {/each}
                </select>
              {/if}
              <button class="btn-icon" on:click={() => startEditAddress(target)}
                disabled={editingId === target.id} title={$_('targets.editTooltip')}><Icon name="pencil" size={15} /></button>
              <button class="btn-icon" class:active={expandedPresetId === target.id}
                on:click={() => togglePresetRow(target.id)} title={$_('targets.presetBindTooltip')}>
                <Icon name="layers" size={15} />
              </button>
              <button class="btn-icon" class:active={expandedOverrideId === target.id}
                on:click={() => toggleOverrides(target.id)} title={$_('targets.overrides.title')}>
                <Icon name="settings" size={15} />
              </button>
              <button class="btn-icon" on:click={() => testTarget(target.id)}
                disabled={target.testing || !target.enabled} title={$_('targets.testTooltip')}><Icon name="plug" size={15} /></button>
              <button class="btn-icon danger" on:click={() => removeTarget(target.id)}
                title={$_('targets.deleteTooltip')}><Icon name="trash-2" size={15} /></button>
            </div>
          </div>
          {#if expandedPresetId === target.id}
            <div class="preset-row">
              {#if bindablePresets.length === 0}
                <span class="preset-note">{$_('targets.presetNone')}</span>
              {:else}
                <select bind:value={rowPreset[target.id]} title={$_('targets.presetHint')}>
                  <option value="">{$_('targets.presetPick')}</option>
                  {#each bindablePresets as p (p.file)}
                    <option value={p.file}>{p.name || p.file}</option>
                  {/each}
                </select>
                <button class="btn-sm primary" on:click={() => bindToExisting(target)}
                  disabled={!rowPreset[target.id] || rowBinding === target.id}>
                  {rowBinding === target.id ? $_('common.working') : $_('targets.presetBind')}
                </button>
              {/if}
              <!-- What is already bound to this equipment, so binding a second
                   one is a deliberate act rather than an accident: each bind
                   creates its own session and its own poll clock. -->
              {#if boundPresetsFor($pollingStore, target.address).length > 0}
                <span class="preset-note">
                  {$_('targets.presetAlreadyBound', {
                    values: {
                      names: boundPresetsFor($pollingStore, target.address)
                        .map((s) => s.preset.name || s.preset.file).join(', '),
                    },
                  })}
                </span>
              {/if}
            </div>
          {/if}

          {#if expandedOverrideId === target.id}
            <TargetOverrideForm
              overrides={$settingsStore.targetOverrides?.[target.address] || {}}
              globalSettings={$settingsStore}
              address={target.address}
              label={target.label}
              on:save={(e) => saveOverride(target.address, e.detail)}
              on:clear={() => clearOverride(target.address)}
              on:saveAsProfile={(e) => saveOverrideAsProfile(target.address, e.detail)}
            />
          {/if}
        </div>
      {/each}
    {/if}
  </div>

  {#if showImport}
    <div class="import-backdrop" on:mousedown={onBackdrop(() => showImport = false)} role="presentation">
      <div class="import-modal" role="dialog" aria-modal="true" tabindex="-1">
        <div class="import-modal-header">
          <h3><Icon name="clipboard-list" size={16} /> {$_('targets.import.title')}</h3>
          <button class="import-close" on:click={() => showImport = false} title={$_('common.close')}>&times;</button>
        </div>
        <p class="import-hint">{$_('targets.import.hint')}</p>
        <textarea
          class="import-textarea"
          bind:value={importText}
          placeholder={$_('targets.import.placeholder')}
          rows="10"
          use:focusOnMount
        ></textarea>
        <div class="import-actions">
          <button class="btn-sm" on:click={() => showImport = false}>{$_('common.cancel')}</button>
          <button class="btn-sm primary" on:click={importTargets} disabled={!importText.trim()}>
            <Icon name="clipboard-list" size={14} /> {$_('targets.import.button')}
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .target-manager { padding: 0; }

  /* Group tabs */
  .group-tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 12px;
    flex-wrap: wrap;
    border-bottom: 1px solid var(--border-color);
    padding-bottom: 8px;
  }

  .group-tab {
    padding: 4px 12px;
    background: transparent;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-muted);
    font-size: 0.82em;
    cursor: pointer;
    transition: all 0.15s;
    display: flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }

  /* The wrapper keeps the box the tab always had, padding included, so the two
     shapes measure the same. The label then reaches BACK OUT through that
     padding with a negative margin, because the padding used to be part of the
     button and clicking it selected the group; without this it becomes dead
     space around a smaller target. `:only-child` covers the right edge too when
     there is no delete button to sit there. */
  .group-tab-label {
    margin: -4px 0 -4px -12px;
    padding: 4px 0 4px 12px;
    background: none;
    border: none;
    color: inherit;
    /* font-SIZE, not the `font` shorthand: that one also resets the family, and
       a <button> does not inherit the app's font. The tab used to BE the button,
       so it drew in the platform default; the shorthand quietly moved these
       three tabs to Nunito while `All` and `+`, still plain buttons, stayed
       behind — a strip in two typefaces. */
    font-size: inherit;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .group-tab-label:only-child {
    margin-right: -12px;
    padding-right: 12px;
  }

  .group-tab:hover { background-color: var(--hover-overlay); color: var(--text-color); }
  .group-tab.active { background-color: var(--accent-color); border-color: var(--accent-color); color: white; }

  .group-count {
    font-size: 0.85em;
    opacity: 0.7;
    background: var(--hover-overlay-strong);
    padding: 0 5px;
    border-radius: 8px;
  }

  .group-delete {
    background: none;
    border: none;
    color: inherit;
    font-size: 0.8em;
    cursor: pointer;
    opacity: 0.5;
    padding: 0 2px;
  }
  .group-delete:hover { opacity: 1; }

  .group-tab.group-add {
    border-style: dashed;
    font-size: 1em;
    padding: 4px 10px;
  }

  .group-add-form {
    display: flex;
    gap: 8px;
    margin-bottom: 10px;
    padding: 8px;
    background-color: var(--bg-color);
    border-radius: 4px;
  }

  .group-add-form input {
    flex: 1;
    padding: 5px 8px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-lighter-color);
    color: var(--text-color);
    font-size: 0.88em;
  }

  .group-select {
    padding: 2px 4px;
    font-size: 0.78em;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 3px;
    color: var(--text-dimmed);
    cursor: pointer;
    max-width: 80px;
  }

  /* Target list */
  .target-header { display: flex; justify-content: flex-end; align-items: center; margin-bottom: 10px; }
  .target-actions { display: flex; gap: 8px; }

  .btn-sm { padding: 4px 10px; font-size: 0.85em; border: 1px solid var(--border-color); background: transparent; color: var(--text-color); border-radius: 4px; cursor: pointer; transition: all 0.2s; }
  .btn-sm:hover:not(:disabled) { background-color: var(--bg-color); }
  .btn-sm.primary { background-color: var(--accent-color); border-color: var(--accent-color); color: white; }
  .btn-sm.primary:hover:not(:disabled) { background-color: var(--accent-hover-color); }
  .btn-sm.danger { color: var(--error-color); border-color: var(--error-border); }
  .btn-sm.danger:hover:not(:disabled) { background-color: var(--error-color); border-color: var(--error-color); color: white; }
  .btn-sm:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Wraps. It held two inputs and a button when it was written; it now holds
     two inputs, a Detect button, a preset picker and Add, and on one line the
     picker grew with the longest preset name until the row ran off the modal. */
  .add-form { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin-bottom: 10px; padding: 10px; background-color: var(--bg-color); border-radius: 4px; }
  .add-form input { flex: 1 1 150px; min-width: 0; padding: 6px 10px; border: 1px solid var(--border-color); border-radius: 4px; background-color: var(--bg-lighter-color); color: var(--text-color); font-size: 0.9em; }
  .add-form input:first-child { flex: 2 1 200px; }
  /* Bounded on BOTH sides: a select sizes itself to its longest option, and a
     preset called "Cisco Catalyst 9300 uplinks and access ports" is a wide
     option. */
  .add-form select { flex: 1 1 160px; max-width: 240px; min-width: 0; padding: 6px 8px; border: 1px solid var(--border-color); border-radius: 4px; background-color: var(--bg-lighter-color); color: var(--text-color); font-size: 0.9em; }
  .add-form .btn-sm { flex: 0 0 auto; }
  .identity { flex: 1 1 100%; margin: 0; font-size: 0.8em; color: var(--text-muted); }
  .identity.failed { color: var(--error-color, #f85149); }
  .identity .matched { margin-left: 0.4rem; }

  .target-list { display: flex; flex-direction: column; gap: 6px; max-height: 400px; overflow-y: auto; }
  .target-entry { display: flex; flex-direction: column; }
  .target-item { display: flex; align-items: center; gap: 10px; padding: 8px 10px; background-color: var(--bg-color); border-radius: 4px; transition: opacity 0.2s; }
  .target-item.disabled { opacity: 0.5; }

  .preset-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin: 4px 0 0 34px;
    padding: 8px 10px;
    background-color: var(--bg-color);
    border-radius: 4px;
  }

  .preset-row select {
    flex: 1 1 160px;
    max-width: 260px;
    min-width: 0;
    padding: 5px 8px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-lighter-color);
    color: var(--text-color);
    font-size: 0.85em;
  }

  .preset-note {
    flex: 1 1 100%;
    color: var(--text-muted);
    font-size: 0.78em;
  }

  .target-checkbox { display: flex; align-items: center; }
  .target-checkbox input { width: 16px; height: 16px; cursor: pointer; }

  .target-info { flex: 1; display: flex; align-items: center; gap: 10px; min-width: 0; }
  .target-address { font-family: 'Courier New', monospace; font-size: 0.9em; font-weight: 500; white-space: nowrap; display: flex; align-items: center; gap: 4px; }
  .target-address.disabled { text-decoration: line-through; color: var(--text-muted); }
  .override-badge { font-size: 0.75em; color: var(--accent-color); }

  .target-label-input { flex: 1; padding: 4px 8px; border: 1px solid transparent; border-radius: 3px; background: transparent; color: var(--text-muted); font-size: 0.85em; min-width: 80px; }
  .target-label-input:hover, .target-label-input:focus { border-color: var(--border-color); background-color: var(--bg-lighter-color); }
  .target-label-input:focus { outline: none; border-color: var(--accent-color); }

  .target-address-input {
    flex: 1;
    padding: 4px 8px;
    border: 1px solid var(--accent-color);
    border-radius: 3px;
    background-color: var(--bg-lighter-color);
    color: var(--text-color);
    font-family: 'Courier New', monospace;
    font-size: 0.9em;
    font-weight: 500;
    min-width: 120px;
  }
  .target-address-input:focus { outline: none; }

  .target-status { width: 24px; text-align: center; }
  .status-icon { font-size: 0.9em; }
  .status-icon.testing { animation: pulse 1s infinite; }

  .target-buttons { display: flex; gap: 4px; align-items: center; }
  .btn-icon { background: transparent; border: none; cursor: pointer; padding: 4px 6px; font-size: 0.85em; opacity: 0.6; transition: opacity 0.2s; border-radius: 3px; }
  .btn-icon:hover:not(:disabled) { opacity: 1; background-color: var(--hover-overlay-medium); }
  .btn-icon:disabled { opacity: 0.3; cursor: not-allowed; }
  .btn-icon.active { opacity: 1; background-color: var(--accent-subtle-strong); }
  .btn-icon.danger:hover:not(:disabled) { background-color: var(--error-subtle-strong); }

  .empty-state { text-align: center; padding: 20px; color: var(--text-muted); font-size: 0.9em; }

  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }

  /* Import modal */
  .import-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1100;
  }
  .import-modal {
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    padding: 16px;
    width: min(720px, 94vw);
    box-shadow: 0 10px 40px rgba(0, 0, 0, 0.4);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .import-modal-header { display: flex; justify-content: space-between; align-items: center; }
  .import-modal-header h3 { margin: 0; font-size: 1.05em; }
  .import-close {
    background: none;
    border: none;
    color: var(--text-muted);
    font-size: 1.4em;
    line-height: 1;
    cursor: pointer;
    padding: 0 4px;
  }
  .import-close:hover { color: var(--text-color); }
  .import-hint { margin: 0; font-size: 0.82em; color: var(--text-muted); }
  .import-textarea {
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
    padding: 8px 10px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background-color: var(--bg-lighter-color);
    color: var(--text-color);
    font-family: 'Courier New', monospace;
    font-size: 0.88em;
    line-height: 1.5;
  }
  .import-textarea:focus { outline: none; border-color: var(--accent-color); }
  .import-actions { display: flex; justify-content: flex-end; gap: 8px; }
</style>
