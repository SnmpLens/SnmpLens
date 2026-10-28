import { writable } from 'svelte/store';

const STORAGE_KEY = 'snmplens_paths';

function createMibPathsStore() {
  const { subscribe, set, update } = writable({
    customPaths: [],
    detectedMibs: {},
    enabledMibs: {}
  });

  return {
    subscribe,
    
    // Add a custom MIB path
    addPath(path) {
      update(state => {
        if (!state.customPaths.includes(path)) {
          state.customPaths.push(path);
          this.save(state);
        }
        return state;
      });
    },
    
    // Remove a custom MIB path
    removePath(path) {
      update(state => {
        state.customPaths = state.customPaths.filter(p => p !== path);
        // Remove MIBs from this path
        delete state.detectedMibs[path];
        this.save(state);
        return state;
      });
    },
    
    // Set detected MIBs for a path
    setDetectedMibs(path, mibs) {
      update(state => {
        state.detectedMibs[path] = mibs;
        // Enable all MIBs by default
        mibs.forEach(mib => {
          if (state.enabledMibs[path] === undefined) {
            state.enabledMibs[path] = {};
          }
          if (state.enabledMibs[path][mib] === undefined) {
            state.enabledMibs[path][mib] = true;
          }
        });
        this.save(state);
        return state;
      });
    },
    
    // Toggle MIB enabled/disabled state
    toggleMib(path, mib) {
      update(state => {
        if (!state.enabledMibs[path]) {
          state.enabledMibs[path] = {};
        }
        state.enabledMibs[path][mib] = !state.enabledMibs[path][mib];
        this.save(state);
        return state;
      });
    },
    
    // Set whether a MIB loads, explicitly. toggleMib flips what is stored, and
    // a file the store has not seen yet is enabled without an entry: flipping
    // the missing entry made it `true`, which changed nothing.
    setMibEnabled(path, mib, enabled) {
      update(state => {
        if (!state.enabledMibs[path]) {
          state.enabledMibs[path] = {};
        }
        state.enabledMibs[path][mib] = !!enabled;
        this.save(state);
        return state;
      });
    },

    // A renamed file keeps whether it loads.
    renameMib(path, from, to) {
      update(state => {
        const flags = state.enabledMibs[path];
        if (flags && from in flags) {
          flags[to] = flags[from];
          delete flags[from];
          this.save(state);
        }
        return state;
      });
    },

    // Enable all MIBs in a path
    enableAllInPath(path) {
      update(state => {
        if (state.detectedMibs[path]) {
          state.detectedMibs[path].forEach(mib => {
            if (!state.enabledMibs[path]) {
              state.enabledMibs[path] = {};
            }
            state.enabledMibs[path][mib] = true;
          });
          this.save(state);
        }
        return state;
      });
    },
    
    // Disable all MIBs in a path
    disableAllInPath(path) {
      update(state => {
        if (state.detectedMibs[path]) {
          state.detectedMibs[path].forEach(mib => {
            if (!state.enabledMibs[path]) {
              state.enabledMibs[path] = {};
            }
            state.enabledMibs[path][mib] = false;
          });
          this.save(state);
        }
        return state;
      });
    },
    
    // Save to localStorage
    save(state) {
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
      } catch (e) {
        console.error('Failed to save MIB paths to localStorage:', e);
      }
    },
    
    // Load from localStorage
    load() {
      try {
        const stored = localStorage.getItem(STORAGE_KEY);
        if (stored) {
          const state = JSON.parse(stored);
          set(state);
        }
      } catch (e) {
        console.error('Failed to load MIB paths from localStorage:', e);
      }
    },
    
    // Clear all
    clear() {
      set({
        customPaths: [],
        detectedMibs: {},
        enabledMibs: {}
      });
      localStorage.removeItem(STORAGE_KEY);
    }
  };
}

export const mibPathsStore = createMibPathsStore();
