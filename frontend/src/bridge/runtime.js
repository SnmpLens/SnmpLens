// What the interface asks of the Wails runtime, in one place. The screenshot and
// demo builds and the tests replace this module, and bridge/App.js beside it, so
// nothing else imports @wailsio/runtime.
import { Call, Events } from '@wailsio/runtime';

// A Go error reaches a caller as a RuntimeError, and the components show what they
// catch: `${e}`, String(e), `e.message || e`. Without this every message on screen
// would begin with "RuntimeError: ".
Call.RuntimeError.prototype.toString = function toString() {
  return this.message;
};

/**
 * Listens for an event from Go. The callback gets what Go emitted, and the
 * returned function stops listening.
 */
export function EventsOn(name, callback) {
  return Events.On(name, (event) => callback(event.data));
}

export function EventsOff(name) {
  Events.Off(name);
}

let stopDrop = null;

/**
 * Listens for files dropped on the window, from anywhere in the operating system.
 * The callback gets their paths. Only what is inside an element marked
 * `data-file-drop-target` counts as a drop.
 */
export function OnFileDrop(callback) {
  OnFileDropOff();
  stopDrop = EventsOn('files:dropped', callback);
}

export function OnFileDropOff() {
  stopDrop?.();
  stopDrop = null;
}
