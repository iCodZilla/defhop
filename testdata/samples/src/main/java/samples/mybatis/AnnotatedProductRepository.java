package samples.mybatis;

import samples.lang.Order;

public class AnnotatedProductRepository {
    private final AnnotatedProductMapper annotatedProductMapper;

    public AnnotatedProductRepository(AnnotatedProductMapper annotatedProductMapper) {
        this.annotatedProductMapper = annotatedProductMapper;
    }

    public Order load(String id) {
        return annotatedProductMapper.findAnnotatedById(id);
    }
}
