package dataframe

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// celEvalNode evaluates n. A nil result means CEL null. Every error is a
// *celError that carries the position of the offending node.
func celEvalNode(n celNode, refs map[*celIdentNode]celRef, env celEnv) (interface{}, error) {
	switch n := n.(type) {
	case *celNullLit:
		return nil, nil
	case *celBoolLit:
		return n.val, nil
	case *celIntLit:
		return n.val, nil
	case *celDoubleLit:
		return n.val, nil
	case *celStringLit:
		return n.val, nil
	case *celListLit:
		out := make([]interface{}, len(n.elems))
		for i, elem := range n.elems {
			v, err := celEvalNode(elem, refs, env)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case *celIdentNode:
		ref, ok := refs[n]
		if !ok {
			return nil, celErrAt(n.off, "identifier %q was not bound", n.name)
		}
		v, err := env.Value(ref)
		if err != nil {
			if cerr, ok := err.(*celError); ok {
				return nil, cerr
			}
			return nil, celErrAt(n.off, "%v", err)
		}
		return v, nil
	case *celSelectNode:
		return nil, celErrAt(n.off, "field selection is not supported")
	case *celIndexNode:
		return celEvalIndex(n, refs, env)
	case *celUnaryNode:
		return celEvalUnary(n, refs, env)
	case *celBinaryNode:
		return celEvalBinary(n, refs, env)
	case *celCallNode:
		return celEvalCall(n, refs, env)
	case *celCondNode:
		test, err := celEvalNode(n.test, refs, env)
		if err != nil {
			return nil, err
		}
		switch t := test.(type) {
		case nil:
			return nil, celErrAt(n.test.position(), "conditional test is null")
		case bool:
			if t {
				return celEvalNode(n.then, refs, env)
			}
			return celEvalNode(n.other, refs, env)
		}
		return nil, celErrAt(n.test.position(), "conditional test is not a bool")
	}
	return nil, celErrAt(n.position(), "unsupported expression")
}

func celEvalIndex(n *celIndexNode, refs map[*celIdentNode]celRef, env celEnv) (interface{}, error) {
	operand, err := celEvalNode(n.operand, refs, env)
	if err != nil {
		return nil, err
	}
	if operand == nil {
		return nil, nil
	}
	list, ok := operand.([]interface{})
	if !ok {
		return nil, celErrAt(n.off, "indexed value is not a list")
	}
	index, err := celEvalNode(n.index, refs, env)
	if err != nil {
		return nil, err
	}
	i, ok := index.(int64)
	if !ok {
		return nil, celErrAt(n.off, "list index is not an int")
	}
	if i < 0 || i >= int64(len(list)) {
		return nil, celErrAt(n.off, "list index %d is out of range for a list of length %d", i, len(list))
	}
	return list[i], nil
}

func celEvalUnary(n *celUnaryNode, refs map[*celIdentNode]celRef, env celEnv) (interface{}, error) {
	operand, err := celEvalNode(n.operand, refs, env)
	if err != nil {
		return nil, err
	}
	if operand == nil {
		return nil, nil
	}
	switch n.op {
	case "!":
		b, ok := operand.(bool)
		if !ok {
			return nil, celErrAt(n.off, "operator `!` applied to a non-bool value")
		}
		return !b, nil
	case "-":
		switch v := operand.(type) {
		case int64:
			if v == math.MinInt64 {
				return nil, celErrAt(n.off, "integer overflow: negating %d", v)
			}
			return -v, nil
		case float64:
			return -v, nil
		}
		return nil, celErrAt(n.off, "operator `-` applied to a non-numeric value")
	}
	return nil, celErrAt(n.off, "unknown unary operator %q", n.op)
}

func celEvalBinary(n *celBinaryNode, refs map[*celIdentNode]celRef, env celEnv) (interface{}, error) {
	// Logical operators are evaluated with CEL's error absorbing,
	// short-circuiting semantics, so they are handled before evaluating both
	// sides eagerly.
	if n.op == "&&" || n.op == "||" {
		return celEvalLogical(n, refs, env)
	}
	left, err := celEvalNode(n.left, refs, env)
	if err != nil {
		return nil, err
	}
	right, err := celEvalNode(n.right, refs, env)
	if err != nil {
		return nil, err
	}
	if left == nil || right == nil {
		switch n.op {
		case "==":
			return left == nil && right == nil, nil
		case "!=":
			return !(left == nil && right == nil), nil
		}
		// Null propagates through every other operator. This includes the
		// ordering operators, which return null instead of an error so that a
		// missing value is never mistaken for a comparison result.
		return nil, nil
	}
	switch n.op {
	case "==":
		return celEqualValues(left, right), nil
	case "!=":
		return !celEqualValues(left, right), nil
	case "<", "<=", ">", ">=":
		return celCompareValues(n, left, right)
	case "+":
		return celAddValues(n, left, right)
	case "-":
		return celSubtractValues(n, left, right)
	case "*":
		return celMultiplyValues(n, left, right)
	case "/":
		return celDivideValues(n, left, right)
	case "%":
		return celModuloValues(n, left, right)
	case "in":
		return celInValues(n, left, right)
	}
	return nil, celErrAt(n.off, "unknown operator %q", n.op)
}

// celEvalLogical implements && and ||. It follows CEL's short-circuiting,
// error-absorbing semantics: an operand that determines the result (false for
// &&, true for ||) wins even when the other operand would fail, and the right
// operand is not evaluated at all when the left one already determines the
// result. This is observable because an operand may divide by zero or overflow.
func celEvalLogical(n *celBinaryNode, refs map[*celIdentNode]celRef, env celEnv) (interface{}, error) {
	left, leftErr := celEvalNode(n.left, refs, env)
	if leftErr == nil && celLogicalDecides(n.op, left) {
		return left, nil
	}
	right, rightErr := celEvalNode(n.right, refs, env)
	if rightErr == nil && celLogicalDecides(n.op, right) {
		return right, nil
	}
	if leftErr != nil {
		return nil, leftErr
	}
	if rightErr != nil {
		return nil, rightErr
	}
	return celLogicalCombine(n.op, left, right, n)
}

// celLogicalDecides reports whether v alone determines the result of op.
func celLogicalDecides(op string, v interface{}) bool {
	b, ok := v.(bool)
	if !ok {
		return false
	}
	if op == "&&" {
		return !b
	}
	return b
}

func celLogicalCombine(op string, left, right interface{}, n *celBinaryNode) (interface{}, error) {
	lb, lok := left.(bool)
	rb, rok := right.(bool)
	if left == nil && right == nil {
		return nil, nil
	}
	switch op {
	case "&&":
		if (lok && !lb) || (rok && !rb) {
			return false, nil
		}
		if lok && rok {
			return true, nil
		}
		return nil, nil
	case "||":
		if (lok && lb) || (rok && rb) {
			return true, nil
		}
		if lok && rok {
			return false, nil
		}
		return nil, nil
	}
	return nil, celErrAt(n.off, "unknown logical operator %q", op)
}

func celCompareValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	// IEEE 754 ordering: every ordering comparison involving NaN is false
	// (== is handled by equalValues, where NaN is also not equal to itself).
	if celIsNaNValue(left) || celIsNaNValue(right) {
		return false, nil
	}
	cmp, ok := celCompareOrdered(left, right)
	if !ok {
		return nil, celErrAt(n.off, "operator %q cannot compare values of different kinds", n.op)
	}
	switch n.op {
	case "<":
		return cmp < 0, nil
	case "<=":
		return cmp <= 0, nil
	case ">":
		return cmp > 0, nil
	default:
		return cmp >= 0, nil
	}
}

