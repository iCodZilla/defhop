package deps

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// ClassInfo is a lean view of a .class file (public API only).
type ClassInfo struct {
	BinaryName string // org.apache.http.client.config.RequestConfig
	IsInterface bool
	IsPublic    bool
	SuperName   string
	Interfaces  []string
	Methods     []MemberInfo
	Fields      []MemberInfo
}

// MemberInfo is a public field or method.
type MemberInfo struct {
	Name       string
	Descriptor string // JVM descriptor; used lightly for stubs
	IsStatic   bool
}

const (
	accPublic    = 0x0001
	accStatic    = 0x0008
	accInterface = 0x0200
)

// ParseClassFile extracts the public type shape from a JVM .class blob.
func ParseClassFile(data []byte) (*ClassInfo, error) {
	if len(data) < 10 || binary.BigEndian.Uint32(data[0:4]) != 0xCAFEBABE {
		return nil, fmt.Errorf("not a class file")
	}
	r := &cfReader{b: data, i: 8} // skip magic + minor + major
	cpCount := int(r.u16())
	cp := make([]cpEntry, cpCount)
	for i := 1; i < cpCount; i++ {
		tag := r.u8()
		switch tag {
		case 1: // Utf8
			n := int(r.u16())
			cp[i] = cpEntry{tag: tag, s: string(r.bytes(n))}
		case 7: // Class
			cp[i] = cpEntry{tag: tag, a: r.u16()}
		case 8, 16, 19, 20: // String / MethodType / Module / Package
			r.u16()
		case 3, 4, 9, 10, 11, 12, 17, 18: // Integer/Float/Fieldref/Methodref/InterfaceMethodref/NameAndType/Dynamic/InvokeDynamic
			r.u16()
			r.u16()
		case 5, 6: // Long / Double — take two slots
			r.u32()
			r.u32()
			i++
		case 15: // MethodHandle
			r.u8()
			r.u16()
		default:
			return nil, fmt.Errorf("unknown cp tag %d", tag)
		}
	}
	access := r.u16()
	thisIdx := r.u16()
	superIdx := r.u16()
	ifaceCount := int(r.u16())
	ifaces := make([]string, 0, ifaceCount)
	for i := 0; i < ifaceCount; i++ {
		if n := className(cp, r.u16()); n != "" {
			ifaces = append(ifaces, n)
		}
	}
	fieldCount := int(r.u16())
	var fields []MemberInfo
	for i := 0; i < fieldCount; i++ {
		fa := r.u16()
		nameIdx := r.u16()
		descIdx := r.u16()
		attrCount := int(r.u16())
		for a := 0; a < attrCount; a++ {
			r.u16()
			r.skip(int(r.u32()))
		}
		if fa&accPublic == 0 {
			continue
		}
		fields = append(fields, MemberInfo{
			Name:       utf8(cp, nameIdx),
			Descriptor: utf8(cp, descIdx),
			IsStatic:   fa&accStatic != 0,
		})
	}
	methodCount := int(r.u16())
	var methods []MemberInfo
	for i := 0; i < methodCount; i++ {
		ma := r.u16()
		nameIdx := r.u16()
		descIdx := r.u16()
		attrCount := int(r.u16())
		for a := 0; a < attrCount; a++ {
			r.u16()
			r.skip(int(r.u32()))
		}
		if ma&accPublic == 0 {
			continue
		}
		name := utf8(cp, nameIdx)
		if name == "<clinit>" {
			continue
		}
		methods = append(methods, MemberInfo{
			Name:       name,
			Descriptor: utf8(cp, descIdx),
			IsStatic:   ma&accStatic != 0,
		})
	}
	bin := className(cp, thisIdx)
	if bin == "" {
		return nil, fmt.Errorf("missing this_class")
	}
	return &ClassInfo{
		BinaryName:  bin,
		IsInterface: access&accInterface != 0,
		IsPublic:    access&accPublic != 0,
		SuperName:   className(cp, superIdx),
		Interfaces:  ifaces,
		Methods:     methods,
		Fields:      fields,
	}, nil
}

type cpEntry struct {
	tag uint8
	s   string
	a   uint16
}

type cfReader struct {
	b []byte
	i int
}

func (r *cfReader) u8() uint8 {
	if r.i >= len(r.b) {
		return 0
	}
	v := r.b[r.i]
	r.i++
	return v
}

func (r *cfReader) u16() uint16 {
	if r.i+2 > len(r.b) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.i:])
	r.i += 2
	return v
}

func (r *cfReader) u32() uint32 {
	if r.i+4 > len(r.b) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.i:])
	r.i += 4
	return v
}

func (r *cfReader) bytes(n int) []byte {
	if n < 0 || r.i+n > len(r.b) {
		r.i = len(r.b)
		return nil
	}
	v := r.b[r.i : r.i+n]
	r.i += n
	return v
}

func (r *cfReader) skip(n int) {
	if n < 0 {
		return
	}
	r.i += n
	if r.i > len(r.b) {
		r.i = len(r.b)
	}
}

func utf8(cp []cpEntry, idx uint16) string {
	if int(idx) >= len(cp) {
		return ""
	}
	return cp[idx].s
}

func className(cp []cpEntry, idx uint16) string {
	if int(idx) >= len(cp) || cp[idx].tag != 7 {
		return ""
	}
	raw := utf8(cp, cp[idx].a)
	if raw == "" {
		return ""
	}
	return strings.ReplaceAll(raw, "/", ".")
}
