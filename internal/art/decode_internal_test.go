package art

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestDecodeRejectsHugeDimensions(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if _, err := decode(buf.Bytes()); err != nil {
		t.Fatalf("a small image decodes: %v", err)
	}
	// Rewrite the IHDR width and height to 100000x100000.
	b := buf.Bytes()
	binary.BigEndian.PutUint32(b[16:], 100000)
	binary.BigEndian.PutUint32(b[20:], 100000)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	_, err := decode(b)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("want a size error, got %v", err)
	}
}
