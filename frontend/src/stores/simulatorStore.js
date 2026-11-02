import { writable, derived } from 'svelte/store';
import {
  ListSimulatorModels,
  ListSimulatorModelIcons,
  ListSimulatedDevices,
  SimulatorSaveDevice,
  SimulatorDeleteDevice,
  SimulatorStartDevice,
  SimulatorStopDevice,
  SimulatorDeviceCredentials,
  SimulatorSuggestAddress,
  SimulatorSendTrap,
  ImportSimulatorModelsDialog,
  SimulatorDeleteModel,
  SimulatorRecordDevice,
  SimulatorCancelRecording,
  SimulatorExportModelDialog,
  ImportSimulatedDevicesDialog,
  SimulatorExportDevicesDialog,
  SimulatorDuplicateDevice,
  SimulatorRestartDevice,
  SimulatorSetFaults,
  SimulatorPreview,
  TrapListenerEngineID,
} from '../bridge/App';
import { EventsOn } from '../bridge/runtime';

/**
 * The simulated devices and the models they are made from, as Go holds them.
 *
 * Go keeps the only copy: this store lists and asks, and a change comes back
 * as 'simulator:changed' — which is also how the status in the header learns
 * of a device started from the modal, or stopped by the application closing a
 * session — or as 'simulator:models' when a model is imported or deleted.
 * Nothing here is persisted; the lists are re-read.
 */
function createSimulatorStore() {
  // recording is the recording under way — { objects } kept so far — or null.
  const { subscribe, update } = writable({ models: [], icons: {}, devices: [], recording: null, loaded: false });
  let subscribed = false;

  /** The devices, re-read: what the modal polls while it is open. */
  async function refresh() {
    const devices = await ListSimulatedDevices();
    update((s) => ({ ...s, devices: devices || [], loaded: true }));
  }

  /**
   * The models, and the custom ones' icons by model ID. Re-read when they
   * change and not with the devices: an icon crosses the bridge as a data URI,
   * and the devices are polled every two seconds.
   */
  async function refreshModels() {
    const [models, icons] = await Promise.all([ListSimulatorModels(), ListSimulatorModelIcons()]);
    update((s) => ({
      ...s,
      models: models || [],
      icons: Object.fromEntries((icons || []).map((i) => [i.id, i.icon])),
    }));
  }

  /** Subscribes once, however many times it is called, and lists. */
  function init() {
    if (!subscribed) {
      subscribed = true;
      EventsOn('simulator:changed', () => {
        refresh().catch(() => {});
      });
      // A model imported again restarts the devices made from it.
      EventsOn('simulator:models', () => {
        Promise.all([refreshModels(), refresh()]).catch(() => {});
      });
      // How far a recording has got, a few hundred objects at a time.
      EventsOn('simulator:recording', (objects) => {
        update((s) => (s.recording ? { ...s, recording: { objects } } : s));
      });
    }
    return Promise.all([refreshModels(), refresh()]);
  }

  return {
    subscribe,
    init,
    refresh,
    refreshModels,
    save: (device) => SimulatorSaveDevice(device),
    remove: (id) => SimulatorDeleteDevice(id),
    start: (id) => SimulatorStartDevice(id),
    stop: (id) => SimulatorStopDevice(id),
    credentials: async (id) => (await SimulatorDeviceCredentials(id)) || { community: '', users: {}, destinations: {} },
    suggestAddress: () => SimulatorSuggestAddress(),
    /** Sends a notification now; resolves to what became of it at each destination. */
    sendTrap: async (id, name) => (await SimulatorSendTrap(id, name)) || [],
    /** Asks for model files, JSON or ZIP, and imports them; resolves to what became of each. */
    importModels: async () => (await ImportSimulatorModelsDialog()) || [],
    removeModel: (id) => SimulatorDeleteModel(id),
    /**
     * Records a device as a model; resolves to what became of it, as an import
     * does, and rejects when it was stopped or could not be walked.
     */
    record: async (req) => {
      update((s) => ({ ...s, recording: { objects: 0 } }));
      try {
        return await SimulatorRecordDevice(req);
      } finally {
        update((s) => ({ ...s, recording: null }));
      }
    },
    stopRecording: () => SimulatorCancelRecording(),
    /** Writes a custom model to a ZIP the user names; resolves to where, or '' when cancelled. */
    exportModel: async (id) => (await SimulatorExportModelDialog(id)) || '',
    /** Asks for files of simulated devices and imports them; resolves to what became of each device. */
    importDevices: async () => (await ImportSimulatedDevicesDialog()) || [],
    /**
     * Writes devices — those named, or every one — to a file the user names,
     * their passwords only when withSecrets says so; resolves to where, or ''
     * when cancelled.
     */
    exportDevices: async (ids, withSecrets) => (await SimulatorExportDevicesDialog(ids || [], !!withSecrets)) || '',
    duplicate: (id, name) => SimulatorDuplicateDevice(id, name),
    restart: (id) => SimulatorRestartDevice(id),
    /** Changes what a device does wrong, at once and without restarting it. */
    setFaults: (id, faults) => SimulatorSetFaults(id, faults),
    /** What a device, as the editor holds it, would answer (previewQuery): { rows, total }. */
    preview: async (device, query) => (await SimulatorPreview(device, query)) || { rows: [], total: 0 },
    /** The engine ID SnmpLens's own trap listener stands for, in hex. */
    listenerEngineId: async () => (await TrapListenerEngineID()) || '',
  };
}

export const simulatorStore = createSimulatorStore();

/** How many simulated devices are answering: the figure in the header. */
export const runningSimulated = derived(simulatorStore, ($s) => $s.devices.filter((d) => d.running).length);
