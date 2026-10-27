package demo;

import com.baomidou.mybatisplus.extension.service.impl.ServiceImpl;
import org.springframework.stereotype.Service;

@Service
public class PubVouchRelationServiceImpl extends ServiceImpl<PubVouchRelationMapper, PubVouchRelation>
    implements PubVouchRelationService {

    @Override
    public PubVouchRelation find(String bizId) {
        // baseMapper is injected by MyBatis-Plus ServiceImpl as PubVouchRelationMapper
        return baseMapper.selectByBiz(bizId);
    }
}
