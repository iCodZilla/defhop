package samples.mybatis;

import org.apache.ibatis.annotations.Param;
import org.apache.ibatis.annotations.Select;
import samples.lang.Order;

/** Annotation-style Mapper — Go to MyBatis SQL lands on {@code @Select}. */
public interface AnnotatedProductMapper {
    @Select("SELECT id, amount, status FROM product WHERE id = #{id}")
    Order findAnnotatedById(@Param("id") String id);
}
