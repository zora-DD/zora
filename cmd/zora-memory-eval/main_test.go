package main

import (
	"strings"
	"testing"

	"github.com/zhiruo/zora/internal/domain"
)

func TestRecalledMemoryIDsReadsAuditEvent(t *testing.T) {
	t.Parallel()
	events := []domain.RunEvent{{
		Type: "memory_recall_completed",
		Payload: map[string]any{"matches": []any{
			map[string]any{"memory_id": "mem_1", "score": 0.9},
			map[string]any{"memory_id": "mem_2", "score": 0.8},
		}},
	}}
	ids, err := recalledMemoryIDs(events, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "mem_1" || ids[1] != "mem_2" {
		t.Fatalf("recalled ids = %+v", ids)
	}
}

func TestRecalledMemoryIDsRequiresTreatmentAudit(t *testing.T) {
	t.Parallel()
	if _, err := recalledMemoryIDs(nil, true); err == nil || !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("error = %v", err)
	}
	ids, err := recalledMemoryIDs(nil, false)
	if err != nil || len(ids) != 0 {
		t.Fatalf("control ids = %+v, %v", ids, err)
	}
}
