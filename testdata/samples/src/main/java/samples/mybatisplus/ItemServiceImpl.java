package samples.mybatisplus;

import com.baomidou.mybatisplus.extension.service.impl.ServiceImpl;
import org.springframework.stereotype.Service;

@Service
public class ItemServiceImpl extends ServiceImpl<ItemMapper, Item> implements ItemService {

    @Override
    public Item findByCode(String code) {
        return baseMapper.selectByCode(code);
    }
}
