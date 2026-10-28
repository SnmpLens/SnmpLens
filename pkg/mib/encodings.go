package mib

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// Reading a MIB in an encoding the person chooses.
//
// DecodeSource guesses — UTF-8, else Windows-1252 — and the guess is right for
// most files that are not UTF-8 and wrong for the rest: a MIB written in
// Shift_JIS or GBK has its DESCRIPTIONs turned into accented Latin garbage,
// and saving it then writes the garbage as UTF-8. The editor says which
// encoding it read a file in; these are the ones it can be told to read in
// instead. Saving still writes UTF-8, which every MIB tool reads.

// Encodings the editor offers, besides UTF-8 and Windows-1252.
var otherEncodings = map[string]encoding.Encoding{
	"iso-8859-1":   charmap.ISO8859_1,
	"iso-8859-15":  charmap.ISO8859_15,
	"windows-1250": charmap.Windows1250,
	"windows-1251": charmap.Windows1251,
	"koi8-r":       charmap.KOI8R,
	"shift_jis":    japanese.ShiftJIS,
	"euc-jp":       japanese.EUCJP,
	"gbk":          simplifiedchinese.GBK,
	"gb18030":      simplifiedchinese.GB18030,
	"big5":         traditionalchinese.Big5,
	"euc-kr":       korean.EUCKR,
}

// Encodings lists the encodings a MIB can be read as: the two DecodeSource
// reports first, the others after in a fixed order.
func Encodings() []string {
	return []string{
		EncodingUTF8, EncodingWindows1252,
		"iso-8859-1", "iso-8859-15", "windows-1250", "windows-1251", "koi8-r",
		"shift_jis", "euc-jp", "gbk", "gb18030", "big5", "euc-kr",
	}
}

// DecodeAs reads a MIB file's bytes in the named encoding, with line endings
// made LF as DecodeSource makes them. UTF-8 is refused for bytes that are not
// UTF-8: decoded anyway, every such byte would become U+FFFD, and saving would
// write those over the original.
func DecodeAs(raw []byte, name string) (content, eol string, err error) {
	var text string
	switch strings.ToLower(name) {
	case EncodingUTF8:
		if !utf8.Valid(raw) {
			return "", "", fmt.Errorf("this file is not valid UTF-8; choose the encoding it was written in")
		}
		text = string(raw)
	case EncodingWindows1252:
		// Ours, not x/text's: the five bytes the code page leaves undefined are
		// kept as the C1 control of the same value, as DecodeSource keeps them.
		text = decodeWindows1252(raw)
	default:
		enc, ok := otherEncodings[strings.ToLower(name)]
		if !ok {
			return "", "", fmt.Errorf("%q is not an encoding SnmpLens reads", name)
		}
		out, err := enc.NewDecoder().Bytes(raw)
		if err != nil {
			return "", "", fmt.Errorf("this file cannot be read as %s: %v", name, err)
		}
		text = string(out)
	}
	content, eol = NormaliseSource([]byte(text))
	return content, eol, nil
}

// ChecksumBytes identifies a file's bytes as they are on disk.
//
// What tells the editor a file changed underneath it. It was the checksum of
// the DECODED text, which made the check depend on the encoding: a file read
// as GBK and compared as decoded by the guess was "changed on disk" the moment
// it was opened. The bytes are what changed, or did not.
func ChecksumBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
