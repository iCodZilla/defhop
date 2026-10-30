package samples.spring;

import org.springframework.stereotype.Service;

@Service("backupPayment")
public class BackupPaymentGateway implements PaymentGateway {
    @Override
    public String charge(String orderId, int amount) {
        return "backup:" + orderId + ":" + amount;
    }
}
