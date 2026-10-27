package demo;

import org.mapstruct.Mapper;
import org.mapstruct.Mapping;

@Mapper
public interface UserMapperMS {
    @Mapping(target = "name", source = "name")
    UserDto toDto(User user);
}
