package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type generatorStub struct {
	content string
	input   []*schema.Message
}

func (g *generatorStub) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	g.input = input
	return schema.AssistantMessage(g.content, nil), nil
}

func TestModelExtractorParsesStructuredCandidates(t *testing.T) {
	t.Parallel()
	stub := &generatorStub{content: "```json\n" + `{
  "candidates": [
    {"kind":"semantic","memory_key":"Preference:Programming-Language","content":"用户偏好使用 Go。","importance":0.8,"expires_at":null},
    {"kind":"episodic","memory_key":"event:zora-v03","content":"用户开始开发 Zora V0.3。","importance":0.7,"expires_at":null}
  ]
}` + "\n```"}
	extractor, err := NewModelExtractor(stub, 1)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := extractor.Extract(context.Background(), ExtractionInput{
		UserContent: "我喜欢 Go", AssistantContent: "知道了",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].MemoryKey != "preference:programming-language" || candidates[0].Importance != 0.8 {
		t.Fatalf("unexpected candidates: %+v", candidates)
	}
	if len(stub.input) != 2 || !strings.Contains(stub.input[0].Content, "只提取用户明确表达") || !strings.Contains(stub.input[1].Content, `"user_message":"我喜欢 Go"`) {
		t.Fatalf("unexpected model input: %+v", stub.input)
	}
}

func TestRuleExtractorOnlyCapturesExplicitStableFacts(t *testing.T) {
	t.Parallel()
	extractor, err := NewRuleExtractor(3)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := extractor.Extract(context.Background(), ExtractionInput{
		UserContent: "我的主要编程语言是 Go。我以后希望你用中文回答。今天天气怎么样？",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].MemoryKey != "profile:主要编程语言" || candidates[1].MemoryKey != "interaction:response-language" {
		t.Fatalf("unexpected candidate keys: %+v", candidates)
	}

	sensitive, err := extractor.Extract(context.Background(), ExtractionInput{UserContent: "请记住：我的密码是 abc123"})
	if err != nil || len(sensitive) != 0 {
		t.Fatalf("sensitive candidates = %+v, %v", sensitive, err)
	}
	question, err := extractor.Extract(context.Background(), ExtractionInput{UserContent: "我的主要编程语言是什么？"})
	if err != nil || len(question) != 0 {
		t.Fatalf("question must not become a memory: %+v, %v", question, err)
	}
}
