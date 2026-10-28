package mib

import (
	"strings"
	"unicode/utf8"
)

// What a MIB file's bytes say, as text.
//
// An SMI module is ASCII by grammar, and vendor MIBs are not: their
// DESCRIPTIONs and comments carry accented letters, typographic dashes and
// degree signs, written by whatever editor the vendor used — which, for a file
// that is not UTF-8, is overwhelmingly Windows-1252. gosmi does not mind (its
// lexer accepts any rune inside a string or a comment), so such a file LOADS;
// what refused it was everything around the loader. The editor would not open
// it, and the import gate turned it away whenever the first such byte came
// within its first 4 KiB, as "not valid UTF-8".
//
// Decoding it as Windows-1252 is a reading, not a certainty: a file in another
// legacy encoding comes out with the wrong letters in its prose. That is why
// the encoding is REPORTED wherever it matters — the editor says so before
// anything is saved — rather than applied silently.

// Encodings DecodeSource reports.
const (
	EncodingUTF8        = "utf-8"
	EncodingWindows1252 = "windows-1252"
)

// DecodeSource turns a MIB file's bytes into text: a UTF-8 byte order mark is
// dropped, a file that is not UTF-8 is read as Windows-1252, and line endings
// become LF. eol and encoding say what the file used.
func DecodeSource(raw []byte) (content, eol, encoding string) {
	encoding = EncodingUTF8
	var text string
	if utf8.Valid(raw) {
		text = string(raw)
	} else {
		text = decodeWindows1252(raw)
		encoding = EncodingWindows1252
	}
	content, eol = NormaliseSource([]byte(text))
	return content, eol, encoding
}

// windows1252High is what the bytes 0x80 to 0x9F mean in Windows-1252. The
// five the code page leaves undefined map to the C1 control of the same value,
// as the WHATWG decoder does, so every byte has a reading and none is lost.
var windows1252High = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

// decodeWindows1252 reads bytes as Windows-1252. Above 0x9F the code page is
// Latin-1, whose bytes are their own code points.
func decodeWindows1252(raw []byte) string {
	var b strings.Builder
	b.Grow(len(raw) + len(raw)/8)
	for _, c := range raw {
		switch {
		case c < 0x80:
			b.WriteByte(c)
		case c < 0xA0:
			b.WriteRune(windows1252High[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}
