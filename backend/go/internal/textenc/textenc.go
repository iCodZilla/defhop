// Package textenc decodes source files that are not UTF-8.
//
// Java sources in Chinese Windows projects are frequently GBK/GB18030. The
// indexer reads files from disk directly (open editor buffers already arrive
// as UTF-8 from VS Code), so without this step unopened GBK files would be
// tokenized as mojibake. Files that are valid UTF-8 (the overwhelming common
// case) are returned unchanged with no extra allocation.
package textenc

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Decode returns src as UTF-8. A UTF-8 BOM is stripped; otherwise the input
// is decoded as GB18030 when possible. Binary-looking data (NUL bytes) and
// undecodable inputs are returned unchanged.
func Decode(src []byte) []byte {
	if len(src) >= 3 && src[0] == 0xEF && src[1] == 0xBB && src[2] == 0xBF {
		src = src[3:]
	}
	if utf8.Valid(src) {
		return src
	}
	if bytes.IndexByte(src, 0) >= 0 {
		return src
	}
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), src)
	if err != nil || len(out) == 0 || !utf8.Valid(out) {
		return src
	}
	return out
}

// DecodeString is Decode for string inputs.
func DecodeString(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return string(Decode([]byte(s)))
}
