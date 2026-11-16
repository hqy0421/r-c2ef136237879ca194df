package dataframe

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/go-gota/gota/series"
)

// loadMixedRecords builds a DataFrame whose columns deliberately have different
// types: name is a string column, qty an int column, price a float column,
// active a bool column and note a string column that contains both a real empty
// string and a missing value.
func loadMixedRecords() DataFrame {
	return LoadRecords([][]string{
		{"name", "qty", "price", "active", "note"},
		{"widget", "3", "2.5", "true", "hello"},
		{"gadget", "10", "1.25", "false", ""},
		{"gizmo", "7", "4.0", "true", "NA"},
	})
}

// cleanRecords returns the records of a DataFrame, including its header.
func cleanRecords(df DataFrame) [][]string {
	return df.Records()
}

func TestCompileRowCleaner_ConfigurationErrors(t *testing.T) {
	input := loadMixedRecords()
	tests := []struct {
		name     string
		formulas []RowFormula
		filter   string
		want     string
	}{
		{
			name:     "no formulas and no filter",
			formulas: nil,
			want:     "at least one formula or a filter expression",
		},
		{
			name: "unknown column",
			formulas: []RowFormula{
				{Column: "x", Expr: "missing + 1", Type: series.Int},
			},
			want: "unknown identifier",
		},
		{
			name: "syntax error",
			formulas: []RowFormula{
				{Column: "x", Expr: "1 +", Type: series.Int},
			},
			want: "unexpected end of expression",
		},
		{
			name: "declared type mismatch",
			formulas: []RowFormula{
				{Column: "x", Expr: "qty + 1", Type: series.String},
			},
			want: "declared output type is string",
		},
		{
			name: "implicit int to double is refused",
			formulas: []RowFormula{
				{Column: "x", Expr: "qty + 1.5", Type: series.Float},
			},
			want: "convert one operand explicitly",
		},
		{
			name: "missing declared type",
			formulas: []RowFormula{
				{Column: "x", Expr: "qty", Type: ""},
			},
			want: "declared output type is empty",
		},
		{
			name: "reserved column name",
			formulas: []RowFormula{
				{Column: RejectMessageColumn, Expr: `"x"`, Type: series.String},
			},
			want: "reserved for the reject table",
		},
		{
			name: "empty column name",
			formulas: []RowFormula{
				{Column: "", Expr: `"x"`, Type: series.String},
			},
			want: "output column name is empty",
		},
		{
			name:     "filter is not a bool",
			formulas: []RowFormula{{Column: "x", Expr: "qty", Type: series.Int}},
			filter:   "qty + 1",
			want:     "want bool",
		},
		{
			name:     "filter has a syntax error",
			formulas: []RowFormula{{Column: "x", Expr: "qty", Type: series.Int}},
			filter:   "qty >",
			want:     "unexpected end of expression",
		},
		{
			name: "later formula cannot see a later column",
			formulas: []RowFormula{
				{Column: "first", Expr: "second + 1", Type: series.Int},
				{Column: "second", Expr: "qty", Type: series.Int},
			},
			want: "unknown identifier",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cleaner, err := CompileRowCleaner(input, tc.formulas, tc.filter)
			if err == nil {
				t.Fatalf("expected an error containing %q, got cleaner %v", tc.want, cleaner)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "compiling row cleaner") {
				t.Fatalf("error = %q, want it to mention compilation", err)
			}
		})
	}
}

