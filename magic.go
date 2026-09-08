package psql

import "fmt"

var magicEngineTypes = map[Engine]map[string]string{
	EngineMySQL:      {},
	EnginePostgreSQL: {"[]uint8": "type=BYTEA,null=0"},
	EngineSQLite:     {},
}

// quickhand types that can be imported easily, for example via `sql:",import=UUID"`
var magicTypes = map[string]string{
	"UUID":     "type=CHAR,size=36,default=00000000-0000-0000-0000-000000000000,collation=latin1_general_ci,validator=uuid",
	"INT":      "type=INT,size=11",
	"BIGINT":   "type=BIGINT,size=20",
	"FLOAT":    "type=FLOAT",
	"DOUBLE":   "type=DOUBLE",
	"KEY":      "type=BIGINT,size=20,unsigned=1,null=0",
	"TS":       "type=TIMESTAMP,size=6",
	"DATE":     "type=DATE",
	"TEXT":     "type=TEXT",
	"LONGTEXT": "type=LONGTEXT",
	"CURRENCY": "type=CHAR,size=5,default=USD,collation=latin1_general_ci",
	"COUNTRY":  "type=CHAR,size=3,default=US,collation=latin1_general_ci",
	"LANGUAGE": "type=CHAR,size=5,default=en-US,collation=latin1_general_ci,validator=language",
	"IP":       "type=VARCHAR,size=39,collation=latin1_general_ci",
	"CIDR":     "type=VARCHAR,size=43,collation=latin1_general_ci",
	"SHA1":     "type=CHAR,size=40,collation=latin1_general_ci",
	"SHA256":   "type=CHAR,size=64,collation=latin1_general_ci",

	// based on types
	"xuid.XUID":       "import=UUID,null=0",
	"*xuid.XUID":      "import=UUID,null=1", // nullable
	"time.Time":       "import=DATETIME,null=0",
	"*time.Time":      "import=DATETIME,null=1",
	"uint64":          "type=BIGINT,size=20,unsigned=1,null=0",
	"int64":           "type=BIGINT,size=21,unsigned=0,null=0",
	"*uint64":         "type=BIGINT,size=20,unsigned=1,null=1",
	"*int64":          "type=BIGINT,size=21,unsigned=0,null=1",
	"float64":         "type=DOUBLE,null=0",
	"*float64":        "type=DOUBLE,null=1",
	"bool":            "type=TINYINT,size=1,null=0",
	"*bool":           "type=TINYINT,size=1,null=1",
	"psql.Set":        "type=SET,null=0",
	"*psql.Set":       "type=SET,null=1",
	"Stamp+time.Time": "import=TS", // for time.Time fields named "Stamp"
	"[]uint8":         "type=BLOB,null=0",
	"psql.Vector":     "type=VECTOR,null=1",
}

// DefineMagicType registers a column definition that can be imported by name
// (`sql:",import=NAME"`) or that applies automatically to Go fields of the
// given type (e.g. "mypkg.MyType" or "*mypkg.MyType"); "Field+type" keys apply
// to fields with that name and type. The definition uses the sql tag attribute
// syntax, e.g. "type=CHAR,size=36,null=0". It panics if typ is already defined.
// Call it during initialization, before tables are used.
func DefineMagicType(typ string, definition string) {
	if _, found := magicTypes[typ]; found {
		panic(fmt.Sprintf("multiple definitions of type %s", typ))
	}
	magicTypes[typ] = definition
}

// DefineMagicTypeEngine is like [DefineMagicType] but the definition only
// applies on the given engine, taking precedence over the engine-independent one.
func DefineMagicTypeEngine(e Engine, typ string, definition string) {
	if magicEngineTypes[e] == nil {
		magicEngineTypes[e] = make(map[string]string)
	}
	if _, found := magicEngineTypes[e][typ]; found {
		panic(fmt.Sprintf("multiple definitions of type %s", typ))
	}
	magicEngineTypes[e][typ] = definition
}
