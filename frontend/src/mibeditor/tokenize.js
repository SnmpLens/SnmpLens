/**
 * A tokeniser for SMIv2, the language MIBs are written in.
 *
 * Written as CodeMirror's stream parsers are (smiStream): one token per call,
 * the state — inside a string or not — carried from line to line, since a
 * DESCRIPTION spans many. mibeditor/smiLanguage.js gives it to the editor;
 * highlight() runs the very same tokeniser to produce markup, which is what
 * tests/tokenize.test.mjs reads, so the editor's colours are the ones tested.
 *
 * The rules are SMI's: a comment runs from `--` to the next `--` or to the end
 * of the line, a string from one quote to the next across lines, and a word is
 * coloured by the closed vocabularies below.
 */

// The SMIv2 macro keywords: what a definition IS.
const MACROS = new Set([
  'MODULE-IDENTITY', 'OBJECT-IDENTITY', 'OBJECT-TYPE', 'NOTIFICATION-TYPE',
  'TEXTUAL-CONVENTION', 'OBJECT-GROUP', 'NOTIFICATION-GROUP', 'MODULE-COMPLIANCE',
  'AGENT-CAPABILITIES', 'TRAP-TYPE',
]);

// Clause keywords: the fields inside a definition.
const CLAUSES = new Set([
  'DEFINITIONS', 'BEGIN', 'END', 'IMPORTS', 'EXPORTS', 'FROM',
  'SYNTAX', 'UNITS', 'MAX-ACCESS', 'ACCESS', 'MIN-ACCESS', 'STATUS',
  'DESCRIPTION', 'REFERENCE', 'INDEX', 'AUGMENTS', 'IMPLIED', 'DEFVAL',
  'LAST-UPDATED', 'ORGANIZATION', 'CONTACT-INFO', 'REVISION',
  'OBJECTS', 'NOTIFICATIONS', 'OBJECT', 'MODULE', 'MANDATORY-GROUPS',
  'GROUP', 'WRITE-SYNTAX', 'PRODUCT-RELEASE', 'SUPPORTS', 'VARIABLES',
  'IDENTIFIER', 'SEQUENCE', 'OF', 'CHOICE', 'SIZE',
]);

// Base and application types.
const TYPES = new Set([
  'INTEGER', 'Integer32', 'Unsigned32', 'Counter32', 'Counter64', 'Gauge32',
  'TimeTicks', 'OCTET', 'STRING', 'OBJECT', 'NULL', 'BITS', 'IpAddress',
  'Opaque', 'DisplayString', 'TruthValue', 'RowStatus', 'PhysAddress',
  'MacAddress', 'TimeStamp', 'TimeInterval', 'DateAndTime', 'AutonomousType',
  'TestAndIncr', 'RowPointer', 'StorageType', 'TDomain', 'TAddress',
]);

// The closed vocabularies of MAX-ACCESS and STATUS. Highlighting these makes a
// typo like "read-onl" visibly not a keyword.
const VALUES = new Set([
  'read-only', 'read-write', 'read-create', 'not-accessible',
  'accessible-for-notify', 'write-only',
  'current', 'deprecated', 'obsolete', 'mandatory', 'optional',
  'true', 'false',
]);

const ESCAPES = { '&': '&amp;', '<': '&lt;', '>': '&gt;' };

function escapeHtml(s) {
  return s.replace(/[&<>]/g, (c) => ESCAPES[c]);
}

/**
 * Classify one bare word.
 * @param {string} word
 * @returns {string|null} a token class, or null to leave it plain
 */
function classifyWord(word) {
  if (MACROS.has(word)) return 'mac';
  if (CLAUSES.has(word)) return 'kw';
  if (TYPES.has(word)) return 'ty';
  if (VALUES.has(word)) return 'val';
  if (/^\d+$/.test(word)) return 'num';
  return null;
}

/**
 * Whether position `index` sits inside a quoted string.
 *
 * This is what lets the editor colour only the visible window. Highlighting
 * carries `inString` across lines — a DESCRIPTION spans many — so starting
 * mid-file needs to know the state at that point, and getting it wrong repaints
 * half the file as a string. Scanning for it costs a fraction of tokenising,
 * because it produces nothing: no markup, no escaping, no allocation per token.
 *
 * @param {string} text
 * @param {number} index character offset
 * @returns {boolean}
 */
export function stringStateAt(text, index) {
  const upto = Math.max(0, Math.min(index, text.length));
  let inString = false;
  let i = 0;

  while (i < upto) {
    const ch = text[i];
    if (inString) {
      if (ch === '"') inString = false;
      i++;
      continue;
    }
    if (ch === '"') {
      inString = true;
      i++;
      continue;
    }
    // A comment runs to the next `--` or to end of line, and a quote inside it
    // opens nothing.
    if (ch === '-' && text[i + 1] === '-') {
      let j = i + 2;
      while (j < text.length && text[j] !== '\n') {
        if (text[j] === '-' && text[j + 1] === '-') { j += 2; break; }
        j++;
      }
      i = j;
      continue;
    }
    // A string never spans a blank line boundary in practice, but the
    // tokeniser resets nothing at newlines, so neither does this.
    i++;
  }
  return inString;
}

/**
 * The SMI tokeniser, as a CodeMirror stream parser: `token` reads one token
 * from the stream and names its class, or returns null for plain text.
 */
