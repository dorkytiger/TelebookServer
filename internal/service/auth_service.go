package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

// RegisterRequest 设备注册请求。
type RegisterRequest struct {
	ConnectionKey string `json:"connection_key" binding:"required"`
	DeviceID      string `json:"device_id" binding:"required"`
	DeviceName    string `json:"device_name" binding:"required"`
	Platform      string `json:"platform" binding:"required"`
}

// RegisterResponse 注册响应：access token（短期 JWT）+ refresh token（长期，可轮换）。
type RegisterResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	DeviceID     string `json:"device_id"`
}

// RefreshRequest 刷新请求。
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// RefreshResponse 刷新响应：新的 access token + 轮换后的 refresh token。
type RefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

var (
	ErrInvalidKey          = errors.New("invalid connection key")
	ErrDeviceConflict      = errors.New("device already registered with a different key")
	ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")
)

// AuthService 单用户设备认证（access/refresh 双 token）。
//
// refresh token 是不透明随机串，只存 sha256 哈希于 TokenStore（Redis），
// TTL 自动过期；轮换 = 删旧写新；设备登记仍以 devices 表为准。
type AuthService struct {
	devices    store.DeviceStore
	tokens     store.TokenStore
	jwt        *JWTService
	syncSecret string
	refreshTTL time.Duration
}

func NewAuthService(devices store.DeviceStore, tokens store.TokenStore, jwt *JWTService, syncSecret string, refreshTTL time.Duration) *AuthService {
	return &AuthService{devices: devices, tokens: tokens, jwt: jwt, syncSecret: syncSecret, refreshTTL: refreshTTL}
}

// Register 校验连接密钥并注册/复用设备，签发 access + refresh token。
func (s *AuthService) Register(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	// 常量时间比较，防时序攻击
	if subtle.ConstantTimeCompare([]byte(req.ConnectionKey), []byte(s.syncSecret)) != 1 {
		return nil, ErrInvalidKey
	}

	_, err := s.devices.GetDeviceByID(ctx, req.DeviceID)
	switch {
	case err == nil:
		// 设备已存在且密钥一致（上面已验证）：重新签发
	case errors.Is(err, store.ErrDeviceNotFound):
		hash, err := bcrypt.GenerateFromPassword([]byte(req.ConnectionKey), bcrypt.DefaultCost)
		if err != nil {
			return nil, err
		}
		device := &model.Device{
			ID:       req.DeviceID,
			Name:     req.DeviceName,
			Platform: req.Platform,
			KeyHash:  string(hash),
		}
		if err := s.devices.CreateDevice(ctx, device); err != nil {
			return nil, ErrDeviceConflict // 并发注册竞争等
		}
	default:
		return nil, err
	}

	access, err := s.jwt.Issue(req.DeviceID, req.DeviceName)
	if err != nil {
		return nil, err
	}
	refresh, refreshHash := newRefreshToken()
	if err := s.tokens.SaveRefreshToken(ctx, refreshHash, req.DeviceID, s.refreshTTL); err != nil {
		return nil, err
	}
	_ = s.devices.TouchDevice(ctx, req.DeviceID)
	return &RegisterResponse{AccessToken: access, RefreshToken: refresh, DeviceID: req.DeviceID}, nil
}

// Refresh 用 refresh token 换取新 access token，并轮换 refresh token。
func (s *AuthService) Refresh(ctx context.Context, req RefreshRequest) (*RefreshResponse, error) {
	hash := refreshTokenHash(req.RefreshToken)
	deviceID, err := s.tokens.GetDeviceByRefreshToken(ctx, hash)
	if err != nil {
		return nil, ErrInvalidRefreshToken
	}
	device, err := s.devices.GetDeviceByID(ctx, deviceID)
	if err != nil {
		// 设备被删：连带吊销 refresh token
		_ = s.tokens.DeleteRefreshToken(ctx, hash)
		return nil, ErrInvalidRefreshToken
	}

	access, err := s.jwt.Issue(device.ID, device.Name)
	if err != nil {
		return nil, err
	}
	// 轮换：删旧 refresh token，写新
	if err := s.tokens.DeleteRefreshToken(ctx, hash); err != nil {
		return nil, err
	}
	refresh, refreshHash := newRefreshToken()
	if err := s.tokens.SaveRefreshToken(ctx, refreshHash, device.ID, s.refreshTTL); err != nil {
		return nil, err
	}
	_ = s.devices.TouchDevice(ctx, device.ID)
	return &RefreshResponse{AccessToken: access, RefreshToken: refresh}, nil
}

// newRefreshToken 生成不透明 refresh token（256 位随机），返回明文与 sha256 哈希。
func newRefreshToken() (token, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		token = hex.EncodeToString([]byte(time.Now().String()))
	} else {
		token = hex.EncodeToString(b)
	}
	return token, refreshTokenHash(token)
}

// refreshTokenHash refresh token 的 sha256（存储层只存哈希，泄库不泄 token）。
func refreshTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
