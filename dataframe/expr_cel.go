package dataframe

// This file contains the subset of the Common Expression Language (CEL) that is
// used to clean rows with formulas. It is self contained and depends only on
// the Go standard library, so no new module dependency is required.
//
// A formula is parsed once with celParse, bound once against a table schema
// with celExpr.Bind, and can then be evaluated many times, including
// concurrently, with celProgram.Eval.
//
// Expressions operate on nil (the CEL null value), bool, int64, float64,
// string and []interface{} (lists). There is no implicit conversion between
// value kinds: an int is never converted to a double implicitly, which keeps
// integer arithmetic exact. Null propagates through every operator except
// equality, where it behaves as expected (null == null is true and null == 1 is
// false).

import (
	"fmt"
)

// celKind identifies the CEL value kind of a celType.
type celKind int

const (
	// NullKind is the kind of the CEL null value.
	celNullKind celKind = iota
	// BoolKind is the kind of CEL bool values.
	celBoolKind
	// IntKind is the kind of CEL int values.
	celIntKind
	// DoubleKind is the kind of CEL double values.
	celDoubleKind
	// StringKind is the kind of CEL string values.
	celStringKind
	// ListKind is the kind of CEL list values.
	celListKind
)

func (k celKind) String() string {
	switch k {
	case celNullKind:
		return "null"
	case celBoolKind:
		return "bool"
	case celIntKind:
		return "int"
	case celDoubleKind:
		return "double"
	case celStringKind:
		return "string"
	case celListKind:
		return "list"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// celType is the static type of an expression. List types carry the type of
// their elements; all other types are singletons.
type celType struct {
	kind celKind
	elem *celType
}

var (
	// NullType is the type of the null literal (and of an all-null list).
	celNullType = celType{kind: celNullKind}
	// BoolType is the type of boolean expressions.
	celBoolType = celType{kind: celBoolKind}
	// IntType is the type of integer expressions.
	celIntType = celType{kind: celIntKind}
	// DoubleType is the type of floating point expressions.
	celDoubleType = celType{kind: celDoubleKind}
	// StringType is the type of string expressions.
	celStringType = celType{kind: celStringKind}
)

// celListOf returns the list type with the given element type.
func celListOf(elem celType) celType {
	return celType{kind: celListKind, elem: &elem}
}

// Kind returns the kind of t.
func (t celType) Kind() celKind { return t.kind }

// Elem returns the element type of a list. It returns NullType for non-list
// types.
func (t celType) Elem() celType {
	if t.kind == celListKind && t.elem != nil {
		return *t.elem
	}
	return celNullType
}

// String implements fmt.Stringer.
func (t celType) String() string {
	if t.kind == celListKind {
		return "list<" + t.Elem().String() + ">"
	}
	return t.kind.String()
}

// Equal reports whether t and o are the same type.
func (t celType) Equal(o celType) bool {
	if t.kind != o.kind {
		return false
	}
	if t.kind == celListKind {
		return t.Elem().Equal(o.Elem())
	}
	return true
}

func (t celType) isNumeric() bool {
	return t.kind == celIntKind || t.kind == celDoubleKind
}

// celRef is an opaque reference returned by a celResolver and passed back to a
// celEnv.
// It allows the caller to bind identifiers to indexes without the expression
// engine having to know anything about the underlying data layout.
type celRef int

// celResolver maps identifier names to a reference and its static type.
// Resolve returns false when the name is not known.
type celResolver interface {
	Resolve(name string) (celRef, celType, bool)
}

// celEnv returns the value referenced by ref. A nil value means CEL null.
type celEnv interface {
	Value(ref celRef) (interface{}, error)
}

// celError is a syntax, type or evaluation error. Offset is the byte offset in
// the expression source where the error was detected.
type celError struct {
	Offset  int
	Message string
}

// Error implements the error interface.
func (e *celError) Error() string {
	return fmt.Sprintf("%s (at offset %d)", e.Message, e.Offset)
}

func celErrAt(offset int, format string, args ...interface{}) *celError {
	return &celError{Offset: offset, Message: fmt.Sprintf(format, args...)}
}

// celExpr is a parsed, immutable expression. It can be bound any number of
// times, with different schemas, and can be used concurrently.
type celExpr struct {
	root celNode
	text string
}

// Source returns the original expression text.
func (e *celExpr) Source() string { return e.text }

// celParse parses text into an expression. Syntax errors are reported as
// *celError values.
func celParse(text string) (*celExpr, error) {
	toks, err := celLex(text)
	if err != nil {
		return nil, err
	}
	p := &celParser{tokens: toks}
	root, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if tok := p.peek(); tok.kind != celTkEOF {
		return nil, celErrAt(tok.pos, "unexpected %s", celTokenDescription(tok))
	}
	return &celExpr{root: root, text: text}, nil
}

// celProgram is an expression bound to a schema. It is immutable and safe for
// concurrent evaluation.
type celProgram struct {
	expr *celExpr
	refs map[*celIdentNode]celRef
	typ  celType
}

// Type returns the static type of the expression.
func (p *celProgram) Type() celType { return p.typ }

// Source returns the original expression text.
func (p *celProgram) Source() string { return p.expr.text }

// Bind resolves the identifiers of e using r and type-checks the expression.
// Unknown identifiers, unknown functions and type errors are reported as
// *celError values. The returned program does not share mutable state with
// other programs, so the same expression can be bound against multiple schemas,
// including concurrently.
func (e *celExpr) Bind(r celResolver) (*celProgram, error) {
	c := &celChecker{resolver: r, refs: make(map[*celIdentNode]celRef)}
	typ, err := c.check(e.root)
	if err != nil {
		return nil, err
	}
	return &celProgram{expr: e, refs: c.refs, typ: typ}, nil
}

// Eval evaluates the program against env. A nil result means CEL null.
func (p *celProgram) Eval(env celEnv) (interface{}, error) {
	return celEvalNode(p.expr.root, p.refs, env)
}
