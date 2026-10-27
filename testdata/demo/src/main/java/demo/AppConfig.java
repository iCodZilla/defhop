package demo;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.stereotype.Component;

@Component
public class AppConfig {
    @Value("${spring.application.name}")
    private String appName;

    @Value("${server.port:8080}")
    private int serverPort;

    @Value("${custom.greeting:hello-${spring.application.name}}")
    private String greeting;

    // SpEL + nested ${} (quoted) and plain placeholder — both should navigate.
    // @Value("#{'${bank-import.profile}' == 'prod' ? 'true': 'false'}")
    @Value("${bank-import.enabled:false}")
    public boolean enabled;
}

@ConfigurationProperties(prefix = "app.feature")
@Component
class FeatureProps {
    private boolean enabled;
    private String displayName;

    public boolean isEnabled() { return enabled; }
    public void setEnabled(boolean enabled) { this.enabled = enabled; }
    public String getDisplayName() { return displayName; }
    public void setDisplayName(String displayName) { this.displayName = displayName; }
}
