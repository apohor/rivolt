package backfill

import (
	"strings"
	"testing"
)

// The SQL a Fill builds sets only the declared columns, each guarded by its own empty predicate, and locks the rows it reads.
func TestFillSQLWritesOnlyDeclaredColumnsWhereEmpty(t *testing.T) {
	f := Fill{Table: "drives", Key: "id", KeyType: "uuid", Columns: []FillColumn{
		{Name: "new_a", Expr: "t.old_a * 2"},
		{Name: "new_b", Expr: "t.old_b + 1", Empty: "t.new_b = 0"},
	}}
	if err := f.validate(); err != nil {
		t.Fatal(err)
	}
	q := f.sql()
	for _, want := range []string{
		"new_a = CASE WHEN t.new_a IS NULL THEN t.old_a * 2 ELSE t.new_a END",
		"new_b = CASE WHEN (t.new_b = 0) THEN t.old_b + 1 ELSE t.new_b END",
		"FOR UPDATE OF t",
		"t.id > ($1)::uuid",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("the statement lacks %q:\n%s", want, q)
		}
	}
	if strings.Count(q, "SET ") != 1 || strings.Contains(q, "old_a =") || strings.Contains(q, "old_b =") {
		t.Errorf("the statement sets something other than the declared columns:\n%s", q)
	}
}

func TestFillValidation(t *testing.T) {
	ok := Fill{Table: "drives", Key: "id", KeyType: "uuid", Columns: []FillColumn{{Name: "x", Expr: "1"}}}
	for name, tc := range map[string]struct {
		mut  func(f *Fill)
		want bool
	}{
		"fine":              {func(f *Fill) {}, true},
		"no columns":        {func(f *Fill) { f.Columns = nil }, false},
		"table with a dot":  {func(f *Fill) { f.Table = "public.drives" }, false},
		"key as a column":   {func(f *Fill) { f.Columns[0].Name = "id" }, false},
		"semicolon in expr": {func(f *Fill) { f.Columns[0].Expr = "1; DROP TABLE x" }, false},
		"unknown key type":  {func(f *Fill) { f.KeyType = "money" }, false},
		"empty expr":        {func(f *Fill) { f.Columns[0].Expr = " " }, false},
	} {
		c := ok
		c.Columns = append([]FillColumn(nil), ok.Columns...)
		tc.mut(&c)
		if got := c.validate() == nil; got != tc.want {
			t.Errorf("%s: valid=%v, want %v", name, got, tc.want)
		}
	}
}

// A raw Step on a recorder table is refused; a Fill on one is accepted; a raw Step elsewhere is fine; a job needs a name and a table.
func TestJobValidateRecorderTables(t *testing.T) {
	step := Job{Name: "j", Table: "drives", Step: Fill{}.Step()}
	if err := step.Validate(); err == nil {
		t.Error("a raw Step on drives was accepted")
	}
	fill := Job{Name: "j", Fill: &Fill{Table: "charges", Key: "id", KeyType: "uuid", Columns: []FillColumn{{Name: "x", Expr: "1"}}}}
	if err := fill.Validate(); err != nil || fill.Step == nil || fill.Table != "charges" {
		t.Errorf("a Fill on charges: %v", err)
	}
	other := Job{Name: "j", Table: "lookup_things", Step: Fill{}.Step()}
	if err := other.Validate(); err != nil {
		t.Errorf("a raw Step on a non-recorder table: %v", err)
	}
	if err := (&Job{Step: Fill{}.Step(), Table: "t"}).Validate(); err == nil {
		t.Error("a job without a name was accepted")
	}
	if err := (&Job{Name: "j", Step: Fill{}.Step()}).Validate(); err == nil {
		t.Error("a job without a table was accepted")
	}
}
