package chezmoi

import (
	"fmt"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// nativeNegationIntersects checks languages, not existing filenames. The suffix
// accepts only normalized path components, so an impossible child is no witness.
func nativeNegationIntersects(rule, root string) (bool, error) {
	if rule == "" || root == "" || !utf8.ValidString(rule) || !utf8.ValidString(root) {
		return false, fmt.Errorf("empty or invalid UTF-8 glob/root")
	}
	alternatives, err := expandGlobBraces(rule)
	if err != nil {
		return false, err
	}
	component := `(?:[^./\x00][^/\x00]*|\.[^./\x00][^/\x00]*|\.\.[^/\x00]+)`
	target, err := compileGlobRegexp(regexp.QuoteMeta(root) + `(?:/` + component + `)*`)
	if err != nil {
		return false, err
	}
	for _, alternative := range alternatives {
		expression, expressionErr := globRegexp(alternative)
		if expressionErr != nil {
			return false, expressionErr
		}
		program, compileErr := compileGlobRegexp(expression)
		if compileErr != nil {
			return false, compileErr
		}
		intersects, intersectionErr := regexesIntersect(program, target)
		if intersectionErr != nil || intersects {
			return intersects, intersectionErr
		}
	}
	return false, nil
}

func compileGlobRegexp(expression string) (*syntax.Prog, error) {
	parsed, err := syntax.Parse(expression, syntax.Perl)
	if err != nil {
		return nil, err
	}
	return syntax.Compile(parsed.Simplify())
}

// Expand before translating: an alternative can change whether ** occupies a
// whole path component. Escaped braces and braces inside classes stay literal.
func expandGlobBraces(pattern string) ([]string, error) {
	var result []string
	var expand func(string) error
	expand = func(current string) error {
		start, depth, part := -1, 0, 0
		var alternatives []string
		for i := 0; i < len(current); i++ {
			switch current[i] {
			case '\\':
				i++
				if i == len(current) {
					return fmt.Errorf("trailing escape in glob")
				}
			case '[':
				i++
				if i < len(current) && (current[i] == '!' || current[i] == '^') {
					i++
				}
				if i == len(current) || current[i] == ']' {
					return fmt.Errorf("empty or unterminated glob class")
				}
				for i < len(current) && current[i] != ']' {
					if current[i] == '\\' {
						i++
					}
					i++
				}
				if i >= len(current) {
					return fmt.Errorf("unterminated glob class")
				}
			case '{':
				if depth == 0 {
					start, part = i, i+1
				}
				depth++
			case ',':
				if depth == 1 {
					alternatives = append(alternatives, current[part:i])
					part = i + 1
				}
			case '}':
				depth--
				if depth < 0 {
					return fmt.Errorf("unmatched } in glob")
				}
				if depth != 0 {
					continue
				}
				alternatives = append(alternatives, current[part:i])
				for _, alternative := range alternatives {
					if err := expand(current[:start] + alternative + current[i+1:]); err != nil {
						return err
					}
				}
				return nil
			}
		}
		if depth != 0 {
			return fmt.Errorf("unmatched { in glob")
		}
		if len(result) >= 4096 {
			return fmt.Errorf("glob alternative limit exceeded; the effective check would be incomplete")
		}
		result = append(result, current)
		return nil
	}
	if err := expand(pattern); err != nil {
		return nil, err
	}
	return result, nil
}

func globRegexp(pattern string) (string, error) {
	runes := []rune(pattern)
	var expression strings.Builder
	segmentStart := true
	for i := 0; i < len(runes); i++ {
		switch char := runes[i]; char {
		case '\\':
			i++
			if i == len(runes) {
				return "", fmt.Errorf("trailing escape in glob")
			}
			expression.WriteString(regexp.QuoteMeta(string(runes[i])))
			segmentStart = runes[i] == '/'
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if segmentStart && (i+1 == len(runes) || runes[i+1] == '/') {
					if i+1 < len(runes) {
						i++
						expression.WriteString(`(?:[\s\S]*/)?`)
					} else {
						expression.WriteString(`[\s\S]*`)
					}
					continue
				}
			}
			expression.WriteString(`[^/]*`)
			segmentStart = false
		case '?':
			expression.WriteString(`[^/]`)
			segmentStart = false
		case '[':
			class, end, err := globClass(runes[i:])
			if err != nil {
				return "", err
			}
			expression.WriteString(class)
			i += end - 1
			segmentStart = false
		default:
			// Upstream lets a terminal /** or /**/ match its parent itself.
			remaining := len(runes) - i
			if char == '/' && (remaining == 3 || (remaining == 4 && runes[i+3] == '/')) &&
				runes[i+1] == '*' && runes[i+2] == '*' {
				if remaining == 4 {
					expression.WriteString(`(?:/(?:[\s\S]*/)?)?`)
				} else {
					expression.WriteString(`(?:/[\s\S]*)?`)
				}
				return expression.String(), nil
			}
			expression.WriteString(regexp.QuoteMeta(string(char)))
			segmentStart = char == '/'
		}
	}
	return expression.String(), nil
}

type globRange struct{ low, high rune }