func TestRowCleaner_SequentialFormulasAndOverwrite(t *testing.T) {
	input := loadMixedRecords()

	// "total" is added, then read by the next formula. "qty" is overwritten
	// and the overwriting formula still sees the original qty. The final
	// filter runs on the results of every formula.
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "total", Expr: "qty * 100", Type: series.Int},
		{Column: "qty", Expr: "qty + 1", Type: series.Int},
		{Column: "qty", Expr: "qty * 10", Type: series.Int},
		{Column: "label", Expr: `name + ":" + string(total)`, Type: series.String},
	}, "total >= 500 && qty > 0")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}

	if got, want := cleaner.Names(), []string{"name", "qty", "price", "active", "note", "total", "label"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got, want := cleaner.Types(), []series.Type{series.String, series.Int, series.Float, series.Bool, series.String, series.Int, series.String}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Types() = %v, want %v", got, want)
	}

	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0 (%v)", n, cleanRecords(rejected))
	}

	want := [][]string{
		{"name", "qty", "price", "active", "note", "total", "label"},
		{"gadget", "110", "1.250000", "false", "", "1000", "gadget:1000"},
		{"gizmo", "80", "4.000000", "true", "NaN", "700", "gizmo:700"},
	}
	if got := cleanRecords(cleaned); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned records = %v, want %v", got, want)
	}

	// The input is untouched: qty still holds its original values.
	if got, want := input.Col("qty").Records(), []string{"3", "10", "7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input qty = %v, want %v", got, want)
	}
	if got, want := input.Ncol(), 5; got != want {
		t.Fatalf("input Ncol() = %d, want %d", got, want)
	}
}

func TestRowCleaner_PreservesMixedColumnTypes(t *testing.T) {
	input := loadMixedRecords()
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "qty2", Expr: "qty", Type: series.Int},
		{Column: "price2", Expr: "price", Type: series.Float},
		{Column: "active2", Expr: "active", Type: series.Bool},
		{Column: "name2", Expr: "name", Type: series.String},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, _, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	wantTypes := []series.Type{
		series.String, series.Int, series.Float, series.Bool, series.String,
		series.Int, series.Float, series.Bool, series.String,
	}
	if got := cleaned.Types(); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("Types() = %v, want %v", got, wantTypes)
	}
	if got, want := cleaned.Col("qty2").Records(), []string{"3", "10", "7"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("qty2 = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("price2").Records(), []string{"2.500000", "1.250000", "4.000000"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("price2 = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("active2").Records(), []string{"true", "false", "true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("active2 = %v, want %v", got, want)
	}
}

func TestRowCleaner_MissingIsNotAnEmptyString(t *testing.T) {
	input := loadMixedRecords()

	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "is_empty", Expr: `note == ""`, Type: series.Bool},
		{Column: "is_null", Expr: "note == null", Type: series.Bool},
		{Column: "note_upper", Expr: "upper(note)", Type: series.String},
		{Column: "note_or_default", Expr: `coalesce(note, "none")`, Type: series.String},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, _, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := [][]string{
		{"name", "qty", "price", "active", "note", "is_empty", "is_null", "note_upper", "note_or_default"},
		{"widget", "3", "2.500000", "true", "hello", "false", "false", "HELLO", "hello"},
		{"gadget", "10", "1.250000", "false", "", "true", "false", "", ""},
		{"gizmo", "7", "4.000000", "true", "NaN", "false", "true", "NaN", "none"},
	}
	if got := cleanRecords(cleaned); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned records = %v, want %v", got, want)
	}
}

func TestRowCleaner_IntStaysExact(t *testing.T) {
	// 2^53+1 is not representable as a float64; if the int went through a
	// float the result would be wrong.
	input := New(
		series.New([]int{1, 2}, series.Int, "id"),
		series.New([]string{"9007199254740993", "9007199254740994"}, series.Int, "big"),
	)
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "big_plus_one", Expr: "big + 1", Type: series.Int},
		{Column: "is_even", Expr: "big % 2 == 0", Type: series.Bool},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0", n)
	}
	if got, want := cleaned.Col("big_plus_one").Records(), []string{"9007199254740994", "9007199254740995"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("big_plus_one = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("is_even").Records(), []string{"false", "true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("is_even = %v, want %v", got, want)
	}
}

const maxInt64Text = "9223372036854775807"

