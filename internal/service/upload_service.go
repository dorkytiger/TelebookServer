package service

import (
	"context"
	"encoding/json"
	"errors"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

// ErrUploadNotFound 上传任务不存在。
var ErrUploadNotFound = errors.New("upload task not found")

// ErrUploadIncomplete 上传未完成。
var ErrUploadIncomplete = errors.New("upload incomplete")

// UploadService 书籍上传编排（§2.1.3 上传状态机 + §6 uuid 服务器分配）：
// init（分配 uuid + 建任务）→ 逐文件 done → 断点续传 status → complete（整本校验后落库）。
type UploadService struct {
	uploads store.BookUploadStore
	books   store.BookStore
}

func NewUploadService(uploads store.BookUploadStore, books store.BookStore) *UploadService {
	return &UploadService{uploads: uploads, books: books}
}

// InitUpload 为客户端上报的一组书创建上传任务，返回每本书分配的 uuid + 待上传清单。
func (s *UploadService) InitUpload(ctx context.Context, deviceID string, req model.UploadInitRequest) (*model.UploadInitResponse, error) {
	resp := &model.UploadInitResponse{Books: make([]model.UploadInitBookResult, 0, len(req.Books))}
	for _, b := range req.Books {
		task, pending, err := s.uploads.InitUpload(ctx, b.UUID, b.Name, b.DataVersion, deviceID, b.Files)
		if err != nil {
			return nil, err
		}
		resp.Books = append(resp.Books, model.UploadInitBookResult{
			ClientID:     b.ClientID,
			UUID:         task.UUID,
			TotalFiles:   task.TotalFiles,
			PendingFiles: pending,
		})
	}
	return resp, nil
}

// Status 断点续传查询：返回某上传任务的进度与尚未完成的文件。
func (s *UploadService) Status(ctx context.Context, uuid string) (*model.UploadStatusResponse, error) {
	task, pending, err := s.uploads.GetTask(ctx, uuid)
	if err != nil {
		return nil, err
	}
	return &model.UploadStatusResponse{
		UUID:         task.UUID,
		Name:         task.Name,
		Status:       task.Status,
		TotalFiles:   task.TotalFiles,
		DoneFiles:    task.TotalFiles - len(pending),
		PendingFiles: pending,
	}, nil
}

// MarkFileDone 标记单个文件上传完成（分片 complete 后由客户端调用）。
func (s *UploadService) MarkFileDone(ctx context.Context, uuid, hash string) error {
	return s.uploads.MarkFileDone(ctx, uuid, hash)
}

// Complete 整本完成：校验全部文件 done → 落库 current_book + 事件 → 标记任务 done。
// 返回 done=false 表示仍有文件未传完（incomplete）。
func (s *UploadService) Complete(ctx context.Context, deviceID, uuid string) (*model.UploadCompleteResponse, error) {
	task, _, err := s.uploads.GetTask(ctx, uuid)
	if err != nil {
		return nil, err
	}
	complete, err := s.uploads.Complete(ctx, uuid)
	if err != nil {
		return nil, err
	}
	if !complete {
		return &model.UploadCompleteResponse{UUID: uuid, Done: false, Reason: "incomplete"}, nil
	}
	// 全部文件 done → 落库 current_book（用 ForceUpsertBook：服务器权威写入 + 事件）
	// 文件清单从 book_upload_file 读取（status=done）构造 payload
	payload, err := s.uploadPayload(ctx, uuid, task)
	if err != nil {
		return nil, err
	}
	outcome, err := s.books.ForceUpsertBook(ctx, uuid, payload, deviceID)
	if err != nil {
		return nil, err
	}
	return &model.UploadCompleteResponse{
		UUID:     uuid,
		Done:     true,
		Reason:   "ok",
		Revision: outcome.Revision,
	}, nil
}

// MarkFailed 上传失败：清理任务（垃圾回收）。
func (s *UploadService) MarkFailed(ctx context.Context, uuid string) error {
	return s.uploads.MarkFailed(ctx, uuid)
}

// uploadPayload 从 book_upload_file 构造 BookPayload（name + 全部文件清单）。
func (s *UploadService) uploadPayload(ctx context.Context, uuid string, task *model.UploadBookTask) (json.RawMessage, error) {
	fullFiles, err := s.uploads.ListAllFiles(ctx, uuid)
	if err != nil {
		return nil, err
	}
	payload := model.BookPayload{Name: task.Name, CurrentPage: 0, Files: fullFiles}
	return json.Marshal(payload)
}
