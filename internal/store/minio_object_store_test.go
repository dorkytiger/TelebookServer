package store

import (
	"testing"
)

func TestNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"192.168.31.202:19000", "192.168.31.202"},
		{"192.168.31.202", "192.168.31.202"},
		{"  example.com:8080 ", "example.com"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeHost(c.in); got != c.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPresignClientForHost(t *testing.T) {
	s, err := NewMinioObjectStore("minio:9000", "ak", "sk", "bucket", false, "19000", "")
	if err != nil {
		t.Fatal(err)
	}
	// host 为空 → 内部 client
	if c := s.presignClientFor(""); c != s.client {
		t.Error("empty host should fall back to internal client")
	}
	// 同一 host 缓存复用
	c1 := s.presignClientFor("192.168.31.202")
	c2 := s.presignClientFor("192.168.31.202")
	if c1 != c2 {
		t.Error("same host should reuse cached client")
	}
	// 不同 host 不同 client
	c3 := s.presignClientFor("10.0.0.5")
	if c1 == c3 {
		t.Error("different hosts should create different clients")
	}
}

func TestPresignClientForExplicitEndpoint(t *testing.T) {
	s, err := NewMinioObjectStore("minio:9000", "ak", "sk", "bucket", false, "19000", "10.0.0.9:19000")
	if err != nil {
		t.Fatal(err)
	}
	// 显式配置优先：任何 host 都返回同一 presign client
	c := s.presignClientFor("whatever")
	if s.presignClient == nil || c != s.presignClient {
		t.Error("explicit endpoint should win over host inference")
	}
}
