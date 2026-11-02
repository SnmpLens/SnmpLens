<script>
  import { createEventDispatcher } from 'svelte';
  import Icon from '../Icon.svelte';
  import ContextMenu from '../ContextMenu.svelte';
  import ResultsComparison from './ResultsComparison.svelte';
  import { copyToClipboard, copyRich } from '../utils/clipboard';
  import { escapeCSV, downloadFile } from '../utils/csv';
  import { formatValueWithEnum as _formatValueWithEnum, findTableParentNode } from '../utils/mibTree';
  import { notificationStore } from '../stores/notifications';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';
  import { anonMode, anonymizeIp } from '../utils/anonymize';
  import {
    buildTableData as pivot, withDecodedIndexes, sortRows,
    buildRowVarbinds, buildDestroyVarbinds,
  } from './tableRows';
  import { MibTable, MibDecodeIndexes, MibEncodeIndex } from '../bridge/App';
  import { targetLabels } from '../stores/targetLabels';
  import { displayTarget, targetTitle } from '../utils/targets';

  const dispatch = createEventDispatcher();

  /** @type {Array} */
  export let bulkResults = [];

  /** @type {string} */
  export let activeOperation = 'GET';

  /** @type {object|null} */
  export let selectedNode = null;

  /** @type {object} */
  export let oidInfoCache = {};

  /** @type {Array} */
  export let mibTree = [];

  // Internal state
  let tableViewEnabled = false;
  let sortColumn = null;
  let sortAscending = true;

  // The table's INDEX clause and one decoded instance per row. Both come from
  // Go: splitting an instance needs the SYNTAX of each index object, whether
  // its size is fixed, and whether the row says IMPLIED — none of which is in
  // the walk. Absent, the table still renders with the raw instance.
  let tableInfo = null;
  let decodedIndexes = [];
  let indexesFor = null; // the (table, results) the decode above describes

  // The default view (raw/table) is decided exactly once per result set. This
  // guard is what stops the view from flipping — and the filter input from
  // losing focus — while the user is interacting with the results.
  let autoViewApplied = false;
  let lastResultsForView = null;

  // Raw WALK/GETBULK list sorting (clickable OID/Type/Value headers).
  let rawSortKey = null; // 'oid' | 'type' | 'value'
  let rawSortAsc = true;

  // Table cell context menu
  let cellMenu = { visible: false, x: 0, y: 0, items: [] };
  let cellMenuCtx = null;
  let comparisonViewEnabled = false;
  let compareEnabled = false;
  let walkFilter = '';


  /**
   * Filter WALK items by text or regex against OID, name, type, and value.
   */
  function filterWalkItems(items, filterText, cache) {
    const query = (filterText || '').trim();
    if (!query) return items;
    let test;
    try {
      const re = new RegExp(query, 'i');
      test = (str) => re.test(str);
    } catch {
      const lower = query.toLowerCase();
      test = (str) => str.toLowerCase().includes(lower);
    }
    return items.filter(item => {
      const name = cache[item.oid]?.name || '';
      return test(item.oid) || test(name) || test(item.type) || test(String(item.value));
    });
  }

  // Compare two OIDs numerically, segment by segment (so 1.2 < 1.10).
  function compareOids(a, b) {
    const pa = String(a).replace(/^\./, '').split('.').map(Number);
    const pb = String(b).replace(/^\./, '').split('.').map(Number);
    const n = Math.max(pa.length, pb.length);
    for (let i = 0; i < n; i++) {
      const x = pa[i] ?? -1;
      const y = pb[i] ?? -1;
      if (x !== y) return x - y;
    }
    return 0;
  }

  // Sort raw WALK/GETBULK items by a column (kept out of buildTableData so it
  // doesn't affect the reconstructed MIB-table view).
  function sortWalkItems(items, key, asc) {
    if (!key) return items;
    const arr = [...items];
    arr.sort((a, b) => {
      let cmp;
      if (key === 'oid') {
        cmp = compareOids(a.oid, b.oid);
      } else {
        const av = key === 'type' ? a.type : a.value;
        const bv = key === 'type' ? b.type : b.value;
        const an = Number(av);
        const bn = Number(bv);
        cmp = (!isNaN(an) && !isNaN(bn)) ? an - bn : String(av ?? '').localeCompare(String(bv ?? ''));
      }
      return asc ? cmp : -cmp;
    });
    return arr;
  }

  function sortRaw(key) {
    if (rawSortKey === key) {
      rawSortAsc = !rawSortAsc;
    } else {
      rawSortKey = key;
      rawSortAsc = true;
    }
  }

  // Filter reconstructed table ROWS: keep a row if the query matches its index or
  // any cell's value / column name / OID (same query as the raw-view filter).
  function filterTableRows(rows, columns, query) {
    const q = (query || '').trim();
    if (!q) return rows;
    let test;
    try {
      const re = new RegExp(q, 'i');
      test = (s) => re.test(s);
    } catch {
      const lower = q.toLowerCase();
      test = (s) => String(s).toLowerCase().includes(lower);
    }
    return rows.filter(row => {
      // What is on screen. Once the index is decoded, the raw sub-OID is
      // displayed nowhere — typing the interface name a column header invites
      // you to type matched nothing at all.
      if (row.indexParts) {
        for (const part of row.indexParts) {
          if (test(String(part.display)) || test(part.name || '')) return true;
        }
      }
      if (test(String(row.index))) return true;
      for (const col of columns) {
        const cell = row.cells[col.oid];
        if (!cell) continue;
        if (test(String(cell.value)) || test(col.name) || test(cell.fullOid || '')) return true;
      }
      return false;
    });
  }

  // Reactive: reset table view when operation changes away from WALK/GETBULK
  $: if (activeOperation !== 'WALK' && activeOperation !== 'GETBULK') {
    tableViewEnabled = false;
  }

  // Can show comparison view: multi-target + WALK/GETBULK
  $: canShowComparison = (activeOperation === 'WALK' || activeOperation === 'GETBULK')
    && bulkResults.filter(r => !r.error && Array.isArray(r.result?.value)).length > 1;

  $: uniqueTargets = [...new Set(bulkResults.filter(r => !r.error).map(r => r.target))];
  $: canCompare = uniqueTargets.length >= 2;

  // Wrapper: resolves the cache entry then delegates to the shared util.
  //
  // `cache` is a parameter and not a read of the `oidInfoCache` prop, for the
  // reason set out on buildTableData below: this renders. The cache is filled by
  // an OID lookup that lands after the first paint, and the value it decides
  // between is `6` and `ethernetCsmacd(6)` — translating an enumeration is the
  // whole point of a MIB browser.
  function formatValueWithEnum(value, oid, snmpType, cache) {
    return _formatValueWithEnum(value, cache[oid], snmpType);
  }


  // Export results as CSV
  function exportAsCSV() {
    if (bulkResults.length === 0) return;
    const lines = [];
    const isMulti = activeOperation === 'WALK' || activeOperation === 'GETBULK';

    if (isMulti) {
      lines.push('Target,OID,Type,Value');
      for (const res of bulkResults) {
        if (res.error) {
          lines.push(`${escapeCSV(res.target)},,,"Error: ${escapeCSV(res.error)}"`);
          continue;
        }
        if (Array.isArray(res.result?.value)) {
          for (const item of res.result.value) {
            lines.push(`${escapeCSV(res.target)},${escapeCSV(item.oid)},${escapeCSV(item.type)},${escapeCSV(typeof item.value === 'string' ? item.value : JSON.stringify(item.value))}`);
          }
        }
      }
    } else {
      lines.push('Target,OID,Type,Value,Error');
      for (const res of bulkResults) {
        if (res.error) {
          lines.push(`${escapeCSV(res.target)},,,,${escapeCSV(res.error)}`);
        } else {
          lines.push(`${escapeCSV(res.target)},${escapeCSV(res.result.oid)},${escapeCSV(res.result.type)},${escapeCSV(typeof res.result.value === 'string' ? res.result.value : JSON.stringify(res.result.value))},`);
        }
      }
    }

    const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile(lines.join('\n'), `snmp-${activeOperation.toLowerCase()}-${timestamp}.csv`, 'text/csv');
    notificationStore.add(get(_)('results.exportedCsv'), 'success');
  }

  // Export results as text
  function exportAsText() {
    if (bulkResults.length === 0) return;
    const lines = [];
    const isMulti = activeOperation === 'WALK' || activeOperation === 'GETBULK';

    for (const res of bulkResults) {
      lines.push(`--- Target: ${res.target} ---`);
      if (res.error) {
        lines.push(`  Error: ${res.error}`);
      } else if (isMulti && Array.isArray(res.result?.value)) {
        for (const item of res.result.value) {
          const val = typeof item.value === 'string' ? item.value : JSON.stringify(item.value);
          lines.push(`  ${item.oid} = ${item.type}: ${val}`);
        }
        lines.push(`  (${res.result.value.length} results)`);
      } else {
        const val = typeof res.result.value === 'string' ? res.result.value : JSON.stringify(res.result.value);
        lines.push(`  ${res.result.oid} = ${res.result.type}: ${val}`);
      }
      lines.push('');
    }

    const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile(lines.join('\n'), `snmp-${activeOperation.toLowerCase()}-${timestamp}.txt`, 'text/plain');
    notificationStore.add(get(_)('results.exportedTxt'), 'success');
  }

  // Export table view as CSV
  function exportTableAsCSV() {
    if (bulkResults.length === 0 || !effectiveTableNode) return;
    const colDefs = getTableColumnDefs(effectiveTableNode);
    if (colDefs.length === 0) return;

    // Use the first result's walk data
    const firstRes = bulkResults.find(r => !r.error && Array.isArray(r.result?.value));
    if (!firstRes) return;

    const tableData = buildTableData(firstRes.result.value, colDefs, sortColumn, sortAscending,
      decodedIndexes, tableInfo?.index);
    const lines = [];

    // Header. The decoded index columns when there are any, so the file says
    // the same thing the screen does rather than one opaque instance.
    const idxHeaders = tableData.indexColumns
      ? tableData.indexColumns.map(c => c.name)
      : ['Index'];
    lines.push([...idxHeaders, ...tableData.columns.map(c => c.name)].map(escapeCSV).join(','));

    // Rows
    for (const row of tableData.rows) {
      const idxCells = tableData.indexColumns
        ? (row.indexParts
            ? row.indexParts.map(p => p.display)
            : [row.index, ...Array(tableData.indexColumns.length - 1).fill('')])
        : [row.index];
      const cells = [...idxCells, ...tableData.columns.map(col => {
        const cell = row.cells[col.oid];
        if (!cell) return '';
        return typeof cell.value === 'string' ? cell.value : JSON.stringify(cell.value);
      })];
      lines.push(cells.map(escapeCSV).join(','));
    }

    const timestamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    downloadFile(lines.join('\n'), `snmp-table-${timestamp}.csv`, 'text/csv');
    notificationStore.add(get(_)('results.exportedTable'), 'success');
  }

  // Display value of a table cell (enum-decoded / formatted, like the rendered cell).
  function cellText(cell) {
    return cell && cell.value !== undefined ? formatValueWithEnum(cell.value, cell.fullOid || '', cell.type, oidInfoCache) : '';
  }

  const htmlEscape = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  // Copy the whole table as an HTML table (pastes into Word/Docs/Outlook as a
  // real table) with a TSV plain-text fallback for editors and spreadsheets.
  function copyTableForWord() {
    if (bulkResults.length === 0 || !effectiveTableNode) return;
    const colDefs = getTableColumnDefs(effectiveTableNode);
    if (colDefs.length === 0) return;
    const firstRes = bulkResults.find(r => !r.error && Array.isArray(r.result?.value));
    if (!firstRes) return;
    const td = buildTableData(firstRes.result.value, colDefs, sortColumn, sortAscending,
      decodedIndexes, tableInfo?.index);

    const headers = [get(_)('results.index'), ...td.columns.map(c => c.name)];
    let html = '<table border="1" cellspacing="0" cellpadding="4" style="border-collapse:collapse">';
    html += '<thead><tr>' + headers.map(h => `<th>${htmlEscape(h)}</th>`).join('') + '</tr></thead><tbody>';
    const tsv = [headers.join('\t')];
    for (const row of td.rows) {
      const cells = [row.index, ...td.columns.map(col => cellText(row.cells[col.oid]))];
      html += '<tr>' + cells.map(c => `<td>${htmlEscape(c)}</td>`).join('') + '</tr>';
      tsv.push(cells.join('\t'));
    }
    html += '</tbody></table>';
    copyRich(html, tsv.join('\n'), get(_)('results.tableView'));
  }

  // Right-click a table cell → context menu with copy actions.
  function openCellMenu(event, row, col, columns, target) {
    event.preventDefault();
    event.stopPropagation();
    const cell = row.cells[col.oid];
    cellMenuCtx = { row, col, columns, cell, target };
    const t = get(_);
    cellMenu = {
      visible: true,
      x: event.clientX,
      y: event.clientY,
      items: [
        { label: t('common.copyValue'), icon: 'copy', action: 'value', disabled: !cell },
        { label: t('common.copyOid'), icon: 'route', action: 'oid', disabled: !cell },
        { label: t('results.copyOidValue'), icon: 'copy', action: 'oidValue', disabled: !cell },
        { label: '---', action: 'sep' },
        { label: t('results.copyRow'), icon: 'table', action: 'row' },
        { label: t('results.copyIndex'), icon: 'copy', action: 'index' },
        { label: t('results.copyColumn'), icon: 'columns-3', action: 'column' },
        // RFC 2579 gives no other way to remove a conceptual row, so without a
        // RowStatus column there is nothing to offer.
        ...(tableInfo?.rowStatusOid
          ? [
              { label: '---', action: 'sep' },
              { label: t('results.deleteRow'), icon: 'trash-2', action: 'destroyRow' },
            ]
          : []),
      ],
    };
  }

  function handleCellMenuAction(e) {
    const action = e.detail.action;
    const ctx = cellMenuCtx;
    cellMenu = { ...cellMenu, visible: false };
    if (!ctx) return;
    const { row, col, columns, cell, target } = ctx;
    const t = get(_);
    if (action === 'value') copyToClipboard(cellText(cell), t('common.value'));
    else if (action === 'oid') copyToClipboard(cell?.fullOid || '', t('common.oid'));
    else if (action === 'oidValue') copyToClipboard(`${cell?.fullOid || ''} = ${cellText(cell)}`, t('common.value'));
    else if (action === 'index') copyToClipboard(String(row.index), t('results.index'));
    else if (action === 'column') copyToClipboard(col.name, col.name);
    else if (action === 'row') {
      const cells = [row.index, ...columns.map(c => cellText(row.cells[c.oid]))];
      copyToClipboard(cells.join('\t'), t('results.tableView'));
    }
    else if (action === 'destroyRow') destroyRow = { ...row, target };
  }

  // ============ TABLE EDITING ============

  // A row being created, and one pending destruction. Both are confirmed
  // before anything is sent: a SET changes a device, and destroy(6) cannot be
  // taken back from here.
  let newRow = null;
  let destroyRow = null;

  function startNewRow(target) {
    if (!tableInfo?.rowStatusOid) return;
    newRow = {
      target,
      index: tableInfo.index.map(() => ''),
      values: {},
      // createAndGo asks the agent to activate the row at once; createAndWait
      // leaves it notReady so the remaining columns can be filled in after.
      status: 4,
      error: '',
      busy: false,
    };
  }

  async function submitNewRow() {
    if (!newRow || newRow.busy) return;
    newRow = { ...newRow, busy: true, error: '' };
    let instance;
    try {
      instance = await MibEncodeIndex(tableInfo.oid, newRow.index);
    } catch (e) {
      newRow = { ...newRow, busy: false, error: String(e?.message || e) };
      return;
    }
    dispatch('tableRowWrite', {
      // The device whose table this row belongs to. Without it the write goes
      // to every configured target — including devices the user never looked
      // at, whose same-numbered row is a different thing entirely.
      target: newRow.target,
      vars: buildRowVarbinds(tableInfo, instance, newRow.values, newRow.status),
      label: tableInfo.name + ' [' + newRow.index.join(', ') + ']',
      kind: 'create',
    });
    newRow = null;
  }

  // Escape closes the open row modal.
  //
  // On the window, not on the backdrop: a keydown starts at whatever has focus
  // — an input inside the modal, normally — and reaching the backdrop depends
  // on nothing in between stopping it. The inner div stops CLICKS on purpose,
  // and stopping keys along with them is what made Escape do nothing at all.
  function closeOnEscape(e) {
    if (e.key !== 'Escape') return;
    if (destroyRow) {
      destroyRow = null;
    } else if (newRow) {
      newRow = null;
    }
  }

  function confirmDestroy() {
    if (!destroyRow || !tableInfo?.rowStatusOid) return;
    dispatch('tableRowWrite', {
      target: destroyRow.target,
      vars: buildDestroyVarbinds(tableInfo, destroyRow.index),
      label: tableInfo.name + ' [' + destroyRow.index + ']',
      kind: 'destroy',
    });
    destroyRow = null;
  }

  // ============ TABLE VIEW FUNCTIONS ============

  // Get column definitions from the MIB tree for a Table or Row node
  function getTableColumnDefs(node) {
    if (!node) return [];
    let rowNode = node;
    if (node.mibType === 'Table') {
      rowNode = (node.children || []).find(c => c.mibType === 'Row');
      if (!rowNode) return [];
    }
    if (rowNode.mibType !== 'Row') return [];
    return (rowNode.children || [])
      .filter(c => c.mibType === 'Column')
      .sort((a, b) => {
        const aLast = parseInt(a.oid.split('.').pop());
        const bLast = parseInt(b.oid.split('.').pop());
        return aLast - bLast;
      });
  }

  // Check if table view is applicable
  function canShowTableView(node, results) {
    if (!node) return false;
    if (results.length === 0) return false;
    const nodeType = node.mibType;
    if (nodeType !== 'Table' && nodeType !== 'Row') return false;
    return getTableColumnDefs(node).length > 0;
  }

  // Reconstruct WALK results into a structured table
  // The pivot itself lives in tableRows.js so it can be tested without a
  // browser; what stays here is the wiring to the MIB-decoded index.
  //
  // `decoded` and `indexParts` are ARGUMENTS rather than reads of
  // `decodedIndexes` and `tableInfo`, and that is load-bearing. This is called
  // from a `{@const}` in the markup, and both arrive from `loadIndexes` two
  // awaits later — so a template expression that does not NAME them has nothing
  // linking it to their arrival. Svelte 3 hid that by recomputing every
  // `{@const}` on any update; Svelte 5 tracks what the expression actually
  // reads, and the table rendered its one-time fallback for ever: `ifTable`
  // showed a column headed "Index" holding the raw instance, which is precisely
  // what decoding the INDEX exists to replace. Measured, not guessed — the
  // decode resolved with `ifIndex` and three rows, and this ran exactly once,
  // before it.
  function buildTableData(walkResults, columnDefs, sortCol, sortAsc, decoded, indexParts) {
    const base = pivot(walkResults, columnDefs);
    const withIdx = withDecodedIndexes(base, decoded, indexParts);
    return { ...withIdx, rows: sortRows(withIdx.rows, sortCol, sortAsc) };
  }

  $: autoDetectedTableNode = (() => {
    // Only try auto-detection for WALK/GETBULK when selectedNode doesn't provide table structure
    if (activeOperation !== 'WALK' && activeOperation !== 'GETBULK') return null;
    if (selectedNode && canShowTableView(selectedNode, bulkResults)) return null;
    if (!bulkResults.length || !mibTree.length) return null;

    // Find first successful walk result with data
    const firstRes = bulkResults.find(r => !r.error && r.result?.type === 'WalkResponse' && Array.isArray(r.result?.value) && r.result.value.length > 0);
    if (!firstRes) return null;

    // Try to detect table from first few OIDs
    return findTableParentNode(firstRes.result.value[0].oid, mibTree);
  })();

  // Load the table's INDEX and decode every instance, once per result set.
  //
  // Batched: a table has as many instances as rows, and one bridge call each
  // would cost more than the walk did. The answer is applied only if it still
  // describes what is on screen — these overlap when someone walks twice, and
  // nothing orders them.
  async function loadIndexes(node, results) {
    const oid = node?.oid;
    if (!oid || !results?.length) {
      tableInfo = null;
      decodedIndexes = [];
      indexesFor = null;
      return;
    }
    // Every target's varbind count, not results[0]'s: the first target may be
    // the unreachable one, in which case the old token was permanently
    // "oid|0|n" and every later walk short-circuited as already decoded —
    // including the re-walk after creating a row, so the new row never showed
    // a decoded index.
    const token = oid + '|' + results.length + '|' +
      results.map(r => (Array.isArray(r?.result?.value) ? r.result.value.length : -1)).join(',');
    if (indexesFor === token) return;
    indexesFor = token;

    let info = null;
    try {
      info = await MibTable(oid);
    } catch (e) {
      // Guarded like the success path below. A stale call that rejects after a
      // fresher one has already published its result would otherwise wipe it,
      // and since indexesFor still holds the newer token the reactive
      // statement early-returns forever after: the table falls back to raw
      // instances and stays there until you switch tables and come back.
      if (indexesFor === token) {
        tableInfo = null;
        decodedIndexes = [];
      }
      return;
    }
    if (indexesFor !== token) return;

    const cols = getTableColumnDefs(node);
    // The union across targets. Decoding only the first one's instances left
    // every OTHER device's rows undecoded — rendered as the raw sub-OID in an
    // italic cell, which is precisely the string this feature removes — and
    // two switches rarely have the same interface numbering.
    const seen = new Set();
    for (const r of results) {
      if (r?.error || !Array.isArray(r?.result?.value)) continue;
      for (const row of pivot(r.result.value, cols).rows) seen.add(row.index);
    }
    const instances = [...seen];

    let decoded = [];
    try {
      decoded = instances.length ? await MibDecodeIndexes(info.oid, instances) : [];
    } catch (e) {
      decoded = [];
    }
    if (indexesFor !== token) return;

    tableInfo = info;
    decodedIndexes = decoded;
  }

  $: if (tableViewEnabled) loadIndexes(effectiveTableNode, bulkResults);

  // Use detected table node as fallback for table view
  $: effectiveTableNode = (selectedNode && canShowTableView(selectedNode, bulkResults)) ? selectedNode : autoDetectedTableNode;

  // New result set → reset the filter and re-arm the one-time view decision.
  $: if (bulkResults !== lastResultsForView) {
    lastResultsForView = bulkResults;
    walkFilter = '';
    rawSortKey = null;
    autoViewApplied = false;
  }

  // Decide the default view exactly once (table when a Table/Row is detected,
  // raw otherwise). Only once, so it never flips while the user interacts.
  $: if (!autoViewApplied && bulkResults.length > 0 && (activeOperation === 'WALK' || activeOperation === 'GETBULK')) {
    tableViewEnabled = !!(effectiveTableNode && (effectiveTableNode.mibType === 'Table' || effectiveTableNode.mibType === 'Row'));
    autoViewApplied = true;
  }

  function handleColumnSort(colId) {
    if (sortColumn === colId) {
      sortAscending = !sortAscending;
    } else {
      sortColumn = colId;
      sortAscending = true;
    }
  }
