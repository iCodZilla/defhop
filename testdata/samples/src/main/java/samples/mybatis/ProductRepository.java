package samples.mybatis;

import samples.lang.Order;

/** Uses MyBatis mapper — DEF on selectById should route to XML. */
public class ProductRepository {
    private final ProductMapper productMapper;

    public ProductRepository(ProductMapper productMapper) {
        this.productMapper = productMapper;
    }

    public Order find(String id) {
        return productMapper.selectById(id);
    }
}
