package samples.lombokdemo;

/** Uses Lombok-generated accessors — Defhop should navigate getName / builder. */
public class CustomerFacade {
    public String title(Customer customer) {
        return customer.getName() + "#" + customer.getScore();
    }

    public Customer sample() {
        return Customer.builder().id("c1").name("Ada").score(10).build();
    }
}
