<script>
  import { _ } from 'svelte-i18n';
  import Icon from '../Icon.svelte';
  import UsmFields from './UsmFields.svelte';
  import TrapAcceptToggle from './TrapAcceptToggle.svelte';
  import { onBackdrop } from '../utils/modal';
  import { anonMode, anonymizeIp, maskString, maskSysDescr } from '../utils/anonymize';
  import { TestConnection } from '../bridge/App';
  import { buildTestRequest } from '../utils/snmpParams';
  import { getEffectiveSettings, parseTargetLines } from '../utils/targets';
  import {
    MAX_NAME,
    PROFILE_VERSIONS,
    assignProfile,
    blankProfile,
    findProfile,
    hasCustomCredentials,
    normaliseProfile,
    profileIdentity,
    profileUsage,
    removeProfile,
    uniqueName,
    validateProfile,
  } from '../utils/credentialProfiles.js';
  import { AUTH_PROTOCOLS, PRIV_PROTOCOLS, protocolLabel, usesAuth, usesPriv } from '../utils/snmpSecurity.js';

  /**
   * The settings being edited: the settings dialog's working copy. A profile
   * reaches the store when that dialog is saved, like every other field in it,
   * so Cancel there still means cancel.
   */
  export let settings;

  // The editor works on a DRAFT and on the set of addresses ticked for it;
  // nothing touches `settings` until Apply.
  let editing = null; // { draft, isNew, assigned: Set<string> }
  let tried = false; // errors show once Apply has been pressed; warnings always
  let pendingDelete = null; // the id of a profile awaiting confirmation
  let filter = '';
  let testTarget = '';
  let testing = false;
  let testResult = null;

  $: profiles = settings.credentialProfiles || [];
  $: targets = parseTargetLines(settings.targets);
  $: check = editing ? validateProfile(normaliseProfile(editing.draft) || editing.draft, profiles) : null;
  $: problems = problemsOf(check, tried, $_);

  function problemsOf(result, showErrors, t) {
    const out = {};
    if (!result) return out;
    for (const w of result.warnings) out[w.field] = { level: 'warn', text: t(w.key, { values: w.values }) };
    if (showErrors) {
      for (const e of result.errors) out[e.field] = { level: 'error', text: t(e.key, { values: e.values }) };
    }
    return out;
  }

  function open(draft, isNew, assigned) {
    // Both shapes are kept while editing, so v2c → v3 → v2c does not throw
    // away what was typed. normaliseProfile drops the unused one on Apply.
    editing = {
      draft: { community: '', acceptTraps: true, ...draft, v3: { ...blankProfile('v3').v3, ...(draft.v3 || {}) } },
      isNew,
      assigned,
    };
    tried = false;
    pendingDelete = null;
    filter = '';
    testResult = null;
    // A target that uses the profile — that is the device the question is
    // about — or else the first enabled one. Never in Anonymous Mode: the
    // field would show the address the mode exists to hide.
    testTarget = $anonMode ? '' : [...assigned][0] || targets.find((t) => t.enabled)?.address || '';
  }

  function openNew(version) {
    open(blankProfile(version, profiles), true, new Set());
  }

  function openEdit(profile) {
    open(JSON.parse(JSON.stringify(profile)), false, new Set(profileUsage(settings, profile.id)));
  }

  function openDuplicate(profile) {
    const copy = JSON.parse(JSON.stringify(profile));
    copy.id = blankProfile(profile.version, profiles).id;
    copy.name = uniqueName($_('profiles.copyOf', { values: { name: profile.name } }), profiles);
    // A copy starts with no targets: it exists to become something else.
    open(copy, true, new Set());
  }

  function toggleAssigned(address) {
    const next = new Set(editing.assigned);
    if (next.has(address)) next.delete(address);
    else next.add(address);
    editing.assigned = next;
  }

  // The quick switch on a profile's row: heard by the trap listener or not. It
  // edits the dialog's working copy like everything else here, so it takes
  // effect when the settings are saved.
  function toggleTraps(id) {
    settings.credentialProfiles = profiles.map((p) => (p.id === id ? { ...p, acceptTraps: p.acceptTraps === false } : p));
  }

  function apply() {
    const profile = normaliseProfile(editing.draft);
    if (validateProfile(profile, profiles).errors.length > 0) {
      tried = true;
      return;
    }
    const list = editing.isNew
      ? [...profiles, profile]
      : profiles.map((p) => (p.id === profile.id ? profile : p));

    // Every ticked target gets the profile; every target that had it and is no
    // longer ticked goes back to the default identifiers.
    let overrides = settings.targetOverrides || {};
    for (const t of targets) {
      const has = overrides[t.address]?.profile === profile.id;
      const wants = editing.assigned.has(t.address);
      if (wants && !has) overrides = assignProfile(overrides, t.address, profile.id);
      else if (!wants && has) overrides = assignProfile(overrides, t.address, null);
    }
    settings.credentialProfiles = list;
    settings.targetOverrides = overrides;
    editing = null;
  }

  // A profile nothing uses goes at once; one in use asks first, saying how
  // many targets will fall back to the default identifiers.
  function requestDelete(profile) {
    if (profileUsage(settings, profile.id).length === 0) {
      remove(profile.id);
      return;
    }
    pendingDelete = profile.id;
  }

  function remove(id) {
    const next = removeProfile(settings, id);
    settings.credentialProfiles = next.credentialProfiles;
    settings.targetOverrides = next.targetOverrides;
    pendingDelete = null;
    if (editing?.draft.id === id) editing = null;
  }

  async function testDraft() {
    const address = testTarget.trim();
    if (!address || !editing) return;
    testing = true;
    testResult = null;
    try {
      // The target's own transport — port, timeout — with the DRAFT's identity:
      // the question is whether these identifiers work on that device.
      const identity = profileIdentity(normaliseProfile(editing.draft));
      testResult = await TestConnection(buildTestRequest({ ...getEffectiveSettings(settings, address), ...identity }, address));
    } catch (e) {
      testResult = { error: String(e) };
    } finally {
      testing = false;
    }
  }

  function summary(profile, anon, t) {
    if (profile.version !== 'v3') {
      return profile.community ? t('profiles.summaryCommunity') : t('profiles.summaryNoCommunity');
    }
    const v = profile.v3 || {};
    const parts = [anon ? maskString(v.user) : v.user, v.secLevel];
    if (usesAuth(v.secLevel)) parts.push(protocolLabel(AUTH_PROTOCOLS, v.authProto));
    if (usesPriv(v.secLevel)) parts.push(protocolLabel(PRIV_PROTOCOLS, v.privProto));
    return parts.filter(Boolean).join(' · ');
  }

  function isWeak(profile) {
    const v = profile.v3;
    if (profile.version !== 'v3' || !v) return false;
    return (usesAuth(v.secLevel) && v.authProto === 'MD5') || (usesPriv(v.secLevel) && v.privProto === 'DES');
  }

  function otherProfile(s, address, id) {
    const ref = s.targetOverrides?.[address]?.profile;
    return ref && ref !== id ? findProfile(s, ref) : null;
  }

  function ownIdentifiers(s, address) {
    const ov = s.targetOverrides?.[address];
    return !ov?.profile && hasCustomCredentials(ov);
  }

  function matching(list, query) {
    const q = query.trim().toLowerCase();
    if (!q) return list;
    return list.filter((t) => t.address.toLowerCase().includes(q) || t.label.toLowerCase().includes(q));
  }

  function focus(node) {
    node.focus();
  }

  // Escape closes the editor and ONLY the editor. Handled on the window in the
  // CAPTURE phase and stopped there: the settings dialog closes on an Escape
  // that reaches the window while bubbling, and without this one key closed
  // both — taking every unsaved change in the dialog with the profile.
  function onWindowKey(e) {
    if (e.key !== 'Escape' || !editing) return;
    e.stopPropagation();
    editing = null;
  }