func TestRowCleaner_OverflowRejectsOnlyTheFailingRow(t *testing.T) {
	input := New(
		series.New([]string{"1", "9223372036854775807", "2"}, series.Int, "n"),
	)
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "doubled", Expr: "n * 2", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got, want := cleaned.Col("doubled").Records(), []string{"2", "4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned doubled = %v, want %v", got, want)
	}

	if got, want := rejected.Names(), []string{
		"n", RejectRowColumn, RejectSourceColumn, RejectExpressionColumn,
		RejectOffsetColumn, RejectMessageColumn,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reject columns = %v, want %v", got, want)
	}
	records := rejected.Records()
	if len(records) != 2 {
		t.Fatalf("reject records = %v, want a header and one row", records)
	}
	row := records[1]
	if row[0] != maxInt64Text {
		t.Errorf("rejected original value = %q, want %q (the original row must be preserved)", row[0], maxInt64Text)
	}
	if row[1] != "1" {
		t.Errorf("reject row index = %q, want \"1\"", row[1])
	}
	if row[2] != "doubled" {
		t.Errorf("reject source = %q, want \"doubled\"", row[2])
	}
	if row[3] != "n * 2" {
		t.Errorf("reject expression = %q, want \"n * 2\"", row[3])
	}
	// The multiplication operator is at offset 2 of "n * 2".
	if row[4] != "2" {
		t.Errorf("reject offset = %q, want \"2\"", row[4])
	}
	if !strings.Contains(row[5], "integer overflow") {
		t.Errorf("reject message = %q, want it to mention an integer overflow", row[5])
	}

	// The reject column has the type of the input column, not a stringified
	// copy of everything.
	if got, want := rejected.Types(), []series.Type{
		series.Int, series.Int, series.String, series.String, series.Int, series.String,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reject types = %v, want %v", got, want)
	}

	// A runtime error does not make Apply fail.
	if err != nil {
		t.Fatalf("Apply returned an error for a row level problem: %v", err)
	}
}

func TestRowCleaner_ErrorLocationOfEveryFormula(t *testing.T) {
	input := New(series.New([]string{"1", "0"}, series.Int, "n"))
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "ok", Expr: "n + 1", Type: series.Int},
		{Column: "bad", Expr: "100 / n", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	_, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	records := rejected.Records()
	if len(records) != 2 {
		t.Fatalf("reject records = %v, want one rejected row", records)
	}
	row := records[1]
	if row[1] != "1" || row[2] != "bad" || row[3] != "100 / n" || row[4] != "4" {
		t.Fatalf("reject row = %v, want row 1 rejected by \"bad\" at offset 4", row)
	}

	// A filter error is attributed to the filter, not to a formula.
	cleaner, err = CompileRowCleaner(input, nil, "10 / n > 1")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := cleaned.Nrow(), 1; got != want {
		t.Fatalf("cleaned rows = %d, want %d", got, want)
	}
	records = rejected.Records()
	if len(records) != 2 {
		t.Fatalf("reject records = %v, want one rejected row", records)
	}
	row = records[1]
	if row[2] != FilterSource || row[3] != "10 / n > 1" || row[4] != "3" {
		t.Fatalf("reject row = %v, want it attributed to the filter at offset 3", row)
	}
}

func TestRowCleaner_ShortCircuit(t *testing.T) {
	input := New(series.New([]string{"0", "1"}, series.Int, "n"))
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "safe_and", Expr: "n != 0 && 10 / n > 1", Type: series.Bool},
		{Column: "safe_or", Expr: "n == 0 || 10 / n > 1", Type: series.Bool},
		{Column: "safe_cond", Expr: "n == 0 ? 0 : 10 / n", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0: short-circuited operands must not be evaluated", n)
	}
	if got, want := cleaned.Col("safe_and").Records(), []string{"false", "true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("safe_and = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("safe_or").Records(), []string{"true", "true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("safe_or = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("safe_cond").Records(), []string{"0", "10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("safe_cond = %v, want %v", got, want)
	}
}

func TestRowCleaner_FilterDropsFalseAndNull(t *testing.T) {
	input := loadMixedRecords()
	cleaner, err := CompileRowCleaner(input, nil, `qty > 5 && note != null`)
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0", n)
	}
	if got, want := cleaned.Col("name").Records(), []string{"gadget"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned names = %v, want %v", got, want)
	}

	// A filter that is null for every row drops every row without rejecting
	// any of them.
	cleaner, err = CompileRowCleaner(input, nil, "note")
	if err == nil {
		t.Fatal("a non bool filter must not compile")
	}
	cleaner, err = CompileRowCleaner(input, nil, "note == null")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err = cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := cleaned.Col("name").Records(), []string{"gizmo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned names = %v, want %v", got, want)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0", n)
	}
}

