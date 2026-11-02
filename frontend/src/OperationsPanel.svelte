<script>
  import { SnmpGet, SnmpSet, SnmpSetMultiple, SnmpWalk, SnmpGetNext, SnmpGetBulk, ResolveOids } from './bridge/App';
  import { buildSnmpRequest, buildSetRequest, buildSetMultiRequest, buildGetBulkRequest } from './utils/snmpParams';
  import { notificationStore } from './stores/notifications';
  import { settingsStore } from './stores/settingsStore';
  import { historyStore } from './stores/historyStore';
  import { getTargetsAsArray, groupTargetsByConfig, getEffectiveSettings } from './utils/targets';
  import { isNodeWritable } from './utils/mibTree';
  import { mibStore } from './stores/mibStore';
  import ResultsDisplay from './operations/ResultsDisplay.svelte';
  import Icon from './Icon.svelte';
  import SavedQueries from './operations/SavedQueries.svelte';
  import RecentHistory from './operations/RecentHistory.svelte';
  import OperationTabs from './operations/OperationTabs.svelte';
  import SetConfirmModal from './operations/SetConfirmModal.svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';

  /**
   * @typedef {import('../bindings/SnmpLens/pkg/mib/models.js').Node} MibNode
   */

  /** @type {MibNode | null} */
  export let selectedNode = null;
  
  /** @type {{ type: string, oid: string, name: string } | null} */
  export let pendingAction = null;

  let activeOperation = 'GET'; // 'GET', 'SET', 'GETNEXT', 'GETBULK', or 'WALK'

  let snmpGetOid = '';
  let snmpSetOid = '';
  let snmpSetValue = '';
  let snmpSetType = 'string';
  let showSetConfirm = false;
  let dontAskSetAgain = false;
  let snmpGetNextOid = '';
  let snmpGetBulkOid = '';
  let snmpWalkOid = '';
  let maxRepetitions = 10;
  let nonRepeaters = 0;

  let bulkResults = [];
  let isLoading = false;
  let oidInfoCache = {}; // Maps OID string -> {name, description, syntax, enumValues}

  // Resolve OID info (enum values, names) for a batch of OIDs from results
  async function resolveResultOids(results) {
    const allOids = new Set();
    for (const res of results) {
      if (res.error) continue;
      if (res.result?.type === 'WalkResponse' || res.result?.type === 'GetBulkResponse') {
        if (Array.isArray(res.result.value)) {
          for (const item of res.result.value) {
            if (item.oid) allOids.add(item.oid);
          }
        }
      } else if (res.result?.oid) {
        allOids.add(res.result.oid);
      }
    }
    if (allOids.size > 0) {
      try {
        oidInfoCache = await ResolveOids([...allOids]);
      } catch (e) {
        console.error('Failed to resolve OIDs:', e);
      }
    }
  }
  let previousSelectedNode = null;
  let lastAutoFilledValue = ''; // Store the last auto-filled value

  // Track user manual edits to prevent auto-overwrite
  let userEditedGetOid = false;
  let userEditedSetOid = false;
  let userEditedGetNextOid = false;
  let userEditedGetBulkOid = false;
  let userEditedWalkOid = false;

  // Instance index for table columns (default .0)
  let instanceIndex = '0';
  
  // Auto options are now stored in settingsStore for persistence
  $: autoGetEnabled = $settingsStore.autoGetEnabled;
  $: autoFillSetEnabled = $settingsStore.autoFillSetEnabled;

  // Reactive variable to track if current node is writable
  $: isCurrentNodeWritable = selectedNode ? isNodeWritable(selectedNode) : false;

  // Get readable reason why SET is disabled
  $: setDisabledReason = selectedNode
    ? (!isCurrentNodeWritable ? $_('operations.setNotWritable', { values: { access: selectedNode.access || 'read-only' } }) : '')
    : $_('operations.selectNodeFirst');

  // Reactive block to update form fields when a new node is selected
  $: {
    if (selectedNode) {
      const isNewNodeSelection = selectedNode !== previousSelectedNode;

      if (isNewNodeSelection) {
        // Calculate the appropriate OID based on node type
        let baseOid = selectedNode.oid;
        let oidWithInstance = baseOid;

        if (selectedNode.mibType === 'Scalar') {
          oidWithInstance = baseOid + '.0';
        } else if (selectedNode.mibType === 'Column') {
          // Reset instance index for new column selection
          instanceIndex = '0';
          oidWithInstance = baseOid + '.' + instanceIndex;
        }

        if (selectedNode.mibType === 'Table' || selectedNode.mibType === 'Row') {
          activeOperation = 'WALK';
        }

        // Only auto-fill OID fields if user hasn't manually edited them
        if (!userEditedGetOid) {
          snmpGetOid = oidWithInstance;
        }
        if (!userEditedSetOid) {
          snmpSetOid = oidWithInstance;
        }
        if (!userEditedGetNextOid) {
          snmpGetNextOid = oidWithInstance;
        }
        if (!userEditedGetBulkOid) {
          snmpGetBulkOid = baseOid;
        }
        if (!userEditedWalkOid) {
          snmpWalkOid = baseOid; // Walk uses the base OID
        }

        // Reset edit flags for the new node - fresh start
        userEditedGetOid = false;
        userEditedSetOid = false;
        userEditedGetNextOid = false;
        userEditedGetBulkOid = false;
        userEditedWalkOid = false;

        snmpSetType = selectedNode.syntax || 'string';

        // Set a default value for enums
        if (selectedNode.enumValues) {
          snmpSetValue = String(Object.values(selectedNode.enumValues)[0]);
        } else {
          // Keep empty for auto-fill to work, it will be filled by the GET
          snmpSetValue = '';
        }
        lastAutoFilledValue = ''; // Reset the last auto-filled value

        // Auto GET if enabled and node changed
        if (autoGetEnabled && !isLoading) {
          // Only auto-GET for Scalar and Column types
          if (selectedNode.mibType === 'Scalar' || selectedNode.mibType === 'Column') {
            // Delay to ensure UI is updated
            setTimeout(() => handleSnmpGet(), 100);
          }
        }

        previousSelectedNode = selectedNode;
      }
    }
  }

  // Handle pending actions from context menu
  $: {
    if (pendingAction) {
      const { type, oid } = pendingAction;
      
      if (type === 'GET') {
        activeOperation = 'GET';
        snmpGetOid = oid;
      } else if (type === 'SET') {
        activeOperation = 'SET';
        snmpSetOid = oid;
      } else if (type === 'GETNEXT') {
        activeOperation = 'GETNEXT';
        snmpGetNextOid = oid;
      } else if (type === 'GETBULK') {
        activeOperation = 'GETBULK';
        snmpGetBulkOid = oid;
      } else if (type === 'WALK') {
        activeOperation = 'WALK';
        snmpWalkOid = oid;
      } else if (type === 'WALK_TABLE') {
        activeOperation = 'WALK';
        snmpWalkOid = oid;
      }
    }
  }

  // Update settings when targets or version change
  function updateTargets(value) {
    settingsStore.save({ ...$settingsStore, targets: value });
  }
  

  // Convert value to appropriate format for SET, handling enumerations
  function formatValueForSet(value, node) {
    if (!node || !node.enumValues) {
      return String(value);
    }

    // If the node has enum values, check if the value matches one
    const numValue = Number(value);
    if (!isNaN(numValue)) {
      // Find the enum name for this value
      for (const [enumName, enumValue] of Object.entries(node.enumValues)) {
        if (enumValue === numValue) {
          // Return just the numeric value (the select will show the name)
          return String(numValue);
        }
      }
    }

    return String(value);
  }

  // Detect if an enumeration is a boolean type (True/False, Enabled/Disabled, etc.)
  function isBooleanEnum(node) {
    if (!node || !node.enumValues) return false;

    const entries = Object.entries(node.enumValues);
    if (entries.length !== 2) return false;

    // Common boolean patterns in SNMP MIBs
    const booleanPatterns = [
      ['true', 'false'],
      ['false', 'true'],
      ['enabled', 'disabled'],
      ['disabled', 'enabled'],
      ['up', 'down'],
      ['down', 'up'],
      ['on', 'off'],
      ['off', 'on'],
      ['yes', 'no'],
      ['no', 'yes'],
      ['active', 'inactive'],
      ['inactive', 'active'],
    ];

    const names = entries.map(([name]) => name.toLowerCase()).sort();

    return booleanPatterns.some(pattern => {
      const sortedPattern = [...pattern].sort();
      return names[0] === sortedPattern[0] && names[1] === sortedPattern[1];
    });
  }

  // Get the "true" and "false" values for a boolean enum
  function getBooleanEnumValues(node) {
    if (!node || !node.enumValues) return { trueValue: '1', falseValue: '0', trueLabel: 'True', falseLabel: 'False' };

    const entries = Object.entries(node.enumValues);

    // Define which names represent "true" state
    const trueNames = ['true', 'enabled', 'up', 'on', 'yes', 'active'];

    let trueEntry = null;
    let falseEntry = null;

    for (const [name, value] of entries) {
      if (trueNames.includes(name.toLowerCase())) {
        trueEntry = { name, value };
      } else {
        falseEntry = { name, value };
      }
    }

    // If we couldn't determine which is true/false, use the order
    if (!trueEntry || !falseEntry) {
      trueEntry = { name: entries[0][0], value: entries[0][1] };
      falseEntry = { name: entries[1][0], value: entries[1][1] };
    }

    return {
      trueValue: String(trueEntry.value),
      falseValue: String(falseEntry.value),
      trueLabel: trueEntry.name,
      falseLabel: falseEntry.name
    };
  }

  // Check if current SET value is the "true" value for boolean enum
  $: booleanEnumValues = selectedNode && isBooleanEnum(selectedNode)
    ? getBooleanEnumValues(selectedNode)
    : null;
  $: isBooleanTrue = booleanEnumValues && snmpSetValue === booleanEnumValues.trueValue;

  // Manual OID input handlers - mark as user-edited to prevent auto-overwrite
  function handleGetOidInput(event) {
    snmpGetOid = event.target.value;
    userEditedGetOid = true;
  }

  function handleSetOidInput(event) {
    snmpSetOid = event.target.value;
    userEditedSetOid = true;
  }

  function handleGetNextOidInput(event) {
    snmpGetNextOid = event.target.value;
    userEditedGetNextOid = true;
  }

  function handleGetBulkOidInput(event) {
    snmpGetBulkOid = event.target.value;
    userEditedGetBulkOid = true;
  }

  function handleWalkOidInput(event) {
    snmpWalkOid = event.target.value;
    userEditedWalkOid = true;
  }

  // Handle instance index change for table columns
  function handleInstanceChange(event) {
    instanceIndex = event.target.value;
    if (selectedNode && selectedNode.mibType === 'Column') {
      const newOid = selectedNode.oid + '.' + instanceIndex;
      snmpGetOid = newOid;
      snmpSetOid = newOid;
    }
  }

  // Handle clicking a WALK result row to populate OID fields
  function handleWalkResultClick(walkItem) {
    const fullOid = walkItem.oid;

    // Set OID fields with the clicked OID
    snmpGetOid = fullOid;
    snmpSetOid = fullOid;

    // Mark as manually edited so they won't be overwritten
    userEditedGetOid = true;
    userEditedSetOid = true;

    // Try to extract instance from OID if a Column node is selected
    if (selectedNode && selectedNode.mibType === 'Column') {
      const baseOid = selectedNode.oid;
      if (fullOid.startsWith(baseOid + '.')) {
        instanceIndex = fullOid.substring(baseOid.length + 1);
      }
    }

    // Switch to GET tab for convenience
    activeOperation = 'GET';

    const t = get(_);
    notificationStore.add(t('operations.oidLoaded', { values: { oid: fullOid } }), 'info');
  }

  // Execute an SNMP operation across target groups (handles per-target overrides)
  async function executeGrouped(oid, snmpFn, buildFn, ...extraBuildArgs) {
    const groups = groupTargetsByConfig($settingsStore);
    const allTargets = groups.flatMap(g => g.targets);
    if (allTargets.length === 0) return [];
    const promises = groups.map(group =>
      snmpFn(buildFn(group.effectiveSettings, group.targets, oid, ...extraBuildArgs))
    );
    const resultArrays = await Promise.all(promises);
    return resultArrays.flat();
  }

  // A conceptual row being created or destroyed, from the table view.
  //
  // It runs through executeGrouped like every other operation, so a row is
  // written with the same per-target credentials the rest of the panel uses —
  // a second connection path would be a second place for those to be wrong.
  // The walk is re-run afterwards because a created row is invisible until it
  // is read back, and "did it work" is the only question that matters here.
  async function handleTableRowWrite(e) {
    const { vars, label, kind, target } = e.detail;
    const t = get(_);
    if (!vars?.length) return;

    // ONE device: the one whose table the row was clicked in.
    //
    // Not executeGrouped. A conceptual row belongs to the agent that holds it,
    // and index 3 of ipNetToMediaTable on another switch is a different ARP
    // entry — writing destroy(6) to every configured target would remove rows
    // on devices nobody looked at, and report success for all of them.
    if (!target) {
      notificationStore.add(t('operations.enterTarget'), 'error');
      return;
    }

    isLoading = true;
    try {
      const effective = getEffectiveSettings($settingsStore, target);
      const results = await SnmpSetMultiple(buildSetMultiRequest(effective, [target], '', vars));
      const failed = results.filter(r => r?.error);
      if (failed.length) {
        notificationStore.add(
          t('results.rowWriteFailed', { values: { label, error: failed[0].error } }), 'error');
      } else {
        notificationStore.add(
          t(kind === 'destroy' ? 'results.rowDestroyed' : 'results.rowCreated',
            { values: { label } }), 'success');
        if (activeOperation === 'WALK') await handleSnmpWalk();
        else if (activeOperation === 'GETBULK') await handleSnmpGetBulk();
      }
    } catch (err) {
      notificationStore.add(String(err?.message || err), 'error');
    } finally {
      isLoading = false;
    }
  }

  // ============ SNMP OPERATION HANDLERS ============

  async function handleSnmpGet() {
    bulkResults = [];
    isLoading = true;
    const startTime = Date.now();
    const targetArray = getTargetsAsArray($settingsStore.targets);
    const t = get(_);

    try {
      if (targetArray.length === 0) {
        notificationStore.add(t('operations.enterTarget'), 'error');
        return;
      }
      bulkResults = await executeGrouped(snmpGetOid, SnmpGet, buildSnmpRequest);

      // Resolve OID info for enum decoding
      await resolveResultOids(bulkResults);

      // Save to history
      historyStore.add({
        operation: 'GET',
        targets: targetArray,
        oid: snmpGetOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        results: bulkResults,
        success: !bulkResults.some(r => r.error)
      });
      
      // Auto-fill SET if enabled and node is writable
      if (autoFillSetEnabled && selectedNode && isCurrentNodeWritable && bulkResults.length > 0) {
        const firstResult = bulkResults[0];
        if (firstResult && !firstResult.error && firstResult.result?.value !== undefined) {
          const rawValue = firstResult.result.value;
          
          // For enum values, we need to use the string numeric value to match SELECT options
          if (selectedNode.enumValues) {
            const numValue = Number(rawValue);
            // Find matching enum to display in notification
            let displayValue = `${numValue}`;
            for (const [enumName, enumValue] of Object.entries(selectedNode.enumValues)) {
              if (enumValue === numValue) {
                displayValue = `${enumName}(${numValue})`;
                break;
              }
            }
            // Assign the string numeric value for SELECT binding (HTML select values are always strings)
            snmpSetValue = String(numValue);
            lastAutoFilledValue = String(numValue);
            notificationStore.add(t('operations.getAutoFilled', { values: { value: displayValue } }), 'success');
          } else {
            // For non-enum values, use string format
            const formattedValue = formatValueForSet(rawValue, selectedNode);
            snmpSetValue = formattedValue;
            lastAutoFilledValue = formattedValue;
            notificationStore.add(t('operations.getAutoFilled', { values: { value: formattedValue } }), 'success');
          }
        } else {
          notificationStore.add(t('operations.getCompleted', { values: { count: targetArray.length } }), 'success');
        }
      } else {
        notificationStore.add(t('operations.getCompleted', { values: { count: targetArray.length } }), 'success');
      }
    } catch (error) {
      console.error(error);
      notificationStore.add(t('operations.getFailed', { values: { error: String(error) } }), 'error');
      
      // Save error to history
      historyStore.add({
        operation: 'GET',
        targets: targetArray,
        oid: snmpGetOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        error: String(error),
        success: false
      });
    } finally {
      isLoading = false;
    }
  }

  // Entry point for the SET button: validates, then either asks for
  // confirmation (default) or executes directly when the user opted out.
  async function handleSnmpSet() {
    const t = get(_);
    // Check if the node is writable before attempting SET
    if (!isCurrentNodeWritable) {
      notificationStore.add(setDisabledReason, 'error');
      return;
    }
    if (getTargetsAsArray($settingsStore.targets).length === 0) {
      notificationStore.add(t('operations.enterTarget'), 'error');
      return;
    }
    if ($settingsStore.skipSetConfirm) {
      await performSnmpSet();
    } else {
      dontAskSetAgain = false;
      showSetConfirm = true;
    }
  }

  // Confirm handler: optionally persists the opt-out, then runs the SET.
  async function confirmSetAndExecute() {
    showSetConfirm = false;
    if (dontAskSetAgain) {
      await settingsStore.save({ ...$settingsStore, skipSetConfirm: true });
    }
    await performSnmpSet();
  }

  async function performSnmpSet() {
    const t = get(_);
    bulkResults = [];
    isLoading = true;
    const startTime = Date.now();
    const targetArray = getTargetsAsArray($settingsStore.targets);

    try {
      if (targetArray.length === 0) {
        notificationStore.add(t('operations.enterTarget'), 'error');
        return;
      }
      bulkResults = await executeGrouped(snmpSetOid, SnmpSet, buildSetRequest, String(snmpSetValue), snmpSetType);
      
      // Save to history
      historyStore.add({
        operation: 'SET',
        targets: targetArray,
        oid: snmpSetOid,
        value: String(snmpSetValue),
        valueType: snmpSetType,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        results: bulkResults,
        success: !bulkResults.some(r => r.error)
      });
      
      notificationStore.add(t('operations.setCompleted', { values: { count: targetArray.length } }), 'success');
    } catch (error) {
      console.error(error);
      notificationStore.add(t('operations.setFailed', { values: { error: String(error) } }), 'error');
      
      // Save error to history
      historyStore.add({
        operation: 'SET',
        targets: targetArray,
        oid: snmpSetOid,
        value: String(snmpSetValue),
        valueType: snmpSetType,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        error: String(error),
        success: false
      });
    } finally {
      isLoading = false;
    }
  }

  async function handleSnmpGetNext() {
    bulkResults = [];
    isLoading = true;
    const startTime = Date.now();
    const targetArray = getTargetsAsArray($settingsStore.targets);
    const t = get(_);

    try {
      if (targetArray.length === 0) {
        notificationStore.add(t('operations.enterTarget'), 'error');
        return;
      }
      bulkResults = await executeGrouped(snmpGetNextOid, SnmpGetNext, buildSnmpRequest);

      await resolveResultOids(bulkResults);

      historyStore.add({
        operation: 'GETNEXT',
        targets: targetArray,
        oid: snmpGetNextOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        results: bulkResults,
        success: !bulkResults.some(r => r.error)
      });

      notificationStore.add(t('operations.getNextCompleted', { values: { count: targetArray.length } }), 'success');
    } catch (error) {
      console.error(error);
      notificationStore.add(t('operations.getNextFailed', { values: { error: String(error) } }), 'error');

      historyStore.add({
        operation: 'GETNEXT',
        targets: targetArray,
        oid: snmpGetNextOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        error: String(error),
        success: false
      });
    } finally {
      isLoading = false;
    }
  }

  async function handleSnmpGetBulk() {
    bulkResults = [];
    isLoading = true;
    const startTime = Date.now();
    const targetArray = getTargetsAsArray($settingsStore.targets);
    const t = get(_);

    try {
      if (targetArray.length === 0) {
        notificationStore.add(t('operations.enterTarget'), 'error');
        return;
      }
      if ($settingsStore.snmpVersion === 'v1') {
        notificationStore.add(t('operations.getBulkV1Warning'), 'error');
        return;
      }
      bulkResults = await executeGrouped(snmpGetBulkOid, SnmpGetBulk, buildGetBulkRequest, nonRepeaters, maxRepetitions);

      await resolveResultOids(bulkResults);

      let totalResults = 0;
      bulkResults.forEach(res => {
        if (res.result && res.result.value && Array.isArray(res.result.value)) {
          totalResults += res.result.value.length;
        }
      });

      historyStore.add({
        operation: 'GETBULK',
        targets: targetArray,
        oid: snmpGetBulkOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        results: bulkResults,
        totalResults,
        success: !bulkResults.some(r => r.error)
      });

      notificationStore.add(t('operations.getBulkCompleted', { values: { count: totalResults } }), 'success');
    } catch (error) {
      console.error(error);
      notificationStore.add(t('operations.getBulkFailed', { values: { error: String(error) } }), 'error');

      historyStore.add({
        operation: 'GETBULK',
        targets: targetArray,
        oid: snmpGetBulkOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        error: String(error),
        success: false
      });
    } finally {
      isLoading = false;
    }
  }

  async function handleSnmpWalk() {
    bulkResults = [];
    isLoading = true;
    const startTime = Date.now();
    const targetArray = getTargetsAsArray($settingsStore.targets);
    const t = get(_);

    try {
      if (targetArray.length === 0) {
        notificationStore.add(t('operations.enterTarget'), 'error');
        return;
      }
      bulkResults = await executeGrouped(snmpWalkOid, SnmpWalk, buildSnmpRequest);

      // Resolve OID info for enum decoding
      await resolveResultOids(bulkResults);

      // Calculate total results
      let totalResults = 0;
      bulkResults.forEach(res => {
        if (res.result && res.result.value && Array.isArray(res.result.value)) {
          totalResults += res.result.value.length;
        }
      });

      // Save to history
      historyStore.add({
        operation: 'WALK',
        targets: targetArray,
        oid: snmpWalkOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        results: bulkResults,
        totalResults,
        success: !bulkResults.some(r => r.error)
      });
      
      notificationStore.add(t('operations.walkCompleted', { values: { count: targetArray.length, total: totalResults } }), 'success');
    } catch (error) {
      console.error(error);
      notificationStore.add(t('operations.walkFailed', { values: { error: String(error) } }), 'error');
      
      // Save error to history
      historyStore.add({
        operation: 'WALK',
        targets: targetArray,
        oid: snmpWalkOid,
        version: $settingsStore.snmpVersion,
        duration: Date.now() - startTime,
        error: String(error),
        success: false
      });
    } finally {
      isLoading = false;
    }
  }

  function handleLoadQuery(event) {
    const q = event.detail;
    activeOperation = q.operation;
    if (q.operation === 'GET') snmpGetOid = q.oid;
    else if (q.operation === 'SET') {
      snmpSetOid = q.oid;
      if (q.params?.value) snmpSetValue = q.params.value;
      if (q.params?.type) snmpSetType = q.params.type;
    } else if (q.operation === 'GETNEXT') snmpGetNextOid = q.oid;
    else if (q.operation === 'GETBULK') {
      snmpGetBulkOid = q.oid;
      if (q.params?.maxRepetitions) maxRepetitions = q.params.maxRepetitions;
      if (q.params?.nonRepeaters !== undefined) nonRepeaters = q.params.nonRepeaters;
    } else if (q.operation === 'WALK') snmpWalkOid = q.oid;
  }

  function handleWalkResultClickEvent(event) {
    handleWalkResultClick(event.detail);
  }
