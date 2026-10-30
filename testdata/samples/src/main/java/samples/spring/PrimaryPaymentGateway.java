package samples.spring;

import org.springframework.stereotype.Service;

@Service("primaryPayment")
public class PrimaryPaymentGateway implements PaymentGateway {
    @Override
    public String charge(String orderId, int amount) {
        return "primary:" + orderId + ":" + amount;
    }
}
