package hub

import (
	"defhop/internal/model"
	"testing"
)

func TestSampleDetectLombokAndMapstruct(t *testing.T) {
	facts := &model.WorkspaceFacts{
		AnnotationHits: map[string]int{},
		ImportHits:     map[string]int{},
	}
	sampleDetect(`package demo;
import lombok.Data;
import org.mapstruct.Mapper;
import org.mapstruct.Mapping;
@Data
@Mapper
public interface X {
  @Mapping(target = "a", source = "b")
  A to(B b);
}
`, facts)
	if facts.ImportHits["lombok."] == 0 {
		t.Fatal("expected lombok. import hit")
	}
	if facts.AnnotationHits["Data"] == 0 {
		t.Fatal("expected @Data hit")
	}
	if facts.ImportHits["org.mapstruct"] == 0 {
		t.Fatal("expected org.mapstruct import hit")
	}
	if facts.AnnotationHits["Mapping"] == 0 {
		t.Fatal("expected @Mapping hit")
	}
}