func TestRowCleaner_AllRowsFilteredOut(t *testing.T) {
	input := loadMixedRecords()
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "extra", Expr: "qty * 2", Type: series.Int},
	}, "false")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if nrows, ncols := cleaned.Dims(); nrows != 0 || ncols != 6 {
		t.Fatalf("cleaned Dims() = (%d, %d), want (0, 6)", nrows, ncols)
	}
	if got, want := cleaned.Names(), cleaner.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned names = %v, want %v", got, want)
	}
	if got, want := cleaned.Types(), cleaner.Types(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned types = %v, want %v", got, want)
	}
	if cleaned.Err != nil {
		t.Fatalf("cleaned DataFrame has an error: %v", cleaned.Err)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0", n)
	}
}

func TestRowCleaner_AllRowsRejected(t *testing.T) {
	input := New(series.New([]string{"0", "0"}, series.Int, "n"))
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "inv", Expr: "1 / n", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if nrows, ncols := cleaned.Dims(); nrows != 0 || ncols != 2 {
		t.Fatalf("cleaned Dims() = (%d, %d), want (0, 2)", nrows, ncols)
	}
	if got, want := cleaned.Names(), []string{"n", "inv"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned names = %v, want %v", got, want)
	}
	if n := rejected.Nrow(); n != 2 {
		t.Fatalf("rejected rows = %d, want 2", n)
	}
}

func TestRowCleaner_ReuseAndConcurrentApply(t *testing.T) {
	input := loadMixedRecords()
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "total", Expr: "qty * 100", Type: series.Int},
	}, "total > 0")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}

	type result struct {
		records [][]string
		err     error
	}
	const goroutines = 8
	done := make(chan result, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			cleaned, _, err := cleaner.Apply(input)
			done <- result{records: cleaned.Records(), err: err}
		}()
	}
	var first [][]string
	for g := 0; g < goroutines; g++ {
		res := <-done
		if res.err != nil {
			t.Fatalf("Apply: %v", res.err)
		}
		if first == nil {
			first = res.records
			continue
		}
		if !reflect.DeepEqual(first, res.records) {
			t.Fatalf("concurrent Apply results differ: %v and %v", first, res.records)
		}
	}
	want := [][]string{
		{"name", "qty", "price", "active", "note", "total"},
		{"widget", "3", "2.500000", "true", "hello", "300"},
		{"gadget", "10", "1.250000", "false", "", "1000"},
		{"gizmo", "7", "4.000000", "true", "NaN", "700"},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("records = %v, want %v", first, want)
	}
}