// celIsNaNValue reports whether v is a floating point NaN.
func celIsNaNValue(v interface{}) bool {
	f, ok := v.(float64)
	return ok && math.IsNaN(f)
}

// celCompareOrdered returns -1, 0 or 1 for two ordered values of the same kind.
func celCompareOrdered(left, right interface{}) (int, bool) {
	switch l := left.(type) {
	case int64:
		r, ok := right.(int64)
		if !ok {
			return 0, false
		}
		switch {
		case l < r:
			return -1, true
		case l > r:
			return 1, true
		}
		return 0, true
	case float64:
		r, ok := right.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case l < r:
			return -1, true
		case l > r:
			return 1, true
		}
		return 0, true
	case string:
		r, ok := right.(string)
		if !ok {
			return 0, false
		}
		return strings.Compare(l, r), true
	}
	return 0, false
}

// celEqualValues implements CEL equality: null is equal to null and to nothing
// else; values of different kinds are never equal; NaN is not equal to itself.
func celEqualValues(left, right interface{}) bool {
	switch l := left.(type) {
	case nil:
		return right == nil
	case bool:
		r, ok := right.(bool)
		return ok && l == r
	case int64:
		r, ok := right.(int64)
		return ok && l == r
	case float64:
		r, ok := right.(float64)
		return ok && l == r
	case string:
		r, ok := right.(string)
		return ok && l == r
	case []interface{}:
		r, ok := right.([]interface{})
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !celEqualValues(l[i], r[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func celAddValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	switch l := left.(type) {
	case int64:
		r, ok := right.(int64)
		if !ok {
			return nil, celErrAt(n.off, "operator `+` cannot add an int and a non-int")
		}
		sum, ok := celAddInt64(l, r)
		if !ok {
			return nil, celErrAt(n.off, "integer overflow: %d + %d", l, r)
		}
		return sum, nil
	case float64:
		r, ok := right.(float64)
		if !ok {
			return nil, celErrAt(n.off, "operator `+` cannot add a double and a non-double")
		}
		return l + r, nil
	case string:
		r, ok := right.(string)
		if !ok {
			return nil, celErrAt(n.off, "operator `+` cannot concatenate a string and a non-string")
		}
		return l + r, nil
	case []interface{}:
		r, ok := right.([]interface{})
		if !ok {
			return nil, celErrAt(n.off, "operator `+` cannot concatenate a list and a non-list")
		}
		out := make([]interface{}, 0, len(l)+len(r))
		out = append(out, l...)
		out = append(out, r...)
		return out, nil
	}
	return nil, celErrAt(n.off, "operator `+` applied to unsupported values")
}

func celSubtractValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	switch l := left.(type) {
	case int64:
		r, ok := right.(int64)
		if !ok {
			return nil, celErrAt(n.off, "operator `-` cannot subtract a non-int from an int")
		}
		diff, ok := celSubInt64(l, r)
		if !ok {
			return nil, celErrAt(n.off, "integer overflow: %d - %d", l, r)
		}
		return diff, nil
	case float64:
		r, ok := right.(float64)
		if !ok {
			return nil, celErrAt(n.off, "operator `-` cannot subtract a non-double from a double")
		}
		return l - r, nil
	}
	return nil, celErrAt(n.off, "operator `-` applied to non-numeric values")
}

func celMultiplyValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	switch l := left.(type) {
	case int64:
		r, ok := right.(int64)
		if !ok {
			return nil, celErrAt(n.off, "operator `*` cannot multiply an int by a non-int")
		}
		product, ok := celMulInt64(l, r)
		if !ok {
			return nil, celErrAt(n.off, "integer overflow: %d * %d", l, r)
		}
		return product, nil
	case float64:
		r, ok := right.(float64)
		if !ok {
			return nil, celErrAt(n.off, "operator `*` cannot multiply a double by a non-double")
		}
		return l * r, nil
	}
	return nil, celErrAt(n.off, "operator `*` applied to non-numeric values")
}

func celDivideValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	switch l := left.(type) {
	case int64:
		r, ok := right.(int64)
		if !ok {
			return nil, celErrAt(n.off, "operator `/` cannot divide an int by a non-int")
		}
		if r == 0 {
			return nil, celErrAt(n.off, "division by zero")
		}
		if l == math.MinInt64 && r == -1 {
			return nil, celErrAt(n.off, "integer overflow: %d / %d", l, r)
		}
		return l / r, nil
	case float64:
		r, ok := right.(float64)
		if !ok {
			return nil, celErrAt(n.off, "operator `/` cannot divide a double by a non-double")
		}
		// IEEE 754 semantics: division by zero yields an infinity or NaN.
		return l / r, nil
	}
	return nil, celErrAt(n.off, "operator `/` applied to non-numeric values")
}

func celModuloValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	l, lok := left.(int64)
	r, rok := right.(int64)
	if !lok || !rok {
		return nil, celErrAt(n.off, "operator `%%` applied to non-int values")
	}
	if r == 0 {
		return nil, celErrAt(n.off, "modulo by zero")
	}
	return l % r, nil
}

func celInValues(n *celBinaryNode, left, right interface{}) (interface{}, error) {
	list, ok := right.([]interface{})
	if !ok {
		return nil, celErrAt(n.off, "operator `in` requires a list on the right")
	}
	for _, elem := range list {
		if celEqualValues(left, elem) {
			return true, nil
		}
	}
	return false, nil
}

// addInt64 adds two int64 values and reports whether the result fits.
func celAddInt64(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, false
	}
	return a + b, true
}

// subInt64 subtracts b from a and reports whether the result fits.
func celSubInt64(a, b int64) (int64, bool) {
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return 0, false
	}
	return a - b, true
}

// mulInt64 multiplies two int64 values and reports whether the result fits.
func celMulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a == -1 {
		if b == math.MinInt64 {
			return 0, false
		}
		return -b, true
	}
	if b == -1 {
		if a == math.MinInt64 {
			return 0, false
		}
		return -a, true
	}
	product := a * b
	if product/b != a {
		return 0, false
	}
	return product, true
}

