package demo;

import org.apache.ibatis.annotations.Mapper;

@Mapper
public interface PubVouchRelationMapper {
    PubVouchRelation selectByBiz(String bizId);
}
