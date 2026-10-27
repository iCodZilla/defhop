package demo;

import java.util.Optional;

public class ScopeLocals {
    public String first(String code) {
        String defaultConfig = "a-" + code;
        return defaultConfig;
    }

    public String second(String code) {
        return Optional.ofNullable((String) null)
                .orElseGet(() -> {
                    String defaultConfig = "b-" + code;
                    return defaultConfig + "!";
                });
    }
}
