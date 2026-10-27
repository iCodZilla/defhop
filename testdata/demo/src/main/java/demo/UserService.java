package demo;

import org.springframework.stereotype.Service;

@Service
public class UserService implements UserApi {
    @Override
    public String findName(long id) {
        return "user-" + id;
    }
}
