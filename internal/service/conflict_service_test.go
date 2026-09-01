package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"TelebookServer/internal/model"
)

// makeConflict 制造一个冲突：设备 A 建书 rev1，设备 B 用过期 base=0 提交 → 冲突。
func makeConflict(t *testing.T, s *SyncService) model.Conflict {
	t.Helper()
	_, err := s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "服务器版"}),
	})
	if err != nil {
		t.Fatalf("push base failed: %v", err)
	}
	results, err := s.Push(context.Background(), "dev-b", "auto", []model.Change{
		change("c2", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "本地版"}),
	})
	if err != nil {
		t.Fatalf("conflict push failed: %v", err)
	}
	if results[0].Accepted || results[0].Reason != model.ReasonConflict || results[0].ConflictID == 0 {
		t.Fatalf("expected conflict result, got %+v", results[0])
	}
	conflicts, err := s.ListConflicts(context.Background())
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("expected 1 open conflict, got %d (%v)", len(conflicts), err)
	}
	return conflicts[0]
}

func TestConflictCreatedAndListed(t *testing.T) {
	s := newTestSyncService()
	c := makeConflict(t, s)
	if c.EntityID != "book-1" || c.Status != model.ConflictOpen {
		t.Fatalf("bad conflict: %+v", c)
	}
	if string(c.LocalPayload) == "" || string(c.ServerPayload) == "" {
		t.Fatalf("payloads missing: local=%s server=%s", c.LocalPayload, c.ServerPayload)
	}
}

func TestResolveKeepServer(t *testing.T) {
	s := newTestSyncService()
	c := makeConflict(t, s)

	if err := s.ResolveConflict(context.Background(), "dev-b", c.ID, model.ResolveRequest{
		Strategy: model.StrategyKeepServer,
	}); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	// 服务器版本已传播，无新事件
	pull, _ := s.Pull(context.Background(), "dev-a", 0, 100)
	if len(pull.Events) != 1 {
		t.Fatalf("expected 1 event (no resolution event for keep_server), got %d", len(pull.Events))
	}
	// 冲突已关闭
	open, _ := s.ListConflicts(context.Background())
	if len(open) != 0 {
		t.Fatalf("expected 0 open conflicts, got %d", len(open))
	}
	// 再次解决 → 已解决错误
	if err := s.ResolveConflict(context.Background(), "dev-b", c.ID, model.ResolveRequest{Strategy: model.StrategyKeepServer}); !errors.Is(err, ErrConflictAlreadyResolved) {
		t.Fatalf("expected already-resolved error, got %v", err)
	}
}

func TestResolveKeepLocal(t *testing.T) {
	s := newTestSyncService()
	c := makeConflict(t, s)

	if err := s.ResolveConflict(context.Background(), "dev-b", c.ID, model.ResolveRequest{
		Strategy: model.StrategyKeepLocal,
	}); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	// 本地快照胜出 → 产生一条新事件（解决结果）
	pull, _ := s.Pull(context.Background(), "dev-a", 0, 100)
	if len(pull.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(pull.Events))
	}
	var name map[string]string
	_ = json.Unmarshal(pull.Events[1].Payload, &name)
	if name["name"] != "本地版" {
		t.Fatalf("expected local payload to win, got %v", name)
	}
}

func TestResolveManual(t *testing.T) {
	s := newTestSyncService()
	c := makeConflict(t, s)

	payload := json.RawMessage(`{"name":"手动合并"}`)
	if err := s.ResolveConflict(context.Background(), "dev-b", c.ID, model.ResolveRequest{
		Strategy: model.StrategyManual,
		Payload:  payload,
	}); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	pull, _ := s.Pull(context.Background(), "dev-a", 0, 100)
	var name map[string]string
	_ = json.Unmarshal(pull.Events[1].Payload, &name)
	if name["name"] != "手动合并" {
		t.Fatalf("expected manual payload, got %v", name)
	}
}

func TestResolveManualWithoutPayload(t *testing.T) {
	s := newTestSyncService()
	c := makeConflict(t, s)
	err := s.ResolveConflict(context.Background(), "dev-b", c.ID, model.ResolveRequest{Strategy: model.StrategyManual})
	if err == nil {
		t.Fatal("expected error for manual without payload")
	}
}

func TestResolveInvalidStrategy(t *testing.T) {
	s := newTestSyncService()
	c := makeConflict(t, s)
	err := s.ResolveConflict(context.Background(), "dev-b", c.ID, model.ResolveRequest{Strategy: "whatever"})
	if !errors.Is(err, ErrInvalidStrategy) {
		t.Fatalf("expected ErrInvalidStrategy, got %v", err)
	}
}
