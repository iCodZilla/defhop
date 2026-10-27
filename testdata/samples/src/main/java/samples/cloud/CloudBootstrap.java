package samples.cloud;

import org.springframework.cloud.openfeign.EnableFeignClients;
import org.springframework.context.annotation.Configuration;

/** Spring Cloud style enablement (OpenFeign). */
@Configuration
@EnableFeignClients(basePackageClasses = OrderFeignClient.class)
public class CloudBootstrap {
}
