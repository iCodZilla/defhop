package deps

import (
	"fmt"
	"strings"
)

// StubJava synthesizes a navigable .java source from a .class shape.
func StubJava(ci *ClassInfo) string {
	if ci == nil || ci.BinaryName == "" {
		return ""
	}
	pkg, simple := splitBinary(ci.BinaryName)
	var b strings.Builder
	if pkg != "" {
		fmt.Fprintf(&b, "package %s;\n\n", pkg)
	}
	kind := "class"
	if ci.IsInterface {
		kind = "interface"
	}
	fmt.Fprintf(&b, "public %s %s", kind, simple)
	if !ci.IsInterface && ci.SuperName != "" && ci.SuperName != "java.lang.Object" {
		fmt.Fprintf(&b, " extends %s", simpleOf(ci.SuperName))
	}
	if len(ci.Interfaces) > 0 {
		kw := " implements "
		if ci.IsInterface {
			kw = " extends "
		}
		b.WriteString(kw)
		for i, iface := range ci.Interfaces {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(simpleOf(iface))
		}
	}
	b.WriteString(" {\n")
	for _, f := range ci.Fields {
		if f.Name == "" || strings.Contains(f.Name, "$") {
			continue
		}
		mod := "public"
		if f.IsStatic {
			mod += " static"
		}
		fmt.Fprintf(&b, "  %s %s %s;\n", mod, javaType(f.Descriptor), f.Name)
	}
	for _, m := range ci.Methods {
		if m.Name == "" || strings.Contains(m.Name, "$") {
			continue
		}
		mod := "public"
		if m.IsStatic {
			mod += " static"
		}
		ret, params := methodSig(m.Descriptor)
		name := m.Name
		if name == "<init>" {
			fmt.Fprintf(&b, "  %s %s(%s) {}\n", mod, simple, params)
			continue
		}
		if ret == "void" {
			fmt.Fprintf(&b, "  %s void %s(%s) {}\n", mod, name, params)
			continue
		}
		fmt.Fprintf(&b, "  %s %s %s(%s) { return %s; }\n", mod, ret, name, params, zeroValue(ret))
	}
	b.WriteString("}\n")
	return b.String()
}

func splitBinary(bin string) (pkg, simple string) {
	if i := strings.LastIndex(bin, "."); i >= 0 {
		return bin[:i], bin[i+1:]
	}
	return "", bin
}

func simpleOf(bin string) string {
	_, s := splitBinary(bin)
	return s
}

func javaType(desc string) string {
	if desc == "" {
		return "Object"
	}
	switch desc[0] {
	case 'V':
		return "void"
	case 'Z':
		return "boolean"
	case 'B':
		return "byte"
	case 'C':
		return "char"
	case 'S':
		return "short"
	case 'I':
		return "int"
	case 'J':
		return "long"
	case 'F':
		return "float"
	case 'D':
		return "double"
	case '[':
		return javaType(desc[1:]) + "[]"
	case 'L':
		end := strings.IndexByte(desc, ';')
		if end < 0 {
			return "Object"
		}
		return simpleOf(strings.ReplaceAll(desc[1:end], "/", "."))
	default:
		return "Object"
	}
}

func methodSig(desc string) (ret, params string) {
	if !strings.HasPrefix(desc, "(") {
		return "Object", ""
	}
	end := strings.IndexByte(desc, ')')
	if end < 0 {
		return "Object", ""
	}
	argDesc := desc[1:end]
	ret = javaType(desc[end+1:])
	var parts []string
	i := 0
	argN := 0
	for i < len(argDesc) {
		start := i
		for argDesc[i] == '[' {
			i++
		}
		if i >= len(argDesc) {
			break
		}
		if argDesc[i] == 'L' {
			semi := strings.IndexByte(argDesc[i:], ';')
			if semi < 0 {
				break
			}
			i += semi + 1
		} else {
			i++
		}
		parts = append(parts, fmt.Sprintf("%s a%d", javaType(argDesc[start:i]), argN))
		argN++
	}
	return ret, strings.Join(parts, ", ")
}

func zeroValue(javaTyp string) string {
	switch javaTyp {
	case "void":
		return ""
	case "boolean":
		return "false"
	case "byte", "char", "short", "int", "long", "float", "double":
		return "0"
	default:
		if strings.HasSuffix(javaTyp, "[]") {
			return "null"
		}
		return "null"
	}
}
