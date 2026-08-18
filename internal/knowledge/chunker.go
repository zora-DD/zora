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

// ChunkText 使用递归字符分隔策略切块：依次尝试 Markdown 标题、段落、换行、句末和空格。
// 每一级找不到边界才递归降级，既保持结构又保证任何输入都有确定的硬上限。
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
	matchers := []func(int) bool{
		// 新标题应从新分块开始，避免标题和正文证据被拆散。
		func(i int) bool { return isMarkdownHeadingBoundary(runes, i) },
		func(i int) bool { return i > 0 && i < len(runes) && runes[i-1] == '\n' && runes[i] == '\n' },
		func(i int) bool { return i < len(runes) && runes[i] == '\n' },
		func(i int) bool { return i > 0 && strings.ContainsRune("。！？.!?；;", runes[i-1]) },
		func(i int) bool { return i < len(runes) && unicode.IsSpace(runes[i]) },
	}
	return findBoundaryRecursive(runes, minimum, maximum, matchers, 0)
}

func findBoundaryRecursive(runes []rune, minimum, maximum int, matchers []func(int) bool, level int) int {
	if level >= len(matchers) {
		return maximum
	}
	for index := maximum; index >= minimum; index-- {
		if matchers[level](index) {
			return index
		}
	}
	return findBoundaryRecursive(runes, minimum, maximum, matchers, level+1)
}

func isMarkdownHeadingBoundary(runes []rune, index int) bool {
	if index <= 0 || index >= len(runes) || runes[index-1] != '\n' || runes[index] != '#' {
		return false
	}
	for cursor := index; cursor < len(runes) && cursor < index+7; cursor++ {
		if runes[cursor] == ' ' {
			return cursor > index
		}
		if runes[cursor] != '#' {
			return false
		}
	}
	return false
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
