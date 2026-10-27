package samples.mybatis;

import org.apache.ibatis.annotations.Param;
import samples.lang.Order;

public interface ProductMapper {
    Order selectById(@Param("id") String id);

    int insertOrder(Order order);
}
