package tickets

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

type dbDialect int

const (
	dialectSQLite dbDialect = iota
	dialectPostgres
)

// rebind converts SQLite-style '?' placeholders into PostgreSQL '$1, $2, ...'
// placeholders when d == dialectPostgres, skipping single-quoted string
// literals (including escaped ” quotes) and SQL comments (-- and /* */).
func (d dbDialect) rebind(query string) string {
	if d != dialectPostgres || !strings.ContainsRune(query, '?') {
		return query
	}
	var out strings.Builder
	out.Grow(len(query) + 16)
	param := 1
	inQuote := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); i++ {
		c := query[i]
		if inLineComment {
			out.WriteByte(c)
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			out.WriteByte(c)
			if c == '*' && i+1 < len(query) && query[i+1] == '/' {
				out.WriteByte('/')
				i++
				inBlockComment = false
			}
			continue
		}
		if inQuote {
			out.WriteByte(c)
			if c == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					out.WriteByte('\'')
					i++
				} else {
					inQuote = false
				}
			}
			continue
		}
		if c == '\'' {
			inQuote = true
			out.WriteByte(c)
			continue
		}
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			inLineComment = true
			out.WriteString("--")
			i++
			continue
		}
		if c == '/' && i+1 < len(query) && query[i+1] == '*' {
			inBlockComment = true
			out.WriteString("/*")
			i++
			continue
		}
		if c == '?' {
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(param))
			param++
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func (d dbDialect) forUpdate() string {
	if d == dialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

type dbQuerier struct {
	db      *sql.DB
	dialect dbDialect
}

func (q dbQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.db.QueryContext(ctx, q.dialect.rebind(query), args...)
}

func (q dbQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(ctx, q.dialect.rebind(query), args...)
}

func (q dbQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return q.db.ExecContext(ctx, q.dialect.rebind(query), args...)
}

type txQuerier struct {
	tx      *sql.Tx
	dialect dbDialect
}

func (q txQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.tx.QueryContext(ctx, q.dialect.rebind(query), args...)
}

func (q txQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return q.tx.QueryRowContext(ctx, q.dialect.rebind(query), args...)
}

func (q txQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return q.tx.ExecContext(ctx, q.dialect.rebind(query), args...)
}

func (s *Store) q() querier {
	return dbQuerier{db: s.db, dialect: s.dialect}
}

func (s *Store) forUpdate() string {
	return s.dialect.forUpdate()
}

func (s *Store) inTxReadOnly(ctx context.Context, work func(querier) error) error {
	var opts *sql.TxOptions
	if s.dialect == dialectPostgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := work(txQuerier{tx: tx, dialect: s.dialect}); err != nil {
		return err
	}
	return tx.Commit()
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is a unique constraint failure on
// SQLite or PostgreSQL.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if isPostgresUniqueViolation(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}

// isPostgresUniqueViolation is extended with *pgconn.PgError (SQLSTATE 23505)
// in Slice 2.2 when pgx/v5 is added.
func isPostgresUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "SQLSTATE 23505")
}
