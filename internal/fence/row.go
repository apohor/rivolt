package fence

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// RowJSON turns a column list and its statement arguments into one JSON document for
// json_populate_record(NULL::table, doc), so a guarded INSERT ... SELECT keeps every column's own
// type with no per-column casts. Arguments are the values the plain statement binds: driver.Valuers
// (sql.NullFloat64 and friends) are unwrapped, a []byte is a JSON document and is embedded as one
// (a jsonb column), and uuid and time values use their own JSON form.
func RowJSON(cols []string, args []any) ([]byte, error) {
	if len(cols) != len(args) {
		return nil, fmt.Errorf("fence: %d columns for %d values", len(cols), len(args))
	}
	doc := make(map[string]any, len(cols))
	for i, c := range cols {
		v := args[i]
		if vr, ok := v.(driver.Valuer); ok {
			dv, err := vr.Value()
			if err != nil {
				return nil, fmt.Errorf("fence: column %s: %w", c, err)
			}
			v = dv
		}
		if b, ok := v.([]byte); ok {
			v = json.RawMessage(b)
		}
		doc[c] = v
	}
	return json.Marshal(doc)
}
