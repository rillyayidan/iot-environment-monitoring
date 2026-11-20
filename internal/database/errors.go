package database

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsPostgresUnavailable recognizes failures to open or use a PostgreSQL connection.
func IsPostgresUnavailable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var server *pgconn.PgError
	if errors.As(err, &server) {
		switch server.Code {
		case "08000", "08001", "08003", "08004", "08006", "08007",
			"57P01", "57P02", "57P03", "53300":
			return true
		default:
			return false
		}
	}
	var connect *pgconn.ConnectError
	var socket *net.OpError
	// pgx may return a bare EOF if an established connection drops while receiving.
	return errors.As(err, &connect) || errors.As(err, &socket) || pgconn.Timeout(err) ||
		errors.Is(err, pgconn.ErrConnClosed) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
