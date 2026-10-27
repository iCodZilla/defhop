package samples.mybatisplus;

import com.baomidou.mybatisplus.extension.service.IService;

public interface ItemService extends IService<Item> {
    Item findByCode(String code);
}
