package knowledge

import (
	"strings"
	"unicode"
)

func tokenize(input string) []string {
	var tokens []string
	var word []rune
	var previousCJK rune

	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, strings.ToLower(string(word)))
			word = word[:0]
		}
	}

	for _, r := range input {
		if isCJK(r) {
			flushWord()
			tokens = append(tokens, string(r))
			if previousCJK != 0 {
				tokens = append(tokens, string([]rune{previousCJK, r}))
			}
			previousCJK = r
			continue
		}
		previousCJK = 0
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word = append(word, unicode.ToLower(r))
			continue
		}
		flushWord()
	}
	flushWord()
	return tokens
}

func termCounts(input string) (map[string]int, int) {
	tokens := tokenize(input)
	counts := make(map[string]int, len(tokens))
	for _, token := range tokens {
		counts[token]++
	}
	return counts, len(tokens)
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
