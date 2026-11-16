package dataframe

// celChecker resolves identifiers and function names, and computes the static
// type of an expression. It records the celRef of every identifier so that
// evaluation does not have to resolve names again.
type celChecker struct {
	resolver celResolver
	refs     map[*celIdentNode]celRef
}

func (c *celChecker) check(n celNode) (celType, error) {
	switch n := n.(type) {
	case *celNullLit:
		return celNullType, nil
	case *celBoolLit:
		return celBoolType, nil
	case *celIntLit:
		return celIntType, nil
	case *celDoubleLit:
		return celDoubleType, nil
	case *celStringLit:
		return celStringType, nil
	case *celListLit:
		var elem celType
		haveElem := false
		for _, item := range n.elems {
			t, err := c.check(item)
			if err != nil {
				return celType{}, err
			}
			if !haveElem {
				elem, haveElem = t, true
				continue
			}
			joined, ok := celUnify(elem, t)
			if !ok {
				return celType{}, celErrAt(n.off, "list elements have incompatible types %s and %s", elem, t)
			}
			elem = joined
		}
		if !haveElem {
			return celListOf(celNullType), nil
		}
		return celListOf(elem), nil
	case *celIdentNode:
		ref, typ, ok := c.resolver.Resolve(n.name)
		if !ok {
			return celType{}, celErrAt(n.off, "unknown identifier %q (columns must exist when the formula is compiled)", n.name)
		}
		c.refs[n] = ref
		return typ, nil
	case *celSelectNode:
		return celType{}, celErrAt(n.off, "field selection is not supported; refer to a whole column by name")
	case *celIndexNode:
		operand, err := c.check(n.operand)
		if err != nil {
			return celType{}, err
		}
		if operand.kind != celListKind {
			return celType{}, celErrAt(n.off, "indexing is only supported on lists, not on %s", operand)
		}
		index, err := c.check(n.index)
		if err != nil {
			return celType{}, err
		}
		if index.kind != celIntKind {
			return celType{}, celErrAt(n.off, "list indexes must be int, not %s", index)
		}
		return operand.Elem(), nil
	case *celUnaryNode:
		operand, err := c.check(n.operand)
		if err != nil {
			return celType{}, err
		}
		switch n.op {
		case "!":
			if operand.kind != celBoolKind {
				return celType{}, celErrAt(n.off, "operator `!` requires a bool, not %s", operand)
			}
			return celBoolType, nil
		case "-":
			if !operand.isNumeric() {
				return celType{}, celErrAt(n.off, "operator `-` requires an int or a double, not %s", operand)
			}
			return operand, nil
		}
		return celType{}, celErrAt(n.off, "unknown unary operator %q", n.op)
	case *celBinaryNode:
		return c.checkBinary(n)
	case *celCallNode:
		return c.checkCall(n)
	case *celCondNode:
		test, err := c.check(n.test)
		if err != nil {
			return celType{}, err
		}
		if test.kind != celBoolKind {
			return celType{}, celErrAt(n.test.position(), "conditional test must be a bool, not %s", test)
		}
		thenType, err := c.check(n.then)
		if err != nil {
			return celType{}, err
		}
		otherType, err := c.check(n.other)
		if err != nil {
			return celType{}, err
		}
		joined, ok := celUnify(thenType, otherType)
		if !ok {
			return celType{}, celErrAt(n.off, "conditional branches have incompatible types %s and %s", thenType, otherType)
		}
		return joined, nil
	}
	return celType{}, celErrAt(n.position(), "unsupported expression")
}

