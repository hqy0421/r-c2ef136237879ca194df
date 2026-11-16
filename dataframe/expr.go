package dataframe

import (
	"fmt"

	"github.com/go-gota/gota/series"
)

// Names of the columns that a RowCleaner adds to the reject DataFrame. They are
// reserved: an input DataFrame (or a formula) that uses one of them is rejected
// when the cleaner is compiled.
const (
	// RejectRowColumn holds the zero based index of the rejected row in the
	// input DataFrame.
	RejectRowColumn = "__row__"
	// RejectSourceColumn holds the name of the formula column that failed, or
	// FilterSource when the filter expression failed.
	RejectSourceColumn = "__source__"
	// RejectExpressionColumn holds the source text of the failing expression.
	RejectExpressionColumn = "__expression__"
	// RejectOffsetColumn holds the byte offset in the expression where the
	// error was detected.
	RejectOffsetColumn = "__offset__"
	// RejectMessageColumn holds the error message.
	RejectMessageColumn = "__error__"
	// FilterSource is the value of RejectSourceColumn for errors raised by the
	// filter expression.
	FilterSource = "__filter__"
)

// RowFormula declares one column computed by a RowCleaner for every row.
type RowFormula struct {
	// Column is the name of the output column. If the column exists in the
	// input it is overwritten in place, otherwise it is appended to the right
	// of the table.
	Column string
	// Expr is a CEL expression evaluated once per row.
	Expr string
	// Type is the declared type of the output column: series.String,
	// series.Int, series.Float or series.Bool.
	Type series.Type
}

// RowCleaner is a compiled, reusable row cleaning program. It is created by
// CompileRowCleaner and applied to DataFrames with the Apply method.
//
// A RowCleaner evaluates an ordered list of formulas for every row. Each
// formula can read the original columns of the row and every column computed by
// an earlier formula, and writes a column that is either added to the table or
// overwrites an existing one. A column that a formula writes is visible to the
// formulas that follow it, but the formula that writes a column sees the value
// the column had before (so "C" overwritten with the formula "C + 1" increments
// the previous value). After all formulas have been evaluated the row is tested
// against an optional filter expression, and rows for which the filter is false
// or null are dropped.
//
// Unlike Rapply, values are never converted to a single common type: every
// column keeps its own type and the expression environment contains each
// value with its native type (bool, int64, float64, string, or null). There is
// no implicit conversion between types; int and double arithmetic are separate
// and integer arithmetic is performed on int64 values with overflow checked
// (an overflowing row is rejected instead of silently wrapping). Explicit
// conversions are available through the int(), double(), string() and bool()
// functions in a formula.
//
// Missing values are represented by the CEL null value, and existing Gota NA
// elements are presented as null. A missing value is therefore different from
// an empty string, and comparing a missing value with == null is true while
// comparing it with an ordinary value or an empty string is false. A float
// element that holds NaN is also missing, exactly as in the rest of Gota, so
// it is presented as null rather than as a NaN double. Applying an operator to
// null produces null, which the filter treats as false.
//
// A formula that overwrites a column may change its type; the new type is what
// the following formulas and the cleaned DataFrame see. Every formula must
// declare its output type, and the type of the expression must match it (null
// may be stored in any column), so no value is ever converted implicitly.
//
// Syntax errors, unknown columns, and type errors are configuration errors:
// CompileRowCleaner returns them and no row is processed. Errors that can only
// be detected for a particular row (division by zero, integer overflow, a failed
// conversion...) are runtime errors: the row is copied, with its original
// values, to a reject DataFrame and the remaining rows are processed normally.
// The reject DataFrame has the columns of the input plus the RejectRowColumn,
// RejectSourceColumn, RejectExpressionColumn, RejectOffsetColumn and
// RejectMessageColumn columns, which identify the row and the exact position of
// the error.
//
// A RowCleaner is immutable after compilation and Apply does not modify the
// input DataFrame, so a compiled cleaner can be reused, including concurrently,
// on any DataFrame with the same column names, order and types.
type RowCleaner struct {
	formulas   []RowFormula
	programs   []*celProgram
	outIdx     []int
	filterText string
	filter     *celProgram
	inputNames []string
	inputTypes []series.Type
	names      []string
	types      []series.Type
}

