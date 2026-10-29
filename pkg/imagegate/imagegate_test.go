package imagegate

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// pngOf encodes a real PNG, because the gate is a DECODE and a handmade file
// would test a sniffer this package deliberately does not have. The one
// exception is pngHeaderOf below, for what a header DECLARES.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding a PNG: %v", err)
	}
	return buf.Bytes()
}

// pngHeaderOf is a PNG that is only its header: the signature and an IHDR
// declaring w x h RGBA, with its CRC. It is what a decompression bomb looks
// like to DecodeConfig, which stops reading there — and it costs nothing to
// make, where encoding 64 real megapixels under -race took most of this
// package's test time.
func pngHeaderOf(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8 bits per sample, RGBA
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)))
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

// The three formats that get in, and what the gate learned about them.
func TestTheThreeFormatsAreAccepted(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 12, 8))

	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, nil); err != nil {
		t.Fatal(err)
	}
	var g bytes.Buffer
	if err := gif.Encode(&g, img, nil); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		data  []byte
		media string
	}{
		{pngOf(t, 12, 8), "image/png"},
		{jpg.Bytes(), "image/jpeg"},
		{g.Bytes(), "image/gif"},
	}
	for _, c := range cases {
		info, err := Inspect(c.data)
		if err != nil {
			t.Fatalf("%s was refused: %v", c.media, err)
		}
		if info.Media != c.media {
			t.Errorf("media = %q, want %q", info.Media, c.media)
		}
		if info.Width != 12 || info.Height != 8 {
			t.Errorf("%s: %dx%d, want 12x8", c.media, info.Width, info.Height)
		}
	}
}

// The format is what the DECODER says, never what the name says.
//
// Nothing here takes a name at all, and that is the point: the one place this
// picture ends up is an <img> in a WebView, and the media type it is served
// with is what the browser believes. Reading it off the extension is how a
// browser gets asked to sniff.
func TestTheFormatComesFromTheBytes(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(g.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != "gif" || info.Media != "image/gif" {
		t.Errorf("a GIF called anything is still a GIF: %+v", info)
	}
}

// SVG is refused, and it is NAMED.
//
// It is the format somebody will reach for first — it is a drawing, it scales,
// every diagram tool exports it — so refusing it silently looks like a bug. It
// is refused because an <svg> is a document with script in it and this
// application renders into a WebView with a bridge to the operating system on
// the other side.
func TestAnSvgIsRefusedAndSaidSo(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10">` +
		`<script>fetch('http://example.invalid')</script><rect width="10" height="10"/></svg>`)
	_, err := Inspect(svg)
	if err == nil {
		t.Fatal("an SVG was accepted")
	}
	if !strings.Contains(err.Error(), "SVG") {
		t.Errorf("the refusal does not say what it is: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "deliberately") {
		t.Errorf("the refusal reads as a failure rather than a decision: %v", err)
	}
}

// The other four things people download by mistake. All of them reach a decoder
// as the same sentence, and saying which one it is turns a puzzle into a
// fixable mistake — the rule pkg/mib/diagnose.go follows.
func TestWhatPeopleActuallyPickByMistake(t *testing.T) {
	cases := []struct {
		why  string
		data []byte
		says string
	}{
		{"a PDF", []byte("%PDF-1.7\n1 0 obj\n"), "PDF"},
		{"a zip", []byte("PK\x03\x04\x14\x00\x00\x00"), "zip"},
		{"an HTML page", []byte("<!doctype html>\n<html><body>Sign in</body></html>"), "HTML"},
		{"a WebP", append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...), "WebP"},
	}
	for _, c := range cases {
		_, err := Inspect(c.data)
		if err == nil {
			t.Errorf("%s was accepted", c.why)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: the refusal does not name it: %v", c.why, err)
		}
	}
}

// Two shapes of the same trick, and DecodeConfig is what makes refusing them
// cheap: it reads the header and never allocates the pixels, so the bomb is
// refused without ever being built — by the gate, and by this test.
func TestADecompressionBombIsRefusedByItsHeader(t *testing.T) {
	// An ordinary size gets in, which is also what proves the header alone is
	// read as the image it declares: otherwise a refusal below could be the
	// decoder rejecting a malformed header, and prove nothing.
	if info, err := Inspect(pngHeaderOf(1200, 800)); err != nil || info.Width != 1200 || info.Height != 800 {
		t.Fatalf("a header declaring 1200x800: %+v, %v", info, err)
	}
	if _, err := Inspect(pngHeaderOf(30000, 4)); err == nil || !strings.Contains(err.Error(), "on a side") {
		t.Errorf("a 30000-pixel side: %v", err)
	}
	// 8000 x 8000 is 64 megapixels: inside the per-side bound and past the
	// area one. Both bounds are needed; either alone lets one shape through.
	if _, err := Inspect(pngHeaderOf(8000, 8000)); err == nil || !strings.Contains(err.Error(), "megapixels") {
		t.Errorf("a 64-megapixel image: %v", err)
	}
}

func TestNothingAndTooMuchAreBothRefused(t *testing.T) {
	if _, err := Inspect(nil); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := Inspect(make([]byte, MaxBytes+1)); err == nil {
		t.Error("a file over the bound was accepted")
	}
}

// A LEAF, and nothing else. The background directory sits beside mibs/ and
// presets/, one level under monitoring.db, service.json and the secret store.
func TestOnlyALeafNameIsValid(t *testing.T) {
	for _, ok := range []string{"rack-a.png", "Salle B 2026.jpg", "plan.gif", "a.png"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	bad := []string{
		"", "   ", " rack.png", "rack.png ",
		"../monitoring.db", "..\\service.json", "sub/rack.png", `sub\rack.png`,
		"C:rack.png", ".hidden.png", "rack\n.png", "rack\x00.png",
		strings.Repeat("a", 121) + ".png",
	}
	for _, name := range bad {
		if err := ValidName(name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}
