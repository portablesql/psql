package psql

import "encoding/json"

// export transforms various known types to types easier to handle for the SQL server.
// It delegates to the engine's [Dialect.ExportArg] for engine-specific formatting.
// Fields with format=json are JSON-marshaled before export.
func (e Engine) export(in any, f *StructField) any {
	if f != nil && f.IsJSON() {
		if in == nil {
			return nil
		}
		data, err := json.Marshal(in)
		if err != nil {
			return nil
		}
		return string(data)
	}
	return e.dialect().ExportArg(in)
}
