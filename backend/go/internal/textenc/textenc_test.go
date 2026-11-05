package textenc

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestDecodeUTF8Passthrough(t *testing.T) {
	src := []byte("package demo; // 中文注释\n")
	if got := Decode(src); !bytes.Equal(got, src) {
		t.Fatalf("UTF-8 input changed: %q", got)
	}
}

func TestDecodeStripsBOM(t *testing.T) {
	src := append([]byte{0xEF, 0xBB, 0xBF}, []byte("class A {}")...)
	if got := string(Decode(src)); got != "class A {}" {
		t.Fatalf("BOM not stripped: %q", got)
	}
}

func TestDecodeGBK(t *testing.T) {
	want := "package 演示; // 中文注释"
	gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(Decode(gbk)); got != want {
		t.Fatalf("GBK decode = %q, want %q", got, want)
	}
}

func TestDecodeLeavesBinaryAlone(t *testing.T) {
	src := []byte{0x00, 0x01, 0xFF, 0xFE, 0x7F}
	if got := Decode(src); !bytes.Equal(got, src) {
		t.Fatalf("binary input changed: %v", got)
	}
}

func TestDecodeString(t *testing.T) {
	if got := DecodeString("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
}
