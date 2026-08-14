package knowledge

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ChunkOptions struct {
	MaxRunes     int
	OverlapRunes int
}

type TextChunk struct {
	Ordinal   int
	Content   string
	StartRune int
	EndRune   int
}

// ChunkText 按 Unicode rune 切块，并优先在段落、换行、句末和空格处截断。
func ChunkText(input string, options ChunkOptions) ([]TextChunk, error) {
	if !utf8.ValidString(input) {
		return nil, fmt.Errorf("文档不是有效的 UTF-8 文本")
	}
	if options.MaxRunes < 100 {
		return nil, fmt.Errorf("分块大小至少为 100 个字符")
	}
	if options.OverlapRunes < 0 || options.OverlapRunes >= options.MaxRunes/2 {
		return nil, fmt.Errorf("分块重叠量不能小于 0，且必须小于分块大小的一半")
	}

	normalized := strings.ReplaceAll(strings.ReplaceAll(input, "\r\n", "\n"), "\r", "\n")
	runes := []rune(normalized)
	start, ordinal := 0, 0
	chunks := make([]TextChunk, 0, (len(runes)/options.MaxRunes)+1)

	for start < len(runes) {
		end := start + options.MaxRunes
		if end >= len(runes) {
			end = len(runes)
		} else {
			end = findBoundary(runes, start+options.MaxRunes/2, end)
		}
		trimmedStart, trimmedEnd := trimRuneRange(runes, start, end)
		if trimmedStart < trimmedEnd {
			chunks = append(chunks, TextChunk{
				Ordinal: ordinal, Content: string(runes[trimmedStart:trimmedEnd]),
				StartRune: trimmedStart, EndRune: trimmedEnd,
			})
			ordinal++
		}
		if end == len(runes) {
			break
		}
		next := end - options.OverlapRunes
		if next <= start {
			next = end
		}
		start = next
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("文档中没有可索引的文本内容")
	}
	return chunks, nil
}

func findBoundary(runes []rune, minimum, maximum int) int {
	// 依次偏好段落、换行、中文/英文句末和空格。
	for _, matcher := range []func(int) bool{
		func(i int) bool { return i > 0 && runes[i-1] == '\n' && runes[i] == '\n' },
		func(i int) bool { return runes[i] == '\n' },
		func(i int) bool { return strings.ContainsRune("。！？.!?；;", runes[i-1]) },
		func(i int) bool { return unicode.IsSpace(runes[i]) },
	} {
		for i := maximum; i >= minimum; i-- {
			if i < len(runes) && matcher(i) {
				return i
			}
		}
	}
	return maximum
}

func trimRuneRange(runes []rune, start, end int) (int, int) {
	for start < end && unicode.IsSpace(runes[start]) {
		start++
	}
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	return start, end
}
