package samples.mybatisplus.service;

import com.baomidou.mybatisplus.extension.service.impl.ServiceImpl;
import java.util.List;
import java.util.Map;
import samples.mybatisplus.dao.*;

/** Star-import Mapper on ServiceImpl — baseMapper.queryList must jump to StockMapper. */
public class StockServiceImpl extends ServiceImpl<StockMapper, Object> {
    public List<Map<String, Object>> run(String sql, String sql2, boolean flag) {
        return baseMapper.queryList(sql,
                sql2,
                flag,
                "id desc");
    }
}
