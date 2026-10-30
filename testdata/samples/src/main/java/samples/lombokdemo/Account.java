package samples.lombokdemo;

import lombok.AllArgsConstructor;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.SuperBuilder;

/**
 * Lombok constructors / SuperBuilder — F12 on {@code new Account(...)} / {@code builder()}.
 */
@NoArgsConstructor
@RequiredArgsConstructor
@AllArgsConstructor
@SuperBuilder
public class Account {
    private final String id;
    @NonNull
    private String code;
    private String name;

    public String getId() {
        return id;
    }

    public String getCode() {
        return code;
    }

    public String getName() {
        return name;
    }
}
