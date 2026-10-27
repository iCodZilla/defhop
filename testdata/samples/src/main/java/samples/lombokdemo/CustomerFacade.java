package samples.lombokdemo;

/** Uses Lombok-generated accessors — Defhop should navigate getFullName / builder. */
public class CustomerFacade {
    public String title(Customer customer) {
        return customer.getFullName() + "#" + customer.getScore();
    }

    public Customer sample() {
        return Customer.builder().id("c1").fullName("Ada").score(10).build();
    }
}