func celEvalCall(n *celCallNode, refs map[*celIdentNode]celRef, env celEnv) (interface{}, error) {
	if n.name == "coalesce" {
		for _, arg := range n.args {
			v, err := celEvalNode(arg, refs, env)
			if err != nil {
				return nil, err
			}
			if v != nil {
				return v, nil
			}
		}
		return nil, nil
	}
	args := make([]interface{}, len(n.args))
	for i, arg := range n.args {
		v, err := celEvalNode(arg, refs, env)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	switch n.name {
	case "size":
		return celCallSize(n, args[0])
	case "string":
		return celConvertToString(n, args[0])
	case "int":
		return celConvertToInt(n, args[0])
	case "double":
		return celConvertToDouble(n, args[0])
	case "bool":
		return celConvertToBool(n, args[0])
	case "abs":
		return celCallAbs(n, args[0])
	case "min":
		return celCallMinMax(n, args[0], args[1], true)
	case "max":
		return celCallMinMax(n, args[0], args[1], false)
	case "lower":
		return celCallStringFn(n, args[0], strings.ToLower)
	case "upper":
		return celCallStringFn(n, args[0], strings.ToUpper)
	case "trim":
		return celCallStringFn(n, args[0], strings.TrimSpace)
	case "contains":
		return celCallStringPredicate(n, args[0], args[1], strings.Contains)
	case "startsWith":
		return celCallStringPredicate(n, args[0], args[1], strings.HasPrefix)
	case "endsWith":
		return celCallStringPredicate(n, args[0], args[1], strings.HasSuffix)
	case "isNaN":
		if args[0] == nil {
			return nil, nil
		}
		f, ok := args[0].(float64)
		if !ok {
			return nil, celErrAt(n.off, "argument 1 of \"isNaN\" is not a double")
		}
		return math.IsNaN(f), nil
	}
	return nil, celErrAt(n.off, "unknown function %q", n.name)
}

func celCallSize(n *celCallNode, arg interface{}) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	switch v := arg.(type) {
	case string:
		return int64(utf8.RuneCountInString(v)), nil
	case []interface{}:
		return int64(len(v)), nil
	}
	return nil, celErrAt(n.off, "argument 1 of \"size\" is neither a string nor a list")
}

