package demo;

/** Simulated MapStruct generated implementation. */
public class UserMapperMSImpl implements UserMapperMS {
    @Override
    public UserDto toDto(User user) {
        if (user == null) {
            return null;
        }
        UserDto dto = new UserDto();
        dto.setName(user.getName());
        dto.setId(user.getId());
        return dto;
    }
}
