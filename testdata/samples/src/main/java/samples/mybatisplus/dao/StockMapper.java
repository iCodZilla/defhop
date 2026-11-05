package samples.mybatisplus.dao;

import java.util.List;
import java.util.Map;
import org.apache.ibatis.annotations.Param;

public interface StockMapper {
    List<Map<String, Object>> queryList(
            @Param("sql") String sql,
            @Param("sql2") String sql2,
            @Param("flag") boolean flag,
            @Param("orderBy") String orderBy);
}
