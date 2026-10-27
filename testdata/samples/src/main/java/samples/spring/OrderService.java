package samples.spring;

import org.springframework.stereotype.Service;
import samples.lang.Order;
import samples.lang.Status;

@Service
public class OrderService {
    public Order create(String id, int amount) {
        return new Order(id, amount, Status.NEW);
    }

    public Status next(Status current) {
        if (current == Status.NEW) {
            return Status.ACTIVE;
        }
        return Status.CLOSED;
    }
}
