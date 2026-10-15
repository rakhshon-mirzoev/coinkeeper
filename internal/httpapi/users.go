package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/user"
)

// UserLister — список членов семьи для фильтра в панели.
type UserLister interface {
	List(ctx context.Context) ([]user.User, error)
}

type userResponse struct {
	ID        int64   `json:"id"`
	FirstName string  `json:"first_name"`
	Username  *string `json:"username"`
}

type userHandler struct {
	users UserLister
	log   *slog.Logger
}

func (h *userHandler) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.users.List(r.Context())
	if err != nil {
		writeServiceError(w, h.log, err)
		return
	}

	resp := make([]userResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, userResponse{ID: u.ID, FirstName: u.FirstName, Username: u.Username})
	}
	writeJSON(w, http.StatusOK, resp)
}
