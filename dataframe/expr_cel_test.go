package dataframe

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// celTestResolver resolves a fixed set of typed variables.
type celTestResolver map[string]celType

func (r celTestResolver) Resolve(name string) (celRef, celType, bool) {
	typ, ok := r[name]
	if !ok {
		return 0, celType{}, false
	}
	return celTestRefs[name], typ, true
}

// celTestRefs assigns a stable Ref to every identifier used by the tests.
var celTestRefs = map[string]celRef{
	"s":       0,
	"i":       1,
	"f":       2,
	"b":       3,
	"n":       4,
	"ints":    5,
	"strings": 6,
}

// celTestEnv returns the values of the identifiers used by the tests.
type celTestEnv map[string]interface{}

func (e celTestEnv) Value(ref celRef) (interface{}, error) {
	for name, r := range celTestRefs {
		if r == ref {
			if v, ok := e[name]; ok {
				return v, nil
			}
		}
	}
	return nil, nil
}

var celTestTypes = celTestResolver{
	"s":       celStringType,
	"i":       celIntType,
	"f":       celDoubleType,
	"b":       celBoolType,
	"n":       celNullType,
	"ints":    celListOf(celIntType),
	"strings": celListOf(celStringType),
}

// celTestEval parses, binds and evaluates text against env.
func celTestEval(text string, env celTestEnv) (interface{}, error) {
	expr, err := celParse(text)
	if err != nil {
		return nil, err
	}
	program, err := expr.Bind(celTestTypes)
	if err != nil {
		return nil, err
	}
	return program.Eval(env)
}

type celTestEvalCase struct {
	expr string
	env  celTestEnv
	want interface{}
}

