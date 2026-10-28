<script>
  import { createEventDispatcher, onMount, tick } from 'svelte';
  import { treeRequest } from './stores/treeRequest';
  import { GetPersistentMibDirectory } from '../wailsjs/go/app/App';
  import { mibStore } from './stores/mibStore';
  import TreeNode from './TreeNode.svelte';
  import Icon from './Icon.svelte';
  import ContextMenu from './ContextMenu.svelte';
  import Breadcrumb from './Breadcrumb.svelte';
  import Tooltip from './Tooltip.svelte';
  import { notificationStore } from './stores/notifications';
  import { favoritesStore } from './stores/favoritesStore';
  import KeyboardHelp from './mib/KeyboardHelp.svelte';
  import FavoritesPanel from './mib/FavoritesPanel.svelte';
  import NodeDetails from './mib/NodeDetails.svelte';
  import { _ } from 'svelte-i18n';
  import { get } from 'svelte/store';

  const dispatch = createEventDispatcher();

  let persistentMibDir = 'Loading...';
  let selectedNode = null;
  let searchTerm = '';
  let filteredTree = [];
  let treeContainerElement;
  let searchInputElement;
  let showKeyboardHelp = false;
  let compactMode = true; // Compact mode enabled by default
  let activeTypeFilters = [];
  let activeAccessFilters = [];

  // Global tooltip state
  let tooltip = {
    visible: false,
    x: 0,
    y: 0,
    node: null,
  };

  let contextMenu = {
    visible: false,
    x: 0,
    y: 0,
    node: null,
    items: []
  };

  // Check if node is writable based on access
  function isWritable(node) {
    if (!node.access) return true; // Default to writable if access is not specified
    
    const access = node.access.toLowerCase();
    // Writable if access is read-write, write-only, or accessible-for-notify
    return access.includes('write') || access === 'readwrite' || access === 'read-write';
  }

  // Build context menu items based on node type
  function buildContextMenuItems(node) {
    const t = get(_);
    const items = [
      { label: t('mib.contextMenu.copyOid'), icon: 'copy', action: 'copyOid' },
      { label: t('mib.contextMenu.copyName'), icon: 'tag', action: 'copyName' },
      { label: t('mib.contextMenu.copyPath'), icon: 'route', action: 'copyPath' },
    ];

    // Add separator
    items.push({ label: '---', action: 'separator' });

    // Add SNMP operations for Scalar and Column types
    if (node.mibType === 'Scalar' || node.mibType === 'Column') {
      items.push({ label: t('mib.contextMenu.snmpGet'), icon: 'download', action: 'snmpGet' });
      items.push({ label: t('mib.contextMenu.snmpGetNext'), icon: 'arrow-right-to-line', action: 'snmpGetNext' });

      // Check if node is writable
      const writable = isWritable(node);
      items.push({
        label: t('mib.contextMenu.snmpSet'),
        icon: 'upload',
        action: 'snmpSet',
        disabled: !writable,
        disabledReason: !writable ? t('mib.contextMenu.objectIs', { values: { access: node.access || 'read-only' } }) : ''
      });
    }

    // Add SNMP Walk and GETBULK for Table, Row, Node, and Group types
    if (node.mibType === 'Table' || node.mibType === 'Row' || node.mibType === 'Node' || node.mibType === 'Group') {
      items.push({ label: t('mib.contextMenu.snmpWalk'), icon: 'footprints', action: 'snmpWalk' });
      items.push({ label: t('mib.contextMenu.snmpGetBulk'), icon: 'layers', action: 'snmpGetBulk' });
    }

    // Add "Walk as Table" for Table and Row nodes
    if (node.mibType === 'Table' || node.mibType === 'Row') {
      items.push({ label: t('mib.walkAsTable'), icon: 'table', action: 'walkAsTable' });
    }

    // Add separator
    items.push({ label: '---', action: 'separator' });

    // Add/Remove favorite
    const isFav = favoritesStore.isFavorite(node.oid, $favoritesStore);
    if (isFav) {
      items.push({ label: t('mib.contextMenu.removeFavorite'), icon: 'star', iconFilled: true, action: 'removeFavorite' });
    } else {
      items.push({ label: t('mib.contextMenu.addFavorite'), icon: 'star', action: 'addFavorite' });
    }

    return items;
  }

  // NOT async. Svelte calls back whatever the callback RETURNS, and an async
  // function returns a Promise — never a function — so the cleanup below was
  // dropped and the keydown listener stayed on `document`. This panel is
  // unmounted on every tab switch, so they accumulated: after ten switches,
  // ten handlers, each answering the same arrow key.
  //
  // The directory is fetched without awaiting it here. Nothing in the first
  // paint needs it, and making the callback async to shorten that line is
  // exactly the trade that lost the cleanup.
  onMount(() => {
    GetPersistentMibDirectory()
      .then((dir) => { persistentMibDir = dir; })
      .catch((e) => console.warn('Could not read the persistent MIB directory:', e));

    document.addEventListener('keydown', handleKeyboardNavigation);
    return () => {
      document.removeEventListener('keydown', handleKeyboardNavigation);
    };
  });

  function handleNodeClick(node) {
    selectedNode = node;
    dispatch('select', { node: node });
  }

  // Handle breadcrumb navigation
  function handleBreadcrumbNavigation(event) {
    const node = event.detail.node;
    // Expand the node and scroll to it
    expandPathToNode(node);
    selectedNode = node;
    dispatch('select', { node: node });
  }

  // Expand all nodes in the path to the target node
  function expandPathToNode(targetNode) {
    let current = targetNode.parent;
    while (current) {
      current.expanded = true;
      current = current.parent;
    }
    // Trigger reactivity
    filteredTree = [...filteredTree];
  }

  // A node another part of the application asked to show (stores/treeRequest.js).
  // Looked for in the whole tree: a filter that hides it is cleared, since the
  // request is precisely to see it.
  let handledRequest = 0;
  $: if ($treeRequest && $treeRequest.seq !== handledRequest) revealRequested($treeRequest);

  async function revealRequested(req) {
    handledRequest = req.seq;
    if (!findNodeByOid($mibStore.tree, req.oid)) {
      notificationStore.add(get(_)('mib.nodeNotFound', { values: { name: req.name || req.oid } }), 'error');
      return;
    }
    if (searchTerm || activeTypeFilters.length > 0 || activeAccessFilters.length > 0) {
      searchTerm = '';
      activeTypeFilters = [];
      activeAccessFilters = [];
      await tick();
    }
    const node = findNodeByOid(filteredTree, req.oid);
    if (!node) return;
    expandPathToNode(node);
    selectedNode = node;
    await tick();
    scrollToNode(node);
    dispatch('select', { node });
  }

  function handleContextMenu(event) {
    contextMenu.node = event.detail.node;
    contextMenu.x = event.detail.x;
    contextMenu.y = event.detail.y;
    contextMenu.items = buildContextMenuItems(event.detail.node);
    contextMenu.visible = true;
  }

  function handleDblClickNode(event) {
    const { node } = event.detail;
    dispatch('snmpAction', {
      type: 'GET',
      oid: node.oid,
      name: node.name
    });
  }

  function closeContextMenu() {
    contextMenu.visible = false;
    contextMenu.node = null;
  }

  // Tooltip handlers
  function handleShowTooltip(event) {
    tooltip.node = event.detail.node;
    tooltip.x = event.detail.x;
    tooltip.y = event.detail.y;
    tooltip.visible = true;
  }

  function handleHideTooltip() {
    tooltip.visible = false;
    tooltip.node = null;
  }

  // Build full path for Copy Path action
  function buildNodePath(node) {
    const path = [];
    let current = node;
    while (current) {
      path.unshift(current.name);
      current = current.parent;
    }
    return path.join(' › ');
  }

  // Handle click on a favorite - navigate to it
  function handleFavoriteClick(favorite) {
    // Find the node in the tree by OID
    const node = findNodeByOid(filteredTree, favorite.oid);
    if (node) {
      expandPathToNode(node);
      selectedNode = node;
      scrollToNode(node);
      dispatch('select', { node: node });
      notificationStore.add(get(_)('mib.navigatedTo', { values: { name: favorite.name } }), 'success');
    } else {
      notificationStore.add(get(_)('mib.nodeNotFound', { values: { name: favorite.name } }), 'error');
    }
  }

  // Find a node by OID in the tree
  function findNodeByOid(nodes, targetOid) {
    for (const node of nodes) {
      if (node.oid === targetOid) return node;
      if (node.children && node.children.length > 0) {
        const found = findNodeByOid(node.children, targetOid);
        if (found) return found;
      }
    }
    return null;
  }

  async function handleContextMenuAction(event) {
    const action = event.detail.action;
    const node = contextMenu.node;
    if (!node) return;

    try {
      if (action === 'separator') {
        // Do nothing for separators
        return;
      } else if (action === 'copyOid') {
        await navigator.clipboard.writeText(node.oid);
        notificationStore.add(get(_)('mib.oidCopied'), 'success');
      } else if (action === 'copyName') {
        await navigator.clipboard.writeText(node.name);
        notificationStore.add(get(_)('mib.nameCopied'), 'success');
      } else if (action === 'copyPath') {
        const path = buildNodePath(node);
        await navigator.clipboard.writeText(path);
        notificationStore.add(get(_)('mib.pathCopied'), 'success');
      } else if (action === 'snmpGet') {
        // Switch to Operations tab and pre-fill OID
        dispatch('snmpAction', { 
          type: 'GET', 
          oid: node.oid, 
          name: node.name 
        });
        notificationStore.add(get(_)('mib.readyGet', { values: { name: node.name } }), 'info');
      } else if (action === 'snmpSet') {
        // Switch to Operations tab and pre-fill OID
        dispatch('snmpAction', {
          type: 'SET',
          oid: node.oid,
          name: node.name
        });
        notificationStore.add(get(_)('mib.readySet', { values: { name: node.name } }), 'info');
      } else if (action === 'snmpGetNext') {
        dispatch('snmpAction', {
          type: 'GETNEXT',
          oid: node.oid,
          name: node.name
        });
        notificationStore.add(get(_)('mib.readyGetNext', { values: { name: node.name } }), 'info');
      } else if (action === 'snmpGetBulk') {
        dispatch('snmpAction', {
          type: 'GETBULK',
          oid: node.oid,
          name: node.name
        });
        notificationStore.add(get(_)('mib.readyGetBulk', { values: { name: node.name } }), 'info');
      } else if (action === 'snmpWalk') {
        dispatch('snmpAction', {
          type: 'WALK',
          oid: node.oid,
          name: node.name
        });
        notificationStore.add(get(_)('mib.readyWalk', { values: { name: node.name } }), 'info');
      } else if (action === 'walkAsTable') {
        dispatch('snmpAction', { type: 'WALK_TABLE', oid: node.oid, name: node.name });
        notificationStore.add(get(_)('mib.readyWalk', { values: { name: node.name } }), 'info');
      } else if (action === 'addFavorite') {
        const path = buildNodePath(node);
        favoritesStore.add(node, path);
        notificationStore.add(get(_)('mib.addedFavorite', { values: { name: node.name } }), 'success');
      } else if (action === 'removeFavorite') {
        favoritesStore.remove(node.oid);
        notificationStore.add(get(_)('mib.removedFavorite', { values: { name: node.name } }), 'success');
      }
    } catch (err) {
      notificationStore.add(get(_)('mib.actionFailed', { values: { error: err.message } }), 'error');
    }
  }


  function toggleTypeFilter(type) {
    if (activeTypeFilters.includes(type)) {
      activeTypeFilters = activeTypeFilters.filter(t => t !== type);
    } else {
      activeTypeFilters = [...activeTypeFilters, type];
    }
  }

  function toggleAccessFilter(access) {
    if (activeAccessFilters.includes(access)) {
      activeAccessFilters = activeAccessFilters.filter(a => a !== access);
    } else {
      activeAccessFilters = [...activeAccessFilters, access];
    }
  }

  function normalizeAccess(access) {
    if (!access) return 'unknown';
    const a = access.toLowerCase();
    if (a.includes('write')) return 'read-write';
    if (a === 'readonly' || a === 'read-only') return 'read-only';
    if (a === 'notaccessible' || a === 'not-accessible') return 'not-accessible';
    return a;
  }

  // Reactive statement to filter the tree
  $: {
    if (searchTerm || activeTypeFilters.length > 0 || activeAccessFilters.length > 0) {
      const lowerCaseSearch = searchTerm ? searchTerm.toLowerCase() : '';
      filteredTree = filterTree($mibStore.tree, node => {
        // Type filter
        if (activeTypeFilters.length > 0 && !activeTypeFilters.includes(node.mibType)) return false;
        // Access filter
        if (activeAccessFilters.length > 0 && !activeAccessFilters.includes(normalizeAccess(node.access))) return false;
        // No search term but filters passed
        if (!lowerCaseSearch) return true;
        // Name and OID
        if (node.name.toLowerCase().includes(lowerCaseSearch)) return true;
        if (node.oid.includes(lowerCaseSearch)) return true;
        // Description
        if (node.description && node.description.toLowerCase().includes(lowerCaseSearch)) return true;
        // Syntax
        if (node.syntax && node.syntax.toLowerCase().includes(lowerCaseSearch)) return true;
        // Enum value names
        if (node.enumValues) {
          for (const enumName of Object.keys(node.enumValues)) {
            if (enumName.toLowerCase().includes(lowerCaseSearch)) return true;
          }
        }
        return false;
      });
    } else {
      filteredTree = $mibStore.tree;
    }
  }

  // Recursive function to filter the tree.
  // It returns a new tree with only the nodes that match the predicate,
  // including their parent nodes.
  function filterTree(nodes, predicate) {
    return nodes.map(node => {
      // If the node itself matches, we keep it and all its children
      if (predicate(node)) {
        return node;
      }
      // If the node doesn't match, check if any of its children do
      if (node.children && node.children.length > 0) {
        const filteredChildren = filterTree(node.children, predicate);
        if (filteredChildren.length > 0) {
          // If there are matching children, return the parent node with only the filtered children
          return { ...node, children: filteredChildren };
        }
      }
      // If neither the node nor its children match, return null
      return null;
    }).filter(Boolean); // Filter out the nulls
  }

  // Recursively sets the 'expanded' state on all nodes in a tree.
  function toggleAll(isExpanded) {
    function recurse(nodes) {
      nodes.forEach(node => {
        if (node.children && node.children.length > 0) {
          node.expanded = isExpanded;
          recurse(node.children);
        }
      });
    }
    recurse(filteredTree);
    // Trigger a reactivity update by reassigning the array
    filteredTree = [...filteredTree];
  }

  // ============================================================
  // KEYBOARD NAVIGATION
  // ============================================================

  // Get all visible (expanded) nodes in a flat list
  function getFlatNodeList() {
    const flatList = [];
    
    function traverse(nodes) {
      nodes.forEach(node => {
        flatList.push(node);
        if (node.expanded && node.children && node.children.length > 0) {
          traverse(node.children);
        }
      });
    }
    
    traverse(filteredTree);
    return flatList;
  }

  // Find the index of the currently selected node in the flat list
  function getSelectedNodeIndex() {
    if (!selectedNode) return -1;
    const flatList = getFlatNodeList();
    return flatList.findIndex(node => node.oid === selectedNode.oid);
  }

  // Navigate to the next visible node
  function navigateDown() {
    const flatList = getFlatNodeList();
    if (flatList.length === 0) return;
    
    const currentIndex = getSelectedNodeIndex();
    const nextIndex = currentIndex < flatList.length - 1 ? currentIndex + 1 : currentIndex;
    
    if (nextIndex !== currentIndex || !selectedNode) {
      selectedNode = flatList[nextIndex];
      scrollToNode(selectedNode);
      dispatch('select', { node: selectedNode });
    }
  }

  // Navigate to the previous visible node
  function navigateUp() {
    const flatList = getFlatNodeList();
    if (flatList.length === 0) return;
    
    const currentIndex = getSelectedNodeIndex();
    const prevIndex = currentIndex > 0 ? currentIndex - 1 : 0;
    
    if (prevIndex !== currentIndex || !selectedNode) {
      selectedNode = flatList[prevIndex];
      scrollToNode(selectedNode);
      dispatch('select', { node: selectedNode });
    }
  }

  // Expand the current node or navigate to first child
  function navigateRight() {
    if (!selectedNode) return;
    
    if (selectedNode.children && selectedNode.children.length > 0) {
      if (!selectedNode.expanded) {
        // Expand the node
        selectedNode.expanded = true;
        filteredTree = [...filteredTree];
      } else {
        // Already expanded, navigate to first child
        selectedNode = selectedNode.children[0];
        scrollToNode(selectedNode);
        dispatch('select', { node: selectedNode });
      }
    }
  }

  // Collapse the current node or navigate to parent
  function navigateLeft() {
    if (!selectedNode) return;
    
    if (selectedNode.expanded && selectedNode.children && selectedNode.children.length > 0) {
      // Collapse the node
      selectedNode.expanded = false;
      filteredTree = [...filteredTree];
    } else if (selectedNode.parent) {
      // Navigate to parent
      selectedNode = selectedNode.parent;
      scrollToNode(selectedNode);
      dispatch('select', { node: selectedNode });
    }
  }

  // Toggle expand/collapse
  function toggleExpand() {
    if (!selectedNode) return;
    
    if (selectedNode.children && selectedNode.children.length > 0) {
      selectedNode.expanded = !selectedNode.expanded;
      filteredTree = [...filteredTree];
    }
  }

  // Navigate to first node
  function navigateToFirst() {
    const flatList = getFlatNodeList();
    if (flatList.length === 0) return;
    
    selectedNode = flatList[0];
    scrollToNode(selectedNode);
    dispatch('select', { node: selectedNode });
  }

  // Navigate to last visible node
  function navigateToLast() {
    const flatList = getFlatNodeList();
    if (flatList.length === 0) return;
    
    selectedNode = flatList[flatList.length - 1];
    scrollToNode(selectedNode);
    dispatch('select', { node: selectedNode });
  }

  // Scroll to make the node visible
  function scrollToNode(node) {
    // Wait for next tick to ensure DOM is updated
    setTimeout(() => {
      // Find the node element by OID (we'll need to add data attributes)
      const nodeElement = treeContainerElement?.querySelector(`[data-oid="${node.oid}"]`);
      if (nodeElement) {
        nodeElement.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      }
    }, 50);
  }

  // Focus the search input
  function focusSearch() {
    searchInputElement?.focus();
  }

  // Main keyboard event handler
  function handleKeyboardNavigation(event) {
    // Don't interfere if user is typing in an input
    if (event.target.tagName === 'INPUT' || event.target.tagName === 'TEXTAREA') {
      // Allow "/" to escape from search
      if (event.key === 'Escape' && event.target === searchInputElement) {
        searchInputElement.blur();
        event.preventDefault();
      }
      return;
    }

    // Keyboard shortcuts
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        navigateDown();
        break;
      
      case 'ArrowUp':
        event.preventDefault();
        navigateUp();
        break;
      
      case 'ArrowRight':
        event.preventDefault();
        navigateRight();
        break;
      
      case 'ArrowLeft':
        event.preventDefault();
        navigateLeft();
        break;
      
      case 'Enter':
        event.preventDefault();
        toggleExpand();
        break;
      
      case 'Home':
        event.preventDefault();
        navigateToFirst();
        break;
      
      case 'End':
        event.preventDefault();
        navigateToLast();
        break;
      
      case '/':
        event.preventDefault();
        focusSearch();
        break;
      
      case '?':
        event.preventDefault();
        showKeyboardHelp = !showKeyboardHelp;
        break;
      
      default:
        // Do nothing for other keys
        break;
    }
  }