func (c *celChecker) checkBinary(n *celBinaryNode) (celType, error) {
	left, err := c.check(n.left)
	if err != nil {
		return celType{}, err
	}
	right, err := c.check(n.right)
	if err != nil {
		return celType{}, err
	}
	switch n.op {
	case "&&", "||":
		if !celIsBoolOrNull(left) || !celIsBoolOrNull(right) {
			return celType{}, celErrAt(n.off, "operator %q requires bool operands, not %s and %s", n.op, left, right)
		}
		return celBoolType, nil
	case "==", "!=":
		if !celComparableKinds(left, right) {
			return celType{}, celErrAt(n.off, "operator %q cannot compare %s with %s", n.op, left, right)
		}
		return celBoolType, nil
	case "<", "<=", ">", ">=":
		if left.kind != right.kind || !(left.isNumeric() || left.kind == celStringKind) {
			return celType{}, celErrAt(n.off, "operator %q cannot compare %s with %s", n.op, left, right)
		}
		return celBoolType, nil
	case "+":
		switch {
		case left.kind == celNullKind && right.kind == celNullKind:
			return celNullType, nil
		case left.kind == celIntKind && right.kind == celIntKind:
			return celIntType, nil
		case left.kind == celDoubleKind && right.kind == celDoubleKind:
			return celDoubleType, nil
		case left.kind == celStringKind && right.kind == celStringKind:
			return celStringType, nil
		case left.kind == celListKind && right.kind == celListKind && left.Equal(right):
			return left, nil
		}
		return celType{}, celErrAt(n.off, "operator `+` cannot add %s and %s (convert one operand explicitly)", left, right)
	case "-", "*", "/":
		if left.kind != right.kind || !left.isNumeric() {
			return celType{}, celErrAt(n.off, "operator %q requires two ints or two doubles, not %s and %s (convert one operand explicitly)", n.op, left, right)
		}
		return left, nil
	case "%":
		if left.kind != celIntKind || right.kind != celIntKind {
			return celType{}, celErrAt(n.off, "operator `%%` requires two ints, not %s and %s", left, right)
		}
		return celIntType, nil
	case "in":
		if right.kind != celListKind {
			return celType{}, celErrAt(n.off, "operator `in` requires a list on the right, not %s", right)
		}
		if !celComparableKinds(left, right.Elem()) {
			return celType{}, celErrAt(n.off, "operator `in` cannot search for %s in %s", left, right)
		}
		return celBoolType, nil
	}
	return celType{}, celErrAt(n.off, "unknown operator %q", n.op)
}

func (c *celChecker) checkCall(n *celCallNode) (celType, error) {
	if n.name == "coalesce" {
		if len(n.args) == 0 {
			return celType{}, celErrAt(n.off, "coalesce requires at least one argument")
		}
		argTypes, err := c.checkArgs(n)
		if err != nil {
			return celType{}, err
		}
		result := argTypes[0]
		for _, t := range argTypes[1:] {
			joined, ok := celUnify(result, t)
			if !ok {
				return celType{}, celErrAt(n.off, "coalesce arguments have incompatible types %s and %s", result, t)
			}
			result = joined
		}
		return result, nil
	}
	argTypes, err := c.checkArgs(n)
	if err != nil {
		return celType{}, err
	}
	switch n.name {
	case "size":
		if len(argTypes) != 1 {
			return celType{}, celArityError(n, 1, len(argTypes))
		}
		switch argTypes[0].kind {
		case celStringKind, celListKind, celNullKind:
			return celIntType, nil
		}
		return celType{}, celErrAt(n.args[0].position(), "argument 1 of \"size\" must be a string or a list, not %s", argTypes[0])
	case "string", "int", "double", "bool":
		if len(argTypes) != 1 {
			return celType{}, celArityError(n, 1, len(argTypes))
		}
		if !celConversionAccepts(n.name, argTypes[0]) {
			return celType{}, celErrAt(n.args[0].position(), "cannot convert %s to %s", argTypes[0], celConversionKind(n.name))
		}
		return celType{kind: celConversionKind(n.name)}, nil
	case "abs":
		if len(argTypes) != 1 {
			return celType{}, celArityError(n, 1, len(argTypes))
		}
		if !argTypes[0].isNumeric() && argTypes[0].kind != celNullKind {
			return celType{}, celErrAt(n.args[0].position(), "argument 1 of \"abs\" must be an int or a double, not %s", argTypes[0])
		}
		return argTypes[0], nil
	case "min", "max":
		if len(argTypes) != 2 {
			return celType{}, celArityError(n, 2, len(argTypes))
		}
		if !argTypes[0].isNumeric() || !argTypes[0].Equal(argTypes[1]) {
			return celType{}, celErrAt(n.off, "%q requires two ints or two doubles of the same kind, got %s and %s", n.name, argTypes[0], argTypes[1])
		}
		return argTypes[0], nil
	case "lower", "upper", "trim":
		if len(argTypes) != 1 {
			return celType{}, celArityError(n, 1, len(argTypes))
		}
		if !celIsStringOrNull(argTypes[0]) {
			return celType{}, celErrAt(n.args[0].position(), "argument 1 of %q must be a string, not %s", n.name, argTypes[0])
		}
		return celStringType, nil
	case "contains", "startsWith", "endsWith":
		if len(argTypes) != 2 {
			return celType{}, celArityError(n, 2, len(argTypes))
		}
		for i, t := range argTypes {
			if !celIsStringOrNull(t) {
				return celType{}, celErrAt(n.args[i].position(), "argument %d of %q must be a string, not %s", i+1, n.name, t)
			}
		}
		return celBoolType, nil
	case "isNaN":
		if len(argTypes) != 1 {
			return celType{}, celArityError(n, 1, len(argTypes))
		}
		if argTypes[0].kind != celDoubleKind {
			return celType{}, celErrAt(n.args[0].position(), "argument 1 of \"isNaN\" must be a double, not %s", argTypes[0])
		}
		return celBoolType, nil
	}
	return celType{}, celErrAt(n.off, "unknown function %q", n.name)
}