// doublestar classes allow '/' and inverted ranges retain the low singleton.
// Build explicit intervals rather than inheriting regexp's class grammar.
func globClass(pattern []rune) (string, int, error) {
	i := 1
	negate := i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^')
	if negate {
		i++
	}
	if i == len(pattern) || pattern[i] == ']' {
		return "", 0, fmt.Errorf("empty or unterminated glob class")
	}
	var ranges []globRange
	last := rune(unicode.MaxRune)
	for i < len(pattern) && pattern[i] != ']' {
		char := pattern[i]
		i++
		if last < unicode.MaxRune && char == '-' && i < len(pattern) && pattern[i] != ']' {
			if pattern[i] == '\\' {
				i++
			}
			if i == len(pattern) {
				return "", 0, fmt.Errorf("unterminated glob class range")
			}
			if last <= pattern[i] {
				ranges = append(ranges, globRange{last, pattern[i]})
			}
			i++
			last = unicode.MaxRune
			continue
		}
		if char == '\\' {
			if i == len(pattern) {
				return "", 0, fmt.Errorf("unterminated glob class escape")
			}
			char = pattern[i]
			i++
		}
		ranges = append(ranges, globRange{char, char})
		last = char
	}
	if i == len(pattern) {
		return "", 0, fmt.Errorf("unterminated glob class")
	}
	sort.Slice(ranges, func(a, b int) bool { return ranges[a].low < ranges[b].low })
	merged := ranges[:0]
	for _, interval := range ranges {
		if len(merged) == 0 || interval.low > merged[len(merged)-1].high+1 {
			merged = append(merged, interval)
		} else if interval.high > merged[len(merged)-1].high {
			merged[len(merged)-1].high = interval.high
		}
	}
	var encoded []rune
	if negate {
		next := rune(0)
		for _, interval := range merged {
			if next < interval.low {
				encoded = append(encoded, next, interval.low-1)
			}
			next = interval.high + 1
		}
		if next <= unicode.MaxRune {
			encoded = append(encoded, next, unicode.MaxRune)
		}
	} else {
		for _, interval := range merged {
			encoded = append(encoded, interval.low, interval.high)
		}
	}
	class := &syntax.Regexp{Op: syntax.OpCharClass, Rune: encoded}
	return class.String(), i + 1, nil
}

// Reachability in the product of two Thompson NFAs requires no determinization
// or filename generation. Epsilon edges advance independently; rune edges must
// have a nonempty interval intersection.
func regexesIntersect(a, b *syntax.Prog) (bool, error) {
	type state struct{ left, right int }
	initial := state{a.Start, b.Start}
	queue := []state{initial}
	seen := map[state]bool{initial: true}
	push := func(next state) {
		if !seen[next] {
			seen[next] = true
			queue = append(queue, next)
		}
	}
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		left, right := &a.Inst[current.left], &b.Inst[current.right]
		if left.Op == syntax.InstMatch && right.Op == syntax.InstMatch {
			return true, nil
		}
		if left.Op == syntax.InstFail || right.Op == syntax.InstFail {
			continue
		}
		switch left.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			push(state{int(left.Out), current.right})
			push(state{int(left.Arg), current.right})
			continue
		case syntax.InstCapture, syntax.InstNop:
			push(state{int(left.Out), current.right})
			continue
		case syntax.InstEmptyWidth:
			return false, fmt.Errorf("unsupported assertion in glob intersection")
		}
		switch right.Op {
		case syntax.InstAlt, syntax.InstAltMatch:
			push(state{current.left, int(right.Out)})
			push(state{current.left, int(right.Arg)})
			continue
		case syntax.InstCapture, syntax.InstNop:
			push(state{current.left, int(right.Out)})
			continue
		case syntax.InstEmptyWidth:
			return false, fmt.Errorf("unsupported assertion in glob intersection")
		}
		if runeInstructionsIntersect(left, right) {
			push(state{int(left.Out), int(right.Out)})
		}
	}
	return false, nil
}

func instructionRangeCount(instruction *syntax.Inst) int {
	switch instruction.Op {
	case syntax.InstRune, syntax.InstRune1:
		if len(instruction.Rune) == 1 {
			return 1
		}
		return len(instruction.Rune) / 2
	case syntax.InstRuneAny:
		return 1
	case syntax.InstRuneAnyNotNL:
		return 2
	default:
		return 0
	}
}

func instructionRange(instruction *syntax.Inst, index int) (rune, rune) {
	switch instruction.Op {
	case syntax.InstRuneAny:
		return 0, unicode.MaxRune
	case syntax.InstRuneAnyNotNL:
		if index == 0 {
			return 0, '\n' - 1
		}
		return '\n' + 1, unicode.MaxRune
	default:
		if len(instruction.Rune) == 1 {
			return instruction.Rune[0], instruction.Rune[0]
		}
		return instruction.Rune[2*index], instruction.Rune[2*index+1]
	}
}

func runeInstructionsIntersect(a, b *syntax.Inst) bool {
	leftCount, rightCount := instructionRangeCount(a), instructionRangeCount(b)
	for left, right := 0, 0; left < leftCount && right < rightCount; {
		leftLow, leftHigh := instructionRange(a, left)
		rightLow, rightHigh := instructionRange(b, right)
		if leftLow <= rightHigh && rightLow <= leftHigh {
			return true
		}
		if leftHigh < rightLow {
			left++
		} else {
			right++
		}
	}
	return false
}
