package demo;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.RestController;

@RestController
public class UserController {
    @Autowired
    private UserApi userApi;

    public String hello(long id) {
        return userApi.findName(id);
    }
}
