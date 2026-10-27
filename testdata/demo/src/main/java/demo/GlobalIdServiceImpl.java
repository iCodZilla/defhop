package demo;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Service
public class GlobalIdServiceImpl {
    @Autowired
    private UserMapper dzpjGlobalIdMapper;

    public void go() {
        dzpjGlobalIdMapper.selectById(1);
    }
}
