package dataframe

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type celTokenKind int

const (
	celTkEOF celTokenKind = iota
	celTkIdent
	celTkInt
	celTkDouble
	celTkString
	celTkOp
	celTkLParen
	celTkRParen
	celTkLBracket
	celTkRBracket
	celTkComma
	celTkQuestion
	celTkColon
	celTkDot
)

type celToken struct {
	kind celTokenKind
	pos  int
	text string
	ival int64
	fval float64
	sval string
}

func celTokenDescription(tok celToken) string {
	switch tok.kind {
	case celTkEOF:
		return "end of expression"
	case celTkIdent:
		return "identifier " + strconv.Quote(tok.text)
	case celTkInt:
		return "integer literal " + tok.text
	case celTkDouble:
		return "double literal " + tok.text
	case celTkString:
		return "string literal " + strconv.Quote(tok.sval)
	case celTkOp:
		return "operator " + strconv.Quote(tok.text)
	case celTkLParen:
		return "`(`"
	case celTkRParen:
		return "`)`"
	case celTkLBracket:
		return "`[`"
	case celTkRBracket:
		return "`]`"
	case celTkComma:
		return "`,`"
	case celTkQuestion:
		return "`?`"
	case celTkColon:
		return "`:`"
	case celTkDot:
		return "`.`"
	}
	return "token"
}

func celIsIdentStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func celIsIdentPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func celIsDigit(r rune) bool { return r >= '0' && r <= '9' }

// celLex converts text into a sequence of tokens. Every token carries the byte
// offset of its first character. The returned slice always ends with a
// celTkEOF token. Syntax errors are reported as *celError values.
func celLex(text string) ([]celToken, error) {
	var toks []celToken
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			i += size
		case celIsDigit(r):
			tok, next, err := celLexNumber(text, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = next
		case celIsIdentStart(r):
			start := i
			i += size
			for i < len(text) {
				r, size = utf8.DecodeRuneInString(text[i:])
				if !celIsIdentPart(r) {
					break
				}
				i += size
			}
			toks = append(toks, celToken{kind: celTkIdent, pos: start, text: text[start:i]})
		case r == '"' || r == '\'':
			sval, next, err := celLexString(text, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, celToken{kind: celTkString, pos: i, sval: sval})
			i = next
		case r == '(':
			toks = append(toks, celToken{kind: celTkLParen, pos: i})
			i += size
		case r == ')':
			toks = append(toks, celToken{kind: celTkRParen, pos: i})
			i += size
		case r == '[':
			toks = append(toks, celToken{kind: celTkLBracket, pos: i})
			i += size
		case r == ']':
			toks = append(toks, celToken{kind: celTkRBracket, pos: i})
			i += size
		case r == ',':
			toks = append(toks, celToken{kind: celTkComma, pos: i})
			i += size
		case r == '?':
			toks = append(toks, celToken{kind: celTkQuestion, pos: i})
			i += size
		case r == ':':
			toks = append(toks, celToken{kind: celTkColon, pos: i})
			i += size
		case r == '.':
			if i+1 < len(text) {
				if next, _ := utf8.DecodeRuneInString(text[i+1:]); celIsDigit(next) {
					return nil, celErrAt(i, "a double literal must start with a digit (use 0 in front of %q)", text[i:i+2])
				}
			}
			toks = append(toks, celToken{kind: celTkDot, pos: i})
			i += size
		case r == '+' || r == '-' || r == '*' || r == '/' || r == '%' ||
			r == '<' || r == '>' || r == '!' || r == '=' || r == '&' || r == '|':
			op := string(r)
			if i+size < len(text) {
				next, _ := utf8.DecodeRuneInString(text[i+size:])
				two := op + string(next)
				switch two {
				case "==", "!=", "<=", ">=", "&&", "||":
					op = two
				}
			}
			if op == "&" || op == "|" || op == "=" {
				return nil, celErrAt(i, "unexpected operator %q", op)
			}
			toks = append(toks, celToken{kind: celTkOp, pos: i, text: op})
			i += len(op)
		default:
			return nil, celErrAt(i, "unexpected character %q", string(r))
		}
	}
	toks = append(toks, celToken{kind: celTkEOF, pos: len(text)})
	return toks, nil
}

// celLexNumber scans an unsigned integer or floating point literal starting at
// i.
func celLexNumber(text string, i int) (celToken, int, error) {
	start := i
	isDouble := false
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	if i < len(text) && text[i] == '.' {
		// Only consume the dot when it is followed by a digit, so that "1."
		// is reported as an error instead of silently becoming "1".
		if i+1 >= len(text) || text[i+1] < '0' || text[i+1] > '9' {
			return celToken{}, 0, celErrAt(i, "a double literal must have digits after the decimal point")
		}
		isDouble = true
		i++
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		j := i + 1
		if j < len(text) && (text[j] == '+' || text[j] == '-') {
			j++
		}
		if j >= len(text) || text[j] < '0' || text[j] > '9' {
			return celToken{}, 0, celErrAt(i, "malformed exponent in number literal")
		}
		isDouble = true
		i = j
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
	}
	literal := text[start:i]
	if isDouble {
		f, err := strconv.ParseFloat(literal, 64)
		if err != nil {
			return celToken{}, 0, celErrAt(start, "invalid double literal %q", literal)
		}
		return celToken{kind: celTkDouble, pos: start, text: literal, fval: f}, i, nil
	}
	n, err := strconv.ParseInt(literal, 10, 64)
	if err != nil {
		return celToken{}, 0, celErrAt(start, "integer literal %q is out of range for a 64 bit integer", literal)
	}
	return celToken{kind: celTkInt, pos: start, text: literal, ival: n}, i, nil
}

// celLexString scans a string literal starting at i (which points at the
// opening quote). It supports the usual CEL escapes.
func celLexString(text string, i int) (string, int, error) {
	quote := text[i]
	start := i
	i++
	var b strings.Builder
	for i < len(text) {
		c := text[i]
		if c == quote {
			return b.String(), i + 1, nil
		}
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(text) {
			break
		}
		esc := text[i+1]
		switch esc {
		case '\\', '\'', '"':
			b.WriteByte(esc)
			i += 2
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'b':
			b.WriteByte('\b')
			i += 2
		case 'f':
			b.WriteByte('\f')
			i += 2
		default:
			return "", 0, celErrAt(i, "unknown escape sequence %q in string literal", "\\"+string(esc))
		}
	}
	return "", 0, celErrAt(start, "unterminated string literal")
}