func TestRowCleaner_ApplyRejectsIncompatibleInput(t *testing.T) {
	cleaner, err := CompileRowCleaner(loadMixedRecords(), []RowFormula{
		{Column: "total", Expr: "qty * 100", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}

	other := LoadRecords([][]string{
		{"name", "qty"},
		{"a", "1"},
	})
	cleaned, rejected, err := cleaner.Apply(other)
	if err == nil {
		t.Fatal("expected an error when the input schema does not match the compiled schema")
	}
	if cleaned.Err == nil || rejected.Err == nil {
		t.Fatal("expected the returned DataFrames to carry the error")
	}
}

func TestCleanRowsConvenience(t *testing.T) {
	cleaned, rejected, err := CleanRows(loadMixedRecords(), []RowFormula{
		{Column: "total", Expr: "qty * 100", Type: series.Int},
	}, "qty > 5")
	if err != nil {
		t.Fatalf("CleanRows: %v", err)
	}
	if got, want := cleaned.Col("total").Records(), []string{"1000", "700"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("total = %v, want %v", got, want)
	}
	if n := rejected.Nrow(); n != 0 {
		t.Fatalf("rejected rows = %d, want 0", n)
	}

	// A configuration error is returned by CleanRows as well, and both
	// DataFrames carry it.
	cleaned, rejected, err = CleanRows(loadMixedRecords(), []RowFormula{
		{Column: "total", Expr: "nope * 100", Type: series.Int},
	}, "")
	if err == nil {
		t.Fatal("expected a configuration error")
	}
	if cleaned.Err == nil || rejected.Err == nil {
		t.Fatal("expected the returned DataFrames to carry the error")
	}
}

func TestRowCleaner_UnrepresentableStringIsRejected(t *testing.T) {
	input := New(series.New([]string{"a"}, series.String, "name"))
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "copy", Expr: `"NaN"`, Type: series.String},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	_, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := rejected.Nrow(); n != 1 {
		t.Fatalf("rejected rows = %d, want 1", n)
	}
	if msg := rejected.Col(RejectMessageColumn).Records()[0]; !strings.Contains(msg, "missing value marker") {
		t.Fatalf("reject message = %q, want it to explain the NaN marker", msg)
	}
}

func TestRowCleaner_NAOutputsStayMissing(t *testing.T) {
	input := loadMixedRecords()
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "maybe", Expr: "note", Type: series.String},
		{Column: "always_null", Expr: "null", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, _, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := cleaned.Col("maybe").Records(), []string{"hello", "", "NaN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("maybe = %v, want %v", got, want)
	}
	if !cleaned.Col("always_null").HasNaN() {
		t.Fatal("always_null should be missing for every row")
	}
	if got, want := cleaned.Col("always_null").Records(), []string{"NaN", "NaN", "NaN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("always_null = %v, want %v", got, want)
	}
	if _, err := cleaned.Col("always_null").Int(); err == nil {
		t.Fatal("an always_null int column must not be convertible to []int")
	}
}

// TestRowCleaner_DoesNotAffectLegacyBehaviour pins the promise that the new
// entry point leaves Capply, Rapply and Filter exactly as they were.
func TestRowCleaner_DoesNotAffectLegacyBehaviour(t *testing.T) {
	input := LoadRecords([][]string{
		{"A", "B"},
		{"1", "x"},
		{"2", "y"},
	})

	applied := input.Capply(func(s series.Series) series.Series {
		return series.Ints(s.Len())
	})
	if got, want := applied.Types(), []series.Type{series.Int, series.Int}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Capply types = %v, want %v", got, want)
	}

	// Rapply still forces a single row type, exactly as documented.
	r := input.Rapply(func(s series.Series) series.Series {
		return series.Strings(s.Len())
	})
	if got, want := r.Nrow(), 2; got != want {
		t.Fatalf("Rapply rows = %d, want %d", got, want)
	}

	filtered := input.Filter(F{Colname: "A", Comparator: series.Eq, Comparando: "1"})
	if got, want := filtered.Nrow(), 1; got != want {
		t.Fatalf("Filter rows = %d, want %d", got, want)
	}

	// Cleaning the same input leaves the legacy APIs untouched.
	if _, _, err := CleanRows(input, []RowFormula{
		{Column: "C", Expr: "int(A) * 10", Type: series.Int},
	}, ""); err != nil {
		t.Fatalf("CleanRows: %v", err)
	}
	if got, want := input.Ncol(), 2; got != want {
		t.Fatalf("input Ncol() = %d, want %d", got, want)
	}
	if got, want := input.Col("A").Records(), []string{"1", "2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("input A = %v, want %v", got, want)
	}
}

func ExampleRowCleaner() {
	df := LoadRecords([][]string{
		{"item", "qty", "price"},
		{"apple", "3", "1.5"},
		{"pear", "0", "2.0"},
		{"plum", "5", "0.5"},
	})

	cleaner, err := CompileRowCleaner(df, []RowFormula{
		{Column: "amount", Expr: "double(qty) * price", Type: series.Float},
		{Column: "label", Expr: `upper(item) + " x" + string(qty)`, Type: series.String},
	}, "qty > 0 && amount >= 1.0")
	if err != nil {
		fmt.Println("compile error:", err)
		return
	}

	cleaned, rejected, err := cleaner.Apply(df)
	if err != nil {
		fmt.Println("apply error:", err)
		return
	}
	fmt.Println("rejected rows:", rejected.Nrow())
	fmt.Println(cleaned.Col("label").Records())

	// Output:
	// rejected rows: 0
	// [APPLE x3 PLUM x5]
}

