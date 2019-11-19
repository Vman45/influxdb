package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/influxdata/influxdb"
	"github.com/influxdata/influxdb/bolt"
	"github.com/influxdata/influxdb/kit/tracing"
	"github.com/julienschmidt/httprouter"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

// BackupBackend is all services and associated parameters required to construct the BackupHandler.
type BackupBackend struct {
	Logger *zap.Logger
	influxdb.HTTPErrorHandler

	BackupService   influxdb.BackupService
	KVBackupService influxdb.KVBackupService
}

// NewBackupBackend returns a new instance of BackupBackend.
func NewBackupBackend(b *APIBackend) *BackupBackend {
	return &BackupBackend{
		Logger: b.Logger.With(zap.String("handler", "backup")),

		HTTPErrorHandler: b.HTTPErrorHandler,
		BackupService:    b.BackupService,
		KVBackupService:  b.KVBackupService,
	}
}

type BackupHandler struct {
	*httprouter.Router
	influxdb.HTTPErrorHandler
	Logger *zap.Logger

	BackupService   influxdb.BackupService
	KVBackupService influxdb.KVBackupService
}

const (
	backupPath          = "/api/v2/backup"
	backupIDParamName   = "backup_id"
	backupFileParamName = "backup_file"
	backupFilePath      = backupPath + "/:" + backupIDParamName + "/file/:" + backupFileParamName

	httpClientTimeout = time.Hour
)

func composeBackupFilePath(backupID int, backupFile string) string {
	return filepath.Join(backupPath, fmt.Sprint(backupID), "file", backupFile)
}

// NewBackupHandler creates a new handler at /api/v2/backup to receive backup requests.
func NewBackupHandler(b *BackupBackend) *BackupHandler {
	h := &BackupHandler{
		HTTPErrorHandler: b.HTTPErrorHandler,
		Router:           NewRouter(b.HTTPErrorHandler),
		Logger:           b.Logger,
		BackupService:    b.BackupService,
		KVBackupService:  b.KVBackupService,
	}

	h.HandlerFunc(http.MethodPost, backupPath, h.handleCreate)
	h.HandlerFunc(http.MethodGet, backupFilePath, h.handleFetchFile)

	return h
}

type backup struct {
	ID    int      `json:"id,omitempty"`
	Files []string `json:"files,omitempty"`
}

func (h *BackupHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	span, r := tracing.ExtractFromHTTPRequest(r, "BackupHandler.handleCreate")
	defer span.Finish()

	ctx := r.Context()

	id, files, err := h.BackupService.CreateBackup(ctx)
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}

	internalBackupPath := h.BackupService.InternalBackupPath(id)
	metaFilename := filepath.Join(internalBackupPath, bolt.DefaultFilename)
	metaFile, err := os.OpenFile(metaFilename, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0660)
	if err != nil {
		err = multierr.Append(err, os.RemoveAll(internalBackupPath))
		h.HandleHTTPError(ctx, err, w)
		return
	}

	if err = h.KVBackupService.Backup(ctx, metaFile); err != nil {
		err = multierr.Append(err, os.RemoveAll(internalBackupPath))
		h.HandleHTTPError(ctx, err, w)
		return
	}
	files = append(files, bolt.DefaultFilename)

	b := backup{
		ID:    id,
		Files: files,
	}
	if err = json.NewEncoder(w).Encode(&b); err != nil {
		err = multierr.Append(err, os.RemoveAll(internalBackupPath))
		h.HandleHTTPError(ctx, err, w)
		return
	}
}

func (h *BackupHandler) handleFetchFile(w http.ResponseWriter, r *http.Request) {
	span, r := tracing.ExtractFromHTTPRequest(r, "BackupHandler.handleFetchFile")
	defer span.Finish()

	ctx := r.Context()

	params := httprouter.ParamsFromContext(ctx)
	backupID, err := strconv.Atoi(params.ByName("backup_id"))
	if err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
	backupFile := params.ByName("backup_file")

	if err = h.BackupService.FetchBackupFile(ctx, backupID, backupFile, w); err != nil {
		h.HandleHTTPError(ctx, err, w)
		return
	}
}

// BackupService is the client implementation of influxdb.BackupService.
type BackupService struct {
	Addr               string
	Token              string
	InsecureSkipVerify bool
}

func (s *BackupService) CreateBackup(ctx context.Context) (int, []string, error) {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	u, err := NewURL(s.Addr, backupPath)
	if err != nil {
		return 0, nil, err
	}

	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	SetToken(s.Token, req)
	req = req.WithContext(ctx)

	hc := NewClient(u.Scheme, s.InsecureSkipVerify)
	hc.Timeout = httpClientTimeout
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	if err := CheckError(resp); err != nil {
		return 0, nil, err
	}

	var b backup
	if err = json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return 0, nil, err
	}

	return b.ID, b.Files, nil
}

func (s *BackupService) FetchBackupFile(ctx context.Context, backupID int, backupFile string, w io.Writer) error {
	span, ctx := tracing.StartSpanFromContext(ctx)
	defer span.Finish()

	u, err := NewURL(s.Addr, composeBackupFilePath(backupID, backupFile))
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	SetToken(s.Token, req)

	hc := NewClient(u.Scheme, s.InsecureSkipVerify)
	hc.Timeout = httpClientTimeout
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := CheckError(resp); err != nil {
		return err
	}

	_, err = io.CopyBuffer(w, resp.Body, make([]byte, 1024*1024))
	if err != nil {
		return err
	}

	return nil
}

func (s *BackupService) InternalBackupPath(backupID int) string {
	panic("internal method not implemented here")
}
