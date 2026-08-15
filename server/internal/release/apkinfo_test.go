package release

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// --- synthetic AXML builder ---------------------------------------------
//
// Hand-builds a minimal AndroidManifest.xml in AXML (Android Binary XML)
// format: a string pool (attribute-name placeholders + the versionName
// value), a resource map (string-pool index -> android attribute id), and a
// start tag carrying versionCode/versionName attributes. Every chunk's size
// field is computed from the actual bytes written, not hand-counted, so a
// wrong byte count fails loudly instead of silently drifting.

func u16le(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func u32le(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// utf8StringPoolEntry encodes one UTF-8 string pool entry: a 1-byte "utf16
// length" varint (only ever skipped by the parser, so any value <0x80
// works), a 1-byte UTF-8 byte-length varint, then the bytes themselves.
func utf8StringPoolEntry(s string) []byte {
	var out bytes.Buffer
	out.WriteByte(byte(len(s)))
	out.WriteByte(byte(len(s)))
	out.WriteString(s)
	return out.Bytes()
}

func chunk(typ, headerSize uint16, body []byte) []byte {
	var out bytes.Buffer
	out.Write(u16le(typ))
	out.Write(u16le(headerSize))
	out.Write(u32le(uint32(8 + len(body))))
	out.Write(body)
	return out.Bytes()
}

// buildStringPoolChunk lays out entries back to back and computes
// stringsStart/offsets. stringsStart is measured from the chunk's own
// start (matching the parser's `start + stringsStart` arithmetic), i.e.
// 8 (common ResChunk_header) + 20 (five u32 sub-header fields) + 4*n
// (offset table).
func buildStringPoolChunk(entries []string) []byte {
	var data bytes.Buffer
	offsets := make([]uint32, len(entries))
	for i, e := range entries {
		offsets[i] = uint32(data.Len())
		data.Write(utf8StringPoolEntry(e))
	}

	var sub bytes.Buffer
	sub.Write(u32le(uint32(len(entries)))) // stringCount
	sub.Write(u32le(0))                    // styleCount
	sub.Write(u32le(1 << 8))               // flags: UTF8_FLAG
	stringsStart := uint32(8+20) + uint32(len(entries))*4
	sub.Write(u32le(stringsStart)) // stringsStart, relative to chunk start
	sub.Write(u32le(0))            // stylesStart
	for _, off := range offsets {
		sub.Write(u32le(off))
	}
	sub.Write(data.Bytes())

	return chunk(0x0001, 0x001c, sub.Bytes())
}

func buildResourceMapChunk(resIDs []uint32) []byte {
	var sub bytes.Buffer
	for _, id := range resIDs {
		sub.Write(u32le(id))
	}
	return chunk(0x0180, 0x0008, sub.Bytes())
}

type testAttr struct {
	nameIdx     int32
	rawValueIdx int32
	dataType    byte
	data        uint32
}

func buildStartTagChunk(attrs []testAttr) []byte {
	var sub bytes.Buffer
	sub.Write(u32le(0))                  // lineNumber
	sub.Write(u32le(0))                  // comment
	sub.Write(u32le(0xFFFFFFFF))         // ns
	sub.Write(u32le(0xFFFFFFFF))         // name (element name string pool idx; unused by the parser)
	sub.Write(u16le(20))                 // attributeStart (not read by the parser; nominal ResXMLTree_attrExt size)
	sub.Write(u16le(20))                 // attributeSize
	sub.Write(u16le(uint16(len(attrs)))) // attributeCount
	sub.Write(u16le(0))                  // idIndex
	sub.Write(u16le(0))                  // classIndex
	sub.Write(u16le(0))                  // styleIndex
	for _, a := range attrs {
		sub.Write(u32le(0xFFFFFFFF))        // ns
		sub.Write(u32le(uint32(a.nameIdx))) // name (index into resourceMap via the parser's lookup)
		sub.Write(u32le(uint32(a.rawValueIdx)))
		sub.Write(u16le(8)) // ResValue.size
		sub.WriteByte(0)    // ResValue.res0
		sub.WriteByte(a.dataType)
		sub.Write(u32le(a.data))
	}
	return chunk(0x0102, 0x0010, sub.Bytes())
}

// buildManifest assembles a full synthetic AndroidManifest.xml with exactly
// one versionCode/versionName pair on the root element.
func buildManifest(versionCode int, versionName string) []byte {
	strs := buildStringPoolChunk([]string{"versionCode", "versionName", versionName})
	resMap := buildResourceMapChunk([]uint32{AttrVersionCode, AttrVersionName})
	startTag := buildStartTagChunk([]testAttr{
		{nameIdx: 0, rawValueIdx: -1, dataType: 0x10, data: uint32(versionCode)},
		{nameIdx: 1, rawValueIdx: 2, dataType: 0x03, data: 2},
	})

	var body bytes.Buffer
	body.Write(strs)
	body.Write(resMap)
	body.Write(startTag)

	var out bytes.Buffer
	out.Write(u16le(0x0003))
	out.Write(u16le(0x0008))
	out.Write(u32le(uint32(8 + body.Len())))
	out.Write(body.Bytes())
	return out.Bytes()
}

func buildTestApk(t *testing.T, versionCode int, versionName string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create apk: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write(buildManifest(versionCode, versionName)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return path
}

func TestReadApkInfoExtractsVersion(t *testing.T) {
	path := buildTestApk(t, 7, "1.2.3")

	info, err := ReadApkInfo(path)
	if err != nil {
		t.Fatalf("ReadApkInfo: %v", err)
	}
	if info.VersionCode != 7 {
		t.Errorf("VersionCode = %d, want 7", info.VersionCode)
	}
	if info.VersionName != "1.2.3" {
		t.Errorf("VersionName = %q, want %q", info.VersionName, "1.2.3")
	}
	if info.SHA256 == "" {
		t.Error("SHA256 is empty")
	}
	if info.SizeBytes == 0 {
		t.Error("SizeBytes is 0")
	}
}

func TestReadApkInfoCachesOnUnchangedFile(t *testing.T) {
	path := buildTestApk(t, 1, "0.1.0")

	first, err := ReadApkInfo(path)
	if err != nil {
		t.Fatalf("ReadApkInfo: %v", err)
	}
	second, err := ReadApkInfo(path)
	if err != nil {
		t.Fatalf("ReadApkInfo (cached): %v", err)
	}
	if first != second {
		t.Errorf("expected identical cached result, got %+v vs %+v", first, second)
	}
}

func TestReadApkInfoRejectsNonZip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-an-apk.apk")
	if err := os.WriteFile(path, []byte("definitely not a zip"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := ReadApkInfo(path); err == nil {
		t.Fatal("expected an error for a non-zip file, got nil")
	}
}

func TestReadApkInfoRejectsMissingManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-manifest.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("some-other-file.txt")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()
	_ = f.Close()

	if _, err := ReadApkInfo(path); err == nil {
		t.Fatal("expected an error when AndroidManifest.xml is missing, got nil")
	}
}
