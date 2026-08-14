package agenttools

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type expressionParser struct {
	input []rune
	pos   int
}

func calculate(expression string) (float64, error) {
	// 递归下降解析器只识别数字、括号和四则运算符，从根源上避免表达式注入。
	p := &expressionParser{input: []rune(expression)}
	value, err := p.parseExpression()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return 0, fmt.Errorf("第 %d 个字符 %q 不被计算器支持", p.pos+1, p.input[p.pos])
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("计算结果不是有限数值")
	}
	return value, nil
}

func (p *expressionParser) parseExpression() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if !p.accept('+') && !p.accept('-') {
			return left, nil
		}
		op := p.input[p.pos-1]
		right, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *expressionParser) parseTerm() (float64, error) {
	left, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if !p.accept('*') && !p.accept('/') {
			return left, nil
		}
		op := p.input[p.pos-1]
		right, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("不能除以 0")
			}
			left /= right
		}
	}
}

func (p *expressionParser) parseFactor() (float64, error) {
	p.skipSpace()
	if p.accept('+') {
		return p.parseFactor()
	}
	if p.accept('-') {
		value, err := p.parseFactor()
		return -value, err
	}
	if p.accept('(') {
		value, err := p.parseExpression()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if !p.accept(')') {
			return 0, fmt.Errorf("缺少右括号")
		}
		return value, nil
	}
	return p.parseNumber()
}

func (p *expressionParser) parseNumber() (float64, error) {
	p.skipSpace()
	start := p.pos
	dots := 0
	for p.pos < len(p.input) {
		r := p.input[p.pos]
		if r == '.' {
			dots++
			if dots > 1 {
				break
			}
			p.pos++
			continue
		}
		if !unicode.IsDigit(r) {
			break
		}
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("第 %d 个位置应为数字", p.pos+1)
	}
	value, err := strconv.ParseFloat(string(p.input[start:p.pos]), 64)
	if err != nil {
		return 0, fmt.Errorf("数字格式无效：%w", err)
	}
	return value, nil
}

func (p *expressionParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(p.input[p.pos]) {
		p.pos++
	}
}

func (p *expressionParser) accept(want rune) bool {
	if p.pos < len(p.input) && p.input[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func formatNumber(value float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', 10, 64), "0"), ".")
}
