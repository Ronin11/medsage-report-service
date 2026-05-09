package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"medsage/authkit"
	"medsage/report-service/reports"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	httpServer *http.Server
	store      *reports.Store
	pool       *pgxpool.Pool
}

// NewServer wires the HTTP routes. authMW is the Bearer-token middleware that
// every report endpoint (except /health) is mounted behind. requireAdmin is
// composed on top of authMW for endpoints that span all devices (audit log).
func NewServer(addr string, store *reports.Store, pool *pgxpool.Pool, authMW func(http.Handler) http.Handler) *Server {
	s := &Server{store: store, pool: pool}

	requireAdmin := authkit.RequireRole("admin")

	root := http.NewServeMux()

	// Health is intentionally unauthenticated.
	root.HandleFunc("GET /api/reports/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Device-scoped reports: middleware authenticates, handler scope-checks.
	scoped := http.NewServeMux()
	scoped.HandleFunc("GET /api/reports/adherence", s.handleAdherence)
	scoped.HandleFunc("GET /api/reports/adherence/csv", s.handleAdherenceCSV)
	scoped.HandleFunc("GET /api/reports/activity", s.handleActivity)
	scoped.HandleFunc("GET /api/reports/activity/csv", s.handleActivityCSV)
	scoped.HandleFunc("GET /api/reports/events/summary", s.handleEventSummary)
	scoped.HandleFunc("GET /api/reports/events/summary/csv", s.handleEventSummaryCSV)
	root.Handle("/api/reports/adherence", authMW(scoped))
	root.Handle("/api/reports/adherence/csv", authMW(scoped))
	root.Handle("/api/reports/activity", authMW(scoped))
	root.Handle("/api/reports/activity/csv", authMW(scoped))
	root.Handle("/api/reports/events/summary", authMW(scoped))
	root.Handle("/api/reports/events/summary/csv", authMW(scoped))

	// Cross-device audit log: admin only.
	audit := http.NewServeMux()
	audit.HandleFunc("GET /api/reports/audit", s.handleAudit)
	audit.HandleFunc("GET /api/reports/audit/csv", s.handleAuditCSV)
	root.Handle("/api/reports/audit", authMW(requireAdmin(audit)))
	root.Handle("/api/reports/audit/csv", authMW(requireAdmin(audit)))

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: corsMiddleware(root),
	}

	return s
}

func (s *Server) Start() error {
	slog.Info("HTTP server listening", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// --- Helpers ---

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseDeviceID(r *http.Request) (uuid.UUID, error) {
	raw := r.URL.Query().Get("device_id")
	if raw == "" {
		return uuid.Nil, fmt.Errorf("device_id query parameter is required")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid device_id: %w", err)
	}
	return id, nil
}

func parseDateRange(r *http.Request) (time.Time, time.Time, error) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("from and to query parameters are required (YYYY-MM-DD)")
	}

	from, err := time.Parse("2006-01-02", fromStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid from date: %w", err)
	}

	to, err := time.Parse("2006-01-02", toStr)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid to date: %w", err)
	}

	to = to.AddDate(0, 0, 1)

	return from, to, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// authorizeDevice verifies the authenticated caller may read the given device.
// Admins and support staff bypass the per-device check. Returns true on
// success; on failure it has already written the response.
func (s *Server) authorizeDevice(w http.ResponseWriter, r *http.Request, deviceID uuid.UUID) bool {
	user, ok := authkit.UserFromContext(r.Context())
	if !ok {
		writeError(w, "unauthenticated", http.StatusUnauthorized)
		return false
	}
	if user.IsAdmin() || user.IsSupport() {
		return true
	}
	allowed, err := authkit.CanAccessDevice(r.Context(), s.pool, user.ID, deviceID.String())
	if err != nil {
		slog.Error("Device scope check failed", "error", err, "user_id", user.ID, "device_id", deviceID)
		writeError(w, "scope check failed", http.StatusInternalServerError)
		return false
	}
	if !allowed {
		writeError(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// --- Adherence ---

func (s *Server) handleAdherence(w http.ResponseWriter, r *http.Request) {
	deviceID, err := parseDeviceID(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeDevice(w, r, deviceID) {
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	report, err := s.store.GetAdherenceReport(r.Context(), deviceID, from, to)
	if err != nil {
		slog.Error("Failed to get adherence report", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	writeJSON(w, report)
}

func (s *Server) handleAdherenceCSV(w http.ResponseWriter, r *http.Request) {
	deviceID, err := parseDeviceID(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeDevice(w, r, deviceID) {
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	report, err := s.store.GetAdherenceReport(r.Context(), deviceID, from, to)
	if err != nil {
		slog.Error("Failed to get adherence report", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=adherence_%s_%s_%s.csv", deviceID, from.Format("20060102"), to.AddDate(0, 0, -1).Format("20060102")))
	reports.WriteAdherenceCSV(w, report)
}

// --- Activity ---

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	deviceID, err := parseDeviceID(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeDevice(w, r, deviceID) {
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	report, err := s.store.GetActivityReport(r.Context(), deviceID, from, to)
	if err != nil {
		slog.Error("Failed to get activity report", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	writeJSON(w, report)
}

func (s *Server) handleActivityCSV(w http.ResponseWriter, r *http.Request) {
	deviceID, err := parseDeviceID(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeDevice(w, r, deviceID) {
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	report, err := s.store.GetActivityReport(r.Context(), deviceID, from, to)
	if err != nil {
		slog.Error("Failed to get activity report", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=activity_%s_%s_%s.csv", deviceID, from.Format("20060102"), to.AddDate(0, 0, -1).Format("20060102")))
	reports.WriteActivityCSV(w, report)
}

// --- Audit ---

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	var actorID *uuid.UUID
	if raw := r.URL.Query().Get("actor_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, "invalid actor_id", http.StatusBadRequest)
			return
		}
		actorID = &id
	}

	report, err := s.store.GetAuditReport(r.Context(), from, to, actorID, 10000)
	if err != nil {
		slog.Error("Failed to get audit report", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	writeJSON(w, report)
}

func (s *Server) handleAuditCSV(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	var actorID *uuid.UUID
	if raw := r.URL.Query().Get("actor_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, "invalid actor_id", http.StatusBadRequest)
			return
		}
		actorID = &id
	}

	report, err := s.store.GetAuditReport(r.Context(), from, to, actorID, 10000)
	if err != nil {
		slog.Error("Failed to get audit report", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=audit_%s_%s.csv", from.Format("20060102"), to.AddDate(0, 0, -1).Format("20060102")))
	reports.WriteAuditCSV(w, report)
}

// --- Event Summary ---

func (s *Server) handleEventSummary(w http.ResponseWriter, r *http.Request) {
	deviceID, err := parseDeviceID(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeDevice(w, r, deviceID) {
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	summary, err := s.store.GetEventSummary(r.Context(), deviceID, from, to)
	if err != nil {
		slog.Error("Failed to get event summary", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	writeJSON(w, summary)
}

func (s *Server) handleEventSummaryCSV(w http.ResponseWriter, r *http.Request) {
	deviceID, err := parseDeviceID(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeDevice(w, r, deviceID) {
		return
	}
	from, to, err := parseDateRange(r)
	if err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}

	summary, err := s.store.GetEventSummary(r.Context(), deviceID, from, to)
	if err != nil {
		slog.Error("Failed to get event summary", "error", err)
		writeError(w, "failed to generate report", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=events_%s_%s_%s.csv", deviceID, from.Format("20060102"), to.AddDate(0, 0, -1).Format("20060102")))
	reports.WriteEventSummaryCSV(w, summary)
}