</script>

<svelte:window on:keydown|capture={onWindowKey} />

<section class="profiles" id="credential-profiles">
  <div class="sec-head">
    <h4><Icon name="key-round" size={15} /> {$_('profiles.title')}</h4>
    <div class="add-row">
      <button class="btn btn-small" on:click={() => openNew('v2c')}>+ {$_('profiles.addCommunity')}</button>
      <button class="btn btn-small" on:click={() => openNew('v3')}>+ {$_('profiles.addUsm')}</button>
    </div>
  </div>
  <p class="hint">{$_('profiles.hint')}</p>

  {#if profiles.length === 0}
    <p class="empty-state">{$_('profiles.empty')}</p>
  {:else}
    <ul class="list">
      {#each profiles as p (p.id)}
        {@const used = profileUsage(settings, p.id).length}
        <li>
          <span class="badge" class:v3={p.version === 'v3'}>{p.version}</span>
          <span class="name" title={p.name}>{p.name}</span>
          <span class="detail" title={summary(p, $anonMode, $_)}>{summary(p, $anonMode, $_)}</span>
          {#if isWeak(p)}
            <span class="chip-flag">{$_('profiles.weak')}</span>
          {:else}
            <span></span>
          {/if}
          <!-- Only a v3 profile is accepted or refused by the trap listener: the
               community of a v1 or v2c trap is not checked. -->
          {#if p.version === 'v3'}
            <button class="trap-chip" class:off={p.acceptTraps === false} aria-pressed={p.acceptTraps !== false}
              title={p.acceptTraps === false ? $_('profiles.trapsOffTitle') : $_('profiles.trapsOnTitle')}
              on:click={() => toggleTraps(p.id)}>
              <Icon name="radio" size={12} /> {$_('profiles.trapsChip')}
            </button>
          {:else}
            <span></span>
          {/if}
          <span class="usage" class:unused={used === 0}>
            {used ? $_('profiles.usedBy', { values: { count: used } }) : $_('profiles.unused')}
          </span>
          <button class="btn-copy-small" on:click={() => openEdit(p)} title={$_('common.edit')} aria-label={$_('common.edit')}>
            <Icon name="pencil" size={13} />
          </button>
          <button class="btn-copy-small" on:click={() => openDuplicate(p)} title={$_('profiles.duplicate')} aria-label={$_('profiles.duplicate')}>
            <Icon name="copy" size={13} />
          </button>
          <button class="btn-copy-small" on:click={() => requestDelete(p)} title={$_('common.delete')} aria-label={$_('common.delete')}>
            <Icon name="trash-2" size={13} />
          </button>
          {#if pendingDelete === p.id && !editing}
            <div class="confirm">
              <span>{$_('profiles.deleteConfirm', { values: { name: p.name, count: used } })}</span>
              <button class="btn btn-small danger" on:click={() => remove(p.id)}>{$_('common.delete')}</button>
              <button class="btn btn-small secondary" on:click={() => (pendingDelete = null)}>{$_('common.cancel')}</button>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

{#if editing}
  <div class="editor-overlay" on:mousedown={onBackdrop(() => (editing = null))} role="presentation">
    <div class="editor" role="dialog" aria-modal="true" aria-labelledby="profile-editor-title" tabindex="-1">
      <h3 id="profile-editor-title">
        <Icon name="key-round" size={16} />
        {editing.isNew ? $_('profiles.editorNew') : $_('profiles.editorEdit', { values: { name: editing.draft.name } })}
      </h3>

      <!-- The body scrolls and the actions do not: a profile with a long
           target list must not push Apply below the bottom of the window. -->
      <div class="editor-body">
        <div class="fld-row">
          <label class="fld grow">
            <span>{$_('profiles.name')}</span>
            <input type="text" bind:value={editing.draft.name} maxlength={MAX_NAME}
              placeholder={$_('profiles.namePlaceholder')} use:focus />
            {#if problems.name}<em class="problem {problems.name.level}">{problems.name.text}</em>{/if}
          </label>
          <div class="fld">
            <span id="profile-version-label">{$_('profiles.version')}</span>
            <div class="segmented" role="group" aria-labelledby="profile-version-label">
              {#each PROFILE_VERSIONS as v (v)}
                <button type="button" class:active={editing.draft.version === v} aria-pressed={editing.draft.version === v}
                  on:click={() => (editing.draft.version = v)}>{v}</button>
              {/each}
            </div>
          </div>
        </div>

        {#if editing.draft.version === 'v3'}
          <fieldset>
            <legend>{$_('profiles.usm')}</legend>
            <UsmFields bind:v3={editing.draft.v3} idPrefix="profile" {problems} />
            <TrapAcceptToggle bind:checked={editing.draft.acceptTraps} hint={$_('profiles.acceptTrapsHint')} />
          </fieldset>
        {:else}
          <label class="fld">
            <span>{$_('profiles.community')}</span>
            {#if $anonMode}
              <input type="password" autocomplete="off" bind:value={editing.draft.community} />
            {:else}
              <input type="text" autocomplete="off" spellcheck="false" bind:value={editing.draft.community} />
            {/if}
            {#if problems.community}<em class="problem {problems.community.level}">{problems.community.text}</em>{/if}
          </label>
        {/if}

        <fieldset>
          <legend>{$_('profiles.targets')} <span class="count">{editing.assigned.size}</span></legend>
          <p class="hint">{$_('profiles.targetsHint')}</p>
          {#if targets.length === 0}
            <p class="empty-state">{$_('profiles.noTargets')}</p>
          {:else}
            {#if targets.length > 6}
              <input class="filter" type="search" bind:value={filter} placeholder={$_('profiles.targetsFilter')} />
            {/if}
            <ul class="checklist">
              {#each matching(targets, filter) as t (t.address)}
                {@const other = otherProfile(settings, t.address, editing.draft.id)}
                <li>
                  <label class:off={!t.enabled}>
                    <input type="checkbox" checked={editing.assigned.has(t.address)} on:change={() => toggleAssigned(t.address)} />
                    <span class="addr">{$anonMode ? anonymizeIp(t.address) : t.address}</span>
                    {#if t.label && !$anonMode}<span class="lbl">{t.label}</span>{/if}
                    {#if other}
                      <span class="note">{$_('profiles.targetOther', { values: { name: other.name } })}</span>
                    {:else if ownIdentifiers(settings, t.address)}
                      <span class="note">{$_('profiles.targetOwn')}</span>
                    {/if}
                  </label>
                </li>
              {/each}
            </ul>
          {/if}
        </fieldset>

        <fieldset class="test">
          <legend><Icon name="plug" size={14} /> {$_('profiles.test')}</legend>
          <div class="fld-row">
            <label class="fld grow">
              <span>{$_('profiles.testPick')}</span>
              <input type="text" list="profile-test-targets" bind:value={testTarget}
                placeholder={$_('settings.snmp.testTargetPlaceholder')} />
            </label>
            <button class="btn tertiary" on:click={testDraft} disabled={testing || !testTarget.trim()}>
              {#if testing}
                <Icon name="loader-circle" class="icon-spin" size={14} /> {$_('settings.snmp.testing')}
              {:else}
                <Icon name="plug" size={14} /> {$_('settings.snmp.testButton')}
              {/if}
            </button>
          </div>
          {#if !$anonMode}
            <datalist id="profile-test-targets">
              {#each targets as t (t.address)}
                <option value={t.address}>{t.label}</option>
              {/each}
            </datalist>
          {/if}
          {#if testResult}
            <p class="result" class:bad={!!testResult.error}>
              <Icon name={testResult.error ? 'circle-x' : 'circle-check'} size={14} />
              {#if testResult.error}
                {testResult.error}
              {:else}
                sysDescr: {$anonMode ? maskSysDescr(testResult.result?.value || 'OK') : (testResult.result?.value || 'OK')}
              {/if}
            </p>
          {/if}
        </fieldset>
      </div>

      <div class="editor-actions">
        {#if !editing.isNew}
          {#if pendingDelete === editing.draft.id}
            <span class="confirm-text">
              {$_('profiles.deleteConfirm', {
                values: { name: editing.draft.name, count: profileUsage(settings, editing.draft.id).length },
              })}
            </span>
            <button class="btn danger" on:click={() => remove(editing.draft.id)}>{$_('common.delete')}</button>
          {:else}
            <button class="btn tertiary" on:click={() => requestDelete(findProfile(settings, editing.draft.id) || editing.draft)}>
              <Icon name="trash-2" size={14} /> {$_('common.delete')}
            </button>
          {/if}
        {/if}
        <span class="spacer"></span>
        <span class="apply-hint">{$_('profiles.applyHint')}</span>
        <button class="btn secondary" on:click={() => (editing = null)}>{$_('common.cancel')}</button>
        <button class="btn" on:click={apply}>{$_('profiles.apply')}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .profiles {
    margin-bottom: 20px;
  }

  .sec-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    flex-wrap: wrap;
  }

  .sec-head h4 {
    display: flex;
    align-items: center;
    gap: 7px;
    margin: 0;
    font-size: 1.05em;
    font-weight: 600;
  }

  .add-row {
    display: flex;
    gap: 6px;
  }

  .hint {
    font-size: 0.8em;
    color: var(--text-muted);
    margin: 6px 0 10px;
    line-height: 1.45;
  }

  .empty-state {
    font-size: 0.85em;
    color: var(--text-muted);
    font-style: italic;
    margin: 6px 0;
  }

  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border-color);
    border-radius: 5px;
  }

  .list li {
    display: grid;
    grid-template-columns: auto minmax(90px, 1fr) minmax(0, 2fr) auto auto auto auto auto auto;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border-color);
    font-size: 0.85em;
  }

  .list li:last-child {
    border-bottom: none;
  }

  .badge {
    padding: 1px 7px;
    border-radius: 9px;
    font-size: 0.82em;
    font-weight: 600;
    background-color: var(--bg-lighter-color);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .badge.v3 {
    background-color: var(--accent-subtle);
    color: var(--accent-color);
  }

  .name {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .detail {
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .chip-flag {
    font-size: 0.72em;
    padding: 1px 6px;
    border-radius: 8px;
    color: var(--warning-color);
    border: 1px solid var(--warning-color);
    white-space: nowrap;
  }

  .trap-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 7px;
    border: 1px solid var(--accent-color);
    border-radius: 9px;
    background-color: var(--accent-subtle);
    color: var(--accent-color);
    font-size: 0.78em;
    font-weight: 600;
    white-space: nowrap;
    cursor: pointer;
  }

  .trap-chip.off {
    border-color: var(--border-color);
    background-color: transparent;
    color: var(--text-muted);
    text-decoration: line-through;
  }

  .usage {
    font-size: 0.82em;
    color: var(--text-color);
    white-space: nowrap;
  }

  .usage.unused {
    color: var(--text-muted);
    font-style: italic;
  }

  .confirm {
    grid-column: 1 / -1;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 6px 8px;
    border-radius: 4px;
    background-color: var(--error-subtle);
    color: var(--text-color);
  }

  .confirm span {
    flex: 1;
    min-width: 200px;
  }

  /* ---------- the editor ---------- */

  .editor-overlay {
    position: fixed;
    inset: 0;
    background-color: var(--backdrop-color-strong);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1400;
  }

  .editor {
    display: flex;
    flex-direction: column;
    width: min(640px, 92vw);
    max-height: 88vh;
    overflow: hidden;
    background-color: var(--bg-light-color);
    border: 1px solid var(--border-color);
    border-radius: 8px;
  }

  .editor h3 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    padding: 16px 20px 12px;
    font-size: 1.05em;
  }

  .editor-body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 2px 20px 6px;
  }

  .fld-row {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .fld {
    display: block;
    margin-bottom: 12px;
  }

  .fld.grow {
    flex: 1;
    min-width: 200px;
  }

  .fld > span {
    display: block;
    font-size: 0.8em;
    color: var(--text-dimmed);
    margin-bottom: 4px;
  }

  .fld input {
    width: 100%;
    padding: 7px 9px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .problem {
    display: block;
    margin-top: 4px;
    font-size: 0.78em;
    font-style: normal;
  }

  .problem.error {
    color: var(--error-color);
  }

  .problem.warn {
    color: var(--warning-color);
  }

  .segmented {
    display: inline-flex;
    border: 1px solid var(--border-color);
    border-radius: 5px;
    overflow: hidden;
  }

  .segmented button {
    padding: 7px 14px;
    background-color: var(--bg-lighter-color);
    border: none;
    border-radius: 0;
    color: var(--text-muted);
    cursor: pointer;
    font-variant-numeric: tabular-nums;
  }

  .segmented button + button {
    border-left: 1px solid var(--border-color);
  }

  .segmented button.active {
    background-color: var(--accent-subtle);
    color: var(--accent-color);
    font-weight: 600;
  }

  fieldset {
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 12px 14px;
    margin: 0 0 14px;
  }

  legend {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 0 6px;
    font-weight: 500;
  }

  .count {
    font-size: 0.8em;
    padding: 0 6px;
    border-radius: 8px;
    background-color: var(--bg-lighter-color);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .filter {
    width: 100%;
    margin-bottom: 6px;
    padding: 5px 8px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .checklist {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 190px;
    overflow-y: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .checklist li + li {
    border-top: 1px solid var(--border-color);
  }

  .checklist label {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    font-size: 0.85em;
    cursor: pointer;
  }

  .checklist label:hover {
    background-color: var(--hover-overlay);
  }

  .checklist label.off .addr {
    color: var(--text-muted);
    text-decoration: line-through;
  }

  .checklist input {
    accent-color: var(--accent-color);
  }

  .addr {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  .lbl {
    color: var(--text-muted);
  }

  .note {
    margin-left: auto;
    font-size: 0.8em;
    color: var(--text-muted);
    font-style: italic;
  }

  .test .btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    margin-top: 21px;
  }

  .result {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 4px 0 0;
    font-size: 0.85em;
    color: var(--success-color);
    word-break: break-word;
  }

  .result.bad {
    color: var(--error-color);
  }

  .editor-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 12px 20px;
    border-top: 1px solid var(--border-color);
  }

  .editor-actions .btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }

  .spacer {
    flex: 1;
  }

  .apply-hint {
    font-size: 0.78em;
    color: var(--text-muted);
  }

  .confirm-text {
    font-size: 0.82em;
    color: var(--error-color);
    max-width: 260px;
  }
</style>