// CompileRowCleaner validates formulas and filter against the schema of input
// and compiles them into a reusable RowCleaner. The input rows are not read:
// only the column names and types matter, so the same cleaner can later be
// applied to any DataFrame with the same schema.
//
// Every configuration problem, including syntax errors, references to columns
// that do not exist and expressions whose type does not match the declared
// output type, is reported by this function; in that case no row is ever
// processed.
func CompileRowCleaner(input DataFrame, formulas []RowFormula, filter string) (*RowCleaner, error) {
	if input.Err != nil {
		return nil, fmt.Errorf("compiling row cleaner: %v", input.Err)
	}
	if input.Ncol() == 0 {
		return nil, fmt.Errorf("compiling row cleaner: input DataFrame has no columns")
	}
	if len(formulas) == 0 && filter == "" {
		return nil, fmt.Errorf("compiling row cleaner: at least one formula or a filter expression is required")
	}

	inputNames := input.Names()
	inputTypes := input.Types()
	for i, name := range inputNames {
		if isRejectColumn(name) {
			return nil, fmt.Errorf("compiling row cleaner: input column %q uses a name reserved for the reject table", name)
		}
		if _, err := celTypeOf(inputTypes[i]); err != nil {
			return nil, fmt.Errorf("compiling row cleaner: input column %q: %v", name, err)
		}
	}

	names := make([]string, len(inputNames))
	copy(names, inputNames)
	types := make([]series.Type, len(inputTypes))
	copy(types, inputTypes)

	c := &RowCleaner{
		formulas:   make([]RowFormula, len(formulas)),
		programs:   make([]*celProgram, len(formulas)),
		outIdx:     make([]int, len(formulas)),
		filterText: filter,
		inputNames: append([]string(nil), inputNames...),
		inputTypes: append([]series.Type(nil), inputTypes...),
	}

	for i, f := range formulas {
		if f.Column == "" {
			return nil, fmt.Errorf("compiling row cleaner: formula %d: the output column name is empty", i+1)
		}
		if isRejectColumn(f.Column) {
			return nil, fmt.Errorf("compiling row cleaner: formula %d: column %q uses a name reserved for the reject table", i+1, f.Column)
		}
		if f.Type == "" {
			return nil, fmt.Errorf(
				"compiling row cleaner: formula %d (%s): the declared output type is empty (want %q, %q, %q or %q)",
				i+1, f.Column, series.String, series.Int, series.Float, series.Bool)
		}
		declared, err := celTypeOf(f.Type)
		if err != nil {
			return nil, fmt.Errorf("compiling row cleaner: formula %d (%s): %v", i+1, f.Column, err)
		}
		expr, err := celParse(f.Expr)
		if err != nil {
			return nil, fmt.Errorf("compiling row cleaner: formula %d (%s): %v", i+1, f.Column, err)
		}
		// The resolver snapshot is taken before the output column is added or
		// retyped, so a formula that writes a column reads its previous value.
		program, err := expr.Bind(schemaResolver{names: names, types: types})
		if err != nil {
			return nil, fmt.Errorf("compiling row cleaner: formula %d (%s): %v", i+1, f.Column, err)
		}
		if !assignableTo(program.Type(), declared) {
			return nil, fmt.Errorf(
				"compiling row cleaner: formula %d (%s): expression has type %s but the declared output type is %s (convert it explicitly, for example with %s(...))",
				i+1, f.Column, program.Type(), declared, conversionName(declared))
		}

		idx := findInStringSlice(f.Column, names)
		if idx < 0 {
			names = append(names, f.Column)
			types = append(types, f.Type)
			idx = len(names) - 1
		} else {
			types[idx] = f.Type
		}

		c.formulas[i] = f
		c.programs[i] = program
		c.outIdx[i] = idx
	}

	if filter != "" {
		expr, err := celParse(filter)
		if err != nil {
			return nil, fmt.Errorf("compiling row cleaner: filter: %v", err)
		}
		program, err := expr.Bind(schemaResolver{names: names, types: types})
		if err != nil {
			return nil, fmt.Errorf("compiling row cleaner: filter: %v", err)
		}
		switch program.Type().Kind() {
		case celBoolKind, celNullKind:
		default:
			return nil, fmt.Errorf("compiling row cleaner: filter: expression has type %s, want bool", program.Type())
		}
		c.filter = program
	}

	c.names = names
	c.types = types
	return c, nil
}

