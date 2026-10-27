package samples.mapstructdemo;

import org.mapstruct.Mapper;
import org.mapstruct.Mapping;
import samples.lombokdemo.Customer;

@Mapper
public interface CustomerMapStruct {
    @Mapping(target = "displayName", source = "name")
    CustomerDto toDto(Customer customer);

    Customer fromDto(CustomerDto dto);
}
