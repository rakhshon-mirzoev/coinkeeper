package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

type healthResponse struct {
	Message string `json:"message"`
}

func NewRouter(log *slog.Logger, expenses ExpenseService, users UserLister) http.Handler {
	eh := &expenseHandler{svc: expenses, log: log}
	uh := &userHandler{users: users, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Message: "ok"})
	})

	mux.HandleFunc("GET /expenses", eh.list)
	mux.HandleFunc("POST /expenses", eh.create)
	mux.HandleFunc("GET /expenses/{id}", eh.get)
	mux.HandleFunc("PATCH /expenses/{id}", eh.update)
	mux.HandleFunc("DELETE /expenses/{id}", eh.delete)

	mux.HandleFunc("GET /users", uh.list)

	mux.Handle("GET /", webHandler())

	return recoverPanic(log, logRequests(log, cors(mux)))
}

// statusRecorder запоминает статус ответа, чтобы записать его в лог.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// recoverPanic не даёт панике в одном хендлере уронить весь сервер.
func recoverPanic(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic in handler", "panic", v, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// cors разрешает запросы из браузера с любого origin: панель по заданию
// работает без авторизации, а фронтенд при разработке живёт на другом порту.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
