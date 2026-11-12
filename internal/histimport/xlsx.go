package histimport

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// xlsxTable reads the first worksheet of an .xlsx workbook: sheet XML and sharedStrings only, with encoding/xml (which expands no entities).
// Formulas are not evaluated; a cell's cached value is used. Dates arrive as their serial numbers, as the file holds them.
type xlsxTable struct {
	rc     io.Closer
	dec    *xml.Decoder
	shared []string
	header []string
	rows   int
}

// OpenXLSX reads the first worksheet of the workbook in src (size bytes). It refuses a zip over MaxXLSXZipBytes, one that declares more than
// MaxXLSXUnzipped, and one that actually expands past it.
func OpenXLSX(src io.ReaderAt, size int64) (Table, error) {
	if size > MaxXLSXZipBytes {
		return nil, ErrTooBig
	}
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return nil, fmt.Errorf("histimport: not a readable .xlsx file")
	}
	var declared uint64
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		declared += f.UncompressedSize64
		files[f.Name] = f
	}
	if declared > MaxXLSXUnzipped || len(zr.File) > 2000 {
		return nil, ErrTooBig
	}
	budget := &capReader{left: MaxXLSXUnzipped}
	t := &xlsxTable{}
	if f := files["xl/sharedStrings.xml"]; f != nil {
		if t.shared, err = readSharedStrings(f, budget); err != nil {
			return nil, err
		}
	}
	sheet := firstSheet(files)
	if sheet == nil {
		return nil, fmt.Errorf("histimport: the workbook has no worksheet")
	}
	rc, err := sheet.Open()
	if err != nil {
		return nil, fmt.Errorf("histimport: not a readable .xlsx file")
	}
	t.rc = rc
	budget.r = rc
	t.dec = xml.NewDecoder(budget)
	h, err := t.row()
	if err != nil {
		rc.Close()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("histimport: the sheet is empty")
		}
		return nil, err
	}
	t.header = h
	for i := range t.header {
		t.header[i] = strings.TrimSpace(t.header[i])
	}
	return t, nil
}

// firstSheet is the worksheet with the lowest number: the sheet the app wrote first.
func firstSheet(files map[string]*zip.File) *zip.File {
	var names []string
	for n := range files {
		if strings.HasPrefix(n, "xl/worksheets/sheet") && path.Ext(n) == ".xml" {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := sheetNo(names[i]), sheetNo(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	if len(names) == 0 {
		return nil
	}
	return files[names[0]]
}

func sheetNo(n string) int {
	v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(n, "xl/worksheets/sheet"), ".xml"))
	if err != nil {
		return 1 << 30
	}
	return v
}

func readSharedStrings(f *zip.File, budget *capReader) ([]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("histimport: not a readable .xlsx file")
	}
	defer rc.Close()
	budget.r = rc
	dec := xml.NewDecoder(budget)
	var out []string
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, xmlErr(err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = inSI
			}
		case xml.CharData:
			if inT {
				cur.Write(e)
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "t":
				inT = false
			case "si":
				inSI = false
				out = append(out, cur.String())
				if len(out) > 4*MaxRows {
					return nil, ErrTooBig
				}
			}
		}
	}
}

func xmlErr(err error) error {
	if errors.Is(err, ErrTooBig) {
		return ErrTooBig
	}
	return fmt.Errorf("histimport: not a readable .xlsx file")
}

func (t *xlsxTable) Header() []string { return t.header }

func (t *xlsxTable) Next() ([]string, error) {
	for {
		row, err := t.row()
		if err != nil {
			_ = t.rc.Close()
			return nil, err
		}
		if blank(row) {
			continue
		}
		if t.rows++; t.rows > MaxRows {
			return nil, ErrTooBig
		}
		return row, nil
	}
}

// row reads the next <row>, with its cells placed by column letter.
func (t *xlsxTable) row() ([]string, error) {
	var cells []string
	inRow := false
	var col int
	var typ string
	var val strings.Builder
	inV, inT := false, false
	for {
		tok, err := t.dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			return nil, xmlErr(err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "row":
				inRow, cells = true, cells[:0]
			case "c":
				if !inRow {
					continue
				}
				typ, val = "", strings.Builder{}
				col = len(cells)
				for _, a := range e.Attr {
					switch a.Name.Local {
					case "t":
						typ = a.Value
					case "r":
						if c, ok := colIndex(a.Value); ok {
							col = c
						}
					}
				}
				if col >= MaxCols {
					return nil, ErrTooBig
				}
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.CharData:
			if inV || inT {
				val.Write(e)
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				if !inRow {
					continue
				}
				for len(cells) <= col {
					cells = append(cells, "")
				}
				cells[col] = t.cell(typ, val.String())
			case "row":
				if inRow {
					return cells, nil
				}
			}
		}
	}
}

func (t *xlsxTable) cell(typ, v string) string {
	if typ == "s" {
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || i < 0 || i >= len(t.shared) {
			return ""
		}
		return t.shared[i]
	}
	return v
}

// colIndex turns a cell reference such as "AB12" into its zero-based column.
func colIndex(ref string) (int, bool) {
	n := 0
	i := 0
	for ; i < len(ref); i++ {
		c := ref[i]
		if c < 'A' || c > 'Z' {
			break
		}
		n = n*26 + int(c-'A'+1)
		if n > 1<<20 {
			return 0, false
		}
	}
	if i == 0 {
		return 0, false
	}
	return n - 1, true
}
