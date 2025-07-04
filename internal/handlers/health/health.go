package health_handlers

import (
	"database/sql"
	"net/http"
	"os"
	"time"

	"github.com/iamBelugaa/k8s-demo/internal/database"
	"github.com/iamBelugaa/k8s-demo/internal/metrics"
	"github.com/iamBelugaa/k8s-demo/internal/tracing"
	"github.com/iamBelugaa/k8s-demo/pkg/logger"
	"github.com/iamBelugaa/k8s-demo/pkg/response"
	"go.opentelemetry.io/otel/attribute"
)

type handler struct {
	service string
	version string
	db      *sql.DB
	log     *logger.Logger
	metrics *metrics.Metrics
}

type Config struct {
	Service string
	Version string
	DB      *sql.DB
	Log     *logger.Logger
	Metrics *metrics.Metrics
}

func New(cfg *Config) *handler {
	return &handler{
		db:      cfg.DB,
		log:     cfg.Log,
		metrics: cfg.Metrics,
		service: cfg.Service,
		version: cfg.Version,
	}
}

func (h *handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracing.StartSpan(r.Context(), h.service, "health_check")
	defer span.End()

	span.SetAttributes(
		attribute.String("operation.type", "health_check"),
		attribute.String("http.method", r.Method),
		attribute.String("http.path", r.URL.Path),
		attribute.String("http.remote_addr", r.RemoteAddr),
	)

	start := time.Now()
	h.log.WithTrace(ctx).Infow("Health check requested",
		"path", r.URL.Path,
		"user_agent", r.UserAgent(),
		"remote_addr", r.RemoteAddr,
	)

	dbStart := time.Now()
	dbCtx, dbSpan := tracing.StartSpan(ctx, h.service, "health_check_database")
	dbSpan.SetAttributes(
		attribute.String("db.operation", "ping"),
		attribute.String("db.purpose", "health_check"),
	)

	if err := database.StatusCheck(dbCtx, h.db, h.log); err != nil {
		dbDuration := time.Since(dbStart).Seconds()
		h.metrics.RecordDatabaseQuery("health_check", dbDuration)

		dbSpan.RecordError(err)
		dbSpan.SetAttributes(attribute.Bool("db.healthy", false))

		span.RecordError(err)
		span.SetAttributes(attribute.Bool("health_check.passed", false))

		dbSpan.End()

		h.log.WithTrace(ctx).Errorw("Database health check failed",
			"error", err,
			"duration_ms", dbDuration*1000,
		)

		response.RespondError(
			w,
			http.StatusInternalServerError,
			"StatusInternalServerError",
			"Database connectivity issue",
			map[string]any{
				"component":   "database",
				"timestamp":   time.Now().UTC(),
				"duration_ms": dbDuration * 1000,
			},
		)
		return
	}

	dbDuration := time.Since(dbStart).Seconds()
	h.metrics.RecordDatabaseQuery("health_check", dbDuration)
	dbSpan.SetAttributes(
		attribute.Bool("db.healthy", true),
		attribute.Float64("db.duration_seconds", dbDuration),
	)
	dbSpan.End()

	stats := h.db.Stats()
	h.metrics.DatabaseConnections.Set(float64(stats.OpenConnections))

	span.SetAttributes(
		attribute.Int("db.connections.open", stats.OpenConnections),
		attribute.Int("db.connections.idle", stats.Idle),
		attribute.Int("db.connections.in_use", stats.InUse),
		attribute.Bool("health_check.passed", true),
	)

	healthData := map[string]any{
		"uptime_check": "passed",
		"status":       "healthy",
		"service":      h.service,
		"version":      h.version,
		"timestamp":    time.Now().UTC(),
		"nodeName":     os.Getenv("NODE_NAME"),
		"pod": map[string]any{
			"ip":        os.Getenv("POD_IP"),
			"name":      os.Getenv("POD_NAME"),
			"namespace": os.Getenv("POD_NAMESPACE"),
		},
		"checks": map[string]any{
			"database": map[string]any{
				"status":      "connected",
				"duration_ms": dbDuration * 1000,
				"connections": map[string]any{
					"open":            stats.OpenConnections,
					"idle":            stats.Idle,
					"in_use":          stats.InUse,
					"max_open":        stats.MaxOpenConnections,
					"wait_count":      stats.WaitCount,
					"max_idle_closed": stats.MaxIdleClosed,
				},
			},
		},
	}

	response.RespondSuccess(w, http.StatusOK, "Service healthy", healthData)

	totalDuration := time.Since(start)
	h.log.WithTrace(ctx).Infow("Health check completed successfully",
		"total_duration_ms", totalDuration.Milliseconds(),
		"db_duration_ms", dbDuration*1000,
		"db_connections_open", stats.OpenConnections,
		"db_connections_idle", stats.Idle,
		"db_connections_in_use", stats.InUse,
	)

	span.SetAttributes(
		attribute.Float64("health_check.total_duration_seconds", totalDuration.Seconds()),
		attribute.Float64("health_check.db_duration_seconds", dbDuration),
	)
}
