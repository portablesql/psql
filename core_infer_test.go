package psql_test

import (
	"testing"

	"github.com/portablesql/psql"
)

type inferAttrs struct {
	psql.Name `sql:"infer_attrs"`
	ID        uint64      `sql:",key=PRIMARY"`
	Score     float64     `sql:",null=1"`
	Emb       psql.Vector `sql:",size=3"`
}

// A tag that only carries attributes such as null= or size= still infers the
// SQL type from the Go type, with the tag attributes overriding the magic type.
func TestInferTypeWithExtraAttrs(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	tm := psql.Table[inferAttrs]()
	score := tm.FieldByColumn("Score").GetAttrs(be)
	if score["type"] != "DOUBLE" || score["null"] != "1" {
		t.Fatalf("Score attrs = %v", score)
	}
	emb := tm.FieldByColumn("Emb").GetAttrs(be)
	if emb["type"] != "VECTOR" || emb["size"] != "3" {
		t.Fatalf("Emb attrs = %v", emb)
	}
	if def := tm.FieldByColumn("Score").DefString(be); def == "" {
		t.Fatal("Score must render a column definition")
	}
}
