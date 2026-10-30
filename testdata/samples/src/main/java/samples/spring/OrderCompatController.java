package samples.spring;

import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;
import samples.lang.Order;

/**
 * Same HTTP path as {@link OrderController#create} — peer Controller for Refs / Route.
 */
@RestController
@RequestMapping("/orders")
public class OrderCompatController {
    @PostMapping
    public Order createCompat(@RequestBody String id) {
        return new Order(id, 0, samples.lang.Status.ACTIVE);
    }
}
