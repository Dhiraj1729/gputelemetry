// Package api defines typed read-only operations shared by runtime and OpenAPI generation.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"gpu-telemetry/internal/config"
	"gpu-telemetry/internal/storage/postgres"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Reader interface {
	Ready(context.Context) error
	VisitGPUs(context.Context, func(postgres.GPU) error) error
	VisitTelemetry(context.Context, string, *time.Time, *time.Time, func(postgres.Datapoint) error) error
}
type TelemetryInput struct {
	ID    string `path:"id" doc:"GPU UUID, not the host-local GPU index"`
	Start string `query:"start_time" format:"date-time" doc:"Optional inclusive lower processed_at bound; RFC3339"`
	End   string `query:"end_time" format:"date-time" doc:"Optional inclusive upper processed_at bound; RFC3339"`
}

// ArrayOutput streams the prepared JSON file. Element types generate its schema.
type ArrayOutput[T any] struct{ Body func(huma.Context) }
type HealthBody struct {
	Status string `json:"status"`
}
type HealthOutput struct{ Body HealthBody }

func New(lifetime context.Context, db Reader, c config.API, log *slog.Logger) (http.Handler, huma.API) {
	mux := http.NewServeMux()
	hc := huma.DefaultConfig("GPU Telemetry API", "1.0.0")
	hc.Info.Description = "Read-only persisted GPU telemetry. Inclusive processed_at filters, complete arrays and no pagination. Query/resource failures return 503 before a success body begins."
	// No schema-link response transformer: public arrays remain plain arrays.
	hc.CreateHooks = nil
	api := humago.New(mux, hc)
	slots := make(chan struct{}, c.MaxConcurrent)
	huma.Register(api, operation[postgres.GPU](api, "list-gpus", "/api/v1/gpus", "List all GPUs with telemetry"), func(ctx context.Context, in *struct{}) (*ArrayOutput[postgres.GPU], error) {
		return &ArrayOutput[postgres.GPU]{Body: func(hctx huma.Context) {
			raw := hctx.URL()
			if raw.RawQuery != "" {
				_ = huma.WriteErr(api, hctx, 400, "This endpoint accepts no query parameters")
				return
			}
			sendArray(api, hctx, lifetime, c, slots, log, func(ctx context.Context, emit func(postgres.GPU) error) error { return db.VisitGPUs(ctx, emit) })
		}}, nil
	})
	huma.Register(api, operation[postgres.Datapoint](api, "get-gpu-telemetry", "/api/v1/gpus/{id}/telemetry", "Read all matching telemetry ordered by processed_at and event_id"), func(ctx context.Context, in *TelemetryInput) (*ArrayOutput[postgres.Datapoint], error) {
		return &ArrayOutput[postgres.Datapoint]{Body: func(hctx huma.Context) {
			raw := hctx.URL()
			start, end, err := parseRange(raw.RawQuery)
			if err != nil {
				_ = huma.WriteErr(api, hctx, 400, err.Error())
				return
			}
			if in.ID == "" || strings.ContainsRune(in.ID, 0) {
				_ = huma.WriteErr(api, hctx, 400, "Invalid GPU UUID")
				return
			}
			sendArray(api, hctx, lifetime, c, slots, log, func(ctx context.Context, emit func(postgres.Datapoint) error) error {
				return db.VisitTelemetry(ctx, in.ID, start, end, emit)
			})
		}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "health", Method: "GET", Path: "/healthz", Summary: "Process liveness"}, func(ctx context.Context, in *struct{}) (*HealthOutput, error) {
		return &HealthOutput{Body: HealthBody{"ok"}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "ready", Method: "GET", Path: "/readyz", Summary: "PostgreSQL and schema readiness", Errors: []int{503}}, func(ctx context.Context, in *struct{}) (*HealthOutput, error) {
		check, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if lifetime.Err() != nil || db.Ready(check) != nil {
			return nil, huma.Error503ServiceUnavailable("Database unavailable or migrations missing")
		}
		return &HealthOutput{Body: HealthBody{"ready"}}, nil
	})
	// All query parsing is explicit and returns 400; Huma adds 422 by default
	// for typed parameters even when automatic validation is disabled.
	delete(api.OpenAPI().Paths["/api/v1/gpus/{id}/telemetry"].Get.Responses, "422")
	return mux, api
}
func operation[T any](api huma.API, id, path, summary string) huma.Operation {
	schema := huma.SchemaFromType(api.OpenAPI().Components.Schemas, reflect.TypeFor[[]T]())
	schema.Nullable = false
	return huma.Operation{OperationID: id, Method: "GET", Path: path, Summary: summary, DefaultStatus: 200, SkipValidateParams: true, Errors: []int{400, 404, 503}, Responses: map[string]*huma.Response{"200": {Description: "Complete ordered JSON array; empty results are []. No row limit. Requests exceeding the configured query or response disk budget fail with 503.", Content: map[string]*huma.MediaType{"application/json": {Schema: schema}}}}}
}

var rfc3339 = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func parseRange(raw string) (*time.Time, *time.Time, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("Malformed query string")
	}
	for k, v := range q {
		if k != "start_time" && k != "end_time" {
			return nil, nil, fmt.Errorf("Unknown query parameter: %s", k)
		}
		if len(v) != 1 || v[0] == "" {
			return nil, nil, fmt.Errorf("%s must appear once with an RFC3339 timestamp", k)
		}
	}
	parse := func(k string) (*time.Time, error) {
		if !q.Has(k) {
			return nil, nil
		}
		if !rfc3339.MatchString(q.Get(k)) {
			return nil, fmt.Errorf("%s must be an RFC3339 timestamp with at most 9 fractional digits", k)
		}
		v, err := time.Parse(time.RFC3339Nano, q.Get(k))
		if err != nil || v.Year() < 1 || v.Year() > 9999 {
			return nil, fmt.Errorf("%s must be an RFC3339 timestamp", k)
		}
		v = v.UTC()
		return &v, nil
	}
	start, err := parse("start_time")
	if err != nil {
		return nil, nil, err
	}
	end, err := parse("end_time")
	if err != nil {
		return nil, nil, err
	}
	if start != nil && end != nil && start.After(*end) {
		return nil, nil, fmt.Errorf("start_time must not be after end_time")
	}
	return start, end, nil
}

type limitedWriter struct {
	w    io.Writer
	left int64
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.left {
		return 0, errors.New("response disk budget exceeded")
	}
	n, err := w.w.Write(b)
	w.left -= int64(n)
	return n, err
}

// All database rows are consumed and validated before writing HTTP success.
// The temporary file bounds RAM while preserving exact 503 status semantics.
func sendArray[T any](api huma.API, hctx huma.Context, lifetime context.Context, c config.API, slots chan struct{}, log *slog.Logger, visit func(context.Context, func(T) error) error) {
	if lifetime.Err() != nil {
		_ = huma.WriteErr(api, hctx, 503, "API is shutting down")
		return
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		_ = huma.WriteErr(api, hctx, 503, "API request capacity exhausted")
		return
	}
	started := time.Now()
	defer func() {
		log.Info("API request completed", "operation", hctx.Operation().OperationID, "duration_ms", time.Since(started).Milliseconds(), "status", hctx.Status())
	}()
	f, err := os.CreateTemp(c.TempDir, "gpu-api-response-*")
	if err != nil {
		_ = huma.WriteErr(api, hctx, 503, "Response storage unavailable")
		return
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	query, cancel := context.WithTimeout(hctx.Context(), c.QueryTimeout)
	defer cancel()
	buf := bufio.NewWriterSize(f, 32<<10)
	out := &limitedWriter{buf, c.MaxResponseBytes}
	enc := json.NewEncoder(out)
	_, err = out.Write([]byte("["))
	first := true
	if err == nil {
		err = visit(query, func(v T) error {
			if err := query.Err(); err != nil {
				return err
			}
			if !first {
				if _, err := out.Write([]byte(",")); err != nil {
					return err
				}
			}
			first = false
			return enc.Encode(v)
		})
	}
	if err == nil {
		err = query.Err()
	}
	if err == nil {
		_, err = out.Write([]byte("]"))
	}
	if err == nil {
		err = buf.Flush()
	}
	if err != nil {
		if errors.Is(err, postgres.ErrGPUNotFound) {
			_ = huma.WriteErr(api, hctx, 404, "GPU not found")
		} else {
			_ = huma.WriteErr(api, hctx, 503, "Query failed or response resource budget exceeded")
		}
		return
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = huma.WriteErr(api, hctx, 503, "Response storage unavailable")
		return
	}
	hctx.SetHeader("Content-Type", "application/json")
	hctx.SetHeader("Content-Length", strconv.FormatInt(size, 10))
	hctx.SetStatus(200)
	if _, err = io.CopyBuffer(hctx.BodyWriter(), contextReader{hctx.Context(), f}, make([]byte, 32<<10)); err != nil {
		log.Warn("API response transfer interrupted", "operation", hctx.Operation().OperationID)
		panic(http.ErrAbortHandler)
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