</script>

<div class="panel mib-panel">
  <div class="search-bar">
    <input 
      type="text" 
      placeholder={$_('mib.searchPlaceholder')}
      bind:value={searchTerm} 
      bind:this={searchInputElement}
    />
  </div>

  {#if searchTerm || activeTypeFilters.length > 0 || activeAccessFilters.length > 0}
    <div class="filter-chips">
      <div class="chip-group">
        <span class="chip-label">{$_('mib.filterType')}</span>
        {#each ['Scalar', 'Table', 'Column', 'Notification'] as type}
          <button
            class="filter-chip"
            class:active={activeTypeFilters.includes(type)}
            on:click={() => toggleTypeFilter(type)}
          >
            {type}
          </button>
        {/each}
      </div>
      <div class="chip-group">
        <span class="chip-label">{$_('mib.filterAccess')}</span>
        {#each ['read-only', 'read-write'] as access}
          <button
            class="filter-chip"
            class:active={activeAccessFilters.includes(access)}
            on:click={() => toggleAccessFilter(access)}
          >
            {access}
          </button>
        {/each}
      </div>
    </div>
  {/if}

  <div class="tree-actions">
    <button class="btn tertiary" on:click={() => toggleAll(true)}>{$_('mib.expandAll')}</button>
    <button class="btn tertiary" on:click={() => toggleAll(false)}>{$_('mib.collapseAll')}</button>
    <button 
      class="btn tertiary compact-mode-btn" 
      class:active={compactMode}
      on:click={() => compactMode = !compactMode}
      title={compactMode ? $_('mib.compactDisable') : $_('mib.compactEnable')}
    >
      <Icon name={compactMode ? 'chevrons-down-up' : 'chevrons-up-down'} />
    </button>
    <button
      class="btn tertiary keyboard-help-btn"
      on:click={() => showKeyboardHelp = !showKeyboardHelp}
      title={$_('mib.keyboardTooltip')}
    >
      <Icon name="keyboard" />
    </button>
  </div>

  <Breadcrumb 
    {selectedNode} 
    on:navigate={handleBreadcrumbNavigation}
  />

  <KeyboardHelp visible={showKeyboardHelp} on:close={() => showKeyboardHelp = false} />

  <FavoritesPanel on:navigate={(e) => handleFavoriteClick(e.detail)} />

  <div class="tree-container" bind:this={treeContainerElement}>
    {#if $mibStore.isLoading}
      <p>{$_('mib.loadingMibs')}</p>
    {:else if filteredTree.length === 0}
      <p class="no-mibs">
        {#if searchTerm}
          {$_('mib.noMibsFilter')}
        {:else}
          {$_('mib.noMibsLoaded')}
        {/if}
      </p>
    {/if}
    {#each filteredTree as rootNode (rootNode.oid)}
      <TreeNode
        node={rootNode}
        onNodeClick={handleNodeClick}
        {compactMode}
        {searchTerm}
        bind:selectedNode
        on:contextmenu={handleContextMenu}
        on:dblclicknode={handleDblClickNode}
        on:showtooltip={handleShowTooltip}
        on:hidetooltip={handleHideTooltip}
      />
    {/each}
  </div>

  <NodeDetails {selectedNode} />
</div>

{#if contextMenu.visible}
  <ContextMenu 
    x={contextMenu.x} 
    y={contextMenu.y} 
    items={contextMenu.items}
    on:close={closeContextMenu}
    on:action={handleContextMenuAction}
  />
{/if}

<Tooltip 
  node={tooltip.node}
  x={tooltip.x}
  y={tooltip.y}
  visible={tooltip.visible}
/>

<style>
  .mib-panel {
    flex: 1;
    padding: 10px;
    border-right: 1px solid var(--border-color);
    display: flex;
    flex-direction: column;
    overflow-x: hidden;
    overflow-y: auto;
    width: 100%;
    box-sizing: border-box;
  }

  .mib-panel * {
    box-sizing: border-box;
  }

  .search-bar {
    margin-bottom: 10px;
    flex-shrink: 0;
  }

  .search-bar input {
    width: 100%;
    padding: 8px 10px;
    background-color: var(--bg-lighter-color);
    border: 1px solid var(--border-color);
    border-radius: 4px;
    color: var(--text-color);
  }

  .tree-actions {
    display: flex;
    gap: 10px;
    margin-bottom: 10px;
    flex-wrap: wrap;
    flex-shrink: 0;
  }

  .compact-mode-btn.active {
    background-color: var(--favorites-subtle-strong) !important;
    border-color: var(--favorites-border-strong) !important;
    color: var(--favorites-color) !important;
  }

  .tree-container {
    flex-grow: 1;
    /* Allow the tree to scroll inside its own area instead of growing the
       whole panel and crushing the fixed elements above it (breadcrumb…). */
    min-height: 0;
    overflow-y: auto;
    overflow-x: hidden;
    margin-bottom: 10px;
  }
  
  .no-mibs {
    color: var(--text-muted);
    padding: 10px;
  }

  .btn.tertiary {
    background-color: transparent;
    border: 1px solid var(--border-color);
    color: var(--text-color);
    flex-grow: 1; /* Make buttons share space equally */
  }
  .btn.tertiary:hover {
    background-color: var(--bg-lighter-color);
    border-color: var(--border-hover);
  }

  .btn.keyboard-help-btn {
    flex-grow: 0;
    padding: 8px 12px;
    font-size: 1.1em;
  }

  /* Focus indicator for keyboard navigation */
  .tree-container:focus-within {
    outline: 2px solid var(--accent-color);
    outline-offset: -2px;
  }

  .filter-chips {
    padding: 6px 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    border-bottom: 1px solid var(--border-color);
    background-color: var(--bg-light-color);
  }

  .chip-group {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
  }

  .chip-label {
    font-size: 0.75em;
    color: var(--text-muted);
    margin-right: 4px;
    font-weight: 500;
  }

  .filter-chip {
    padding: 2px 8px;
    border-radius: 12px;
    border: 1px solid var(--border-color);
    background: transparent;
    color: var(--text-dimmed);
    font-size: 0.75em;
    cursor: pointer;
    transition: all 0.15s;
  }

  .filter-chip:hover {
    border-color: var(--accent-color);
    color: var(--text-color);
  }

  .filter-chip.active {
    background-color: var(--accent-color);
    border-color: var(--accent-color);
    color: white;
  }

</style>