import { writable, get } from 'svelte/store';
import { LoadMibsWithDiagnostics, GetPersistentMibDirectory, ListMibFiles, SetMibDirectories } from '../../wailsjs/go/app/App';
import { notificationStore } from './notifications';
import { mibPathsStore } from './mibPathsStore';

export const mibDiagnostics = writable([]);

function createMibStore() {
  const { subscribe, set, update } = writable({
    tree: [],
    isLoading: true,
    error: null,
  });

  // Add parent references to all nodes in the tree
  function addParentReferences(nodes, parent = null) {
    if (!nodes) return;
    nodes.forEach(node => {
      node.parent = parent;
      if (node.children && node.children.length > 0) {
        addParentReferences(node.children, node);
      }
    });
  }

  // Get list of enabled MIB files from mibPathsStore
  async function getEnabledMibFiles() {
    const pathsState = get(mibPathsStore);
    const defaultPath = await GetPersistentMibDirectory();
    const enabledFiles = [];

    // The DIRECTORY is what is there, not a list detected at start-up.
    //
    // Reading the cached list meant a MIB the editor had just written — the
    // documented way to import an external one — was never in it, so the
    // reload skipped the new file entirely and still reported success. The
    // store is consulted only for what has been switched OFF.
    let onDisk = pathsState.detectedMibs[defaultPath] || [];
    try {
      onDisk = (await ListMibFiles(defaultPath)) || onDisk;
    } catch (e) {
      // Fall back to the cached list rather than loading nothing.
    }
    onDisk.forEach(mib => {
      if (pathsState.enabledMibs[defaultPath]?.[mib] !== false) {
        enabledFiles.push(mib);
      }
    });

    // The folders added in the settings load from where they are: Go is told
    // which they are before every load (pkg/mib/library.go). Their files used
    // to be sent by name alone and looked up in the MIB folder, where they
    // were not — so nothing in an added folder ever loaded.
    try {
      await SetMibDirectories(pathsState.customPaths);
    } catch (e) {
      console.error('SetMibDirectories failed', e);
    }

    // Get enabled MIBs from custom paths
    pathsState.customPaths.forEach(customPath => {
      if (pathsState.detectedMibs[customPath]) {
        pathsState.detectedMibs[customPath].forEach(mib => {
          // A name the MIB folder already has is that file (an added folder's
          // copy is shadowed), and is asked for once.
          if (pathsState.enabledMibs[customPath]?.[mib] !== false && !enabledFiles.includes(mib)) {
            enabledFiles.push(mib);
          }
        });
      }
    });

    return enabledFiles;
  }

  async function loadInternal(silent = false) {
    update(store => ({ ...store, isLoading: true, error: null }));
    try {
      const enabledFiles = await getEnabledMibFiles();

      console.log(`Loading ${enabledFiles.length} enabled MIBs with diagnostics`);
      const response = await LoadMibsWithDiagnostics(enabledFiles);

      const tree = response.tree || [];
      const diagnostics = response.diagnostics || [];

      // Store diagnostics
      mibDiagnostics.set(diagnostics);

      // Add parent references for breadcrumb navigation
      addParentReferences(tree);
      set({ tree, isLoading: false, error: null });

      if (!silent) {
        const successCount = diagnostics.filter(d => d.success).length;
        const failCount = diagnostics.filter(d => !d.success).length;

        if (tree.length > 0) {
          let msg = `${successCount} MIB(s) loaded successfully.`;
          if (failCount > 0) {
            msg += ` ${failCount} failed.`;
          }
          notificationStore.add(msg, failCount > 0 ? 'info' : 'success');
        } else {
          notificationStore.add('No MIBs found or loaded from the directory.', 'info');
        }
      }
    } catch (err) {
      set({ tree: [], isLoading: false, error: err });
      mibDiagnostics.set([]);
      if (!silent) {
        notificationStore.add(`Error loading MIBs: ${err}`, 'error');
      }
    }
  }

  return {
    subscribe,
    load: () => loadInternal(false),
    loadSilent: () => loadInternal(true),
    // Exposed so the editor's reload asks the same question this store does.
    // It had its own copy with the opposite default for an unknown MIB, so
    // the two panels disagreed about the same directory.
    getEnabledMibFiles,
  };
}

export const mibStore = createMibStore();
