package dataframe

const celMaxParseDepth = 200

type celParser struct {
	tokens []celToken
	pos    int
	depth  int
}

func (p *celParser) peek() celToken {
	if p.pos >= len(p.tokens) {
		return celToken{kind: celTkEOF, pos: p.tokens[len(p.tokens)-1].pos}
	}
	return p.tokens[p.pos]
}

func (p *celParser) next() celToken {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

func (p *celParser) accept(kind celTokenKind) (celToken, bool) {
	if tok := p.peek(); tok.kind == kind {
		p.pos++
		return tok, true
	}
	return celToken{}, false
}

func (p *celParser) expect(kind celTokenKind, what string) (celToken, error) {
	tok := p.peek()
	if tok.kind != kind {
		return celToken{}, celErrAt(tok.pos, "expected %s but found %s", what, celTokenDescription(tok))
	}
	p.pos++
	return tok, nil
}

// celParseExpression parses the whole expression grammar, starting at the lowest
// precedence rule (the conditional operator).
func (p *celParser) parseExpression() (celNode, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > celMaxParseDepth {
		return nil, celErrAt(p.peek().pos, "expression is nested too deeply")
	}
	cond, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	q, ok := p.accept(celTkQuestion)
	if !ok {
		return cond, nil
	}
	thenExpr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(celTkColon, "`:` in conditional expression"); err != nil {
		return nil, err
	}
	other, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	return &celCondNode{off: q.pos, test: cond, then: thenExpr, other: other}, nil
}

func (p *celParser) parseOr() (celNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.kind != celTkOp || tok.text != "||" {
			return left, nil
		}
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &celBinaryNode{off: tok.pos, op: tok.text, left: left, right: right}
	}
}

func (p *celParser) parseAnd() (celNode, error) {
	left, err := p.parseRelation()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.kind != celTkOp || tok.text != "&&" {
			return left, nil
		}
		p.pos++
		right, err := p.parseRelation()
		if err != nil {
			return nil, err
		}
		left = &celBinaryNode{off: tok.pos, op: tok.text, left: left, right: right}
	}
}

func celIsRelationOp(tok celToken) bool {
	if tok.kind == celTkIdent && tok.text == "in" {
		return true
	}
	if tok.kind != celTkOp {
		return false
	}
	switch tok.text {
	case "==", "!=", "<", "<=", ">", ">=":
		return true
	}
	return false
}

func (p *celParser) parseRelation() (celNode, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if !celIsRelationOp(tok) {
			return left, nil
		}
		p.pos++
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		op := tok.text
		left = &celBinaryNode{off: tok.pos, op: op, left: left, right: right}
	}
}

func (p *celParser) parseAdditive() (celNode, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.kind != celTkOp || (tok.text != "+" && tok.text != "-") {
			return left, nil
		}
		p.pos++
		right, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		left = &celBinaryNode{off: tok.pos, op: tok.text, left: left, right: right}
	}
}

func (p *celParser) parseMultiplicative() (celNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.kind != celTkOp || (tok.text != "*" && tok.text != "/" && tok.text != "%") {
			return left, nil
		}
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &celBinaryNode{off: tok.pos, op: tok.text, left: left, right: right}
	}
}

func (p *celParser) parseUnary() (celNode, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > celMaxParseDepth {
		return nil, celErrAt(p.peek().pos, "expression is nested too deeply")
	}
	tok := p.peek()
	if tok.kind == celTkOp && (tok.text == "!" || tok.text == "-") {
		p.pos++
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &celUnaryNode{off: tok.pos, op: tok.text, operand: operand}, nil
	}
	return p.parsePostfix()
}

func (p *celParser) parsePostfix() (celNode, error) {
	operand, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for {
		switch tok := p.peek(); tok.kind {
		case celTkLBracket:
			p.pos++
			index, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(celTkRBracket, "`]`"); err != nil {
				return nil, err
			}
			operand = &celIndexNode{off: tok.pos, operand: operand, index: index}
		case celTkDot:
			p.pos++
			field, err := p.expect(celTkIdent, "a field name after `.`")
			if err != nil {
				return nil, err
			}
			operand = &celSelectNode{off: tok.pos, operand: operand, field: field.text}
		default:
			return operand, nil
		}
	}
}

func (p *celParser) parsePrimary() (celNode, error) {
	tok := p.next()
	switch tok.kind {
	case celTkInt:
		return &celIntLit{off: tok.pos, val: tok.ival}, nil
	case celTkDouble:
		return &celDoubleLit{off: tok.pos, val: tok.fval}, nil
	case celTkString:
		return &celStringLit{off: tok.pos, val: tok.sval}, nil
	case celTkIdent:
		switch tok.text {
		case "true":
			return &celBoolLit{off: tok.pos, val: true}, nil
		case "false":
			return &celBoolLit{off: tok.pos, val: false}, nil
		case "null":
			return &celNullLit{off: tok.pos}, nil
		}
		if _, ok := p.accept(celTkLParen); ok {
			args, err := p.parseArgs()
			if err != nil {
				return nil, err
			}
			return &celCallNode{off: tok.pos, name: tok.text, args: args}, nil
		}
		return &celIdentNode{off: tok.pos, name: tok.text}, nil
	case celTkLParen:
		inner, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(celTkRParen, "`)`"); err != nil {
			return nil, err
		}
		return inner, nil
	case celTkLBracket:
		var elems []celNode
		for {
			if _, ok := p.accept(celTkRBracket); ok {
				break
			}
			elem, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			elems = append(elems, elem)
			if _, ok := p.accept(celTkComma); ok {
				if _, ok := p.accept(celTkRBracket); ok {
					break
				}
				continue
			}
			if _, err := p.expect(celTkRBracket, "`,` or `]` in list literal"); err != nil {
				return nil, err
			}
			break
		}
		return &celListLit{off: tok.pos, elems: elems}, nil
	}
	return nil, celErrAt(tok.pos, "unexpected %s", celTokenDescription(tok))
}

// celParseArgs parses a call argument list. The opening parenthesis has already
// been consumed.
func (p *celParser) parseArgs() ([]celNode, error) {
	var args []celNode
	if _, ok := p.accept(celTkRParen); ok {
		return args, nil
	}
	for {
		arg, err := p.parseExpression()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if _, ok := p.accept(celTkComma); ok {
			continue
		}
		if _, err := p.expect(celTkRParen, "`,` or `)` in argument list"); err != nil {
			return nil, err
		}
		return args, nil
	}
}
