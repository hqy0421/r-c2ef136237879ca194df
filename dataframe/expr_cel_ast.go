package dataframe

// celNode is the interface implemented by every node of the expression tree.
type celNode interface {
	position() int
}

// celNullLit is the expression "null".
type celNullLit struct{ off int }

func (n *celNullLit) position() int { return n.off }

// celBoolLit is a boolean literal.
type celBoolLit struct {
	off int
	val bool
}

func (n *celBoolLit) position() int { return n.off }

// celIntLit is an integer literal.
type celIntLit struct {
	off int
	val int64
}

func (n *celIntLit) position() int { return n.off }

// celDoubleLit is a floating point literal.
type celDoubleLit struct {
	off int
	val float64
}

func (n *celDoubleLit) position() int { return n.off }

// celStringLit is a string literal.
type celStringLit struct {
	off int
	val string
}

func (n *celStringLit) position() int { return n.off }

// celListLit is a list literal such as [1, 2, 3].
type celListLit struct {
	off   int
	elems []celNode
}

func (n *celListLit) position() int { return n.off }

// celIdentNode is a reference to a variable. Bind records the celRef of every
// celIdentNode pointer in the program; the pointer identity is what allows the
// evaluator to find the value without another map lookup by name.
type celIdentNode struct {
	off  int
	name string
}

func (n *celIdentNode) position() int { return n.off }

// celSelectNode is a field selection such as a.b. It is parsed so that a
// useful error message can be produced, but it is not supported by the type
// checker.
type celSelectNode struct {
	off     int
	operand celNode
	field   string
}

func (n *celSelectNode) position() int { return n.off }

// celIndexNode is a list index such as a[0].
type celIndexNode struct {
	off     int
	operand celNode
	index   celNode
}

func (n *celIndexNode) position() int { return n.off }

// celUnaryNode is a prefix operator expression.
type celUnaryNode struct {
	off     int
	op      string
	operand celNode
}

func (n *celUnaryNode) position() int { return n.off }

// celBinaryNode is an infix operator expression.
type celBinaryNode struct {
	off         int
	op          string
	left, right celNode
}

func (n *celBinaryNode) position() int { return n.off }

// celCallNode is a function call.
type celCallNode struct {
	off  int
	name string
	args []celNode
}

func (n *celCallNode) position() int { return n.off }

// celCondNode is the ternary conditional expression test ? then : other.
type celCondNode struct {
	off         int
	test        celNode
	then, other celNode
}

func (n *celCondNode) position() int { return n.off }
