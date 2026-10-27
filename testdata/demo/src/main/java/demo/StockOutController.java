package demo;

import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/stockOut")
public class StockOutController {
    @PostMapping("/add")
    public String add(@RequestBody String body) {
        return body;
    }

    @PostMapping("/items/{id}/sync")
    public String syncItem(@PathVariable String id, @RequestBody String body) {
        return id + body;
    }
}