func TestRowCleaner_EmptyInput(t *testing.T) {
	input := New(
		series.New([]string{}, series.String, "name"),
		series.New([]interface{}{}, series.Int, "qty"),
	)
	if nrows, _ := input.Dims(); nrows != 0 {
		t.Fatalf("input rows = %d, want 0", nrows)
	}
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "total", Expr: "qty * 2", Type: series.Int},
	}, "qty > 0")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if nrows, ncols := cleaned.Dims(); nrows != 0 || ncols != 3 {
		t.Fatalf("cleaned Dims() = (%d, %d), want (0, 3)", nrows, ncols)
	}
	if got, want := cleaned.Names(), []string{"name", "qty", "total"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned names = %v, want %v", got, want)
	}
	if got, want := cleaned.Types(), []series.Type{series.String, series.Int, series.Int}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned types = %v, want %v", got, want)
	}
	if nrows, ncols := rejected.Dims(); nrows != 0 || ncols != 7 {
		t.Fatalf("rejected Dims() = (%d, %d), want (0, 7)", nrows, ncols)
	}
}

func TestRowCleaner_OverwriteChangesType(t *testing.T) {
	input := LoadRecords([][]string{
		{"code", "n"},
		{"a1", "4"},
		{"b2", "7"},
	})
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		// code is a string column that becomes an int column; the next
		// formula then reads it as an int.
		{Column: "code", Expr: `int(n) * 2`, Type: series.Int},
		{Column: "double_code", Expr: "code * 2", Type: series.Int},
		{Column: "text", Expr: "string(code)", Type: series.String},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, _, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := cleaned.Types(), []series.Type{series.Int, series.Int, series.Int, series.String}; !reflect.DeepEqual(got, want) {
		t.Fatalf("types = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("code").Records(), []string{"8", "14"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("code = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("double_code").Records(), []string{"16", "28"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("double_code = %v, want %v", got, want)
	}
	if got, want := cleaned.Col("text").Records(), []string{"8", "14"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("text = %v, want %v", got, want)
	}
}

func TestRowCleaner_RejectPreservesOriginalRowAfterEarlierFormulas(t *testing.T) {
	input := New(
		series.New([]string{"10", "0"}, series.Int, "n"),
		series.New([]string{"keep", "original"}, series.String, "label"),
	)
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "n", Expr: "n + 100", Type: series.Int},
		{Column: "label", Expr: `"computed"`, Type: series.String},
		{Column: "boom", Expr: "1 / (n - 110)", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := cleaned.Col("n").Records(), []string{"100"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned n = %v, want %v", got, want)
	}
	records := rejected.Records()
	if len(records) != 2 {
		t.Fatalf("reject records = %v, want one rejected row", records)
	}
	// The rejected row keeps the values it had in the input, not the values
	// computed by the formulas that ran before the failing one.
	if records[1][0] != "10" || records[1][1] != "keep" {
		t.Fatalf("reject row = %v, want the original values [10 keep]", records[1])
	}
	if records[1][3] != "boom" {
		t.Fatalf("reject source = %q, want \"boom\"", records[1][3])
	}
}

func TestRowCleaner_TypeErrorOnConversionIsRowLevel(t *testing.T) {
	input := New(series.New([]string{"1", "x", "3"}, series.String, "raw"))
	cleaner, err := CompileRowCleaner(input, []RowFormula{
		{Column: "n", Expr: "int(raw)", Type: series.Int},
	}, "")
	if err != nil {
		t.Fatalf("CompileRowCleaner: %v", err)
	}
	cleaned, rejected, err := cleaner.Apply(input)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, want := cleaned.Col("n").Records(), []string{"1", "3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleaned n = %v, want %v", got, want)
	}
	if n := rejected.Nrow(); n != 1 {
		t.Fatalf("rejected rows = %d, want 1", n)
	}
	row := rejected.Records()[1]
	if row[0] != "x" || row[1] != "1" || row[2] != "n" {
		t.Fatalf("reject row = %v, want original \"x\", row 1, source \"n\"", row)
	}
}
