<script>
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { notificationStore } from '../stores/notifications';
  import { TestConnection } from '../bridge/App';
  import { buildTestRequest } from '../utils/snmpParams';
  import { anonMode, maskSysDescr } from '../utils/anonymize';
  import Icon from '../Icon.svelte';
  import UsmFields from './UsmFields.svelte';
  import CredentialProfiles from './CredentialProfiles.svelte';
  import TrapAcceptToggle from './TrapAcceptToggle.svelte';
  import { credentialState, credentialBackend } from '../utils/crypto';
  import { findProfile, withProfile } from '../utils/credentialProfiles.js';

  export let settings;

  let testTarget = '';
  // '' tests the default identifiers in the version picked beside it; a
  // profile id tests that profile, which carries its own version.
  let testIdentity = '';
  let testVersion = 'v2c';
  let isTesting = false;
  let testResult = null;

  // A profile deleted while it was picked here falls back to the defaults
  // rather than testing something that no longer exists.
  $: if (testIdentity && !findProfile(settings, testIdentity)) testIdentity = '';

  async function handleTestConnection() {
    const t = get(_);
    if (!testTarget.trim()) {
      notificationStore.add(t('settings.snmp.enterTarget'), 'error');
      return;
    }

    isTesting = true;
    testResult = null;

    try {
      const identity = testIdentity
        ? withProfile(settings, testIdentity)
        : { ...settings, snmpVersion: testVersion };
      const result = await TestConnection(buildTestRequest(identity, testTarget.trim()));

      testResult = result;
      if (result.error) {
        notificationStore.add(t('settings.snmp.testFailed', { values: { error: result.error } }), 'error');
      } else {
        notificationStore.add(t('settings.snmp.testSuccess', { values: { target: testTarget } }), 'success');
      }
    } catch (err) {
      testResult = { error: String(err) };
      notificationStore.add(t('settings.snmp.testError', { values: { error: String(err) } }), 'error');
    } finally {
      isTesting = false;
    }
  }
</script>

