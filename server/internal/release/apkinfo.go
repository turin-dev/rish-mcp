// Package release reads versionCode/versionName straight out of the APK the
// relay/public server serves, so "latest release" reflects what's actually
// on disk rather than a constant somebody has to remember to bump. Ported
// from before/server/src/apkinfo.ts and release.ts.
//
// An APK is a zip whose AndroidManifest.xml is Android Binary XML (AXML),
// not text. The zip part uses Go's stdlib archive/zip (the TS version had to
// hand-roll a zip reader since Node has none built in); the AXML parser has
// no stdlib equivalent in either language, so that part is a faithful port.
package release

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
	"unicode/utf16"
)

type ApkInfo struct {
	VersionName string
	VersionCode int
	SizeBytes   int64
	SHA256      string
	ModifiedAt  time.Time
}

// android:versionCode / android:versionName, from android.R.attr.
const (
	attrVersionCode = 0x0101021b
	attrVersionName = 0x0101021c
)

const (
	chunkStringPool  = 0x001c0001
	chunkResourceMap = 0x00080180
	chunkStartTag    = 0x00100102
	axmlMagic        = 0x00080003
)

var (
	infoCacheMu  sync.Mutex
	infoCacheKey string
	infoCache    ApkInfo
)

// ReadApkInfo parses the APK at path. Cached on size+mtime, so replacing the
// file (a fresh build landing) is picked up without a restart while repeated
// requests stay cheap.
func ReadApkInfo(path string) (ApkInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return ApkInfo{}, err
	}
	key := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())

	infoCacheMu.Lock()
	if infoCacheKey == key {
		cached := infoCache
		infoCacheMu.Unlock()
		return cached, nil
	}
	infoCacheMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return ApkInfo{}, err
	}
	info, err := parseApk(data)
	if err != nil {
		return ApkInfo{}, err
	}
	info.SizeBytes = st.Size()
	info.ModifiedAt = st.ModTime()

	infoCacheMu.Lock()
	infoCacheKey = key
	infoCache = info
	infoCacheMu.Unlock()

	return info, nil
}

func parseApk(data []byte) (ApkInfo, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ApkInfo{}, fmt.Errorf("not a zip: %w", err)
	}
	f, err := zr.Open("AndroidManifest.xml")
	if err != nil {
		return ApkInfo{}, fmt.Errorf("AndroidManifest.xml not found in apk: %w", err)
	}
	axml, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return ApkInfo{}, err
	}

	versionName, versionCode, err := parseManifest(axml)
	if err != nil {
		return ApkInfo{}, err
	}

	sum := sha256.Sum256(data)
	return ApkInfo{
		VersionName: versionName,
		VersionCode: versionCode,
		SHA256:      hex.EncodeToString(sum[:]),
	}, nil
}

// parseManifest pulls versionCode/versionName off the root <manifest>
// element of an AXML document.
func parseManifest(axml []byte) (versionName string, versionCode int, err error) {
	if len(axml) < 8 || le32(axml, 0) != axmlMagic {
		return "", 0, errors.New("not android binary xml")
	}

	var strs []string
	var resourceMap []uint32

	p := 8 // skip file magic + size
	for p+8 <= len(axml) {
		typ := le32(axml, p)
		size := le32(axml, p+4)
		if size == 0 || p+int(size) > len(axml) {
			break
		}

		switch typ {
		case chunkStringPool:
			strs, err = parseStringPool(axml, p)
			if err != nil {
				return "", 0, err
			}

		case chunkResourceMap:
			headerSize := int(le16(axml, p+2))
			resourceMap = nil
			for q := p + headerSize; q+4 <= p+int(size); q += 4 {
				resourceMap = append(resourceMap, le32(axml, q))
			}

		case chunkStartTag:
			vName, vCode, ok := parseStartTagAttrs(axml, p, strs, resourceMap)
			if ok {
				return vName, vCode, nil
			}
			return "", 0, errors.New("root element carries no versionCode/versionName")
		}
		p += int(size)
	}
	return "", 0, errors.New("no start tag in manifest")
}