func celConvertToString(n *celCallNode, arg interface{}) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	switch v := arg.(type) {
	case string:
		return v, nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	}
	return nil, celErrAt(n.off, "cannot convert a value of this kind to a string")
}

func celConvertToInt(n *celCallNode, arg interface{}) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	switch v := arg.(type) {
	case int64:
		return v, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, celErrAt(n.off, "cannot convert %v to an int", v)
		}
		truncated := math.Trunc(v)
		if truncated < math.MinInt64 || truncated >= math.MaxInt64 {
			return nil, celErrAt(n.off, "double %v is out of range for an int", v)
		}
		return int64(truncated), nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil, celErrAt(n.off, "cannot convert %q to an int", v)
		}
		return parsed, nil
	case bool:
		if v {
			return int64(1), nil
		}
		return int64(0), nil
	}
	return nil, celErrAt(n.off, "cannot convert a value of this kind to an int")
}

func celConvertToDouble(n *celCallNode, arg interface{}) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	switch v := arg.(type) {
	case int64:
		return float64(v), nil
	case float64:
		return v, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, celErrAt(n.off, "cannot convert %q to a double", v)
		}
		return parsed, nil
	}
	return nil, celErrAt(n.off, "cannot convert a value of this kind to a double")
}

func celConvertToBool(n *celCallNode, arg interface{}) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	switch v := arg.(type) {
	case bool:
		return v, nil
	case string:
		switch v {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, celErrAt(n.off, "cannot convert %q to a bool (only \"true\" and \"false\" are accepted)", v)
	}
	return nil, celErrAt(n.off, "cannot convert a value of this kind to a bool")
}

func celCallAbs(n *celCallNode, arg interface{}) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	switch v := arg.(type) {
	case int64:
		if v == math.MinInt64 {
			return nil, celErrAt(n.off, "integer overflow: abs(%d)", v)
		}
		if v < 0 {
			return -v, nil
		}
		return v, nil
	case float64:
		return math.Abs(v), nil
	}
	return nil, celErrAt(n.off, "argument 1 of \"abs\" is not numeric")
}

func celCallMinMax(n *celCallNode, left, right interface{}, wantMin bool) (interface{}, error) {
	if left == nil || right == nil {
		return nil, nil
	}
	switch l := left.(type) {
	case int64:
		r, ok := right.(int64)
		if !ok {
			return nil, celErrAt(n.off, "arguments of %q have different kinds", n.name)
		}
		if (wantMin && l <= r) || (!wantMin && l >= r) {
			return l, nil
		}
		return r, nil
	case float64:
		r, ok := right.(float64)
		if !ok {
			return nil, celErrAt(n.off, "arguments of %q have different kinds", n.name)
		}
		switch {
		case math.IsNaN(l):
			return l, nil
		case math.IsNaN(r):
			return r, nil
		case wantMin && l <= r, !wantMin && l >= r:
			return l, nil
		}
		return r, nil
	}
	return nil, celErrAt(n.off, "arguments of %q are not numeric", n.name)
}

func celCallStringFn(n *celCallNode, arg interface{}, f func(string) string) (interface{}, error) {
	if arg == nil {
		return nil, nil
	}
	s, ok := arg.(string)
	if !ok {
		return nil, celErrAt(n.off, "argument 1 of %q is not a string", n.name)
	}
	return f(s), nil
}

func celCallStringPredicate(n *celCallNode, left, right interface{}, f func(string, string) bool) (interface{}, error) {
	if left == nil || right == nil {
		return nil, nil
	}
	l, lok := left.(string)
	r, rok := right.(string)
	if !lok || !rok {
		return nil, celErrAt(n.off, "arguments of %q are not strings", n.name)
	}
	return f(l, r), nil
}
