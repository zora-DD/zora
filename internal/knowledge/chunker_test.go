package knowledge

import (
	"strings"
	"testing"
)

func TestChunkTextKeepsOffsetsAndOverlap(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("第一段讲解 Agent 的规划与工具调用。", 12) + "\n\n" +
		strings.Repeat("第二段讲解知识库检索与引用。", 12)
	chunks, err := ChunkText(input, ChunkOptions{MaxRunes: 120, OverlapRunes: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunk count = %d, want at least 3", len(chunks))
	}
	runes := []rune(input)
	for index, chunk := range chunks {
		if chunk.Ordinal != index {
			t.Fatalf("ordinal = %d, want %d", chunk.Ordinal, index)
		}
		if got := string(runes[chunk.StartRune:chunk.EndRune]); got != chunk.Content {
			t.Fatalf("chunk %d offsets do not reproduce content", index)
		}
		if len([]rune(chunk.Content)) > 120 {
			t.Fatalf("chunk %d exceeds size limit", index)
		}
		if index > 0 && chunk.StartRune >= chunks[index-1].EndRune {
			t.Fatalf("chunk %d does not overlap previous chunk", index)
		}
	}
}

func TestChunkTextRejectsBlankDocument(t *testing.T) {
	t.Parallel()
	if _, err := ChunkText(" \n\t ", ChunkOptions{MaxRunes: 100, OverlapRunes: 10}); err == nil {
		t.Fatal("expected blank document error")
	}
}
