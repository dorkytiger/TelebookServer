package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"TelebookServer/internal/config"
	"TelebookServer/internal/model"
	"TelebookServer/internal/service"
	"TelebookServer/internal/store"
)

// fakeDeviceStore api 包测试用的内存实现。
type fakeDeviceStore struct {
	mu      sync.Mutex
	devices map[string]*model.Device
}

func newFakeDeviceStore() *fakeDeviceStore {
	return &fakeDeviceStore{devices: map[string]*model.Device{}}
}

func (f *fakeDeviceStore) CreateDevice(_ context.Context, d *model.Device) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devices[d.ID]; ok {
		return errors.New("duplicate")
	}
	f.devices[d.ID] = d
	return nil
}

func (f *fakeDeviceStore) GetDeviceByID(_ context.Context, id string) (*model.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.devices[id]; ok {
		return d, nil
	}
	return nil, store.ErrDeviceNotFound
}

func (f *fakeDeviceStore) TouchDevice(_ context.Context, _ string) error { return nil }

func setupRouter() (*gin.Engine, *service.JWTService) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		HTTPAddr:    ":8080",
		DatabaseURL: "postgres://x",
		SyncSecret:  "test-connect-key",
		JWTSecret:   "test-jwt-secret",
		JWTTTL:      time.Hour,
		Version:     "0.1.0",
	}
	jwt := service.NewJWTService(cfg.JWTSecret, cfg.JWTTTL)
	devices := newFakeDeviceStore()
	auth := service.NewAuthService(devices, store.NewMemoryTokenStore(), jwt, cfg.SyncSecret, time.Hour)
	memSync := store.NewMemorySyncStore() // 同一个实例：事件写入与查询共享状态
	sync := service.NewSyncService(memSync, memSync, memSync, memSync, memSync, memSync)
	r := NewRouter(&Dependencies{
		Config:  cfg,
		Auth:    auth,
		JWT:     jwt,
		Devices: devices,
		Sync:    sync,
		Files:   service.NewFileService(store.NewMemoryObjectStore(), store.NewMemoryFileStore()),
	})
	return r, jwt
}

func postJSON(t *testing.T, r *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPing(t *testing.T) {
	r, _ := setupRouter()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestRegisterHandlerOK(t *testing.T) {
	r, _ := setupRouter()
	w := postJSON(t, r, "/api/v1/auth/register", map[string]string{
		"connection_key": "test-connect-key",
		"device_id":      "dev-1",
		"device_name":    "iPhone",
		"platform":       "ios",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp service.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.AccessToken == "" {
		t.Fatalf("bad response: %s", w.Body.String())
	}
}

func TestRegisterHandlerWrongKey(t *testing.T) {
	r, _ := setupRouter()
	w := postJSON(t, r, "/api/v1/auth/register", map[string]string{
		"connection_key": "bad-key",
		"device_id":      "dev-1",
		"device_name":    "iPhone",
		"platform":       "ios",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("invalid_connection_key")) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestMeRequiresAuth(t *testing.T) {
	r, _ := setupRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
}

func TestMeWithToken(t *testing.T) {
	r, _ := setupRouter()
	// 先注册拿 token
	w := postJSON(t, r, "/api/v1/auth/register", map[string]string{
		"connection_key": "test-connect-key",
		"device_id":      "dev-1",
		"device_name":    "iPhone",
		"platform":       "ios",
	})
	var reg service.RegisterResponse
	_ = json.Unmarshal(w.Body.Bytes(), &reg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/me", nil)
	req.Header.Set("Authorization", "Bearer "+reg.AccessToken)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
}

// registerAndGetToken 注册设备并返回 token。
func registerAndGetToken(t *testing.T, r *gin.Engine) string {
	t.Helper()
	w := postJSON(t, r, "/api/v1/auth/register", map[string]string{
		"connection_key": "test-connect-key",
		"device_id":      "dev-1",
		"device_name":    "iPhone",
		"platform":       "ios",
	})
	var reg service.RegisterResponse
	_ = json.Unmarshal(w.Body.Bytes(), &reg)
	return reg.AccessToken
}

func authGet(t *testing.T, r *gin.Engine, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPushHandler(t *testing.T) {
	r, _ := setupRouter()
	token := registerAndGetToken(t, r)

	w := postJSONAuth(t, r, token, "/api/v1/sync/push", map[string]any{
		"changes": []map[string]any{
			{"change_id": "c1", "entity_type": "book", "entity_id": "book-1",
				"op": "upsert", "base_revision": 0, "payload": map[string]string{"name": "书"}},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"accepted":true`)) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func postJSONAuth(t *testing.T, r *gin.Engine, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPushHandlerRequiresAuth(t *testing.T) {
	r, _ := setupRouter()
	w := postJSON(t, r, "/api/v1/sync/push", map[string]any{"changes": []any{}})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestPullHandler(t *testing.T) {
	r, _ := setupRouter()
	token := registerAndGetToken(t, r)

	// 先 push 一条
	w := postJSONAuth(t, r, token, "/api/v1/sync/push", map[string]any{
		"changes": []map[string]any{
			{"change_id": "c1", "entity_type": "book", "entity_id": "book-1",
				"op": "upsert", "base_revision": 0, "payload": map[string]string{"name": "书"}},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("push failed: %d %s", w.Code, w.Body.String())
	}

	w = authGet(t, r, token, "/api/v1/sync/pull?cursor=0")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var pull struct {
		Cursor int64 `json:"cursor"`
		Events []struct {
			EntityID string `json:"entity_id"`
			Op       string `json:"op"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &pull); err != nil {
		t.Fatalf("bad json: %v body=%s", err, w.Body.String())
	}
	if len(pull.Events) != 1 || pull.Events[0].EntityID != "book-1" || pull.Events[0].Op != "upsert" {
		t.Fatalf("unexpected pull result: %s", w.Body.String())
	}
}

func TestSyncStatusHandler(t *testing.T) {
	r, _ := setupRouter()
	token := registerAndGetToken(t, r)
	w := authGet(t, r, token, "/api/v1/sync/status")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("cursor")) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}