func parseStartTagAttrs(axml []byte, p int, strs []string, resourceMap []uint32) (versionName string, versionCode int, ok bool) {
	attrStart := p + 8 + 8 + 8 // chunk header, node header (line+comment), ns+name
	if attrStart+6 > len(axml) {
		return "", 0, false
	}
	attrSize := int(le16(axml, attrStart+2))
	attrCount := int(le16(axml, attrStart+4))

	var haveName, haveCode bool
	for a := 0; a < attrCount; a++ {
		at := attrStart + 12 + a*attrSize
		if at+20 > len(axml) {
			break
		}
		nameIdx := int32(le32(axml, at+4))
		rawValueIdx := int32(le32(axml, at+8))
		dataType := axml[at+15]
		data := le32(axml, at+16)

		var resID uint32
		if nameIdx >= 0 && int(nameIdx) < len(resourceMap) {
			resID = resourceMap[nameIdx]
		}

		switch resID {
		case attrVersionCode:
			versionCode = int(data)
			haveCode = true
		case attrVersionName:
			// 0x03 = TYPE_STRING, where data indexes the pool.
			if rawValueIdx >= 0 && int(rawValueIdx) < len(strs) {
				versionName = strs[rawValueIdx]
				haveName = true
			} else if dataType == 0x03 && int(data) < len(strs) {
				versionName = strs[data]
				haveName = true
			}
		}
	}
	return versionName, versionCode, haveName && haveCode
}

func parseStringPool(buf []byte, start int) ([]string, error) {
	if start+28 > len(buf) {
		return nil, errors.New("truncated string pool header")
	}
	count := int(le32(buf, start+8))
	flags := le32(buf, start+16)
	stringsStart := int(le32(buf, start+20))
	utf8 := flags&(1<<8) != 0

	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		offPos := start + 28 + i*4
		if offPos+4 > len(buf) {
			return nil, errors.New("truncated string pool offset table")
		}
		off := start + stringsStart + int(le32(buf, offPos))
		if off < 0 || off >= len(buf) {
			return nil, errors.New("string pool entry out of range")
		}
		s, err := decodeString(buf, off, utf8)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func decodeString(buf []byte, off int, utf8 bool) (string, error) {
	if utf8 {
		p := off
		if p >= len(buf) {
			return "", errors.New("string pool entry out of range")
		}
		// Two varints (utf16 length, then byte length); each is 1 or 2 bytes.
		// The utf16-length varint is only skipped, never used.
		if buf[p]&0x80 != 0 {
			p += 2
		} else {
			p++
		}
		if p >= len(buf) {
			return "", errors.New("truncated string entry")
		}
		byteLen := int(buf[p])
		if byteLen&0x80 != 0 {
			if p+1 >= len(buf) {
				return "", errors.New("truncated string entry")
			}
			byteLen = ((byteLen & 0x7f) << 8) | int(buf[p+1])
			p += 2
		} else {
			p++
		}
		if p+byteLen > len(buf) {
			return "", errors.New("truncated string entry")
		}
		return string(buf[p : p+byteLen]), nil
	}

	if off+2 > len(buf) {
		return "", errors.New("truncated string entry")
	}
	length := int(le16(buf, off))
	p := off + 2
	if length&0x8000 != 0 {
		if p+2 > len(buf) {
			return "", errors.New("truncated string entry")
		}
		length = ((length & 0x7fff) << 16) | int(le16(buf, p))
		p += 2
	}
	if p+length*2 > len(buf) {
		return "", errors.New("truncated string entry")
	}
	u16 := make([]uint16, length)
	for j := 0; j < length; j++ {
		u16[j] = le16(buf, p+j*2)
	}
	return string(utf16.Decode(u16)), nil
}

func le16(buf []byte, off int) uint16 { return binary.LittleEndian.Uint16(buf[off : off+2]) }
func le32(buf []byte, off int) uint32 { return binary.LittleEndian.Uint32(buf[off : off+4]) }
