package samples.mybatisplus;

import com.baomidou.mybatisplus.core.mapper.BaseMapper;

public interface ItemMapper extends BaseMapper<Item> {
    Item selectByCode(String code);
}