// CleanRows compiles formulas and filter against input and applies the result
// to input. It is a convenience wrapper around CompileRowCleaner and
// RowCleaner.Apply for callers that process a single DataFrame.
func CleanRows(input DataFrame, formulas []RowFormula, filter string) (DataFrame, DataFrame, error) {
	cleaner, err := CompileRowCleaner(input, formulas, filter)
	if err != nil {
		return DataFrame{Err: err}, DataFrame{Err: err}, err
	}
	return cleaner.Apply(input)
}

// CleanRows compiles and applies the formulas and filter to df in one call. It
// is equivalent to calling the package level CleanRows function with df, and
// returns the cleaned and rejected DataFrames as described by RowCleaner.Apply.
//
// When the same formulas are applied to several DataFrames that share a schema,
// compile once with CompileRowCleaner and reuse the RowCleaner instead.
func (df DataFrame) CleanRows(formulas []RowFormula, filter string) (DataFrame, DataFrame, error) {
	return CleanRows(df, formulas, filter)
}

// Names returns the columns of the cleaned DataFrame, in order.
func (c *RowCleaner) Names() []string {
	return append([]string(nil), c.names...)
}

// Types returns the types of the columns of the cleaned DataFrame, in order.
func (c *RowCleaner) Types() []series.Type {
	return append([]series.Type(nil), c.types...)
}

// Apply evaluates the compiled formulas and filter for every row of input.
//
// It returns two native DataFrames: the cleaned rows, which have the compiled
// schema, and the rejected rows, which contain the original input columns plus
// the error columns described in the RowCleaner documentation. Rows are only
// rejected by runtime errors; rows for which the filter is false or null are
// dropped from both DataFrames. The returned error is non nil only when the
// input cannot be processed at all (for example because its schema does not
// match the schema the cleaner was compiled against); a row level problem is
// reported through the reject DataFrame.
//
// Apply does not modify input.
func (c *RowCleaner) Apply(input DataFrame) (DataFrame, DataFrame, error) {
	if input.Err != nil {
		err := fmt.Errorf("applying row cleaner: %v", input.Err)
		return DataFrame{Err: err}, DataFrame{Err: err}, err
	}
	if err := c.checkSchema(input); err != nil {
		err = fmt.Errorf("applying row cleaner: %v", err)
		return DataFrame{Err: err}, DataFrame{Err: err}, err
	}

	nrows := input.Nrow()
	ncols := len(c.names)
	nInput := len(c.inputNames)

	cleaned := make([][]interface{}, ncols)
	for i := range cleaned {
		clean := make([]interface{}, 0, nrows)
		cleaned[i] = clean
	}
	var rejectRows []int
	var rejectSources []string
	var rejectExprs []string
	var rejectOffsets []int
	var rejectMessages []string

	values := make([]interface{}, ncols)
	for r := 0; r < nrows; r++ {
		for j := 0; j < nInput; j++ {
			values[j] = elementToCel(input.Elem(r, j))
		}
		for j := nInput; j < ncols; j++ {
			values[j] = nil
		}

		if failure := c.evalFormulas(values); failure != nil {
			rejectRows = append(rejectRows, r)
			rejectSources = append(rejectSources, failure.source)
			rejectExprs = append(rejectExprs, failure.expression)
			rejectOffsets = append(rejectOffsets, failure.offset)
			rejectMessages = append(rejectMessages, failure.message)
			continue
		}

		keep, failure := c.evalFilter(values)
		if failure != nil {
			rejectRows = append(rejectRows, r)
			rejectSources = append(rejectSources, failure.source)
			rejectExprs = append(rejectExprs, failure.expression)
			rejectOffsets = append(rejectOffsets, failure.offset)
			rejectMessages = append(rejectMessages, failure.message)
			continue
		}
		if !keep {
			continue
		}
		for j := 0; j < ncols; j++ {
			cleaned[j] = append(cleaned[j], values[j])
		}
	}

	cleanedDF := c.buildFrame(cleaned, c.names, c.types)
	rejectedDF := c.buildRejectFrame(input, rejectRows, rejectSources, rejectExprs, rejectOffsets, rejectMessages)
	return cleanedDF, rejectedDF, nil
}

