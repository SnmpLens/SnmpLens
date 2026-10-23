// Text for a native OS notification, made safe to hand to the platform.
//
// Part of what a notification says is written by the network: a trap's
// snmpTrapOID.0 carries whatever the sender put there, typed however the sender
// chose, and the listener accepts a v1 or v2c notification from anyone. On
// Windows, Wails up to 2.16 passed the title and body to go-toast, which falls
// back to a generated PowerShell script when the WinRT toast API fails — with
// the text inside a double-quoted here-string, where PowerShell evaluates
// `$(...)`. Wails 2.17 removed that fallback. This is the second layer, and it
// stays: the text still lands in toast XML, inside a CDATA section that `]]>`
// closes, and in whatever the next platform backend does with it.
//
// Every native notification goes through nativeNotify.js, and nativeNotify.js
// puts every string through notificationText; tests/notifytext.test.mjs holds
// both.

const MAX_CHARS = 256;

/** A string fit for a notification: no control characters, no CDATA
 *  terminator, neither of the two characters PowerShell expands inside a
 *  double-quoted string, and bounded in length. */
export function notificationText(value) {
  const cleaned = String(value ?? '')
    .replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ')
    .replace(/]]>/g, '] ]>')
    .replace(/[$`]/g, '');
  // By code point, so a cut never splits a surrogate pair.
  return Array.from(cleaned).slice(0, MAX_CHARS).join('');
}

/** A trap OID as a notification may show it: numeric and dotted, or nothing.
 *  snmpTrapOID.0 is an OBJECT IDENTIFIER by definition, so a value that is not
 *  one is the sender's text, and it is not repeated on the desktop. */
export function trapOidText(value) {
  const s = String(value ?? '').replace(/^\./, '');
  return /^\d+(\.\d+){1,127}$/.test(s) ? s : '';
}
