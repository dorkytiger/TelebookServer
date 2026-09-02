package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"TelebookServer/internal/model"
)

// LibraryBook 参与整库版本计算的单本书摘要。
type LibraryBook struct {
	UUID string
	Name string
	// 文件清单（每文件的 rel_path + hash），用于内容版本。
	Files []model.BookFileMeta
}

// ComputeLibraryVersion 计算整库版本 hash（§4：书名/图片/数量组合，不含进度）。
//
// 算法：对每本书的 (uuid, name, 文件数) + 每个文件的 (rel_path, hash) 做标准化，
// 全局按 uuid 排序后拼接，SHA-256 一次。任何书名/文件变化 → hash 变化；
// 进度的 current_page 不参与。
func ComputeLibraryVersion(books []LibraryBook) string {
	type fileItem struct{ rel, hash string }
	type bookItem struct {
		uuid, name string
		count      int
		files      []fileItem
	}

	items := make([]bookItem, 0, len(books))
	for _, b := range books {
		files := make([]fileItem, 0, len(b.Files))
		for _, f := range b.Files {
			files = append(files, fileItem{rel: f.RelPath, hash: f.Hash})
		}
		// 文件按 rel_path 排序，保证确定性
		sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
		items = append(items, bookItem{
			uuid:  b.UUID,
			name:  b.Name,
			count: len(files),
			files: files,
		})
	}
	// 书按 uuid 排序，保证确定性
	sort.Slice(items, func(i, j int) bool { return items[i].uuid < items[j].uuid })

	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.uuid)
		b.WriteByte('|')
		b.WriteString(it.name)
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(it.count))
		b.WriteByte('\n')
		for _, f := range it.files {
			b.WriteString(f.rel)
			b.WriteByte(':')
			b.WriteString(f.hash)
			b.WriteByte('\n')
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// LibraryVersionStore 库级元数据存取接口。
type LibraryVersionStore interface {
	// GetBookVersion 返回当前整库版本 hash（无则空串）。
	GetBookVersion(ctx context.Context) (string, error)
	// SetBookVersion 更新整库版本 hash（单行覆盖）。
	SetBookVersion(ctx context.Context, version string) error
}

// PGLibraryVersionStore PostgreSQL 实现（单行 id=1）。
type PGLibraryVersionStore struct {
	pool Pool
}

func NewPGLibraryVersionStore(pool Pool) *PGLibraryVersionStore {
	return &PGLibraryVersionStore{pool: pool}
}

func (s *PGLibraryVersionStore) GetBookVersion(ctx context.Context) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx,
		`SELECT book_version FROM library_meta WHERE id = 1`,
	).Scan(&v)
	if err != nil {
		if isNoRows(err) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

func (s *PGLibraryVersionStore) SetBookVersion(ctx context.Context, version string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO library_meta (id, book_version, updated_at)
		VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET book_version = $1, updated_at = now()`,
		version,
	)
	return err
}

// isNoRows 判断 pgx 的 ErrNoRows。
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

var _ LibraryVersionStore = (*PGLibraryVersionStore)(nil)