// failure describes a runtime error for one row.
type failure struct {
	source     string
	expression string
	offset     int
	message    string
}

// evalFormulas applies every formula, in order, to values. The formulas read
// and write values in place, which is how later formulas see earlier results
// and how overwritten columns keep their position.
func (c *RowCleaner) evalFormulas(values []interface{}) *failure {
	env := rowEnv{values: values}
	for i, program := range c.programs {
		result, err := program.Eval(env)
		if err != nil {
			return newFailure(c.formulas[i].Column, c.formulas[i].Expr, err)
		}
		stored, err := coerceFormulaValue(result, c.formulas[i].Type)
		if err != nil {
			return &failure{
				source:     c.formulas[i].Column,
				expression: c.formulas[i].Expr,
				message:    err.Error(),
			}
		}
		values[c.outIdx[i]] = stored
	}
	return nil
}

// evalFilter reports whether the row must be kept. A false or null result
// filters the row out; a runtime error rejects it.
func (c *RowCleaner) evalFilter(values []interface{}) (bool, *failure) {
	if c.filter == nil {
		return true, nil
	}
	result, err := c.filter.Eval(rowEnv{values: values})
	if err != nil {
		return false, newFailure(FilterSource, c.filterText, err)
	}
	switch v := result.(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	}
	return false, nil
}

func (c *RowCleaner) checkSchema(input DataFrame) error {
	names := input.Names()
	types := input.Types()
	if len(names) != len(c.inputNames) {
		return fmt.Errorf("input has %d columns, want %d", len(names), len(c.inputNames))
	}
	for i := range c.inputNames {
		if names[i] != c.inputNames[i] {
			return fmt.Errorf("input column %d is %q, want %q", i, names[i], c.inputNames[i])
		}
		if types[i] != c.inputTypes[i] {
			return fmt.Errorf("input column %q has type %q, want %q", names[i], types[i], c.inputTypes[i])
		}
	}
	return nil
}

// buildFrame builds a native DataFrame from CEL values grouped by column.
func (c *RowCleaner) buildFrame(columns [][]interface{}, names []string, types []series.Type) DataFrame {
	se := make([]series.Series, len(columns))
	for i, values := range columns {
		se[i] = series.New(celValuesToNative(values, types[i]), types[i], names[i])
	}
	return New(se...)
}

// buildRejectFrame builds the reject DataFrame: the original input columns
// followed by the error columns.
func (c *RowCleaner) buildRejectFrame(
	input DataFrame,
	rows []int,
	sources, exprs []string,
	offsets []int,
	messages []string,
) DataFrame {
	nrows := len(rows)
	se := make([]series.Series, 0, len(c.inputNames)+5)
	for j, name := range c.inputNames {
		column := make([]interface{}, nrows)
		for k, r := range rows {
			column[k] = elementToCel(input.Elem(r, j))
		}
		se = append(se, series.New(celValuesToNative(column, c.inputTypes[j]), c.inputTypes[j], name))
	}
	rejectRows := make([]interface{}, nrows)
	rejectSources := make([]interface{}, nrows)
	rejectExprs := make([]interface{}, nrows)
	rejectOffsets := make([]interface{}, nrows)
	rejectMessages := make([]interface{}, nrows)
	for k := 0; k < nrows; k++ {
		rejectRows[k] = rows[k]
		rejectSources[k] = sources[k]
		rejectExprs[k] = exprs[k]
		rejectOffsets[k] = offsets[k]
		rejectMessages[k] = messages[k]
	}
	se = append(se,
		series.New(rejectRows, series.Int, RejectRowColumn),
		series.New(rejectSources, series.String, RejectSourceColumn),
		series.New(rejectExprs, series.String, RejectExpressionColumn),
		series.New(rejectOffsets, series.Int, RejectOffsetColumn),
		series.New(rejectMessages, series.String, RejectMessageColumn),
	)
	return New(se...)
}

// schemaResolver resolves column names against an evolving table schema for the
// expression compiler.
type schemaResolver struct {
	names []string
	types []series.Type
}

