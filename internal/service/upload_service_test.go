package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

// 内存 BookUploadStore（测试用）。
type memUploadStore struct {
	mu     sync.Mutex
	tasks  map[string]*model.UploadBookTask // uuid → task
	files  map[string][]model.BookFileMeta  // uuid → 文件（含 status 由并行 map 记录）
	fileSt map[string]map[string]string     // uuid → rel_path → status
	nextID int
}

func newMemUploadStore() *memUploadStore {
	return &memUploadStore{tasks: map[string]*model.UploadBookTask{}, files: map[string][]model.BookFileMeta{}, fileSt: map[string]map[string]string{}}
}

func (m *memUploadStore) InitUpload(ctx context.Context, uuid, name, dataVersion, deviceID string, files []model.BookFileMeta) (*model.UploadBookTask, []model.BookFileMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if uuid == "" {
		m.nextID++
		uuid = "uuid-" + itoa(m.nextID)
	}
	m.tasks[uuid] = &model.UploadBookTask{UUID: uuid, Name: name, Status: "uploading", TotalFiles: len(files), DataVersion: dataVersion, DeviceID: deviceID}
	m.files[uuid] = files
	st := map[string]string{}
	m.fileSt[uuid] = st
	pending := make([]model.BookFileMeta, 0, len(files))
	for _, f := range files {
		st[f.RelPath] = "pending"
		pending = append(pending, f)
	}
	return m.tasks[uuid], pending, nil
}

func (m *memUploadStore) GetTask(ctx context.Context, uuid string) (*model.UploadBookTask, []model.BookFileMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[uuid]
	if !ok {
		return nil, nil, ErrUploadNotFound
	}
	st := m.fileSt[uuid]
	pending := []model.BookFileMeta{}
	for _, f := range m.files[uuid] {
		if st[f.RelPath] != "done" {
			pending = append(pending, f)
		}
	}
	return t, pending, nil
}

func (m *memUploadStore) ListAllFiles(ctx context.Context, uuid string) ([]model.BookFileMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files[uuid], nil
}

func (m *memUploadStore) MarkFileDone(ctx context.Context, uuid, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.fileSt[uuid]
	for _, f := range m.files[uuid] {
		if f.Hash == hash {
			st[f.RelPath] = "done"
			m.tasks[uuid].DoneFiles++
		}
	}
	return nil
}

func (m *memUploadStore) Complete(ctx context.Context, uuid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tasks[uuid]
	done := 0
	for _, s := range m.fileSt[uuid] {
		if s == "done" {
			done++
		}
	}
	if done >= t.TotalFiles && t.TotalFiles > 0 {
		t.Status = "done"
		return true, nil
	}
	return false, nil
}

func (m *memUploadStore) MarkFailed(ctx context.Context, uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[uuid]; ok {
		t.Status = "failed"
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestUploadComplete(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemorySyncStore()
	up := newMemUploadStore()
	svc := NewUploadService(up, mem)

	// init
	initResp, err := svc.InitUpload(ctx, "dev1", model.UploadInitRequest{
		Books: []model.UploadInitBook{{
			ClientID: "c1",
			Name:     "书A",
			Files: []model.BookFileMeta{
				{RelPath: "cover.jpg", Hash: "h1", Size: 10},
				{RelPath: "p1.jpg", Hash: "h2", Size: 20},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(initResp.Books) != 1 {
		t.Fatal("expected 1 book result")
	}
	book := initResp.Books[0]
	if book.UUID == "" || book.TotalFiles != 2 || len(book.PendingFiles) != 2 {
		t.Fatalf("bad init result: %+v", book)
	}

	// 只传一个文件 → complete 返回 incomplete
	if err := svc.MarkFileDone(ctx, book.UUID, "h1"); err != nil {
		t.Fatal(err)
	}
	resp1, err := svc.Complete(ctx, "dev1", book.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if resp1.Done {
		t.Fatal("should be incomplete after 1/2 files")
	}

	// 传第二个 → complete 成功并落库 current_book
	if err := svc.MarkFileDone(ctx, book.UUID, "h2"); err != nil {
		t.Fatal(err)
	}
	resp2, err := svc.Complete(ctx, "dev1", book.UUID)
	if err != nil {
		t.Fatal(err)
	}
	if !resp2.Done || resp2.Reason != "ok" {
		t.Fatalf("expected ok, got %+v", resp2)
	}

	// 验证 current_book 已落库（ForceUpsertBook）→ 事件流里有该书
	memSnapshot, err := mem.SnapshotLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var items []model.BookSnapshotItem
	json.Unmarshal(memSnapshot, &items)
	if len(items) != 1 || items[0].UUID != book.UUID {
		t.Fatalf("book not persisted: %+v", items)
	}
}

func TestUploadStatusResume(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemorySyncStore()
	up := newMemUploadStore()
	svc := NewUploadService(up, mem)

	initResp, _ := svc.InitUpload(ctx, "dev1", model.UploadInitRequest{
		Books: []model.UploadInitBook{{
			ClientID: "c1", Name: "书B",
			Files: []model.BookFileMeta{
				{RelPath: "cover.jpg", Hash: "h1"}, {RelPath: "p1.jpg", Hash: "h2"}, {RelPath: "p2.jpg", Hash: "h3"},
			},
		}},
	})
	uuid := initResp.Books[0].UUID
	// 完成 1 个 → status 返回 2 个 pending，done=1
	_ = svc.MarkFileDone(ctx, uuid, "h1")
	st, err := svc.Status(ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if st.DoneFiles != 1 || len(st.PendingFiles) != 2 {
		t.Fatalf("resume status wrong: done=%d pending=%d", st.DoneFiles, len(st.PendingFiles))
	}
}

func TestUploadInitKeepsClientUUID(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemorySyncStore()
	up := newMemUploadStore()
	svc := NewUploadService(up, mem)

	// 客户端传 uuid → 服务器保留（§6 方案1）
	resp, err := svc.InitUpload(ctx, "dev1", model.UploadInitRequest{
		Books: []model.UploadInitBook{{
			UUID: "client-uuid-1", ClientID: "c1", Name: "书C",
			Files: []model.BookFileMeta{{RelPath: "p.jpg", Hash: "h1", Size: 10}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Books[0].UUID != "client-uuid-1" {
		t.Fatalf("client uuid not preserved, got %s", resp.Books[0].UUID)
	}

	// 未传 uuid → 服务器分配
	resp2, _ := svc.InitUpload(ctx, "dev1", model.UploadInitRequest{
		Books: []model.UploadInitBook{{ClientID: "c2", Name: "书D", Files: []model.BookFileMeta{}}},
	})
	if resp2.Books[0].UUID == "" {
		t.Fatal("server should assign uuid when empty")
	}
}