export const smiStream = {
  name: 'smi',
  startState: () => ({ inString: false }),
  copyState: (state) => ({ inString: state.inString }),
  token(stream, state) {
    // A multi-line DESCRIPTION string swallows everything until its closing
    // quote, including what would otherwise look like keywords.
    if (state.inString) {
      while (!stream.eol()) {
        if (stream.next() === '"') {
          state.inString = false;
          break;
        }
      }
      return 'str';
    }
    if (stream.eatSpace()) return null;
    const ch = stream.peek();
    if (ch === '"') {
      stream.next();
      state.inString = true;
      return 'str';
    }
    // `--` opens a comment that ends at the next `--` or at end of line.
    if (stream.match('--')) {
      while (!stream.eol()) {
        if (stream.match('--')) break;
        stream.next();
      }
      return 'com';
    }
    const word = stream.match(/^[A-Za-z0-9][A-Za-z0-9_-]*/);
    if (word) return classifyWord(word[0]);
    if (stream.match('::=')) return 'op';
    stream.next();
    return '{}(),;|'.includes(ch) ? 'pun' : null;
  },
};

/**
 * The part of CodeMirror's StringStream smiStream uses, for running it
 * without an editor: highlight() below, and the tests.
 */
class LineStream {
  constructor(string) {
    this.string = string;
    this.pos = 0;
    this.start = 0;
  }

  eol() {
    return this.pos >= this.string.length;
  }

  peek() {
    return this.string.charAt(this.pos) || undefined;
  }

  next() {
    if (this.pos < this.string.length) return this.string.charAt(this.pos++);
    return undefined;
  }

  eatSpace() {
    const from = this.pos;
    while (/[\s ]/.test(this.string.charAt(this.pos)) && this.pos < this.string.length) this.pos++;
    return this.pos > from;
  }

  match(pattern) {
    if (typeof pattern === 'string') {
      if (this.string.startsWith(pattern, this.pos)) {
        this.pos += pattern.length;
        return true;
      }
      return null;
    }
    const m = this.string.slice(this.pos).match(pattern);
    if (!m || m.index > 0) return null;
    this.pos += m[0].length;
    return m;
  }
}

/**
 * MIB source as highlighted HTML, by the same tokeniser the editor uses.
 *
 * @param {string} text
 * @param {boolean} [startInString] the state at the first character
 * @returns {string} HTML whose text content is character-identical to `text`
 */
export function highlight(text, startInString = false) {
  const state = { inString: startInString };
  const lines = String(text ?? '').split('\n');
  let out = '';
  for (let l = 0; l < lines.length; l++) {
    const stream = new LineStream(lines[l]);
    while (!stream.eol()) {
      stream.start = stream.pos;
      const cls = smiStream.token(stream, state);
      const piece = escapeHtml(stream.string.slice(stream.start, stream.pos));
      out += cls ? `<span class="${cls}">${piece}</span>` : piece;
    }
    if (l < lines.length - 1) out += '\n';
  }
  return out;
}

/** Snippets: the skeletons nobody remembers the exact shape of. */
export const SNIPPETS = [
  {
    key: 'objectType',
    text: `${'${name}'} OBJECT-TYPE
    SYNTAX      Integer32
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION
        "Describe what this object reports."
    ::= { parent 1 }
`,
  },
  {
    key: 'moduleIdentity',
    text: `${'${name}'} MODULE-IDENTITY
    LAST-UPDATED "202601010000Z"
    ORGANIZATION "Your organisation"
    CONTACT-INFO
        "Who to contact."
    DESCRIPTION
        "What this module covers."
    ::= { enterprises 99999 }
`,
  },
  {
    key: 'textualConvention',
    text: `${'${name}'} ::= TEXTUAL-CONVENTION
    STATUS      current
    DESCRIPTION
        "What this type means."
    SYNTAX      Integer32
`,
  },
  {
    key: 'table',
    // A table that LOADS.
    //
    // This used to emit the table and its row and nothing else: no SEQUENCE
    // type, so `SYNTAX <name>Entry` referred to nothing; no columns; and an
    // INDEX naming an object that did not exist. Inserting it produced a MIB
    // that cannot resolve, which is the opposite of what a snippet is for.
    // The row's SYNTAX also has to be the CAPITALISED type name — SMI type
    // references start with an upper-case letter.
    text: `${'${name}'}Table OBJECT-TYPE
    SYNTAX      SEQUENCE OF ${'${Name}'}Entry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION
        "What this table lists."
    ::= { parent 1 }

${'${name}'}Entry OBJECT-TYPE
    SYNTAX      ${'${Name}'}Entry
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION
        "One row."
    INDEX   { ${'${name}'}Index }
    ::= { ${'${name}'}Table 1 }

${'${Name}'}Entry ::= SEQUENCE {
    ${'${name}'}Index    Integer32,
    ${'${name}'}Descr    DisplayString
}

${'${name}'}Index OBJECT-TYPE
    SYNTAX      Integer32 (1..2147483647)
    MAX-ACCESS  not-accessible
    STATUS      current
    DESCRIPTION
        "Identifies a row."
    ::= { ${'${name}'}Entry 1 }

${'${name}'}Descr OBJECT-TYPE
    SYNTAX      DisplayString
    MAX-ACCESS  read-only
    STATUS      current
    DESCRIPTION
        "What this row is."
    ::= { ${'${name}'}Entry 2 }
`,
  },
  {
    key: 'imports',
    text: `IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE, Integer32, Counter32
        FROM SNMPv2-SMI
    DisplayString, TEXTUAL-CONVENTION
        FROM SNMPv2-TC;
`,
  },
  {
    key: 'module',
    text: `${'${name}'} DEFINITIONS ::= BEGIN

IMPORTS
    MODULE-IDENTITY, OBJECT-TYPE
        FROM SNMPv2-SMI;

END
`,
  },
];
