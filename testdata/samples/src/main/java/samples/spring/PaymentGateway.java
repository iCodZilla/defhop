package samples.spring;

public interface PaymentGateway {
    String charge(String orderId, int amount);
}
