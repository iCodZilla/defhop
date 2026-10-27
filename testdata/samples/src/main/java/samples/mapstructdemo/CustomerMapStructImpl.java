package samples.mapstructdemo;

import samples.lombokdemo.Customer;

/** Hand-written stand-in for MapStruct generated impl (navigation target). */
public class CustomerMapStructImpl implements CustomerMapStruct {
    @Override
    public CustomerDto toDto(Customer customer) {
        if (customer == null) {
            return null;
        }
        CustomerDto dto = new CustomerDto();
        dto.setId(customer.getId());
        dto.setDisplayName(customer.getName());
        return dto;
    }

    @Override
    public Customer fromDto(CustomerDto dto) {
        if (dto == null) {
            return null;
        }
        return Customer.builder().id(dto.getId()).name(dto.getDisplayName()).score(0).build();
    }
}
