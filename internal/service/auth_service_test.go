package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

// fakeDeviceStore 内存实现，用于单测。
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

func newTestAuthService(devices store.DeviceStore) *AuthService {
	return NewAuthService(devices, store.NewMemoryTokenStore(), NewJWTService("test-jwt-secret", time.Hour), "test-connect-key", 30*24*time.Hour)
}

func TestRegisterSuccess(t *testing.T) {
	s := newTestAuthService(newFakeDeviceStore())

	resp, err := s.Register(context.Background(), RegisterRequest{
		ConnectionKey: "test-connect-key",
		DeviceID:      "dev-1",
		DeviceName:    "iPhone",
		Platform:      "ios",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.AccessToken == "" || resp.DeviceID != "dev-1" {
		t.Fatalf("bad response: %+v", resp)
	}
}

func TestRegisterWrongKey(t *testing.T) {
	s := newTestAuthService(newFakeDeviceStore())

	_, err := s.Register(context.Background(), RegisterRequest{
		ConnectionKey: "wrong-key",
		DeviceID:      "dev-1",
		DeviceName:    "iPhone",
		Platform:      "ios",
	})
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestRegisterReRegisterSameDevice(t *testing.T) {
	s := newTestAuthService(newFakeDeviceStore())
	req := RegisterRequest{
		ConnectionKey: "test-connect-key",
		DeviceID:      "dev-1",
		DeviceName:    "iPhone",
		Platform:      "ios",
	}

	if _, err := s.Register(context.Background(), req); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	// 同设备再次注册（密钥一致）→ 重新签发 token，不报错
	resp, err := s.Register(context.Background(), req)
	if err != nil {
		t.Fatalf("re-register failed: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("expected new token")
	}
}

func TestJWTIssueAndParse(t *testing.T) {
	s := NewJWTService("test-jwt-secret", time.Hour)

	token, err := s.Issue("dev-1", "iPhone")
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	claims, err := s.Parse(token)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if claims.DeviceID != "dev-1" || claims.DeviceName != "iPhone" {
		t.Fatalf("claims mismatch: %+v", claims)
	}
}

func TestJWTRejectsTampered(t *testing.T) {
	s := NewJWTService("test-jwt-secret", time.Hour)
	token, _ := s.Issue("dev-1", "iPhone")

	tampered := token[:len(token)-2] + "xx"
	if _, err := s.Parse(tampered); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestJWTRejectsWrongSecret(t *testing.T) {
	s1 := NewJWTService("secret-a", time.Hour)
	s2 := NewJWTService("secret-b", time.Hour)
	token, _ := s1.Issue("dev-1", "iPhone")

	if _, err := s2.Parse(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

// access/refresh 双 token：refresh 换新 access + 轮换；旧 refresh 失效
func TestRefreshTokenFlow(t *testing.T) {
	devices := newFakeDeviceStore()
	auth := NewAuthService(devices, store.NewMemoryTokenStore(), NewJWTService("test-jwt-secret", time.Hour), "test-connect-key", 30*24*time.Hour)

	reg, err := auth.Register(context.Background(), RegisterRequest{
		ConnectionKey: "test-connect-key",
		DeviceID:      "dev-r1",
		DeviceName:    "phone",
		Platform:      "ios",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.AccessToken == "" || reg.RefreshToken == "" {
		t.Fatal("expected access + refresh token")
	}

	// refresh：换新 access + 新 refresh
	ref, err := auth.Refresh(context.Background(), RefreshRequest{RefreshToken: reg.RefreshToken})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if ref.AccessToken == "" || ref.RefreshToken == "" || ref.RefreshToken == reg.RefreshToken {
		t.Fatalf("expected new access + rotated refresh, got %+v", ref)
	}

	// 旧 refresh 已轮换失效
	if _, err := auth.Refresh(context.Background(), RefreshRequest{RefreshToken: reg.RefreshToken}); err == nil {
		t.Fatal("old refresh token must be revoked after rotation")
	}

	// 无效 refresh
	if _, err := auth.Refresh(context.Background(), RefreshRequest{RefreshToken: "bogus"}); err == nil {
		t.Fatal("expected error for bogus refresh token")
	}
}
