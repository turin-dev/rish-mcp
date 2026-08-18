package release

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
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

// utf16StringPoolEntry encodes one UTF-16 string pool entry: a 2-byte utf16
// length (with the 0x8000 high-bit form for lengths >= 0x8000 code units),
// then the UTF-16 code units themselves. No alignment padding — the parser
// never requires it.
func utf16StringPoolEntry(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	length := len(u16)
	var out bytes.Buffer
	if length < 0x8000 {
		out.Write(u16le(uint16(length)))
	} else {
		out.Write(u16le(uint16(0x8000 | length>>16)))
		out.Write(u16le(uint16(length & 0xffff)))
	}
	for _, c := range u16 {
		out.Write(u16le(c))
	}
	return out.Bytes()
}

// buildStringPoolChunkUTF16 is the UTF-16 (flags=0) twin of
// buildStringPoolChunk. Both must stay in sync: the header layout is
// identical, only the entry encoding differs.
func buildStringPoolChunkUTF16(entries []string) []byte {
	var data bytes.Buffer
	offsets := make([]uint32, len(entries))
	for i, e := range entries {
		offsets[i] = uint32(data.Len())
		data.Write(utf16StringPoolEntry(e))
	}

	var sub bytes.Buffer
	sub.Write(u32le(uint32(len(entries)))) // stringCount
	sub.Write(u32le(0))                    // styleCount
	sub.Write(u32le(0))                    // flags: 0 = UTF-16
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

func TestReadApkInfoCacheSeparatesImmutablePaths(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "one.apk"), filepath.Join(dir, "two.apk")}
	for i, path := range paths {
		if err := os.WriteFile(path, buildTestApkBytes(t, i+1, fmt.Sprintf("%d.0.0", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixedTime := time.Unix(1_700_000_000, 0)
	for _, path := range paths {
		if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
			t.Fatal(err)
		}
	}
	firstStat, err := os.Stat(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	secondStat, err := os.Stat(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	if firstStat.Size() != secondStat.Size() || !firstStat.ModTime().Equal(secondStat.ModTime()) {
		t.Fatalf("test requires identical size+mtime: first=%v second=%v", firstStat, secondStat)
	}

	first, err := ReadApkInfo(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadApkInfo(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	if first.VersionName != "1.0.0" || second.VersionName != "2.0.0" || first.SHA256 == second.SHA256 {
		t.Fatalf("distinct immutable paths aliased in info cache: first=%+v second=%+v", first, second)
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

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestReadApkInfoRejectsOversizedInflatedManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest-zip-bomb.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(w, zeroReader{}, maxAndroidManifestBytes+1); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ReadApkInfo(path)
	if err == nil || !strings.Contains(err.Error(), "AndroidManifest.xml exceeds") {
		t.Fatalf("expected inflated-manifest size rejection, got %v", err)
	}
}

// TestReadApkInfoUTF16StringPool end-to-end: an APK whose manifest string
// pool is UTF-16 (flags=0). Real-world APKs use UTF-16 pools ~as often as
// UTF-8, so the parser must handle both; this exercises the whole
// decodeString UTF-16 path through ReadApkInfo.
func TestReadApkInfoUTF16StringPool(t *testing.T) {
	var body bytes.Buffer
	body.Write(buildStringPoolChunkUTF16([]string{"versionCode", "versionName", "매니페스트"}))
	body.Write(buildResourceMapChunk([]uint32{AttrVersionCode, AttrVersionName}))
	body.Write(buildStartTagChunk([]testAttr{
		{nameIdx: 0, rawValueIdx: -1, dataType: 0x10, data: 17},
		{nameIdx: 1, rawValueIdx: 2, dataType: 0x03, data: 2},
	}))

	var axml bytes.Buffer
	axml.Write(u16le(0x0003))
	axml.Write(u16le(0x0008))
	axml.Write(u32le(uint32(8 + body.Len())))
	axml.Write(body.Bytes())

	var apk bytes.Buffer
	zw := zip.NewWriter(&apk)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write(axml.Bytes()); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "utf16.apk")
	if err := os.WriteFile(path, apk.Bytes(), 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}

	info, err := ReadApkInfo(path)
	if err != nil {
		t.Fatalf("ReadApkInfo: %v", err)
	}
	if info.VersionCode != 17 {
		t.Errorf("VersionCode = %d, want 17", info.VersionCode)
	}
	if info.VersionName != "매니페스트" {
		t.Errorf("VersionName = %q, want %q", info.VersionName, "매니페스트")
	}
}

// --- parseManifest error paths ---------------------------------------------

func TestParseManifestErrors(t *testing.T) {
	t.Run("not android binary xml", func(t *testing.T) {
		_, _, err := parseManifest([]byte("not axml"))
		if err == nil || err.Error() != "not android binary xml" {
			t.Fatalf("expected 'not android binary xml', got %v", err)
		}
	})

	t.Run("no start tag", func(t *testing.T) {
		var body bytes.Buffer
		body.Write(buildStringPoolChunk([]string{"versionCode"}))
		body.Write(buildResourceMapChunk([]uint32{AttrVersionCode}))
		var axml bytes.Buffer
		axml.Write(u16le(0x0003))
		axml.Write(u16le(0x0008))
		axml.Write(u32le(uint32(8 + body.Len())))
		axml.Write(body.Bytes())

		_, _, err := parseManifest(axml.Bytes())
		if err == nil || err.Error() != "no start tag in manifest" {
			t.Fatalf("expected 'no start tag in manifest', got %v", err)
		}
	})

	t.Run("root element has no version attrs", func(t *testing.T) {
		strs := buildStringPoolChunk([]string{"someAttr"})
		resMap := buildResourceMapChunk([]uint32{0x01010000})
		startTag := buildStartTagChunk([]testAttr{
			{nameIdx: 0, rawValueIdx: -1, dataType: 0x10, data: 0},
		})
		var body bytes.Buffer
		body.Write(strs)
		body.Write(resMap)
		body.Write(startTag)
		var axml bytes.Buffer
		axml.Write(u16le(0x0003))
		axml.Write(u16le(0x0008))
		axml.Write(u32le(uint32(8 + body.Len())))
		axml.Write(body.Bytes())

		_, _, err := parseManifest(axml.Bytes())
		if err == nil || err.Error() != "root element carries no versionCode/versionName" {
			t.Fatalf("expected 'root element carries no versionCode/versionName', got %v", err)
		}
	})
}

// --- parseStringPool error paths -------------------------------------------

func TestParseStringPoolErrors(t *testing.T) {
	t.Run("truncated header", func(t *testing.T) {
		_, err := parseStringPool([]byte{0x01, 0x02, 0x03}, 0)
		if err == nil || !strings.Contains(err.Error(), "truncated string pool header") {
			t.Fatalf("expected truncated header error, got %v", err)
		}
	})

	t.Run("truncated offset table", func(t *testing.T) {
		var buf bytes.Buffer
		buf.Write(u16le(0x0001))            // chunk type
		buf.Write(u16le(0x001c))            // header size
		buf.Write(u32le(0))                 // chunk size (placeholder)
		buf.Write(u32le(5))                 // stringCount = 5
		buf.Write(u32le(0))                 // styleCount
		buf.Write(u32le(0))                 // flags
		buf.Write(u32le(0))                 // stringsStart
		buf.Write(u32le(0))                 // stylesStart
		buf.Write([]byte{0x01, 0x02, 0x03}) // only 3 bytes for 5 offsets
		_, err := parseStringPool(buf.Bytes(), 0)
		if err == nil || !strings.Contains(err.Error(), "truncated string pool offset table") {
			t.Fatalf("expected truncated offset table error, got %v", err)
		}
	})

	t.Run("huge declared count is rejected before allocation", func(t *testing.T) {
		buf := make([]byte, 28)
		binary.LittleEndian.PutUint32(buf[8:12], ^uint32(0))
		_, err := parseStringPool(buf, 0)
		if err == nil || !strings.Contains(err.Error(), "truncated string pool offset table") {
			t.Fatalf("expected impossible string count rejection, got %v", err)
		}
	})

	t.Run("entry out of range", func(t *testing.T) {
		var buf bytes.Buffer
		buf.Write(u16le(0x0001)) // chunk type
		buf.Write(u16le(0x001c)) // header size
		buf.Write(u32le(0))      // chunk size
		buf.Write(u32le(1))      // stringCount = 1
		buf.Write(u32le(0))      // styleCount
		buf.Write(u32le(0))      // flags
		buf.Write(u32le(100))    // stringsStart = 100 (beyond buffer)
		buf.Write(u32le(0))      // stylesStart
		buf.Write(u32le(0))      // offset = 0 → entry at start+100
		_, err := parseStringPool(buf.Bytes(), 0)
		if err == nil || !strings.Contains(err.Error(), "out of range") {
			t.Fatalf("expected out of range error, got %v", err)
		}
	})
}

// --- parseStartTagAttrs edge cases -----------------------------------------

func TestParseStartTagAttrsEdgeCases(t *testing.T) {
	t.Run("truncated buffer", func(t *testing.T) {
		_, _, ok := parseStartTagAttrs([]byte{0x01}, 0, nil, nil)
		if ok {
			t.Fatal("expected false for truncated buffer")
		}
	})

	t.Run("nameIdx out of resourceMap range", func(t *testing.T) {
		strs := buildStringPoolChunk([]string{"attr1"})
		resMap := buildResourceMapChunk([]uint32{0x01010000})
		startTag := buildStartTagChunk([]testAttr{
			{nameIdx: 5, rawValueIdx: -1, dataType: 0x10, data: 0},
		})
		var body bytes.Buffer
		body.Write(strs)
		body.Write(resMap)
		body.Write(startTag)
		var axml bytes.Buffer
		axml.Write(u16le(0x0003))
		axml.Write(u16le(0x0008))
		axml.Write(u32le(uint32(8 + body.Len())))
		axml.Write(body.Bytes())

		_, _, err := parseManifest(axml.Bytes())
		if err == nil {
			t.Fatal("expected error for unrecognized attrs")
		}
	})

	t.Run("versionName via data field fallback", func(t *testing.T) {
		// rawValueIdx=-1 for both attrs, but versionName has dataType=0x03
		// and data=2 which indexes the string pool → the fallback at line 196
		// should pick it up.
		strs := buildStringPoolChunk([]string{"versionCode", "versionName", "9.9.9"})
		resMap := buildResourceMapChunk([]uint32{AttrVersionCode, AttrVersionName})
		startTag := buildStartTagChunk([]testAttr{
			{nameIdx: 0, rawValueIdx: -1, dataType: 0x10, data: 42},
			{nameIdx: 1, rawValueIdx: -1, dataType: 0x03, data: 2},
		})
		var body bytes.Buffer
		body.Write(strs)
		body.Write(resMap)
		body.Write(startTag)
		var axml bytes.Buffer
		axml.Write(u16le(0x0003))
		axml.Write(u16le(0x0008))
		axml.Write(u32le(uint32(8 + body.Len())))
		axml.Write(body.Bytes())

		vName, vCode, err := parseManifest(axml.Bytes())
		if err != nil {
			t.Fatalf("parseManifest: %v", err)
		}
		if vCode != 42 {
			t.Errorf("VersionCode = %d, want 42", vCode)
		}
		if vName != "9.9.9" {
			t.Errorf("VersionName = %q, want 9.9.9", vName)
		}
	})
}

// --- ReadApkInfo error paths -----------------------------------------------

func TestReadApkInfoFileNotFound(t *testing.T) {
	_, err := ReadApkInfo("/nonexistent/path.apk")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

func TestReadApkInfoRejectsOversizedFileBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxAPKDownloadBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ReadApkInfo(path)
	if err == nil || !strings.Contains(err.Error(), "APK is too large") {
		t.Fatalf("expected pre-read APK size rejection, got %v", err)
	}
}

// --- ReadApkInfo additional error paths ------------------------------------

func TestReadApkInfoReadFileError(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadApkInfo(dir)
	if err == nil {
		t.Fatal("expected error for directory path, got nil")
	}
}

func TestReadApkInfoBadManifestInZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write([]byte("not valid axml data")); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.apk")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write apk: %v", err)
	}

	_, err = ReadApkInfo(path)
	if err == nil {
		t.Fatal("expected error for bad manifest AXML")
	}
	if !strings.Contains(err.Error(), "not android binary xml") {
		t.Fatalf("expected 'not android binary xml' error, got %v", err)
	}
}

// --- parseManifest additional edge cases -----------------------------------

func TestParseManifestZeroSizeChunk(t *testing.T) {
	// A chunk with size=0 causes the parser to break before reaching the
	// start tag, exercising the `if size == 0 { break }` branch.
	strs := buildStringPoolChunk([]string{"versionCode", "versionName"})
	resMap := buildResourceMapChunk([]uint32{AttrVersionCode, AttrVersionName})
	zeroSize := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	startTag := buildStartTagChunk([]testAttr{
		{nameIdx: 0, rawValueIdx: -1, dataType: 0x10, data: 1},
		{nameIdx: 1, rawValueIdx: -1, dataType: 0x03, data: 2},
	})

	var body bytes.Buffer
	body.Write(strs)
	body.Write(resMap)
	body.Write(zeroSize)
	body.Write(startTag)

	var axml bytes.Buffer
	axml.Write(u16le(0x0003))
	axml.Write(u16le(0x0008))
	axml.Write(u32le(uint32(8 + body.Len())))
	axml.Write(body.Bytes())

	_, _, err := parseManifest(axml.Bytes())
	if err == nil || err.Error() != "no start tag in manifest" {
		t.Fatalf("expected 'no start tag in manifest', got %v", err)
	}
}

func TestParseManifestStringPoolError(t *testing.T) {
	// Build a manifest whose string pool has a truncated UTF-8 entry.
	// This exercises parseManifest's error propagation from parseStringPool.
	var entryData bytes.Buffer
	entryData.WriteByte(0x01) // utf16 length varint
	entryData.WriteByte(0x05) // byte length varint (claims 5 bytes)
	entryData.WriteByte('a')  // only 1 byte provided

	var pool bytes.Buffer
	pool.Write(u16le(0x0001)) // chunk type
	pool.Write(u16le(0x001c)) // header size
	chunkSize := uint32(8 + 20 + 4 + entryData.Len())
	pool.Write(u32le(chunkSize))          // chunk size
	pool.Write(u32le(1))                  // stringCount = 1
	pool.Write(u32le(0))                  // styleCount
	pool.Write(u32le(1 << 8))             // flags: UTF8_FLAG
	pool.Write(u32le(uint32(8 + 20 + 4))) // stringsStart
	pool.Write(u32le(0))                  // stylesStart
	pool.Write(u32le(0))                  // offset 0
	pool.Write(entryData.Bytes())

	var axml bytes.Buffer
	axml.Write(u16le(0x0003))
	axml.Write(u16le(0x0008))
	axml.Write(u32le(uint32(8 + pool.Len())))
	axml.Write(pool.Bytes())

	_, _, err := parseManifest(axml.Bytes())
	if err == nil {
		t.Fatal("expected error from truncated string pool entry")
	}
	if !strings.Contains(err.Error(), "truncated string entry") {
		t.Fatalf("expected 'truncated string entry', got %v", err)
	}
}

func TestParseStartTagAttrsTruncatedAttr(t *testing.T) {
	// Build a start tag with 1 attribute but only write 12 of the 20 attr
	// bytes, so at+20 overflows the buffer → triggers the break at line 175.
	// The chunk size must match the actual written bytes (44) so the parser
	// doesn't bail out via the p+int(size) > len(axml) check first.
	strs := buildStringPoolChunk([]string{"versionCode", "versionName"})
	resMap := buildResourceMapChunk([]uint32{AttrVersionCode, AttrVersionName})

	var startTag bytes.Buffer
	startTag.Write(u16le(0x0102))     // chunk type
	startTag.Write(u16le(0x0010))     // header size
	startTag.Write(u32le(44))         // chunk size = 44 (only 12/20 attr bytes written)
	startTag.Write(u32le(0))          // lineNumber
	startTag.Write(u32le(0))          // comment
	startTag.Write(u32le(0xFFFFFFFF)) // ns
	startTag.Write(u32le(0xFFFFFFFF)) // name
	startTag.Write(u16le(20))         // attributeStart (not used by parser)
	startTag.Write(u16le(20))         // attributeSize
	startTag.Write(u16le(1))          // attributeCount = 1
	startTag.Write(u16le(0))          // idIndex
	startTag.Write(u16le(0))          // classIndex
	startTag.Write(u16le(0))          // styleIndex
	// Only 12 bytes of the attribute (normally 20). The parser reads 20 bytes
	// starting at at, so at+20 overflows → break → no attrs found.
	startTag.Write(u32le(0xFFFFFFFF)) // ns
	startTag.Write(u32le(0))          // nameIdx
	startTag.Write(u32le(0))          // rawValueIdx
	// Missing: 2 bytes size + 1 byte res0 + 1 byte dataType + 4 bytes data = 8 bytes

	var body bytes.Buffer
	body.Write(strs)
	body.Write(resMap)
	body.Write(startTag.Bytes())

	var axml bytes.Buffer
	axml.Write(u16le(0x0003))
	axml.Write(u16le(0x0008))
	axml.Write(u32le(uint32(8 + body.Len())))
	axml.Write(body.Bytes())

	_, _, err := parseManifest(axml.Bytes())
	// The truncated attr causes the for loop to break without finding
	// versionCode/versionName → "root element carries no versionCode/versionName"
	if err == nil || err.Error() != "root element carries no versionCode/versionName" {
		t.Fatalf("expected 'root element carries no versionCode/versionName', got %v", err)
	}
}

func TestParseManifestPPlusSizeOOB(t *testing.T) {
	// A chunk whose size extends past the AXML buffer triggers the
	// p+int(size) > len(axml) break at line 133.
	pool := buildStringPoolChunk([]string{"versionCode"})
	resMap := buildResourceMapChunk([]uint32{AttrVersionCode})

	// A chunk with typ=0xFFFF (unknown, so it would be skipped) but size
	// that overflows the buffer.
	var oversized bytes.Buffer
	oversized.Write(u16le(0xFFFF)) // type (unknown, will be skipped)
	oversized.Write(u16le(0x0008)) // headerSize
	oversized.Write(u32le(9999))   // size = 9999 (way beyond the buffer)

	var body bytes.Buffer
	body.Write(pool)
	body.Write(resMap)
	body.Write(oversized.Bytes())

	var axml bytes.Buffer
	axml.Write(u16le(0x0003))
	axml.Write(u16le(0x0008))
	axml.Write(u32le(uint32(8 + body.Len())))
	axml.Write(body.Bytes())

	_, _, err := parseManifest(axml.Bytes())
	// The oversized chunk causes a break, skipping the start tag
	// → "no start tag in manifest"
	if err == nil || err.Error() != "no start tag in manifest" {
		t.Fatalf("expected 'no start tag in manifest', got %v", err)
	}
}

func TestDecodeString(t *testing.T) {
	t.Run("UTF-8 short", func(t *testing.T) {
		entry := utf8StringPoolEntry("hi")
		got, err := decodeString(entry, 0, true)
		if err != nil {
			t.Fatalf("decodeString: %v", err)
		}
		if got != "hi" {
			t.Errorf("got %q, want hi", got)
		}
	})

	t.Run("UTF-8 long byte length", func(t *testing.T) {
		// utf16 length varint (2-byte form, high bit set), then 2-byte
		// byte-length varint = 0x80,0x80 → ((0x80&0x7f)<<8)|0x80 = 0x80 = 128,
		// then 0x80 'a' bytes.
		entry := []byte{0x80, 0x00, 0x80, 0x80}
		entry = append(entry, bytes.Repeat([]byte{'a'}, 0x80)...)
		got, err := decodeString(entry, 0, true)
		if err != nil {
			t.Fatalf("decodeString: %v", err)
		}
		if got != strings.Repeat("a", 0x80) {
			t.Errorf("got %d bytes, want 128", len(got))
		}
	})

	t.Run("UTF-8 truncated utf16 length", func(t *testing.T) {
		if _, err := decodeString([]byte{0x80}, 0, true); err == nil {
			t.Fatal("expected error for truncated utf16 length varint, got nil")
		}
	})

	t.Run("UTF-8 truncated byte length", func(t *testing.T) {
		if _, err := decodeString([]byte{0x01, 0x80}, 0, true); err == nil {
			t.Fatal("expected error for truncated byte length varint, got nil")
		}
	})

	t.Run("UTF-8 truncated bytes", func(t *testing.T) {
		if _, err := decodeString([]byte{0x01, 0x05, 'a'}, 0, true); err == nil {
			t.Fatal("expected error for truncated UTF-8 bytes, got nil")
		}
	})

	t.Run("UTF-8 out of range", func(t *testing.T) {
		if _, err := decodeString([]byte{}, 5, true); err == nil {
			t.Fatal("expected error for out-of-range entry, got nil")
		}
	})

	// --- UTF-16 ---

	t.Run("UTF-16 short", func(t *testing.T) {
		entry := utf16StringPoolEntry("hello")
		got, err := decodeString(entry, 0, false)
		if err != nil {
			t.Fatalf("decodeString: %v", err)
		}
		if got != "hello" {
			t.Errorf("got %q, want hello", got)
		}
	})

	t.Run("UTF-16 long length", func(t *testing.T) {
		// 1 << 15 UTF-16 code units — requires the 4-byte length form.
		s := strings.Repeat("ab", 1<<14) // 2*2^14 = 2^15 code units
		entry := utf16StringPoolEntry(s)
		got, err := decodeString(entry, 0, false)
		if err != nil {
			t.Fatalf("decodeString: %v", err)
		}
		if got != s {
			t.Errorf("UTF-16 long round-trip mismatch (len %d)", len(got))
		}
	})

	t.Run("UTF-16 surrogate pair", func(t *testing.T) {
		// U+1F600 (😀) — one rune, two UTF-16 code units, must round-trip.
		s := "a😀z"
		entry := utf16StringPoolEntry(s)
		got, err := decodeString(entry, 0, false)
		if err != nil {
			t.Fatalf("decodeString: %v", err)
		}
		if got != s {
			t.Errorf("got %q, want %q", got, s)
		}
	})

	t.Run("UTF-16 truncated length", func(t *testing.T) {
		if _, err := decodeString([]byte{0x01}, 0, false); err == nil {
			t.Fatal("expected error for truncated UTF-16 length, got nil")
		}
	})

	t.Run("UTF-16 truncated long length", func(t *testing.T) {
		// 0x8000 flag set, but only 3 bytes present → the second half of the
		// 4-byte length is missing.
		if _, err := decodeString([]byte{0x00, 0x80, 0x12}, 0, false); err == nil {
			t.Fatal("expected error for truncated UTF-16 long length, got nil")
		}
	})

	t.Run("UTF-16 truncated utf16 bytes", func(t *testing.T) {
		// length=3 but only one code unit follows.
		if _, err := decodeString([]byte{0x03, 0x00, 'h', 0x00}, 0, false); err == nil {
			t.Fatal("expected error for truncated UTF-16 data, got nil")
		}
	})
}
