package samples.lombokdemo;

import java.util.List;
import java.util.stream.Collectors;

public class CustomerService {
    public List<String> names(List<Customer> list) {
        // method ref on Lombok-generated getter
        return list.stream().map(Customer::getName).collect(Collectors.toList());
    }

    public Customer build(String id, String name) {
        return Customer.builder().id(id).name(name).score(0).build();
    }
}
