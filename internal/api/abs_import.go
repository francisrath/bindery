package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/abs"
	"github.com/vavallee/bindery/internal/models"
)

type absImporterAPI interface {
	Start(ctx context.Context, cfg abs.ImportConfig) error
	Progress() abs.ImportProgress
	RecentRuns(ctx context.Context, limit int) ([]models.ABSImportRun, error)
	RollbackPreview(ctx context.Context, runID int64) (*abs.RollbackResult, error)
	Rollback(ctx context.Context, runID int64) (*abs.RollbackResult, error)
}

type ABSImportHandler struct {
	importer absImporterAPI
	loadCfg  func(context.Context) ABSStoredConfig
}

type absImportStartRequest struct {
	DryRun *bool `json:"dryRun"`
}

func NewABSImportHandler(importer absImporterAPI, loadCfg func(context.Context) ABSStoredConfig) *ABSImportHandler {
	return &ABSImportHandler{importer: importer, loadCfg: loadCfg}
}

func (h *ABSImportHandler) Start(w http.ResponseWriter, r *http.Request) {
	cfg := h.loadCfg(r.Context())
	var req absImportStartRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	runCfg := abs.ImportConfig{
		SourceID:   abs.DefaultSourceID,
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		LibraryID:  cfg.LibraryID,
		LibraryIDs: cfg.LibraryIDs,
		PathRemap:  cfg.PathRemap,
		Label:      cfg.Label,
		Enabled:    cfg.Enabled,
	}
	if req.DryRun != nil {
		runCfg.DryRun = *req.DryRun
	}
	if err := runCfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	err := h.importer.Start(context.WithoutCancel(r.Context()), runCfg)
	switch {
	case errors.Is(err, abs.ErrAlreadyRunning):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case errors.Is(err, abs.ErrShuttingDown):
		// The process is draining and refused to start new work. 503 rather
		// than 400: nothing about the request is wrong, it just arrived too
		// late. Same answer the Hardcover list sync gives (#2372).
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	case err != nil:
		// Start validates nothing itself (runCfg.Validate ran above), so what
		// is left is a fault inside the importer, not a bad request.
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.importer.Progress())
}

func (h *ABSImportHandler) Status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.importer.Progress())
}

func (h *ABSImportHandler) Runs(w http.ResponseWriter, r *http.Request) {
	runs, err := h.importer.RecentRuns(r.Context(), 10)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	resp := make([]abs.PersistedImportRun, 0, len(runs))
	for _, run := range runs {
		resp = append(resp, abs.HydrateRun(run))
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeRollbackError answers an error from a rollback or its preview. A run
// that does not exist is a 404 and a rollback the importer was built without is
// a 503; both are the importer's own sentinels. Anything else came from the
// repositories underneath and is a generic 500 rather than the storage text,
// which this handler used to relay as a 400.
func writeRollbackError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, abs.ErrRunNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "import run not found"})
	case errors.Is(err, abs.ErrRollbackUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "rollback is unavailable"})
	default:
		writeServerError(w, r, err)
	}
}

func (h *ABSImportHandler) RollbackPreview(w http.ResponseWriter, r *http.Request) {
	runID, err := absImportRunID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := h.importer.RollbackPreview(r.Context(), runID)
	if err != nil {
		writeRollbackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *ABSImportHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	runID, err := absImportRunID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := h.importer.Rollback(r.Context(), runID)
	if err != nil {
		writeRollbackError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func absImportRunID(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(chi.URLParam(r, "runID"))
	if raw == "" {
		return 0, errors.New("run id is required")
	}
	runID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || runID <= 0 {
		return 0, errors.New("invalid run id")
	}
	return runID, nil
}