func (r schemaResolver) Resolve(name string) (celRef, celType, bool) {
	for i, n := range r.names {
		if n != name {
			continue
		}
		typ, err := celTypeOf(r.types[i])
		if err != nil {
			return 0, celType{}, false
		}
		return celRef(i), typ, true
	}
	return 0, celType{}, false
}

// rowEnv is the expression environment of one row. Values are stored in CEL
// representation: nil, bool, int64, float64 or string.
type rowEnv struct {
	values []interface{}
}

func (e rowEnv) Value(ref celRef) (interface{}, error) {
	return e.values[ref], nil
}

// celTypeOf maps a Gota type to the corresponding expression type.
func celTypeOf(t series.Type) (celType, error) {
	switch t {
	case series.String:
		return celStringType, nil
	case series.Int:
		return celIntType, nil
	case series.Float:
		return celDoubleType, nil
	case series.Bool:
		return celBoolType, nil
	}
	return celType{}, fmt.Errorf(
		"unsupported column type %q (want one of %q, %q, %q or %q)",
		t, series.String, series.Int, series.Float, series.Bool)
}

// assignableTo reports whether a value of type got can be stored in a column of
// type want. Null can be stored anywhere; everything else must match exactly so
// that no value is ever converted implicitly.
func assignableTo(got, want celType) bool {
	return got.Kind() == celNullKind || got.Equal(want)
}

// conversionName returns the name of the expression function that converts a
// value to a column of type t.
func conversionName(t celType) string {
	switch t.Kind() {
	case celIntKind:
		return "int"
	case celDoubleKind:
		return "double"
	case celStringKind:
		return "string"
	default:
		return "bool"
	}
}

// coerceFormulaValue checks that the evaluated value can be stored in a column
// of type t and converts it to the representation used by cleaned rows.
func coerceFormulaValue(value interface{}, t series.Type) (interface{}, error) {
	if value == nil {
		return nil, nil
	}
	switch t {
	case series.String:
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("evaluated value has type %T, want string", value)
		}
		// A String Series uses the literal "NaN" as its missing value
		// marker, so storing it as a normal string would turn an ordinary
		// value into a missing one.
		if s == "NaN" {
			return nil, fmt.Errorf(`the string "NaN" cannot be stored in a string column: it is gota's missing value marker; produce null for a missing value instead`)
		}
		return s, nil
	case series.Int:
		n, ok := value.(int64)
		if !ok {
			return nil, fmt.Errorf("evaluated value has type %T, want int", value)
		}
		if int64(int(n)) != n {
			return nil, fmt.Errorf("integer %d does not fit in the platform int type", n)
		}
		return n, nil
	case series.Float:
		f, ok := value.(float64)
		if !ok {
			return nil, fmt.Errorf("evaluated value has type %T, want double", value)
		}
		return f, nil
	case series.Bool:
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("evaluated value has type %T, want bool", value)
		}
		return b, nil
	}
	return nil, fmt.Errorf("unsupported output type %q", t)
}

// elementToCel converts a Series element to the CEL representation.
func elementToCel(element series.Element) interface{} {
	if element.IsNA() {
		return nil
	}
	switch v := element.Val().(type) {
	case int:
		return int64(v)
	case float64:
		return v
	case bool:
		return v
	case string:
		return v
	}
	return nil
}

// celValuesToNative converts the CEL representation of a column to the values
// expected by series.New for the given type. Missing values become nil.
func celValuesToNative(values []interface{}, t series.Type) []interface{} {
	native := make([]interface{}, len(values))
	for i, v := range values {
		if v == nil {
			native[i] = nil
			continue
		}
		if t == series.Int {
			native[i] = int(v.(int64))
			continue
		}
		native[i] = v
	}
	return native
}

// newFailure converts an expression error into a reject table entry.
func newFailure(source, expression string, err error) *failure {
	f := &failure{source: source, expression: expression, message: err.Error()}
	if exprErr, ok := err.(*celError); ok {
		f.offset = exprErr.Offset
		f.message = exprErr.Message
	}
	return f
}

// isRejectColumn reports whether name is reserved for the reject table.
func isRejectColumn(name string) bool {
	switch name {
	case RejectRowColumn, RejectSourceColumn, RejectExpressionColumn, RejectOffsetColumn, RejectMessageColumn:
		return true
	}
	return false
}