func (c *celChecker) checkArgs(n *celCallNode) ([]celType, error) {
	argTypes := make([]celType, len(n.args))
	for i, arg := range n.args {
		t, err := c.check(arg)
		if err != nil {
			return nil, err
		}
		argTypes[i] = t
	}
	return argTypes, nil
}

func celArityError(n *celCallNode, want, got int) error {
	return celErrAt(n.off, "function %q expects %d argument(s), got %d", n.name, want, got)
}

func celConversionKind(name string) celKind {
	switch name {
	case "string":
		return celStringKind
	case "int":
		return celIntKind
	case "double":
		return celDoubleKind
	default:
		return celBoolKind
	}
}

func celIsBoolOrNull(t celType) bool { return t.kind == celBoolKind || t.kind == celNullKind }
func celIsStringOrNull(t celType) bool {
	return t.kind == celStringKind || t.kind == celNullKind
}

// celConversionAccepts reports whether the conversion function may be applied
// to a value of type t. Like the rest of the language no conversion is
// implicit, but an explicit conversion is allowed between the scalar kinds, so
// the declared output type of a formula can always be produced without relying
// on implicit coercion.
func celConversionAccepts(name string, t celType) bool {
	switch name {
	case "string":
		return t.kind == celStringKind || t.kind == celIntKind || t.kind == celDoubleKind || t.kind == celBoolKind || t.kind == celNullKind
	case "int":
		return t.kind == celIntKind || t.kind == celDoubleKind || t.kind == celStringKind || t.kind == celBoolKind || t.kind == celNullKind
	case "double":
		return t.kind == celIntKind || t.kind == celDoubleKind || t.kind == celStringKind || t.kind == celNullKind
	case "bool":
		return t.kind == celBoolKind || t.kind == celStringKind || t.kind == celNullKind
	}
	return false
}

// celComparableKinds reports whether values of the two types can be compared with
// == or !=. Null compares with anything. Lists compare with lists of the same
// element type.
func celComparableKinds(a, b celType) bool {
	if a.kind == celNullKind || b.kind == celNullKind {
		return true
	}
	if a.kind != b.kind {
		return false
	}
	if a.kind == celListKind {
		return celComparableKinds(a.Elem(), b.Elem())
	}
	return true
}

// celUnify returns a type that can hold values of both a and b. Null unifies with
// every type.
func celUnify(a, b celType) (celType, bool) {
	if a.kind == celNullKind {
		return b, true
	}
	if b.kind == celNullKind {
		return a, true
	}
	if a.kind != b.kind {
		return celType{}, false
	}
	if a.kind == celListKind {
		elem, ok := celUnify(a.Elem(), b.Elem())
		if !ok {
			return celType{}, false
		}
		return celListOf(elem), true
	}
	return a, true
}
