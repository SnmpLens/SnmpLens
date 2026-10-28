package mib

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// A file written in GBK or Shift_JIS reads as written once its encoding is
// chosen, and as Latin garbage by the guess: that is why the choice exists.
func TestAMibReadsInTheEncodingChosen(t *testing.T) {
	text := "EXAMPLE-MIB DEFINITIONS ::= BEGIN\r\n-- %s\r\nEND\r\n"
	for _, tc := range []struct {
		name  string
		prose string
		bytes func(string) ([]byte, error)
	}{
		{"gbk", "温度传感器", func(s string) ([]byte, error) { return simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s)) }},
		{"shift_jis", "温度センサー", func(s string) ([]byte, error) { return japanese.ShiftJIS.NewEncoder().Bytes([]byte(s)) }},
	} {
		want := strings.ReplaceAll(strings.Replace(text, "%s", tc.prose, 1), "\r\n", "\n")
		raw, err := tc.bytes(strings.Replace(text, "%s", tc.prose, 1))
		if err != nil {
			t.Fatal(err)
		}
		guessed, _, enc := DecodeSource(raw)
		if enc != EncodingWindows1252 || strings.Contains(guessed, tc.prose) {
			t.Errorf("%s: the guess should read it as Windows-1252 and lose the prose, got %s", tc.name, enc)
		}
		got, eol, err := DecodeAs(raw, tc.name)
		if err != nil || got != want || eol != "crlf" {
			t.Errorf("%s: %q, %s, %v", tc.name, got, eol, err)
		}
	}
}

func TestOnlyTheEncodingsOffered(t *testing.T) {
	latin := []byte("EXAMPLE-MIB DEFINITIONS ::= BEGIN\n-- R\xe9glage \x96 voir\nEND\n")
	if _, _, err := DecodeAs(latin, "utf-8"); err == nil {
		t.Error("bytes that are not UTF-8 were read as UTF-8, which would write U+FFFD over them on save")
	}
	if _, _, err := DecodeAs(latin, "ebcdic"); err == nil {
		t.Error("an encoding not offered was accepted")
	}
	guessed, _, _ := DecodeSource(latin)
	chosen, _, err := DecodeAs(latin, "Windows-1252")
	if err != nil || chosen != guessed {
		t.Errorf("Windows-1252 chosen reads as the guess does: %q / %q, %v", chosen, guessed, err)
	}
	for _, name := range Encodings() {
		if _, _, err := DecodeAs([]byte("A DEFINITIONS ::= BEGIN END"), name); err != nil {
			t.Errorf("%s is offered and does not read ASCII: %v", name, err)
		}
	}
}

func TestTheChecksumIsOfTheBytes(t *testing.T) {
	if ChecksumBytes([]byte("a\r\n")) == ChecksumBytes([]byte("a\n")) {
		t.Error("two files that differ on disk have one checksum")
	}
}
