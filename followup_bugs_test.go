package psql_test

import (
	"context"
	"testing"

	"github.com/portablesql/psql"
)

type fbIndexed struct {
	psql.Name `sql:"fb_indexed"`
	ID        uint64 `sql:",key=PRIMARY"`
	Group     string `sql:",type=VARCHAR,size=32,key=by_group"`
	Email     string `sql:",type=VARCHAR,size=64,key=UNIQUE:by_email"`
}

// key=name on a column must produce a plain index, not a key of unknown type.
func TestFollowupKeyNameIsIndex(t *testing.T) {
	be := psql.NewBackend(psql.EngineMySQL, nil)
	var byGroup, byEmail *psql.StructKey
	for _, k := range psql.Table[fbIndexed]().AllKeys() {
		switch k.Key {
		case "by_group":
			byGroup = k
		case "by_email":
			byEmail = k
		}
	}
	if byGroup == nil || byGroup.Typ != psql.KeyIndex {
		t.Fatalf("key=by_group should be a KeyIndex, got %+v", byGroup)
	}
	if byEmail == nil || byEmail.Typ != psql.KeyUnique {
		t.Fatalf("key=UNIQUE:by_email should be a KeyUnique, got %+v", byEmail)
	}
	if def := byGroup.DefString(be); def == "" {
		t.Fatal("plain index must render a definition")
	}
}

// DELETE/UPDATE ... LIMIT only exists on MySQL.
func TestFollowupDeleteLimitEngines(t *testing.T) {
	for _, e := range []psql.Engine{psql.EnginePostgreSQL, psql.EngineSQLite} {
		ctx := psql.NewBackend(e, nil).Plug(context.Background())
		if _, err := psql.B().Delete().From("t").Where(map[string]any{"a": 1}).Limit(1).Render(ctx); err == nil {
			t.Errorf("%s: DELETE ... LIMIT must be rejected", e)
		}
		if _, err := psql.B().Update("t").Set(map[string]any{"a": 1}).Limit(1).Render(ctx); err == nil {
			t.Errorf("%s: UPDATE ... LIMIT must be rejected", e)
		}
	}
	ctx := psql.NewBackend(psql.EngineMySQL, nil).Plug(context.Background())
	if _, err := psql.B().Delete().From("t").Where(map[string]any{"a": 1}).Limit(1).Render(ctx); err != nil {
		t.Errorf("MySQL: DELETE ... LIMIT should render: %v", err)
	}
}
