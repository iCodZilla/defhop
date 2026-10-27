package samples.cloud;

import org.springframework.cloud.openfeign.FeignClient;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import samples.lang.Order;

/** Spring Cloud OpenFeign client — should jump to OrderController mappings. */
@FeignClient(name = "defhop-samples-order")
public interface OrderFeignClient {

    @PostMapping("/orders")
    Order create(@RequestBody String id);

    @GetMapping("/orders/{id}")
    String ping(@PathVariable("id") String id);
}
