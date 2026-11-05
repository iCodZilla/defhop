package mybatis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"defhop/internal/hub"
	"defhop/internal/model"
)

const includeMapperJava = `package demo;
import org.apache.ibatis.annotations.Mapper;
@Mapper
public interface OrderMapper {
  java.util.List<Object> findAll();
}
`

const includeOrderXML = `<?xml version="1.0" encoding="UTF-8" ?>
<mapper namespace="demo.OrderMapper">
  <sql id="Base_Column_List">
    id, name
  </sql>
  <select id="findAll" resultType="java.util.Map">
    SELECT <include refid="Base_Column_List"/> FROM orders
  </select>
</mapper>
`

const includeCommonXML = `<?xml version="1.0" encoding="UTF-8" ?>
<mapper namespace="demo.CommonMapper">
  <sql id="Tenant_Filter">tenant_id = #{tenantId}</sql>
</mapper>
`

const includeTenantXML = `<?xml version="1.0" encoding="UTF-8" ?>
<mapper namespace="demo.TenantMapper">
  <select id="findAll" resultType="java.util.Map">
    SELECT * FROM orders WHERE <include refid="demo.CommonMapper.Tenant_Filter"/>
  </select>
</mapper>
`

func TestIncludeRefidDEF(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) string {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("demo/OrderMapper.java", includeMapperJava)
	orderPath := write("mapper/OrderMapper.xml", includeOrderXML)
	commonPath := write("mapper/CommonMapper.xml", includeCommonXML)
	tenantPath := write("mapper/TenantMapper.xml", includeTenantXML)

	h := hub.New(New())
	t.Cleanup(h.StopBackgroundDeps)
	_, _, _, _, active, err := h.Init(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range active {
		if id == "mybatis" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("mybatis plugin not active: %v", active)
	}

	cases := []struct {
		name        string
		path        string
		text        string
		needle      string
		offsetDelta int
		wantPath    string
		wantLine    int
	}{
		{
			name: "local refid", path: orderPath, text: includeOrderXML,
			needle: "refid=\"Base_Column_List\"", offsetDelta: len("refid=\""),
			wantPath: orderPath, wantLine: 2,
		},
		{
			name: "namespace-qualified refid", path: tenantPath, text: includeTenantXML,
			needle: "refid=\"demo.CommonMapper.Tenant_Filter\"", offsetDelta: len("refid=\""),
			wantPath: commonPath, wantLine: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off := strings.Index(tc.text, tc.needle)
			if off < 0 {
				t.Fatalf("%s missing", tc.needle)
			}
			off += tc.offsetDelta
			line, col := lineColAt(tc.text, off)
			locs, _ := h.Navigate(model.NavRequest{
				Kind: model.NavDef, Path: tc.path, Text: tc.text, Line: line, Character: col,
			})
			if len(locs) == 0 {
				t.Fatalf("DEF %s returned no locations", tc.needle)
			}
			if filepath.ToSlash(locs[0].Path) != filepath.ToSlash(tc.wantPath) || locs[0].Line != tc.wantLine {
				t.Fatalf("DEF %s = %s:%d, want %s:%d", tc.needle,
					locs[0].Path, locs[0].Line, tc.wantPath, tc.wantLine)
			}
		})
	}
}

// lineColAt converts a byte offset into the 0-based line and UTF-16 column
// used by the protocol.
func lineColAt(text string, off int) (int, int) {
	line, lineStart := 0, 0
	for i := 0; i < off && i < len(text); i++ {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	col := 0
	for _, r := range text[lineStart:off] {
		if r > 0xFFFF {
			col += 2
		} else {
			col++
		}
	}
	return line, col
}
