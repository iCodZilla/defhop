package samples.spring;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.stereotype.Component;

@Component
public class SampleAppConfig {

    @Value("${spring.application.name}")
    private String appName;

    @Value("${samples.greeting:hi-${spring.application.name}}")
    private String greeting;

    @Value("${samples.bank-import.enabled:false}")
    private boolean enabled;

    public String getAppName() {
        return appName;
    }

    public String getGreeting() {
        return greeting;
    }

    public boolean isEnabled() {
        return enabled;
    }

    @Component
    @ConfigurationProperties(prefix = "samples.feature")
    public static class FeatureProps {
        private String displayName;

        public String getDisplayName() {
            return displayName;
        }

        public void setDisplayName(String displayName) {
            this.displayName = displayName;
        }
    }
}