var celTestEvalCases = []celTestEvalCase{
	// Literals.
	{expr: "1", want: int64(1)},
	{expr: "1 + 2 * 3", want: int64(7)},
	{expr: "(1 + 2) * 3", want: int64(9)},
	{expr: "7 / 2", want: int64(3)},
	{expr: "-7 / 2", want: int64(-3)},
	{expr: "7 % 3", want: int64(1)},
	{expr: "1.5 + 2.25", want: 3.75},
	{expr: `"a" + "b"`, want: "ab"},
	{expr: `'single'`, want: "single"},
	{expr: `"tab\there"`, want: "tab\there"},
	{expr: "true && false", want: false},
	{expr: "true || false", want: true},
	{expr: "!true", want: false},
	{expr: "1 < 2", want: true},
	{expr: `"abc" < "abd"`, want: true},
	{expr: "1 == 1.0", want: errors.New("cannot compare")}, // no implicit numeric conversion
	{expr: "null == null", want: true},
	{expr: "1 == null", want: false},
	{expr: "1 != null", want: true},

	// Variables keep their native type.
	{expr: "i", env: celTestEnv{"i": int64(5)}, want: int64(5)},
	{expr: "i + 1", env: celTestEnv{"i": int64(5)}, want: int64(6)},
	{expr: "i * f", env: celTestEnv{"i": int64(5), "f": 2.0}, want: errors.New("requires two ints")},
	{expr: "f + 0.5", env: celTestEnv{"f": 2.5}, want: 3.0},
	{expr: "s + \"!\"", env: celTestEnv{"s": "hi"}, want: "hi!"},

	// Null propagation: null is not an empty string and does not behave like
	// one.
	{expr: "s + \"!\"", env: celTestEnv{"s": nil}, want: nil},
	{expr: "s == \"\"", env: celTestEnv{"s": nil}, want: false},
	{expr: "s == null", env: celTestEnv{"s": nil}, want: true},
	{expr: "size(s)", env: celTestEnv{"s": nil}, want: nil},
	{expr: "size(s)", env: celTestEnv{"s": ""}, want: int64(0)},
	{expr: "i + 1", env: celTestEnv{"i": nil}, want: nil},
	{expr: "i < 10", env: celTestEnv{"i": nil}, want: nil},

	// Short-circuiting absorbs errors on the other side.
	{expr: "false && (1 / 0 == 1)", want: false},
	{expr: "true || (1 / 0 == 1)", want: true},
	{expr: "b && false", env: celTestEnv{"b": nil}, want: false},
	{expr: "b || true", env: celTestEnv{"b": nil}, want: true},
	{expr: "b && true", env: celTestEnv{"b": nil}, want: nil},
	{expr: "b || false", env: celTestEnv{"b": nil}, want: nil},

	// Conditionals short-circuit as well.
	{expr: "i > 0 ? i : -i", env: celTestEnv{"i": int64(3)}, want: int64(3)},
	{expr: "i > 0 ? i : -i", env: celTestEnv{"i": int64(-3)}, want: int64(3)},
	{expr: "true ? 1 : (1 / 0)", want: int64(1)},
	{expr: "true ? 1 : null", want: int64(1)},
	{expr: "false ? 1 : null", want: nil},

	// Integer arithmetic is exact and checked.
	{expr: "9223372036854775807", want: int64(math.MaxInt64)},
	{expr: "9223372036854775807 + 1", want: errors.New("overflow")},
	{expr: "-9223372036854775807 - 2", want: errors.New("overflow")},
	{expr: "3037000500 * 3037000500", want: errors.New("overflow")},
	{expr: "(-9223372036854775807 - 1) / -1", want: errors.New("overflow")},
	{expr: "1 / 0", want: errors.New("division by zero")},
	{expr: "1 % 0", want: errors.New("modulo by zero")},
	{expr: "i * 3", env: celTestEnv{"i": int64(3000000000)}, want: int64(9000000000)},

	// Floats follow IEEE 754.
	{expr: "1.0 / 0.0", want: math.Inf(1)},
	{expr: "0.0 / 0.0", want: math.NaN()},

	// IEEE 754 ordering: NaN is unordered, so every ordering comparison
	// involving it is false and equality is false as well.
	{expr: "(0.0 / 0.0) < 1.0", want: false},
	{expr: "(0.0 / 0.0) <= 1.0", want: false},
	{expr: "(0.0 / 0.0) > 1.0", want: false},
	{expr: "(0.0 / 0.0) >= 1.0", want: false},
	{expr: "(0.0 / 0.0) == (0.0 / 0.0)", want: false},
	{expr: "(0.0 / 0.0) != (0.0 / 0.0)", want: true},

	// Functions.
	{expr: "size(\"héllo\")", want: int64(5)},
	{expr: "size(strings)", env: celTestEnv{"strings": []interface{}{"a", "b"}}, want: int64(2)},
	{expr: "lower(\"ABC\")", want: "abc"},
	{expr: "upper(\"abc\")", want: "ABC"},
	{expr: "trim(\"  a  \")", want: "a"},
	{expr: "contains(\"abc\", \"b\")", want: true},
	{expr: "startsWith(\"abc\", \"ab\")", want: true},
	{expr: "endsWith(\"abc\", \"bc\")", want: true},
	{expr: "abs(-3)", want: int64(3)},
	{expr: "abs(-3.5)", want: 3.5},
	{expr: "min(2, 3)", want: int64(2)},
	{expr: "max(2, 3)", want: int64(3)},
	{expr: "min(2.5, 1.5)", want: 1.5},
	{expr: "isNaN(0.0 / 0.0)", want: true},
	{expr: "coalesce(s, \"fallback\")", env: celTestEnv{"s": nil}, want: "fallback"},
	{expr: "coalesce(s, \"fallback\")", env: celTestEnv{"s": "value"}, want: "value"},
	{expr: "coalesce(null, null)", want: nil},

	// Explicit conversions; these are the only places where a value changes
	// kind.
	{expr: "int(\"42\")", want: int64(42)},
	{expr: "int(1.9)", want: int64(1)},
	{expr: "int(true)", want: int64(1)},
	{expr: "int(\"x\")", want: errors.New("cannot convert")},
	{expr: "double(2)", want: 2.0},
	{expr: "double(\"2.5\")", want: 2.5},
	{expr: "string(42)", want: "42"},
	{expr: "string(true)", want: "true"},
	{expr: "string(1.5)", want: "1.5"},
	{expr: "bool(\"true\")", want: true},
	{expr: "bool(false)", want: false},
	{expr: "bool(\"yes\")", want: errors.New("cannot convert")},
	{expr: "int(s)", env: celTestEnv{"s": nil}, want: nil},

	// Lists.
	{expr: "[1, 2, 3][1]", want: int64(2)},
	{expr: "size([1, 2, 3])", want: int64(3)},
	{expr: "2 in [1, 2, 3]", want: true},
	{expr: "4 in [1, 2, 3]", want: false},
	{expr: `"b" in ["a", "b"]`, want: true},
	{expr: "[1, 2][5]", want: errors.New("out of range")},
	{expr: "1 in 2", want: errors.New("requires a list")},

	// Compile time errors.
	{expr: "unknown + 1", want: errors.New("unknown identifier")},
	{expr: "nosuchfn(1)", want: errors.New("unknown function")},
	{expr: "size(1)", want: errors.New("must be a string or a list")},
	{expr: "size(1, 2)", want: errors.New("expects 1 argument")},
	{expr: "\"a\" - \"b\"", want: errors.New("requires two ints or two doubles")},
	{expr: "1 + 1.5", want: errors.New("cannot add")},
	{expr: "1 && true", want: errors.New("requires bool operands")},
	{expr: "true ? 1 : \"a\"", want: errors.New("incompatible types")},
	{expr: "s.return", want: errors.New("field selection")},
	{expr: "1 +", want: errors.New("unexpected end of expression")},
	{expr: "(1", want: errors.New("expected `)`")},
	{expr: "[1,]", want: []interface{}{int64(1)}},
	{expr: ".5", want: errors.New("double literal")},
	{expr: "1e", want: errors.New("exponent")},
	{expr: `"unterminated`, want: errors.New("unterminated")},
	{expr: `"\q"`, want: errors.New("unknown escape")},
	{expr: "@", want: errors.New("unexpected character")},
	{expr: "a & b", want: errors.New("unexpected operator")},
	{expr: "1.5.5", want: errors.New("double literal")},
}

