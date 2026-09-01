package service

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTService 签发与校验设备 token（HS256）。
type JWTService struct {
	secret []byte
	ttl    time.Duration
}

// Claims 设备 token 载荷。
type Claims struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	jwt.RegisteredClaims
}

var (
	ErrInvalidToken = errors.New("invalid or expired token")
)

func NewJWTService(secret string, ttl time.Duration) *JWTService {
	return &JWTService{secret: []byte(secret), ttl: ttl}
}

// Issue 为设备签发 token。
func (s *JWTService) Issue(deviceID, deviceName string) (string, error) {
	now := time.Now()
	claims := Claims{
		DeviceID:   deviceID,
		DeviceName: deviceName,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "TelebookServer",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// Parse 校验 token 并返回载荷。
func (s *JWTService) Parse(token string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(
		token,
		claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, ErrInvalidToken
			}
			return s.secret, nil
		},
		jwt.WithIssuer("TelebookServer"),
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	if err != nil || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
