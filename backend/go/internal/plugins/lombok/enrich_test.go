package lombok

import (
	"defhop/internal/model"
	"testing"
)

func TestEnrichDataBuilder(t *testing.T) {
	fd := &model.FileSlice{
		Path: "Customer.java",
		Symbols: []*model.Symbol{
			{
				Key: "T|demo.Customer", Kind: "type", Name: "demo.Customer",
				Annotations: []string{"Data", "Builder"},
				Declaration: &model.Location{Path: "Customer.java", Line: 1},
			},
			{
				Key: "F|T|demo.Customer|name", Kind: "field", Name: "name",
				OwnerKey: "T|demo.Customer", TypeKey: "T|java.lang.String",
				Declaration: &model.Location{Path: "Customer.java", Line: 3},
			},
		},
	}
	Enrich(fd)
	have := map[string]bool{}
	for _, s := range fd.Symbols {
		have[s.Name] = true
	}
	for _, need := range []string{"getName", "setName", "builder", "build"} {
		if !have[need] {
			t.Fatalf("missing synthetic %s", need)
		}
	}
}
