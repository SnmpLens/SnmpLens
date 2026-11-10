/**
 * What the browser demo's simulator answers.
 *
 * The demo has no backend, and a binding no scene answers falls through to the
 * generated fixture — which answers a List… call with an empty array. So the
 * simulator dialog offered no model to make a device from and listed no device.
 *
 * The answers are the screenshot scenes' own, not written a second time: the
 * catalogue and the bench of the "simulator" scene, the lab switch of the
 * "simulator-data" one, the custom model's icon and the suggested address of the
 * editor's. vite.demo.config.js takes the opening state from the catalogue for
 * the same reason — the demo and the pictures of it cannot drift apart.
 *
 * What a simulated device DOES stays refused (bridge/dynamic.js, DEMO_ONLY):
 * starting one opens a UDP socket, and a web page has none to open.
 */
export function demoBindings(scenes) {
  const from = (name) => {
    const found = scenes.find((s) => s.name === name);
    if (!found) throw new Error(`The demo takes its simulator from the ${name} scene, and the catalogue has none.`);
    return found.bindings;
  };
  const bench = from('simulator-dark');
  const editor = from('simulator-editor-dark');
  const lab = from('simulator-data-dark');

  return {
    ListSimulatorModels: bench.ListSimulatorModels,
    ListSimulatorModelIcons: editor.ListSimulatorModelIcons,
    ListSimulatedDevices: [...bench.ListSimulatedDevices, ...lab.ListSimulatedDevices],
    // The bench is shown as the scene photographs it, cables included.
    ListSimulatorLinks: bench.ListSimulatorLinks || [],
    SimulatorSuggestAddress: editor.SimulatorSuggestAddress,
    // One answer for every device, so the editor opens on identifiers rather
    // than on blank fields that read as passwords lost. The bench's server
    // speaks v3 as "ops" and sends v2c traps to its first destination.
    SimulatorDeviceCredentials: {
      ...lab.SimulatorDeviceCredentials,
      users: { ops: { authPass: 'demo-auth-passphrase', privPass: 'demo-priv-passphrase' } },
      destinations: { d1: 'public' },
    },
  };
}
