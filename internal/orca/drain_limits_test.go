package orca

import (
	"context"
	"strings"
	"testing"
)

func TestDrainReportsBoundedPartialQueue(t *testing.T) {
	calls := 0
	safetyMock(t, func(k string) ([]byte, error) {
		calls++
		return []byte(`{"result":{"deliveryId":"pending","messages":[{"type":"worker_done"}]}}`), nil
	})
	result, err := Drain(context.Background(), "coord", true)
	if err == nil || !strings.Contains(err.Error(), "partial drain") {
		t.Fatalf("full queue reported success: %+v %v", result, err)
	}
	if calls != 25 || result == nil || len(result.Messages) != 25 {
		t.Fatalf("unbounded or lost results: %d %+v", calls, result)
	}
}

func TestDrainRefusesStructuredFailure(t *testing.T) {
	safetyMock(t, func(k string) ([]byte, error) {
		return []byte(`{"error":{"code":"not_owner","message":"wrong controller"}}`), nil
	})
	if _, err := Drain(context.Background(), "coord", true); err == nil {
		t.Fatal("structured failure reported empty success")
	}
}