</script>

<svelte:window on:keydown={closeOnEscape} />

{#if bulkResults.length > 0}
  <div class="results-container">
    {#if cellMenu.visible}
      <ContextMenu
        x={cellMenu.x}
        y={cellMenu.y}
        items={cellMenu.items}
        on:action={handleCellMenuAction}
        on:close={() => (cellMenu = { ...cellMenu, visible: false })}
      />
    {/if}

    {#if newRow && tableInfo}
      <div class="modal-backdrop" on:mousedown={() => (newRow = null)} role="presentation">
        <div class="row-modal" on:mousedown|stopPropagation role="presentation">
          <h4>{$_('results.newRowIn', { values: { table: tableInfo.name } })}</h4>

          <p class="hint">{$_('results.newRowIndexHint')}</p>
          {#each tableInfo.index as part, i}
            <label class="fld">
              <span>{part.name}<em>{part.syntax}</em></span>
              <input type="text" bind:value={newRow.index[i]} />
            </label>
          {/each}

          <p class="hint">{$_('results.newRowColumnsHint')}</p>
          <div class="row-columns">
            {#each tableInfo.columns.filter(c => c.writable && !c.isIndex && c.oid !== tableInfo.rowStatusOid) as col}
              <label class="fld">
                <span>{col.name}<em>{col.syntax}</em></span>
                {#if col.enumValues}
                  <select bind:value={newRow.values[col.oid]}>
                    <option value="">—</option>
                    {#each Object.entries(col.enumValues) as [name, value]}
                      <option value={String(value)}>{name} ({value})</option>
                    {/each}
                  </select>
                {:else}
                  <input type="text" bind:value={newRow.values[col.oid]} />
                {/if}
              </label>
            {/each}
          </div>

          <label class="fld">
            <span>{$_('results.rowStatus')}</span>
            <select bind:value={newRow.status}>
              <option value={4}>createAndGo (4)</option>
              <option value={5}>createAndWait (5)</option>
            </select>
          </label>

          {#if newRow.error}
            <p class="row-error"><Icon name="circle-x" size={13} /> {newRow.error}</p>
          {/if}

          <div class="row-actions">
            <button class="btn-small" on:click={() => (newRow = null)}>{$_('common.cancel')}</button>
            <button class="btn-small primary" on:click={submitNewRow} disabled={newRow.busy}>
              {$_('results.createRow')}
            </button>
          </div>
        </div>
      </div>
    {/if}

    {#if destroyRow && tableInfo}
      <div class="modal-backdrop" on:mousedown={() => (destroyRow = null)} role="presentation">
        <div class="row-modal" on:mousedown|stopPropagation role="presentation">
          <h4>{$_('results.deleteRow')}</h4>
          <p>{$_('results.deleteRowConfirm', { values: { table: tableInfo.name, index: destroyRow.index } })}</p>
          <p class="hint">{$_('results.deleteRowHint')}</p>
          <div class="row-actions">
            <button class="btn-small" on:click={() => (destroyRow = null)}>{$_('common.cancel')}</button>
            <button class="btn-small danger" on:click={confirmDestroy}>{$_('common.delete')}</button>
          </div>
        </div>
      </div>
    {/if}

    <div class="results-header">
      <h4>{$_('results.title')}</h4>
      <div class="export-buttons">
        {#if canShowComparison}
          <button
            class="btn-view"
            class:active={comparisonViewEnabled}
            on:click={() => comparisonViewEnabled = !comparisonViewEnabled}
          >
            {$_('results.comparison')}
          </button>
        {/if}
        {#if canCompare}
          <button class="btn-view" class:active={compareEnabled} on:click={() => { compareEnabled = !compareEnabled; }}>
            {$_('results.compare')}
          </button>
        {/if}
        <button class="btn-export" on:click={exportAsCSV} title={$_('results.csv')}>{$_('results.csv')}</button>
        <button class="btn-export" on:click={exportAsText} title={$_('results.txt')}>{$_('results.txt')}</button>
        {#if tableViewEnabled && canShowTableView(effectiveTableNode, bulkResults)}
          <button class="btn-export" on:click={exportTableAsCSV} title={$_('results.tableCsv')}>{$_('results.tableCsv')}</button>
          <button class="btn-export" on:click={copyTableForWord} title={$_('results.copyForWordHint')}>
            <Icon name="copy" size={13} /> {$_('results.copyForWord')}
          </button>
        {/if}
      </div>
    </div>

    {#if compareEnabled}
      <ResultsComparison mode="enhanced" {bulkResults} {oidInfoCache} />
    {/if}

    {#if comparisonViewEnabled && canShowComparison}
      <ResultsComparison mode="legacy" {bulkResults} {oidInfoCache} />
    {:else}
      {#each bulkResults as res}
        <div class="result" class:success={!res.error} class:error={res.error}>
          <p class="result-target" title={targetTitle(res.target, $anonMode)}>
            {displayTarget(res.target, $targetLabels, $anonMode)}
            {#if res.responseTimeMs}
              <span class="response-time-badge">{res.responseTimeMs}ms</span>
            {/if}
          </p>
          {#if res.error}
            <p><strong>{$_('common.error')}:</strong> {res.error}</p>
          {:else if (res.result.type === 'WalkResponse' || res.result.type === 'GetBulkResponse') && Array.isArray(res.result.value)}
          <!-- WALK/GETBULK results display -->
          <div class="result-fields walk-summary">
            <span class="rfield">
              <span class="rlabel">{$_('results.baseOid')}</span>
              <span class="rval mono">{res.result.oid}</span>
            </span>
            <span class="rfield rcount">{$_('results.resultsFound', { values: { count: res.result.value.length } })}</span>
          </div>

          {#if canShowTableView(effectiveTableNode, bulkResults)}
            <div class="view-toggle">
              <button
                class="btn-view"
                class:active={!tableViewEnabled}
                on:click={() => { tableViewEnabled = false; }}
              >
                {$_('results.rawView')}
              </button>
              <button
                class="btn-view"
                class:active={tableViewEnabled}
                on:click={() => { tableViewEnabled = true; sortColumn = null; }}
              >
                {$_('results.tableView')}
              </button>
            </div>
          {/if}

          {#if tableViewEnabled && canShowTableView(effectiveTableNode, bulkResults)}
            {@const colDefs = getTableColumnDefs(effectiveTableNode)}
            {@const tableData = buildTableData(res.result.value, colDefs, sortColumn, sortAscending, decodedIndexes, tableInfo?.index)}
            {@const tableRows = filterTableRows(tableData.rows, tableData.columns, walkFilter)}
            <div class="walk-filter-bar">
              <input
                type="text"
                class="walk-filter-input"
                bind:value={walkFilter}
                placeholder={$_('results.filterPlaceholder')}
              />
              {#if walkFilter.trim()}
                <span class="walk-filter-count">{tableRows.length} / {tableData.rows.length}</span>
                <button class="btn-copy-small" on:click={() => walkFilter = ''} title={$_('common.clear')}>&times;</button>
              {/if}
            </div>
            {#if tableInfo?.rowStatusOid}
              <div class="table-edit-bar">
                <button class="btn-small" on:click={() => startNewRow(res.target)}>
                  <Icon name="plus" size={13} /> {$_('results.newRow')}
                </button>
                <span class="hint-inline">{$_('results.rowStatusHint', { values: { table: tableInfo.name } })}</span>
              </div>
            {/if}
            <div class="table-view-results">
              <table>
                <thead>
                  <tr>
                    {#if tableData.indexColumns}
                      {#each tableData.indexColumns as idxCol, i}
                        <th
                          class="sortable index-col"
                          on:click={() => handleColumnSort('__index:' + i)}
                          title={idxCol.syntax}
                        >
                          {idxCol.name}
                          {#if sortColumn === '__index:' + i}{sortAscending ? '▲' : '▼'}{/if}
                        </th>
                      {/each}
                    {:else}
                      <th
                        class="sortable index-col"
                        on:click={() => handleColumnSort('__index')}
                      >
                        {$_('results.index')} {sortColumn === '__index' ? (sortAscending ? '▲' : '▼') : ''}
                      </th>
                    {/if}
                    {#each tableData.columns as col}
                      <th
                        class="sortable"
                        on:click={() => handleColumnSort(col.oid)}
                        title="{col.oid} ({col.syntax})"
                      >
                        {col.name}
                        {#if sortColumn === col.oid}
                          {sortAscending ? '▲' : '▼'}
                        {/if}
                      </th>
                    {/each}
                  </tr>
                </thead>
                <tbody>
                  {#each tableRows as row}
                    <tr>
                      {#if tableData.indexColumns && row.indexParts}
                        {#each row.indexParts as part}
                          <td class="index-cell" title="{part.name} = {part.display}">{part.display}</td>
                        {/each}
                      {:else if tableData.indexColumns}
                        <!-- This row's instance did not decode; say so rather
                             than leaving cells that look like real values. -->
                        <td class="index-cell undecoded" colspan={tableData.indexColumns.length}
                            title={row.indexError}>{row.index}</td>
                      {:else}
                        <td class="index-cell">{row.index}</td>
                      {/if}
                      {#each tableData.columns as col}
                        <td
                          class="table-value-cell clickable"
                          title={row.cells[col.oid]?.fullOid || ''}
                          on:click={() => row.cells[col.oid] && dispatch('walkResultClick', {oid: row.cells[col.oid].fullOid, value: row.cells[col.oid].value, type: row.cells[col.oid].type})}
                          on:keydown={(e) => e.key === 'Enter' && row.cells[col.oid] && dispatch('walkResultClick', {oid: row.cells[col.oid].fullOid, value: row.cells[col.oid].value, type: row.cells[col.oid].type})}
                          on:contextmenu={(e) => openCellMenu(e, row, col, tableData.columns, res.target)}
                        >
                          {row.cells[col.oid]?.value !== undefined ? formatValueWithEnum(row.cells[col.oid].value, row.cells[col.oid].fullOid || '', row.cells[col.oid].type, oidInfoCache) : '-'}
                        </td>
                      {/each}
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
            <p class="table-info">{$_('results.tableInfo', { values: { rows: tableRows.length, cols: tableData.columns.length } })}</p>
          {:else}
            <!-- Raw WALK results table -->
            {@const filtered = sortWalkItems(filterWalkItems(res.result.value, walkFilter, oidInfoCache), rawSortKey, rawSortAsc)}
            <div class="walk-filter-bar">
              <input
                type="text"
                class="walk-filter-input"
                bind:value={walkFilter}
                placeholder={$_('results.filterPlaceholder')}
              />
              {#if walkFilter.trim()}
                <span class="walk-filter-count">{filtered.length} / {res.result.value.length}</span>
                <button class="btn-copy-small" on:click={() => walkFilter = ''} title={$_('common.clear')}>&times;</button>
              {/if}
            </div>
            <div class="walk-results">
              <table>
                <thead>
                  <tr>
                    <th class="sortable" on:click={() => sortRaw('oid')}>{$_('common.oid')} {rawSortKey === 'oid' ? (rawSortAsc ? '▲' : '▼') : ''}</th>
                    <th class="sortable" on:click={() => sortRaw('type')}>{$_('common.type')} {rawSortKey === 'type' ? (rawSortAsc ? '▲' : '▼') : ''}</th>
                    <th class="sortable" on:click={() => sortRaw('value')}>{$_('common.value')} {rawSortKey === 'value' ? (rawSortAsc ? '▲' : '▼') : ''}</th>
                    <th class="copy-col"></th>
                  </tr>
                </thead>
                <tbody>
                  {#each filtered as walkItem}
                    <tr
                      class="walk-result-row clickable"
                      on:click={() => dispatch('walkResultClick', walkItem)}
                      on:keydown={(e) => e.key === 'Enter' && dispatch('walkResultClick', walkItem)}
                      role="button"
                      tabindex="0"
                      title={$_('results.clickToUseOid')}
                    >
                      <td class="oid-cell" title={walkItem.oid}>
                        {#if oidInfoCache[walkItem.oid]?.name}
                          <span class="oid-name">{oidInfoCache[walkItem.oid].name}</span>
                        {/if}
                        <span class="oid-raw">{walkItem.oid}</span>
                      </td>
                      <td>{walkItem.type}</td>
                      <td class="value-cell" title={JSON.stringify(walkItem.value)}>{formatValueWithEnum(walkItem.value, walkItem.oid, walkItem.type, oidInfoCache)}</td>
                      <td class="copy-cell">
                        <button
                          class="btn-copy-small"
                          on:click|stopPropagation={() => copyToClipboard(String(walkItem.value), $_('common.value'))}
                          title={$_('common.copyValue')}
                        ><Icon name="copy" size={13} /></button>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {/if}
        {:else}
          <!-- GET/SET results display -->
          <div class="result-fields">
            <span class="rfield">
              <span class="rlabel">{$_('common.oid')}</span>
              <span class="rval mono">{res.result.oid}</span>
              <button class="btn-copy-small" on:click={() => copyToClipboard(res.result.oid, $_('common.oid'))} title={$_('common.copyOid')}><Icon name="copy" size={13} /></button>
            </span>
            <span class="rfield">
              <span class="rlabel">{$_('common.type')}</span>
              <span class="rval">{res.result.type}{#if oidInfoCache[res.result.oid]?.name} <span class="resolved-name">({oidInfoCache[res.result.oid].name})</span>{/if}</span>
            </span>
            <span class="rfield rfield-grow">
              <span class="rlabel">{$_('common.value')}</span>
              <span class="rval">{formatValueWithEnum(res.result.value, res.result.oid, res.result.type, oidInfoCache)}</span>
              <button class="btn-copy-small" on:click={() => copyToClipboard(String(res.result.value), $_('common.value'))} title={$_('common.copyValue')}><Icon name="copy" size={13} /></button>
            </span>
          </div>
        {/if}
        </div>
      {/each}
    {/if}
  </div>
{/if}

<style>
  .results-container {
    margin-top: 20px;
  }

  .result {
    margin-top: 10px;
    padding: 12px;
    border-radius: 5px;
    border: 1px solid;
  }

  .result-target {
    font-weight: bold;
    margin-bottom: 8px;
  }

  /* Aligned, responsive result fields (GET/SET display + walk summary) */
  .result-fields {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    row-gap: 6px;
    margin-bottom: 4px;
  }

  .rfield {
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    min-width: 0;
    padding: 0 16px;
    border-left: 1px solid var(--border-color);
  }

  .rfield:first-child {
    padding-left: 0;
    border-left: none;
  }

  .rfield-grow {
    flex: 1 1 220px;
  }

  .rlabel {
    color: var(--text-muted);
    font-size: 0.78em;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    flex-shrink: 0;
  }

  .rval {
    min-width: 0;
    overflow-wrap: anywhere;
  }

  .walk-summary .rcount {
    color: var(--text-light);
    font-weight: 600;
  }

  /* OID cell: MIB name and raw OID separated by a light divider */
  .oid-name {
    color: var(--oid-color);
    font-weight: 600;
    margin-right: 8px;
    padding-right: 8px;
    border-right: 1px solid var(--border-color);
  }

  .oid-raw {
    color: var(--text-muted);
  }

  /* Light vertical separators between result-table columns */
  .walk-results td,
  .walk-results th,
  .table-view-results td,
  .table-view-results th {
    border-right: 1px solid var(--border-color);
  }

  .walk-results td:last-child,
  .walk-results th:last-child,
  .table-view-results td:last-child,
  .table-view-results th:last-child {
    border-right: none;
  }

  .success {
    background-color: var(--success-subtle-medium);
    border-color: var(--success-color);
  }

  .error {
    background-color: var(--error-subtle-medium);
    border-color: var(--error-color);
    color: var(--error-color);
  }

  .walk-filter-bar {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
    margin-bottom: 6px;
  }

  .walk-filter-input {
    flex: 1;
    max-width: 350px;
    padding: 5px 10px;
    font-size: 0.85em;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .walk-filter-input:focus {
    outline: none;
    border-color: var(--accent-color);
  }

  .walk-filter-count {
    font-size: 0.8em;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .walk-results {
    margin-top: 0;
    max-height: 400px;
    overflow-y: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .walk-results table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9em;
  }

  .walk-results thead {
    position: sticky;
    top: 0;
    background-color: var(--bg-lighter-color);
    z-index: 1;
  }

  .walk-results th {
    text-align: left;
    padding: 8px;
    border-bottom: 2px solid var(--border-color);
    font-weight: 600;
  }

  .walk-results th.sortable {
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
  }

  .walk-results th.sortable:hover {
    background-color: var(--hover-overlay);
  }

  .walk-results td {
    padding: 6px 8px;
    border-bottom: 1px solid var(--border-color);
  }

  .walk-results tr:hover {
    background-color: var(--hover-overlay);
  }

  .response-time-badge {
    font-size: 0.8em;
    padding: 2px 8px;
    border-radius: 10px;
    margin-left: 8px;
    font-weight: 600;
    background-color: var(--accent-subtle-strong);
    color: var(--oid-color);
  }

  .oid-name {
    color: var(--name-color);
    font-size: 0.85em;
    margin-right: 6px;
    font-family: inherit;
  }

  .resolved-name {
    color: var(--name-color);
    font-size: 0.9em;
    margin-left: 6px;
  }

  .walk-results .oid-cell {
    font-family: 'Courier New', monospace;
    font-size: 0.85em;
    color: var(--oid-color);
    max-width: 300px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .walk-results .value-cell {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Clickable WALK result rows */
  .walk-result-row.clickable {
    cursor: pointer;
    transition: background-color 0.15s ease;
  }

  .walk-result-row.clickable:hover {
    background-color: var(--accent-subtle-intense) !important;
  }

  .walk-result-row.clickable:focus {
    outline: 2px solid var(--accent-color);
    outline-offset: -2px;
  }

  .walk-result-row.clickable:hover .oid-cell {
    color: var(--accent-color);
    text-decoration: underline;
  }

  /* Copy buttons */
  .btn-copy-small {
    background: transparent;
    border: none;
    cursor: pointer;
    padding: 2px 4px;
    font-size: 0.85em;
    opacity: 0.5;
    transition: opacity 0.2s;
  }

  .btn-copy-small:hover {
    opacity: 1;
  }

  .copy-col {
    width: 40px;
  }

  .copy-cell {
    text-align: center;
  }

  /* Table View styles */
  .view-toggle {
    display: flex;
    gap: 4px;
    margin: 10px 0;
  }

  .btn-view {
    padding: 6px 14px;
    font-size: 0.85em;
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-muted);
    border-radius: 4px;
    cursor: pointer;
    transition: all 0.2s;
    font-weight: 500;
  }

  .btn-view:hover {
    border-color: var(--accent-color);
    color: var(--text-color);
  }

  .btn-view.active {
    background-color: var(--accent-color);
    border-color: var(--accent-color);
    color: white;
  }

  .table-view-results {
    margin-top: 10px;
    max-height: 500px;
    overflow: auto;
    border: 1px solid var(--border-color);
    border-radius: 4px;
  }

  .table-view-results table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.9em;
  }

  .table-view-results thead {
    position: sticky;
    top: 0;
    background-color: var(--bg-lighter-color);
    z-index: 1;
  }

  .table-view-results th {
    text-align: left;
    padding: 8px;
    border-bottom: 2px solid var(--border-color);
    font-weight: 600;
    white-space: nowrap;
  }

  .table-view-results th.sortable {
    cursor: pointer;
    user-select: none;
  }

  .table-view-results th.sortable:hover {
    background-color: var(--accent-subtle-strong);
    color: var(--accent-color);
  }

  .table-view-results td {
    padding: 6px 8px;
    border-bottom: 1px solid var(--border-color);
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .table-view-results tr:hover {
    background-color: var(--hover-overlay);
  }

  .table-value-cell {
    cursor: pointer;
  }

  .table-value-cell:hover {
    background-color: var(--accent-subtle-strong);
    color: var(--accent-color);
  }

  .undecoded {
    font-style: italic;
    opacity: 0.75;
  }
  .index-cell {
    font-family: 'Courier New', monospace;
    color: var(--oid-color);
    font-size: 0.85em;
    font-weight: 600;
  }

  .table-info {
    font-size: 0.85em;
    color: var(--text-muted);
    margin-top: 8px;
    font-style: italic;
    text-align: center;
  }

  /* Export buttons */
  .results-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 5px;
  }

  .results-header h4 {
    margin: 0;
  }

  .export-buttons {
    display: flex;
    gap: 6px;
  }

  .btn-export {
    padding: 4px 10px;
    font-size: 0.8em;
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-dimmed);
    border-radius: 3px;
    cursor: pointer;
    font-weight: 500;
    transition: all 0.2s;
  }

  .btn-export:hover {
    border-color: var(--accent-color);
    color: var(--accent-color);
    background-color: var(--accent-subtle-medium);
  }


  .table-edit-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 6px;
  }

  .hint-inline {
    font-size: 11px;
    color: var(--text-muted, #888);
  }

  .modal-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    background-color: var(--backdrop-color, rgba(0, 0, 0, 0.5));
    display: flex;
    justify-content: center;
    align-items: center;
    z-index: 100;
  }

  .row-modal {
    background: var(--bg-panel, #fff);
    color: var(--text-primary, #222);
    border-radius: 8px;
    padding: 18px;
    width: min(560px, 92vw);
    max-height: 84vh;
    overflow-y: auto;
    box-shadow: 0 8px 30px rgba(0, 0, 0, 0.3);
  }

  .row-modal h4 {
    margin: 0 0 10px;
  }

  .row-modal .fld {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 6px;
  }

  .row-modal .fld > span {
    flex: 0 0 190px;
    font-size: 12px;
  }

  .row-modal .fld em {
    display: block;
    font-style: normal;
    font-size: 10px;
    color: var(--text-muted, #888);
  }

  .row-modal .fld input,
  .row-modal .fld select {
    flex: 1;
    min-width: 0;
  }

  .row-columns {
    max-height: 34vh;
    overflow-y: auto;
  }

  .row-error {
    color: var(--danger, #c0392b);
    font-size: 12px;
  }

  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 14px;
  }
</style>
