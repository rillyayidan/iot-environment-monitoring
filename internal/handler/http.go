package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rillyayidan/iot-environment-monitoring/internal/catalog"
	"github.com/rillyayidan/iot-environment-monitoring/internal/database"
	"github.com/rillyayidan/iot-environment-monitoring/internal/ingest"
)

const timeout = 5 * time.Second

func Admin(token string) gin.HandlerFunc {
	expected := sha256.Sum256([]byte(token))
	return func(c *gin.Context) {
		header := c.Request.Header.Values("Authorization")
		if len(header) == 1 && token != "" {
			scheme, supplied, ok := strings.Cut(header[0], " ")
			actual := sha256.Sum256([]byte(supplied))
			if ok && strings.EqualFold(scheme, "Bearer") && subtle.ConstantTimeCompare(actual[:], expected[:]) == 1 {
				c.Next()
				return
			}
		}
		c.Header("WWW-Authenticate", "Bearer")
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid administrative credentials"})
	}
}

type authenticatedDevice struct{}

func DeviceAuth(service *catalog.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		headers := c.Request.Header.Values("X-API-Key")
		if len(headers) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": catalog.ErrUnauthorized.Error()})
			return
		}
		ctx, cancel := requestContext(c)
		id, err := service.Authenticate(ctx, headers[0])
		cancel()
		if err != nil {
			writeError(c, err)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), authenticatedDevice{}, id))
		c.Next()
	}
}

func deviceIdentity(c *gin.Context) uuid.UUID {
	id, _ := c.Request.Context().Value(authenticatedDevice{}).(uuid.UUID)
	return id
}

func requestContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), timeout)
}

func decode(c *gin.Context, target any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: malformed JSON body", catalog.ErrInvalid)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: expected one JSON object", catalog.ErrInvalid)
	}
	return nil
}

func parseID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if len(value) != 36 || err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: expected a non-zero UUID", catalog.ErrInvalid)
	}
	return id, nil
}

func pagination(c *gin.Context) (int, int, error) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 200 {
		return 0, 0, fmt.Errorf("%w: limit must be between 1 and 200", catalog.ErrInvalid)
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 1000000 {
		return 0, 0, fmt.Errorf("%w: offset must be between 0 and 1000000", catalog.ErrInvalid)
	}
	return limit, offset, nil
}

func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, catalog.ErrInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, catalog.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "resource not found"})
	case errors.Is(err, catalog.ErrUnauthorized):
		c.JSON(http.StatusUnauthorized, gin.H{"error": catalog.ErrUnauthorized.Error()})
	case errors.Is(err, ingest.ErrOwnership):
		c.JSON(http.StatusForbidden, gin.H{"error": ingest.ErrOwnership.Error()})
	case errors.Is(err, ingest.ErrQueueFull), errors.Is(err, ingest.ErrUnavailable):
		serviceUnavailable(c, gin.H{"error": "ingestion temporarily unavailable"})
	case errors.Is(err, context.DeadlineExceeded):
		serviceUnavailable(c, gin.H{"error": "dependency timeout"})
	case database.IsPostgresUnavailable(err):
		serviceUnavailable(c, gin.H{"error": "database temporarily unavailable"})
	default:
		slog.Error("request failed", "route", c.FullPath(), "error_type", fmt.Sprintf("%T", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}

func serviceUnavailable(c *gin.Context, response gin.H) {
	c.Header("Retry-After", "2")
	c.JSON(http.StatusServiceUnavailable, response)
}

func jsonResult[T any](status int, call func(context.Context, *gin.Context) (T, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := requestContext(c)
		defer cancel()
		result, err := call(ctx, c)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(status, result)
	}
}

func byID[T any](status int, call func(context.Context, uuid.UUID) (T, error)) gin.HandlerFunc {
	return jsonResult(status, func(ctx context.Context, c *gin.Context) (T, error) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			var zero T
			return zero, err
		}
		return call(ctx, id)
	})
}

func withBody[I, T any](status int, call func(context.Context, I) (T, error)) gin.HandlerFunc {
	return jsonResult(status, func(ctx context.Context, c *gin.Context) (T, error) {
		var input I
		if err := decode(c, &input); err != nil {
			var zero T
			return zero, err
		}
		return call(ctx, input)
	})
}

func withIDBody[I, T any](status int, call func(context.Context, uuid.UUID, I) (T, error)) gin.HandlerFunc {
	return jsonResult(status, func(ctx context.Context, c *gin.Context) (T, error) {
		var zero T
		id, err := parseID(c.Param("id"))
		if err != nil {
			return zero, err
		}
		var input I
		if err := decode(c, &input); err != nil {
			return zero, err
		}
		return call(ctx, id, input)
	})
}