func TestEval(t *testing.T) {
	for _, tc := range celTestEvalCases {
		tc := tc
		t.Run(tc.expr, func(t *testing.T) {
			got, err := celTestEval(tc.expr, tc.env)
			if wantErr, isErr := tc.want.(error); isErr {
				if err == nil {
					t.Fatalf("celTestEval(%q) = %v, want an error containing %q", tc.expr, got, wantErr.Error())
				}
				if !strings.Contains(err.Error(), wantErr.Error()) {
					t.Fatalf("celTestEval(%q) error = %q, want an error containing %q", tc.expr, err, wantErr.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("celTestEval(%q): unexpected error: %v", tc.expr, err)
			}
			if f, ok := tc.want.(float64); ok && math.IsNaN(f) {
				g, ok := got.(float64)
				if !ok || !math.IsNaN(g) {
					t.Fatalf("celTestEval(%q) = %v, want NaN", tc.expr, got)
				}
				return
			}
			if f, ok := tc.want.(float64); ok && math.IsInf(f, 1) {
				g, ok := got.(float64)
				if !ok || !math.IsInf(g, 1) {
					t.Fatalf("celTestEval(%q) = %v, want +Inf", tc.expr, got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("celTestEval(%q) = %#v, want %#v", tc.expr, got, tc.want)
			}
		})
	}
}

func TestParseErrorsCarryOffset(t *testing.T) {
	tests := []struct {
		expr   string
		offset int
	}{
		{expr: "1 + * 2", offset: 4},
		{expr: `"abc`, offset: 0},
		{expr: "1 +", offset: 3},
		{expr: "1 = 2", offset: 2},
		{expr: "size(1, )", offset: 8},
	}
	for _, tc := range tests {
		_, err := celParse(tc.expr)
		if err == nil {
			t.Fatalf("Parse(%q): expected an error", tc.expr)
		}
		var celErr *celError
		if !errors.As(err, &celErr) {
			t.Fatalf("Parse(%q) error type = %T, want *cel.Error", tc.expr, err)
		}
		if celErr.Offset != tc.offset {
			t.Fatalf("Parse(%q) offset = %d, want %d (%v)", tc.expr, celErr.Offset, tc.offset, err)
		}
	}
}

func TestRuntimeErrorsCarryOffset(t *testing.T) {
	tests := []struct {
		expr   string
		offset int
	}{
		{expr: "1 + 2 / 0", offset: 6},
		{expr: "int(\"x\")", offset: 0},
		{expr: "[1][9]", offset: 3},
	}
	for _, tc := range tests {
		_, err := celTestEval(tc.expr, nil)
		if err == nil {
			t.Fatalf("celTestEval(%q): expected an error", tc.expr)
		}
		var celErr *celError
		if !errors.As(err, &celErr) {
			t.Fatalf("celTestEval(%q) error type = %T, want *cel.Error", tc.expr, err)
		}
		if celErr.Offset != tc.offset {
			t.Fatalf("celTestEval(%q) offset = %d, want %d (%v)", tc.expr, celErr.Offset, tc.offset, err)
		}
	}
}

func TestParseRejectsDeepNesting(t *testing.T) {
	expr := strings.Repeat("(", 500) + "1" + strings.Repeat(")", 500)
	if _, err := celParse(expr); err == nil {
		t.Fatal("expected an error for deeply nested expression")
	}
}

func TestProgramReusable(t *testing.T) {
	expr, err := celParse("i + 1")
	if err != nil {
		t.Fatal(err)
	}
	program, err := expr.Bind(celTestTypes)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 3; i++ {
		got, err := program.Eval(celTestEnv{"i": i})
		if err != nil {
			t.Fatalf("Eval: %v", err)
		}
		if got != i+1 {
			t.Fatalf("Eval = %v, want %v", got, i+1)
		}
	}
}

func TestBindReportsType(t *testing.T) {
	tests := []struct {
		expr string
		want celType
	}{
		{expr: "i + 1", want: celIntType},
		{expr: "f * 2.0", want: celDoubleType},
		{expr: `s + "x"`, want: celStringType},
		{expr: "i > 0", want: celBoolType},
		{expr: "coalesce(i, 0)", want: celIntType},
		{expr: "[1, 2]", want: celListOf(celIntType)},
		{expr: "1 == 1", want: celBoolType},
	}
	for _, tc := range tests {
		expr, err := celParse(tc.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.expr, err)
		}
		program, err := expr.Bind(celTestTypes)
		if err != nil {
			t.Fatalf("Bind(%q): %v", tc.expr, err)
		}
		if !program.Type().Equal(tc.want) {
			t.Fatalf("Bind(%q).Type() = %s, want %s", tc.expr, program.Type(), tc.want)
		}
	}
}

func TestConcurrentEval(t *testing.T) {
	expr, err := celParse(`coalesce(s, "missing") + "-" + string(i)`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := expr.Bind(celTestTypes)
	if err != nil {
		t.Fatal(err)
	}
	const goroutines = 8
	const iterations = 200
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			for i := 0; i < iterations; i++ {
				got, err := program.Eval(celTestEnv{"s": nil, "i": int64(g*iterations + i)})
				if err != nil {
					errs <- err
					return
				}
				if _, ok := got.(string); !ok {
					errs <- errors.New("unexpected result type")
					return
				}
			}
			errs <- nil
		}(g)
	}
	for g := 0; g < goroutines; g++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