</script>

<div class="panel">
  <!-- Operation Tabs -->
  <OperationTabs bind:activeOperation snmpVersion={$settingsStore.snmpVersion} />

  <SavedQueries
    {activeOperation}
    snmpGetOid={snmpGetOid}
    snmpSetOid={snmpSetOid}
    snmpGetNextOid={snmpGetNextOid}
    snmpGetBulkOid={snmpGetBulkOid}
    snmpWalkOid={snmpWalkOid}
    snmpSetValue={snmpSetValue}
    snmpSetType={snmpSetType}
    {maxRepetitions}
    {nonRepeaters}
    on:loadQuery={handleLoadQuery}
  />

  <!-- Operation Forms -->
  <div class="operation-form">
    {#if activeOperation === 'GET'}
      <div class="form-content">
        <div class="oid-row">
          <label for="get-oid">{$_('operations.oidLabel')}</label>
          <input id="get-oid" type="text" value={snmpGetOid} on:input={handleGetOidInput} placeholder={$_('operations.oidPlaceholder')} />
          <label class="auto-toggle" title={$_('operations.autoGetHint')}>
            <input
              type="checkbox"
              checked={$settingsStore.autoGetEnabled}
              on:change={(e) => settingsStore.save({...$settingsStore, autoGetEnabled: /** @type {HTMLInputElement} */(e.target).checked})}
            />
            <Icon name="zap" size={14} /> {$_('operations.autoGet')}
          </label>
          <label class="auto-toggle" title={$_('operations.autoFillSetHint')}>
            <input
              type="checkbox"
              checked={$settingsStore.autoFillSetEnabled}
              on:change={(e) => settingsStore.save({...$settingsStore, autoFillSetEnabled: /** @type {HTMLInputElement} */(e.target).checked})}
            />
            <Icon name="refresh-cw" size={14} /> {$_('operations.autoFillSet')}
          </label>
        </div>
        {#if selectedNode && selectedNode.mibType === 'Column'}
          <div class="form-group instance-group">
            <label for="get-instance">{$_('operations.instanceLabel')}</label>
            <div class="instance-input-wrapper">
              <span class="instance-prefix">{selectedNode.oid}.</span>
              <input id="get-instance" type="text" value={instanceIndex} on:input={handleInstanceChange} placeholder="0" class="instance-input" />
            </div>
            <span class="instance-hint">{$_('operations.instanceHint')}</span>
          </div>
        {:else if selectedNode && selectedNode.mibType === 'Scalar'}
          <div class="form-group instance-group readonly">
            <span class="instance-label">{$_('operations.instanceLabel')}</span>
            <span class="instance-value">{$_('operations.scalarInstance')}</span>
          </div>
        {/if}
        <button class="btn btn-primary" on:click={handleSnmpGet} disabled={isLoading}>
          {#if isLoading}<Icon name="loader-circle" class="icon-spin" /> {$_('common.working')}{:else}<Icon name="download" /> {$_('operations.executeGet')}{/if}
        </button>
      </div>
    {/if}

    {#if activeOperation === 'SET'}
      <div class="form-content">
        {#if selectedNode && !isCurrentNodeWritable}
          <div class="warning-banner">
            <Icon name="triangle-alert" class="icon-warning" size={15} /> {setDisabledReason}
          </div>
        {/if}
        <div class="form-group">
          <label for="set-oid">{$_('operations.oidLabel')}</label>
          <input id="set-oid" type="text" value={snmpSetOid} on:input={handleSetOidInput} placeholder={$_('operations.oidPlaceholder')} />
        </div>
        {#if selectedNode && selectedNode.mibType === 'Column'}
          <div class="form-group instance-group">
            <label for="set-instance">{$_('operations.instanceLabel')}</label>
            <div class="instance-input-wrapper">
              <span class="instance-prefix">{selectedNode.oid}.</span>
              <input id="set-instance" type="text" value={instanceIndex} on:input={handleInstanceChange} placeholder="0" class="instance-input" />
            </div>
          </div>
        {/if}
        <div class="form-group">
          <label for="set-value">{$_('operations.valueLabel')}</label>
          {#if selectedNode && selectedNode.enumValues && isBooleanEnum(selectedNode)}
            <!-- Boolean toggle for True/False enums -->
            <div class="boolean-toggle-container">
              <button
                type="button"
                class="boolean-toggle-btn"
                class:active={!isBooleanTrue}
                disabled={!isCurrentNodeWritable}
                on:click={() => snmpSetValue = booleanEnumValues.falseValue}
              >
                {booleanEnumValues.falseLabel}
              </button>
              <div
                class="boolean-toggle-switch"
                class:checked={isBooleanTrue}
                class:disabled={!isCurrentNodeWritable}
                on:click={() => isCurrentNodeWritable && (snmpSetValue = isBooleanTrue ? booleanEnumValues.falseValue : booleanEnumValues.trueValue)}
                on:keydown={(e) => e.key === 'Enter' && isCurrentNodeWritable && (snmpSetValue = isBooleanTrue ? booleanEnumValues.falseValue : booleanEnumValues.trueValue)}
                role="switch"
                aria-checked={isBooleanTrue}
                tabindex={isCurrentNodeWritable ? 0 : -1}
              >
                <div class="boolean-toggle-thumb"></div>
              </div>
              <button
                type="button"
                class="boolean-toggle-btn"
                class:active={isBooleanTrue}
                disabled={!isCurrentNodeWritable}
                on:click={() => snmpSetValue = booleanEnumValues.trueValue}
              >
                {booleanEnumValues.trueLabel}
              </button>
              <span class="boolean-value-indicator">= {snmpSetValue}</span>
            </div>
          {:else if selectedNode && selectedNode.enumValues}
            <select id="set-value" bind:value={snmpSetValue} disabled={!isCurrentNodeWritable}>
              {#each Object.entries(selectedNode.enumValues) as [name, value]}
                <option value={String(value)}>{name} ({value})</option>
              {/each}
            </select>
          {:else if selectedNode && (selectedNode.syntax?.toLowerCase().includes('int') || selectedNode.syntax?.toLowerCase().includes('gauge'))}
            <input id="set-value" type="number" bind:value={snmpSetValue} disabled={!isCurrentNodeWritable} />
          {:else}
            <input id="set-value" type="text" bind:value={snmpSetValue} disabled={!isCurrentNodeWritable} />
          {/if}
        </div>
        <div class="form-group">
          <label for="set-type">{$_('operations.typeLabel')}</label>
          <input id="set-type" type="text" bind:value={snmpSetType} disabled />
        </div>
        <button 
          class="btn btn-primary" 
          on:click={handleSnmpSet} 
          disabled={isLoading || !selectedNode || !isCurrentNodeWritable}
          title={!isCurrentNodeWritable ? setDisabledReason : ''}
        >
          {#if isLoading}<Icon name="loader-circle" class="icon-spin" /> {$_('common.working')}{:else}<Icon name="upload" /> {$_('operations.executeSet')}{/if}
        </button>
      </div>

      {#if showSetConfirm}
        <SetConfirmModal
          oid={snmpSetOid}
          nodeName={selectedNode?.name || ''}
          value={snmpSetValue}
          type={snmpSetType}
          targets={getTargetsAsArray($settingsStore.targets)}
          bind:dontAsk={dontAskSetAgain}
          on:confirm={confirmSetAndExecute}
          on:cancel={() => showSetConfirm = false}
        />
      {/if}
    {/if}

    {#if activeOperation === 'GETNEXT'}
      <div class="form-content">
        <div class="form-group">
          <label for="getnext-oid">{$_('operations.oidLabel')}</label>
          <input id="getnext-oid" type="text" value={snmpGetNextOid} on:input={handleGetNextOidInput} placeholder={$_('operations.oidPlaceholder')} />
        </div>
        <p class="form-hint">{$_('operations.getNextHint')}</p>
        <button class="btn btn-primary" on:click={handleSnmpGetNext} disabled={isLoading}>
          {#if isLoading}<Icon name="loader-circle" class="icon-spin" /> {$_('common.working')}{:else}<Icon name="arrow-right-to-line" /> {$_('operations.executeGetNext')}{/if}
        </button>
      </div>
    {/if}

    {#if activeOperation === 'GETBULK'}
      <div class="form-content">
        <div class="form-group">
          <label for="getbulk-oid">{$_('operations.oidLabel')}</label>
          <input id="getbulk-oid" type="text" value={snmpGetBulkOid} on:input={handleGetBulkOidInput} placeholder={$_('operations.getBulkOidPlaceholder')} />
        </div>
        <div class="getbulk-params">
          <div class="form-group compact">
            <label for="max-rep">{$_('operations.maxRepetitions')}</label>
            <input id="max-rep" type="number" bind:value={maxRepetitions} min="1" max="100" />
          </div>
          <div class="form-group compact">
            <label for="non-rep">{$_('operations.nonRepeaters')}</label>
            <input id="non-rep" type="number" bind:value={nonRepeaters} min="0" max="10" />
          </div>
        </div>
        {#if $settingsStore.snmpVersion === 'v1'}
          <div class="warning-banner">
            {$_('operations.getBulkV1Warning')}
          </div>
        {/if}
        <button class="btn btn-primary" on:click={handleSnmpGetBulk} disabled={isLoading || $settingsStore.snmpVersion === 'v1'}>
          {#if isLoading}<Icon name="loader-circle" class="icon-spin" /> {$_('common.working')}{:else}<Icon name="layers" /> {$_('operations.executeGetBulk')}{/if}
        </button>
      </div>
    {/if}

    {#if activeOperation === 'WALK'}
      <div class="form-content">
        <div class="form-group">
          <label for="walk-oid">{$_('operations.oidLabel')}</label>
          <input id="walk-oid" type="text" value={snmpWalkOid} on:input={handleWalkOidInput} placeholder={$_('operations.walkOidPlaceholder')} />
        </div>
        <button class="btn btn-primary" on:click={handleSnmpWalk} disabled={isLoading}>
          {#if isLoading}<Icon name="loader-circle" class="icon-spin" /> {$_('common.working')}{:else}<Icon name="footprints" /> {$_('operations.executeWalk')}{/if}
        </button>
      </div>
    {/if}
  </div>

  <ResultsDisplay
    {bulkResults}
    {activeOperation}
    {selectedNode}
    {oidInfoCache}
    mibTree={$mibStore.tree}
    on:walkResultClick={handleWalkResultClickEvent}
    on:tableRowWrite={handleTableRowWrite}
  />

  <RecentHistory on:openHistory on:viewInHistory />
</div>

<style>
  .form-group {
    margin-bottom: 15px;
  }

  .form-group label {
    min-width: 90px;
  }

  .instance-label {
    margin-right: 10px;
    min-width: 90px;
    font-weight: 500;
  }

  input, select {
    flex-grow: 1;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: inherit;
  }

  .form-group.compact {
    margin-bottom: 0;
  }

  /* Operation Tabs */
  /* Operation Form */
  .operation-form {
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 6px;
    padding: 15px;
    margin-bottom: 15px;
  }

  .form-content {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }

  .oid-row {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .oid-row label:first-child {
    min-width: 90px;
    font-weight: 500;
    flex-shrink: 0;
  }

  .oid-row input[type="text"] {
    flex: 1;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
    font-family: inherit;
  }

  .auto-toggle {
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    user-select: none;
    font-size: 0.88em;
    white-space: nowrap;
  }

  .auto-toggle input[type="checkbox"] {
    width: 15px;
    height: 15px;
    cursor: pointer;
    accent-color: var(--accent-color);
    margin: 0;
  }

  .warning-banner {
    background-color: var(--warning-subtle);
    border: 1px solid var(--warning-border);
    border-radius: 4px;
    padding: 10px 12px;
    color: var(--warning-color);
    font-size: 0.9em;
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .btn-primary {
    width: 100%;
    padding: 10px;
    font-size: 1em;
  }

  /* Instance field styles */
  .instance-group {
    flex-wrap: wrap;
  }

  .instance-input-wrapper {
    display: flex;
    align-items: center;
    flex: 1;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    padding-left: 10px;
    overflow: hidden;
  }

  .instance-prefix {
    color: var(--text-muted);
    font-family: 'Courier New', monospace;
    font-size: 0.9em;
    white-space: nowrap;
    max-width: 250px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .instance-input {
    flex: 0 0 80px;
    width: 80px;
    border: none !important;
    border-left: 1px solid var(--border-color) !important;
    border-radius: 0 !important;
    background: transparent;
    padding: 8px 10px;
    font-family: 'Courier New', monospace;
    color: var(--text-color);
  }

  .instance-input:focus {
    outline: none;
    background-color: var(--accent-subtle-medium);
  }

  .instance-hint {
    width: 100%;
    font-size: 0.8em;
    color: var(--text-muted);
    margin-top: 4px;
    font-style: italic;
  }

  .instance-group.readonly {
    opacity: 0.8;
  }

  .instance-value {
    color: var(--text-muted);
    font-style: italic;
    font-size: 0.95em;
  }

  /* Boolean Toggle Styles */
  .boolean-toggle-container {
    display: flex;
    align-items: center;
    gap: 12px;
    flex: 1;
  }

  .boolean-toggle-btn {
    padding: 8px 16px;
    border: 2px solid var(--border-color);
    background: transparent;
    color: var(--text-muted);
    border-radius: 6px;
    font-weight: 600;
    font-size: 0.9em;
    cursor: pointer;
    transition: all 0.2s ease;
    min-width: 80px;
  }

  .boolean-toggle-btn:hover:not(:disabled) {
    border-color: var(--accent-color);
    color: var(--text-color);
    background-color: var(--accent-subtle-medium);
  }

  .boolean-toggle-btn.active {
    border-color: var(--accent-color);
    background-color: var(--accent-color);
    color: white;
  }

  .boolean-toggle-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .boolean-toggle-switch {
    width: 52px;
    height: 28px;
    background-color: var(--border-color);
    border-radius: 14px;
    position: relative;
    cursor: pointer;
    transition: background-color 0.25s ease;
    flex-shrink: 0;
  }

  .boolean-toggle-switch:hover:not(.disabled) {
    background-color: var(--bg-disabled);
  }

  .boolean-toggle-switch:focus {
    outline: 2px solid var(--accent-color);
    outline-offset: 2px;
  }

  .boolean-toggle-switch.checked {
    background-color: var(--success-color);
  }

  .boolean-toggle-switch.disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .boolean-toggle-thumb {
    position: absolute;
    top: 3px;
    left: 3px;
    width: 22px;
    height: 22px;
    background-color: white;
    border-radius: 50%;
    transition: transform 0.25s ease;
    box-shadow: 0 2px 4px var(--shadow-color-strong);
  }

  .boolean-toggle-switch.checked .boolean-toggle-thumb {
    transform: translateX(24px);
  }

  .boolean-value-indicator {
    font-family: 'Courier New', monospace;
    font-size: 0.85em;
    color: var(--text-muted);
    padding: 4px 8px;
    background-color: var(--bg-color);
    border-radius: 4px;
    margin-left: auto;
  }

  /* GETNEXT/GETBULK styles */
  .form-hint {
    font-size: 0.85em;
    color: var(--text-muted);
    font-style: italic;
    margin: 0;
  }

  .getbulk-params {
    display: flex;
    gap: 15px;
  }

  .getbulk-params .form-group {
    flex: 1;
  }

  .getbulk-params input[type="number"] {
    width: 80px;
  }

</style>
