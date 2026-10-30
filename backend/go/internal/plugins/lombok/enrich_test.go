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

func TestEnrichConstructors(t *testing.T) {
	fd := &model.FileSlice{
		Path: "User.java",
		Symbols: []*model.Symbol{
			{
				Key: "T|demo.User", Kind: "type", Name: "demo.User",
				Annotations: []string{"NoArgsConstructor", "RequiredArgsConstructor", "AllArgsConstructor", "SuperBuilder"},
				Declaration: &model.Location{Path: "User.java", Line: 1},
			},
			{
				Key: "F|T|demo.User|id", Kind: "field", Name: "id",
				OwnerKey: "T|demo.User", TypeKey: "T|java.lang.Long",
				Annotations: []string{"final"},
				Declaration: &model.Location{Path: "User.java", Line: 2},
			},
			{
				Key: "F|T|demo.User|name", Kind: "field", Name: "name",
				OwnerKey: "T|demo.User", TypeKey: "T|java.lang.String",
				Declaration: &model.Location{Path: "User.java", Line: 3},
			},
		},
	}
	Enrich(fd)
	keys := map[string]bool{}
	names := map[string]bool{}
	for _, s := range fd.Symbols {
		keys[s.Key] = true
		names[s.Name] = true
	}
	if !keys["M|T|demo.User|User|"] {
		t.Fatal("missing NoArgsConstructor")
	}
	if !keys["M|T|demo.User|User|java.lang.Long"] {
		t.Fatal("missing RequiredArgsConstructor for final id")
	}
	if !keys["M|T|demo.User|User|java.lang.Long,java.lang.String"] {
		t.Fatal("missing AllArgsConstructor")
	}
	if !names["builder"] || !names["build"] {
		t.Fatal("SuperBuilder should synthesize builder/build")
	}
}
