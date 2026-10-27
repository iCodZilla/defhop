package demo;

import org.springframework.cloud.openfeign.FeignClient;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;

@FeignClient(name = "service-demo-stock")
public interface StockFeignClient {
    @PostMapping(value = "/stockOut/add")
    String add(@RequestBody String body);

    @PostMapping("/stockOut/items/{id}/sync")
    String syncItem(@RequestBody String body);
}
