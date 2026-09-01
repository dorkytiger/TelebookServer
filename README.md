
# TelebookServer

TeleBook 个人书库的多设备同步后端（Go + PostgreSQL + MinIO + Redis）。

## 功能

- **事件流同步**：push / pull + cursor 游标增量拉取，乐观锁并发控制
- **双 token 认证**：access + refresh（refresh 轮换，Redis 存储哈希），设备注册与校验
- **整库快照历史**：客户端操作后归档整库快照，支持一键恢复（整库替换）
- **图片文件同步**：SHA-256 内容寻址 + 8MB 分片上传 + 预签名下载（MinIO）
- **公网地址自动推断**：预签名 host 从请求 Host / X-Forwarded-Host 自动获取，仅需配置 MinIO 对外端口

## 快速开始（Docker Compose）

```bash
cp .env.example .env    # 修改 SYNC_SECRET / JWT_SECRET，如需手机访问配置 MINIO_PUBLIC_PORT
docker compose up --build
```

启动后：

- API: http://localhost:18080
- MinIO 控制台: http://localhost:19001
- 表结构启动时自动幂等建表（`CREATE TABLE IF NOT EXISTS`）

## 手动运行

```bash
go mod download
go build -o server ./cmd/server
DATABASE_URL=postgres://telebook:telebook@localhost:15432/telebook?sslmode=disable \
SYNC_SECRET=your-connect-key \
JWT_SECRET=your-jwt-secret \
./server
```

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `HTTP_ADDR` | 否 | `:8080` | 监听地址 |
| `DATABASE_URL` | 是 | - | PostgreSQL 连接串 |
| `SYNC_SECRET` | 是 | - | 连接密钥（设备注册用） |
| `JWT_SECRET` | 是 | - | JWT 签名密钥 |
| `REDIS_ADDR` | 否 | - | Redis 地址（refresh token 存储；空则用内存实现） |
| `MINIO_ENDPOINT` | 否 | - | MinIO 地址（不配置则文件接口返回 503） |
| `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | 否 | - | MinIO 访问凭证 |
| `MINIO_BUCKET` | 否 | `telebook` | MinIO 存储桶 |
| `MINIO_PUBLIC_PORT` | 否 | `9000` | MinIO 对外端口（host 自动从请求推断） |
| `MINIO_PUBLIC_ENDPOINT` | 否 | - | 可选：完整公网地址 host:port，显式覆盖自动推断 |

## 验收流程

```bash
# 1. 健康检查（连接测试）
curl http://localhost:18080/ping

# 2. 设备注册（密钥换双 token）
curl -X POST http://localhost:18080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"connection_key":"your-connect-key","device_id":"uuid-1","device_name":"iPhone","platform":"ios"}'
# → {"access_token":"eyJ...","refresh_token":"eyJ...","device_id":"uuid-1"}

# 3. 带 JWT 访问受保护接口
curl http://localhost:18080/api/v1/devices/me \
  -H "Authorization: Bearer <token>"

# 4. 错误场景
# 密钥错误 → 401 invalid_connection_key
# 无/坏 token → 401 unauthorized
```

## 测试

```bash
go test ./...
```

## 路由一览

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/ping` | 无 | 健康检查 / 连接测试 |
| POST | `/api/v1/auth/register` | 无 | 设备注册，密钥换双 token |
| POST | `/api/v1/auth/refresh` | 无 | refresh token 换新 access（轮换） |
| GET | `/api/v1/devices/me` | JWT | 当前设备信息 |
| POST | `/api/v1/sync/push` | JWT | 推送变更（事件流 + 乐观锁） |
| GET | `/api/v1/sync/pull` | JWT | 拉取变更（cursor 游标） |
| GET | `/api/v1/conflicts` | JWT | 未解决冲突列表 |
| POST | `/api/v1/conflicts/:id/resolve` | JWT | 解决冲突（keep_local / keep_server / manual） |
| GET | `/api/v1/books/history` | JWT | 整库快照历史列表 |
| POST | `/api/v1/books/history` | JWT | 记录整库快照 |
| POST | `/api/v1/books/restore` | JWT | 恢复到归档快照（整库替换） |
| POST | `/api/v1/files/check` | JWT | 批量 hash 比对（缺失清单） |
| POST | `/api/v1/files/upload/init` | JWT | 初始化分片上传 |
| POST | `/api/v1/files/upload` | JWT | 上传分片 |
| POST | `/api/v1/files/upload/complete` | JWT | 完成分片上传 |
| GET | `/api/v1/files/download` | JWT | 预签名下载（302 跳转） |

