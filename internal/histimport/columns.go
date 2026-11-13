package histimport

import "strings"

// Provenance of the header aliases, checked Nov 13: neither TezLab (support article 86, "Exporting Data"), Tessie (article 68, "Import from
// TezLab") nor ABRP (help article 7182124 "My Drives", and the feedback threads on export separators, temperatures and odometer) publishes a
// column list, a unit or a time format. So every alias below is an assumption drawn from the vocabulary exports of this kind use, and none is
// pinned to a real file. The parser is built for that: a file whose required columns do not match is refused with the header names it has
// and the names we expected (RequiredColumns), never imported in part.
const AliasProvenance = "assumed: no published column list for TezLab or ABRP exports (checked Nov 13)"

// ColumnSpec is one required column of a format: the canonical field and the header spellings the parser accepts for it (first few).
type ColumnSpec struct {
	Field    string   `json:"field"`
	Examples []string `json:"examples"`
}

// ExpectedLayout is one file layout of a source and the columns it requires.
type ExpectedLayout struct {
	Kind    string       `json:"kind"` // "drives", "charges" or "points"
	Columns []ColumnSpec `json:"columns"`
}

// RequiredColumns lists, per format, the required fields and up to three accepted spellings of each, in the format's own order. It carries
// no file content, so it can go to the user.
func RequiredColumns(formats []Format) []ExpectedLayout {
	var out []ExpectedLayout
	for _, f := range formats {
		l := ExpectedLayout{Kind: kindName(f.Kind)}
		for _, fld := range f.Fields {
			if !fld.Required {
				continue
			}
			ex := fld.Aliases
			if len(ex) > 3 {
				ex = ex[:3]
			}
			l.Columns = append(l.Columns, ColumnSpec{Field: strings.ReplaceAll(fld.Name, "_", " "), Examples: append([]string(nil), ex...)})
		}
		out = append(out, l)
	}
	return out
}

func kindName(k Kind) string {
	switch k {
	case KindDrives:
		return "drives"
	case KindCharges:
		return "charges"
	}
	return "points"
}
