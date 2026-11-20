package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
)

func TestWriteErrorDependencyStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		code  int
		retry string
	}{
		{"postgres shutdown", fmt.Errorf("query: %w", &pgconn.PgError{Code: "57P01"}), 503, "2"},
		{"postgres startup", &pgconn.PgError{Code: "57P03"}, 503, "2"},
		{"postgres connection failure", &pgconn.PgError{Code: "08006"}, 503, "2"},
		{"postgres connection capacity", &pgconn.PgError{Code: "53300"}, 503, "2"},
		{"closed postgres connection", pgconn.ErrConnClosed, 503, "2"},
		{"transport failure", &net.OpError{Op: "read", Err: errors.New("connection reset")}, 503, "2"},
		{"postgres connection dropped", io.ErrUnexpectedEOF, 503, "2"},
		{"dependency timed out", fmt.Errorf("query: %w", context.DeadlineExceeded), 503, "2"},
		{"redis unavailable", ingest.ErrUnavailable, 503, "2"},
		{"queue full", ingest.ErrQueueFull, 503, "2"},
		{"invalid device key", catalog.ErrUnauthorized, 401, ""},
		{"postgres bad credentials", &pgconn.PgError{Code: "28P01"}, 500, ""},
		{"postgres protocol violation", &pgconn.PgError{Code: "08P01"}, 500, ""},
		{"postgres schema error", &pgconn.PgError{Code: "42P01"}, 500, ""},
		{"postgres constraint violation", &pgconn.PgError{Code: "23505"}, 500, ""},
		{"application error", errors.New("invalid application state"), 500, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			writeError(c, tc.err)
			if rec.Code != tc.code || rec.Header().Get("Retry-After") != tc.retry {
				t.Fatalf("got HTTP %d, Retry-After %q; want %d, %q", rec.Code, rec.Header().Get("Retry-After"), tc.code, tc.retry)
			}
			if tc.code == 500 && rec.Body.String() != "{\"error\":\"internal server error\"}" {
				t.Fatalf("unexpected internal error body: %s", rec.Body.String())
			}
		})
	}
}
