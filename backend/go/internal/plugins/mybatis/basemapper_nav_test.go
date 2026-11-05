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

// ServiceImpl<Mapper, Entity> with star-import Mapper must resolve baseMapper.queryList.
func TestBaseMapperCustomMethodDEF_StarImport(t *testing.T) {
	dir := t.TempDir()
	daoDir := filepath.Join(dir, "demo", "dao")
	svcDir := filepath.Join(dir, "demo", "service")
	if err := os.MkdirAll(daoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mapper := filepath.Join(daoDir, "OrderMapper.java")
	svc := filepath.Join(svcDir, "OrderServiceImpl.java")
	mapperSrc := `package demo.dao;
import java.util.List;
import java.util.Map;
import org.apache.ibatis.annotations.Param;
public interface OrderMapper {
  List<Map<String, Object>> queryList(@Param("sql") String sql,
      @Param("sql2") String sql2,
      @Param("flag") boolean flag,
      @Param("orderBy") String orderBy);
}
`
	svcSrc := `package demo.service;
import demo.dao.*;
import com.baomidou.mybatisplus.extension.service.impl.ServiceImpl;
import java.util.List;
import java.util.Map;
public class OrderServiceImpl extends ServiceImpl<OrderMapper, Object> {
  public void run(String sql, String sql2, boolean flag) {
    List<Map<String, Object>> list = baseMapper.queryList(sql,
        sql2,
        flag,
        "id desc");
  }
}
`
	if err := os.WriteFile(mapper, []byte(mapperSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc, []byte(svcSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	h := hub.New(New())
	t.Cleanup(h.StopBackgroundDeps)
	_, _, _, _, active, err := h.Init(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	foundPlugin := false
	for _, id := range active {
		if id == "mybatis" {
			foundPlugin = true
			break
		}
	}
	if !foundPlugin {
		t.Fatalf("mybatis plugin not active: %v", active)
	}

	off := strings.Index(svcSrc, "queryList(sql")
	if off < 0 {
		t.Fatal("queryList not found")
	}
	line, col := 0, 0
	for i := 0; i < off; i++ {
		if svcSrc[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	locs, _ := h.Navigate(model.NavRequest{
		Kind: model.NavDef, Path: svc, Line: line, Character: col, Text: svcSrc,
	})
	if len(locs) == 0 {
		t.Fatal("expected DEF baseMapper.queryList -> OrderMapper")
	}
	found := false
	for _, loc := range locs {
		if strings.Contains(filepath.ToSlash(loc.Path), "OrderMapper.java") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected OrderMapper.java, got %#v", locs)
	}
}
