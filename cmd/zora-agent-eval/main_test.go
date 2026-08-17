package main

import (
	"testing"

	"github.com/zhiruo/zora/internal/domain"
)

func TestHandoffsFromEventsRequiresClosedAuditChain(t *testing.T) {
	t.Parallel()
	events := []domain.RunEvent{
		{Type: "agent_handoff_started", ToolName: "document_agent", Payload: map[string]any{"tool_call_id": "call-1"}},
		{Type: "agent_output", AgentName: "document_agent", Payload: map[string]any{"content": "证据"}},
		{Type: "agent_handoff_completed", ToolName: "document_agent", Payload: map[string]any{"tool_call_id": "call-1"}},
	}
	handoffs, err := handoffsFromEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(handoffs) != 1 || handoffs[0] != "document_agent" {
		t.Fatalf("handoffs = %v", handoffs)
	}
}

func TestHandoffsFromEventsRejectsMissingAgentOutput(t *testing.T) {
	t.Parallel()
	events := []domain.RunEvent{
		{Type: "agent_handoff_started", ToolName: "writer_agent", Payload: map[string]any{"tool_call_id": "call-1"}},
		{Type: "agent_handoff_completed", ToolName: "writer_agent", Payload: map[string]any{"tool_call_id": "call-1"}},
	}
	if _, err := handoffsFromEvents(events); err == nil {
		t.Fatal("expected missing agent output error")
	}
}