<!-- Where the credentials are typed is where the honest statement about them
     belongs. It names the backend rather than saying "encrypted", because what
     that word buys differs per platform: DPAPI and the Keychain tie the key to
     the account, while the file backend keeps it away from OTHER accounts and
     from a copied profile, and nothing more. -->
{#if $credentialState === 'nostore'}
  <p class="cred-banner warn">
    <Icon name="triangle-alert" size={14} />
    {$_('settings.snmp.credNoStore')}
  </p>
{:else if $credentialState === 'locked'}
  <p class="cred-banner err">
    <Icon name="circle-x" size={14} />
    {$_('settings.snmp.credLocked', { values: { backend: $credentialBackend } })}
  </p>
{:else if $credentialBackend}
  <p class="cred-banner ok">
    <Icon name="shield-check" size={14} />
    {$_('settings.snmp.credStored', { values: { backend: $credentialBackend } })}
  </p>
{/if}

<!-- The default identifiers stay what they always were — the community and the
     v3 block every target uses unless it is given something else — and the
     profiles below are that something else. -->
<section class="defaults">
  <h4 class="section-title">{$_('profiles.defaultTitle')}</h4>
  <p class="hint">{$_('profiles.defaultHint')}</p>

  <fieldset>
    <legend>{$_('settings.snmp.v1v2cTitle')}</legend>
    <div class="settings-grid single-column">
      <div class="form-group">
        <label for="community">{$_('settings.snmp.community')}</label>
        {#if $anonMode}
          <input id="community" type="password" bind:value={settings.community} />
        {:else}
          <input id="community" type="text" bind:value={settings.community} />
        {/if}
      </div>
    </div>
  </fieldset>

  <fieldset>
    <legend>{$_('settings.snmp.v3Title')}</legend>
    <UsmFields bind:v3={settings.v3} idPrefix="v3" />
    <TrapAcceptToggle bind:checked={settings.traps.acceptDefaultUser} hint={$_('profiles.acceptTrapsDefaultHint')} />
  </fieldset>
</section>

<CredentialProfiles bind:settings />

<fieldset class="test-connection">
  <legend><Icon name="plug" size={15} /> {$_('settings.snmp.testTitle')}</legend>
  <div class="test-form">
    <div class="form-group">
      <label for="test-target">{$_('settings.snmp.testTarget')}</label>
      <input
        id="test-target"
        type="text"
        bind:value={testTarget}
        placeholder={$_('settings.snmp.testTargetPlaceholder')}
      />
    </div>
    <div class="form-group">
      <label for="test-identity">{$_('profiles.identifiers')}</label>
      <select id="test-identity" bind:value={testIdentity}>
        <option value="">{$_('profiles.useDefault')}</option>
        {#each settings.credentialProfiles || [] as p (p.id)}
          <option value={p.id}>{p.name} · {p.version}</option>
        {/each}
      </select>
    </div>
    {#if !testIdentity}
      <div class="form-group narrow">
        <label for="test-version">{$_('settings.snmp.testVersion')}</label>
        <select id="test-version" bind:value={testVersion}>
          <option value="v1">v1</option>
          <option value="v2c">v2c</option>
          <option value="v3">v3</option>
        </select>
      </div>
    {/if}
    <button
      class="btn test-btn"
      on:click={handleTestConnection}
      disabled={isTesting || !testTarget.trim()}
    >
      {#if isTesting}<Icon name="loader-circle" class="icon-spin" /> {$_('settings.snmp.testing')}{:else}<Icon name="plug" /> {$_('settings.snmp.testButton')}{/if}
    </button>
  </div>
  {#if testResult}
    <div class="test-result" class:success={!testResult.error} class:error={testResult.error}>
      {#if testResult.error}
        <span class="result-icon"><Icon name="circle-x" class="icon-error" size={16} /></span>
        <span class="result-text">{testResult.error}</span>
      {:else}
        <span class="result-icon"><Icon name="circle-check" class="icon-success" size={16} /></span>
        <span class="result-text">sysDescr: {$anonMode ? maskSysDescr(testResult.result?.value || 'OK') : (testResult.result?.value || 'OK')}</span>
      {/if}
    </div>
  {/if}
</fieldset>

<style>
  .defaults {
    margin-bottom: 22px;
  }

  .section-title {
    margin: 0;
    font-size: 1.05em;
    font-weight: 600;
  }

  .hint {
    font-size: 0.8em;
    color: var(--text-muted);
    margin: 6px 0 12px;
    line-height: 1.45;
  }

  fieldset {
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 20px;
    margin-bottom: 20px;
  }

  legend {
    padding: 0 10px;
    color: var(--text-color);
    font-weight: 500;
    font-size: 1.1em;
  }

  .settings-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 15px 20px;
    margin-top: 10px;
  }

  .settings-grid.single-column {
    grid-template-columns: 1fr;
    max-width: 300px;
  }

  .form-group {
    flex-direction: column;
    align-items: stretch;
    /* Allow grid/flex items to shrink below the intrinsic width of long
       <select> options instead of forcing the column wider. */
    min-width: 0;
  }

  .form-group label {
    margin-bottom: 5px;
    font-size: 0.9em;
    color: var(--text-light);
  }

  input, select {
    width: 100%;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .test-connection {
    margin-top: 15px;
    background-color: var(--accent-subtle);
    border-color: var(--accent-border);
  }

  .test-form {
    display: flex;
    gap: 15px;
    align-items: flex-end;
    flex-wrap: wrap;
  }

  .test-form .form-group {
    flex: 1;
    min-width: 150px;
    margin-bottom: 0;
  }

  .test-form .form-group.narrow {
    flex: 0 0 110px;
    min-width: 0;
  }

  .test-btn {
    flex-shrink: 0;
    height: 38px;
  }

  .test-result {
    margin-top: 12px;
    padding: 10px 12px;
    border-radius: 4px;
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 0.9em;
  }

  .test-result.success {
    background-color: var(--success-subtle-medium);
    border: 1px solid var(--success-border-strong);
    color: var(--success-color);
  }

  .test-result.error {
    background-color: var(--error-subtle-medium);
    border: 1px solid var(--error-border-strong);
    color: var(--error-color);
  }

  .result-text {
    word-break: break-word;
    flex: 1;
  }

  .cred-banner {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    margin: 0 0 10px;
    padding: 6px 8px;
    border-radius: 4px;
  }

  /* The theme's own variables. These read --bg-hover, --text-secondary,
     --warning and --danger, which exist nowhere, so the fallbacks always won:
     a pale banner with grey text on a dark window. */
  .cred-banner.ok {
    color: var(--text-muted);
    background: var(--bg-lighter-color);
  }

  .cred-banner.warn {
    color: var(--warning-color);
    background: var(--warning-subtle);
  }

  .cred-banner.err {
    color: var(--error-color);
    background: var(--error-subtle);
  }
</style>
