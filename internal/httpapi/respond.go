package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/expense"
)

const maxBodyBytes = 1 << 20 // 1 МБ

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeServiceError(w http.ResponseWriter, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, expense.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, expense.ErrInvalidUser),
		errors.Is(err, expense.ErrUnknownUser),
		errors.Is(err, expense.ErrInvalidAmount),
		errors.Is(err, expense.ErrInvalidCategory),
		errors.Is(err, expense.ErrInvalidDate):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		log.Error("request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}
