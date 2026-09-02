package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"TelebookServer/internal/model"
)

// ErrUploadNotFound 上传任务不存在。
var ErrUploadNotFound = errors.New("upload task not found")

// ErrUploadIncomplete 上传未完成。
var ErrUploadIncomplete = errors.New("upload incomplete")

// BookUploadStore 书籍上传任务存取。
type BookUploadStore interface {
	// InitUpload 为客户端上报的一本书创建任务，返回该书的待上传清单。
	// uuid 为空时服务器新生成；非空则保留（客户端本地生成的 uuid，§6 方案1）。
	// 已 done 的文件不重复；status 置 uploading。
	InitUpload(ctx context.Context, uuid, name, dataVersion, deviceID string, files []model.BookFileMeta) (*model.UploadBookTask, []model.BookFileMeta, error)
	// GetTask 查询上传任务（断点续传）。
	GetTask(ctx context.Context, uuid string) (*model.UploadBookTask, []model.BookFileMeta, error)
	// ListAllFiles 返回某任务全部文件（含 done 与 pending），用于 complete 落库时构造 payload。
	ListAllFiles(ctx context.Context, uuid string) ([]model.BookFileMeta, error)
	// MarkFileDone 标记单个文件上传完成。
	MarkFileDone(ctx context.Context, uuid, hash string) error
	// Complete 校验全部文件 done；全部完成则 status=done 并返回 true，否则 false。
	Complete(ctx context.Context, uuid string) (bool, error)
	// MarkFailed 上传失败（清理垃圾，回收任务）。
	MarkFailed(ctx context.Context, uuid string) error
}

// PGBookUploadStore PostgreSQL 实现。
type PGBookUploadStore struct {
	pool Pool
}

func NewPGBookUploadStore(pool Pool) *PGBookUploadStore {
	return &PGBookUploadStore{pool: pool}
}

func (s *PGBookUploadStore) InitUpload(ctx context.Context, uuid, name, dataVersion, deviceID string, files []model.BookFileMeta) (*model.UploadBookTask, []model.BookFileMeta, error) {
	if uuid == "" {
		uuid = newUUID() // 客户端未传 uuid 时才服务器分配（§6 方案1 的兜底）
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	total := len(files)
	// 幂等（§8.2 断点重启）：同 uuid 已存在任务（上次中断 uploading/failed/残留）
	// → 重建为全新 uploading（done_files 清零）；文件对象内容寻址，已传对象由
	// 上传侧的 file-init complete 幂等跳过，无需重复传数据。
	if _, err := tx.Exec(ctx, `
		INSERT INTO book_upload (uuid, name, status, total_files, done_files, data_version, device_id)
		VALUES ($1, $2, 'uploading', $3, 0, $4, $5)
		ON CONFLICT (uuid) DO UPDATE SET
			status = 'uploading',
			total_files = EXCLUDED.total_files,
			done_files = 0,
			name = EXCLUDED.name,
			data_version = EXCLUDED.data_version,
			device_id = EXCLUDED.device_id,
			updated_at = now()`,
		uuid, name, total, dataVersion, deviceID,
	); err != nil {
		return nil, nil, err
	}
	// 重建文件清单（丢弃上次清单/进度）
	if _, err := tx.Exec(ctx, `
		DELETE FROM book_upload_file WHERE upload_uuid = $1`, uuid); err != nil {
		return nil, nil, err
	}
	pending := make([]model.BookFileMeta, 0, total)
	for _, f := range files {
		// 文件已在 MinIO 完整存在 → 跳过（跨设备去重，§2.1.3）
		if _, err := tx.Exec(ctx, `
			INSERT INTO book_upload_file (upload_uuid, rel_path, hash, size, status)
			VALUES ($1, $2, $3, $4, 'pending')`,
			uuid, f.RelPath, f.Hash, f.Size,
		); err != nil {
			return nil, nil, err
		}
		pending = append(pending, f)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &model.UploadBookTask{
		UUID: uuid, Name: name, Status: "uploading",
		TotalFiles: total, DataVersion: dataVersion, DeviceID: deviceID,
	}, pending, nil
}

func (s *PGBookUploadStore) GetTask(ctx context.Context, uuid string) (*model.UploadBookTask, []model.BookFileMeta, error) {
	var t model.UploadBookTask
	if err := s.pool.QueryRow(ctx, `
		SELECT uuid, name, status, total_files, done_files, data_version, device_id
		FROM book_upload WHERE uuid = $1`, uuid,
	).Scan(&t.UUID, &t.Name, &t.Status, &t.TotalFiles, &t.DoneFiles, &t.DataVersion, &t.DeviceID); err != nil {
		return nil, nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT rel_path, hash, size, status FROM book_upload_file WHERE upload_uuid = $1`, uuid)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var pending []model.BookFileMeta
	for rows.Next() {
		var f model.BookFileMeta
		var status string
		if err := rows.Scan(&f.RelPath, &f.Hash, &f.Size, &status); err != nil {
			return nil, nil, err
		}
		if status != "done" {
			pending = append(pending, f)
		}
	}
	return &t, pending, rows.Err()
}

func (s *PGBookUploadStore) ListAllFiles(ctx context.Context, uuid string) ([]model.BookFileMeta, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT rel_path, hash, size FROM book_upload_file WHERE upload_uuid = $1`, uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []model.BookFileMeta
	for rows.Next() {
		var f model.BookFileMeta
		if err := rows.Scan(&f.RelPath, &f.Hash, &f.Size); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

func (s *PGBookUploadStore) MarkFileDone(ctx context.Context, uuid, hash string) error {
	// 按 hash 找到该任务里匹配的文件，标记 done
	res, err := s.pool.Exec(ctx, `
		UPDATE book_upload_file SET status = 'done'
		WHERE upload_uuid = $1 AND hash = $2 AND status != 'done'`,
		uuid, hash,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() > 0 {
		_, err = s.pool.Exec(ctx, `
			UPDATE book_upload SET done_files = (
				SELECT COUNT(*) FROM book_upload_file
				WHERE upload_uuid = $1 AND status = 'done'
			), updated_at = now() WHERE uuid = $1`, uuid)
	}
	return err
}

func (s *PGBookUploadStore) Complete(ctx context.Context, uuid string) (bool, error) {
	var total, done int
	if err := s.pool.QueryRow(ctx, `
		SELECT total_files,
		       (SELECT COUNT(*) FROM book_upload_file WHERE upload_uuid = $1 AND status = 'done')
		FROM book_upload WHERE uuid = $1`, uuid,
	).Scan(&total, &done); err != nil {
		return false, err
	}
	if done >= total && total > 0 {
		_, err := s.pool.Exec(ctx, `
			UPDATE book_upload SET status = 'done', updated_at = now() WHERE uuid = $1`, uuid)
		return err == nil, err
	}
	return false, nil
}

func (s *PGBookUploadStore) MarkFailed(ctx context.Context, uuid string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE book_upload SET status = 'failed', updated_at = now() WHERE uuid = $1`, uuid)
	return err
}

// newUUID 生成 v4 风格 uuid（无外部依赖，用 crypto/rand）。
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var _ BookUploadStore = (*PGBookUploadStore)(nil)
