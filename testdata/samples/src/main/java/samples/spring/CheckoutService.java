package samples.spring;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Service;

@Service
public class CheckoutService {
    @Autowired
    @Qualifier("primaryPayment")
    private PaymentGateway gateway;

    public String pay(String orderId, int amount) {
        return gateway.charge(orderId, amount);
    }
}
