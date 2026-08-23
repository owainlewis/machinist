package controlplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/migrations"
	_ "modernc.org/sqlite"
)

var ErrNotFound = &ServiceError{Code: "not_found", Message: "resource not found", Status: 404}

type ServiceError struct {
	Code    string
	Message string
	Status  int
	Err     error
}

func (e *ServiceError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *ServiceError) Unwrap() error { return e.Err }

func conflict(code, message string) error {
	return &ServiceError{Code: code, Message: message, Status: 409}
}

func invalid(code, message string) error {
	return &ServiceError{Code: code, Message: message, Status: 400}
}

type Store struct {
	db                  *sql.DB
	now                 func() time.Time
	sweepEvery          time.Duration
	defaultBuildRuntime string
}

func Open(ctx context.Context, path string) (*Store, error) {
	return openStore(ctx, path, false)
}

func openExistingStore(ctx context.Context, path string) (*Store, error) {
	return openStore(ctx, path, true)
}

func openStore(ctx context.Context, path string, existingOnly bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if existingOnly && path == ":memory:" {
		return nil, errors.New("existing database path is required")
	}
	if path != ":memory:" {
		if existingOnly {
			info, err := os.Lstat(path)
			if err != nil {
				return nil, fmt.Errorf("inspect existing database: %w", err)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("database must be a regular non-symlink file: %s", path)
			}
		}
		preparedPath, err := prepareDatabasePath(path)
		if err != nil {
			return nil, err
		}
		path = preparedPath
	}

	dsn := "file::memory:?cache=shared"
	if path != ":memory:" {
		u := &url.URL{Scheme: "file", Path: path}
		if existingOnly {
			query := u.Query()
			query.Set("mode", "rw")
			u.RawQuery = query.Encode()
		}
		dsn = u.String()
		if strings.Contains(dsn, "?") {
			dsn += "&"
		} else {
			dsn += "?"
		}
		dsn += "_pragma=busy_timeout%285000%29&_pragma=foreign_keys%281%29&_pragma=journal_mode%28WAL%29&_txlock=immediate"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	store := &Store{
		db: db, now: time.Now, sweepEvery: 5 * time.Second,
		defaultBuildRuntime: protocol.RuntimeCodex,
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if !existingOnly {
		if err := store.ensureStandardBuildProcedure(ctx); err != nil {
			db.Close()
			return nil, err
		}
	}
	if path != ":memory:" {
		if err := restrictDatabaseFilePermissions(path); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) SetDefaultBuildRuntime(value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if !protocol.SupportedRuntime(value) {
		return fmt.Errorf("default Build runtime must be one of %s", strings.Join(protocol.SupportedRuntimes(), ", "))
	}
	s.defaultBuildRuntime = value
	return nil
}

func (s *Store) ensureStandardBuildProcedure(ctx context.Context) error {
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("install built-in standard-build Procedure: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM tasks WHERE id = ?)
	`, protocol.StandardBuildProcedureID).Scan(&exists); err != nil {
		return fmt.Errorf("install built-in standard-build Procedure: %w", err)
	}
	if exists == 0 {
		suffix, err := newID()
		if err != nil {
			return fmt.Errorf("install built-in standard-build Procedure: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks(
				id, name, name_key, prompt, runtime, timeout_seconds, concurrency_limit,
				generation, outcome_contract, archived, migration_only, read_only,
				schedule_enabled, schedule_health_status, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, 1, 0, 'disabled', ?, ?)
		`, protocol.StandardBuildProcedureID, protocol.StandardBuildProcedureName,
			"__factory_builtin_standard_build__:"+suffix,
			protocol.StandardBuildProcedurePrompt, protocol.RuntimeCodex,
			protocol.StandardBuildTimeoutSeconds, protocol.StandardBuildConcurrencyLimit,
			protocol.StandardBuildProcedureGeneration, protocol.OutcomeAgentUpdate, now, now); err != nil {
			return fmt.Errorf("install built-in standard-build Procedure: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE tasks SET
			prompt = ?, timeout_seconds = ?, concurrency_limit = ?, generation = ?,
			outcome_contract = ?, archived = 1, migration_only = 1, read_only = 1,
			schedule_enabled = 0, updated_at = ?
		WHERE id = ?
	`, protocol.StandardBuildProcedurePrompt, protocol.StandardBuildTimeoutSeconds,
		protocol.StandardBuildConcurrencyLimit, protocol.StandardBuildProcedureGeneration,
		protocol.OutcomeAgentUpdate, now, protocol.StandardBuildProcedureID); err != nil {
		return fmt.Errorf("install built-in standard-build Procedure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("install built-in standard-build Procedure: %w", err)
	}
	return nil
}

func prepareDatabasePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	directory := filepath.Dir(absolute)
	existingDirectory, err := deepestExistingDirectory(directory)
	if err != nil {
		return "", err
	}
	effectiveUID := uint32(os.Geteuid())
	if err := validateConfiguredDatabaseDirectoryChain(
		existingDirectory,
		effectiveUID,
		existingDirectory == directory,
	); err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create database directory: %w", err)
	}
	if err := validateConfiguredDatabaseDirectoryChain(directory, effectiveUID, true); err != nil {
		return "", err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("canonicalize database directory: %w", err)
	}
	if err := validateDatabaseDirectory(directory, effectiveUID); err != nil {
		return "", err
	}
	path = filepath.Join(directory, filepath.Base(absolute))
	info, err := os.Lstat(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect database: %w", err)
	}
	if exists && !info.Mode().IsRegular() {
		return "", fmt.Errorf("database must be a regular non-symlink file: %s", path)
	}
	marker := path + ".v2-control-plane"
	if exists {
		if err := validateDatabaseMarker(marker); err != nil {
			return "", err
		}
		if err := restrictDatabaseFilePermissions(path); err != nil {
			return "", err
		}
		return path, nil
	}
	err = createDatabaseMarker(marker)
	if errors.Is(err, os.ErrExist) {
		if err := validateDatabaseMarker(marker); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create Factory database with owner-only permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close new Factory database: %w", err)
	}
	if err := restrictDatabaseFilePermissions(path); err != nil {
		return "", err
	}
	return path, nil
}

func deepestExistingDirectory(path string) (string, error) {
	for {
		_, err := os.Lstat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect database path directory %s: %w", path, err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("database path has no existing ancestor: %s", path)
		}
		path = parent
	}
}

func validateDatabaseDirectory(path string, effectiveUID uint32) error {
	return validateDatabaseDirectoryChain(path, effectiveUID, true)
}

func validateConfiguredDatabaseDirectoryChain(path string, effectiveUID uint32, firstIsDatabaseDirectory bool) error {
	configuredDirectory := path
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect configured database path directory %s: %w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("inspect configured database path owner: unsupported file metadata for %s", path)
		}
		if stat.Uid != effectiveUID && stat.Uid != 0 {
			return fmt.Errorf("configured database path must be owned by effective user %d or root: %s", effectiveUID, path)
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		if !isSymlink && !info.IsDir() {
			return fmt.Errorf("configured database path component must be a directory or trusted symlink: %s", path)
		}
		if !isSymlink && (!firstIsDatabaseDirectory || path != configuredDirectory) &&
			info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf(
				"configured database path ancestor must not be group or world writable unless protected by the sticky bit: %s has mode %#o",
				path,
				info.Mode().Perm(),
			)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func validateDatabaseDirectoryChain(path string, effectiveUID uint32, requireDatabaseDirectory bool) error {
	databaseDirectory := path
	for {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect database path directory %s: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("database path must use real non-symlink directories: %s", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("inspect database path directory owner: unsupported file metadata for %s", path)
		}
		if requireDatabaseDirectory && path == databaseDirectory {
			if stat.Uid != effectiveUID {
				return fmt.Errorf("database directory must be owned by effective user %d: %s", effectiveUID, path)
			}
			if info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf(
					"database directory must not be writable by group or other users: %s has mode %#o; run chmod go-w %q",
					path,
					info.Mode().Perm(),
					path,
				)
			}
		} else {
			if stat.Uid != effectiveUID && stat.Uid != 0 {
				return fmt.Errorf("database path ancestor must be owned by effective user %d or root: %s", effectiveUID, path)
			}
			if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
				return fmt.Errorf(
					"database path ancestor must not be group or world writable unless protected by the sticky bit: %s has mode %#o",
					path,
					info.Mode().Perm(),
				)
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func restrictDatabaseFilePermissions(path string) error {
	for _, file := range []struct {
		path     string
		name     string
		required bool
	}{
		{path: path, name: "database", required: true},
		{path: path + "-wal", name: "database WAL", required: false},
		{path: path + "-shm", name: "database shared-memory file", required: false},
	} {
		info, err := os.Lstat(file.path)
		if errors.Is(err, os.ErrNotExist) && !file.required {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s permissions: %w", file.name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular non-symlink file: %s", file.name, file.path)
		}
		if info.Mode().Perm() == 0o600 {
			continue
		}
		if err := os.Chmod(file.path, 0o600); err != nil {
			return fmt.Errorf("restrict %s permissions to owner-only access: %w", file.name, err)
		}
	}
	return nil
}

type databaseMarkerFile interface {
	WriteString(string) (int, error)
	Sync() error
	Close() error
}

func createDatabaseMarker(marker string) error {
	return createDatabaseMarkerWith(marker, func(path string) (databaseMarkerFile, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	})
}

func createDatabaseMarkerWith(marker string, open func(string) (databaseMarkerFile, error)) error {
	file, err := open(marker)
	if err != nil {
		return fmt.Errorf("create Factory database marker: %w", err)
	}
	if _, err := file.WriteString("factory-v2-control-plane\n"); err != nil {
		return cleanFailedDatabaseMarker(
			file,
			marker,
			fmt.Errorf("write Factory database marker: %w", err),
		)
	}
	if err := file.Sync(); err != nil {
		return cleanFailedDatabaseMarker(
			file,
			marker,
			fmt.Errorf("sync Factory database marker: %w", err),
		)
	}
	if err := file.Close(); err != nil {
		closeErr := fmt.Errorf("close Factory database marker: %w", err)
		if removeErr := os.Remove(marker); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(closeErr, fmt.Errorf("remove failed Factory database marker: %w", removeErr))
		}
		return closeErr
	}
	return nil
}

func cleanFailedDatabaseMarker(file databaseMarkerFile, marker string, cause error) error {
	errs := []error{cause}
	if err := file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close failed Factory database marker: %w", err))
	}
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove failed Factory database marker: %w", err))
	}
	return errors.Join(errs...)
}

func validateDatabaseMarker(marker string) error {
	info, err := os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("refusing an existing database without the Factory control-plane marker")
	}
	if err != nil {
		return fmt.Errorf("inspect Factory database marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("refusing an existing database with a non-regular Factory control-plane marker")
	}
	if info.Mode().Perm() != 0o600 {
		if err := os.Chmod(marker, 0o600); err != nil {
			return fmt.Errorf("restrict Factory database marker permissions to owner-only access: %w", err)
		}
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("read Factory database marker: %w", err)
	}
	if string(body) != "factory-v2-control-plane\n" {
		return errors.New("refusing an existing database with an invalid Factory control-plane marker")
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	names, err := migrations.Files.ReadDir(".")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name() < names[j].Name() })
	for version, entry := range names {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		v := version + 1
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, v).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if exists != 0 {
			continue
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if bytes.HasPrefix(body, []byte("-- factory: foreign-keys-off")) {
			if err := s.applyForeignKeyRebuildMigration(ctx, entry.Name(), v, body); err != nil {
				return err
			}
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, v, time.Now().UnixMilli())
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) applyForeignKeyRebuildMigration(
	ctx context.Context,
	name string,
	version int,
	body []byte,
) (resultErr error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve connection for migration %s: %w", name, err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migration %s: %w", name, err)
	}
	defer func() {
		if _, err := connection.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restore foreign keys after migration %s: %w", name, err))
			return
		}
		var enabled int
		if err := connection.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("verify foreign keys after migration %s: %w", name, err))
		} else if enabled != 1 {
			resultErr = errors.Join(resultErr, fmt.Errorf("verify foreign keys after migration %s: foreign keys remain disabled", name))
		}
	}()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	if _, err = tx.ExecContext(ctx, string(body)); err == nil && name == "027_routines_work.sql" {
		err = normalizeMigratedTaskTitleKeys(ctx, tx)
	}
	if err == nil {
		var rows *sql.Rows
		rows, err = tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err == nil {
			if rows.Next() {
				err = errors.New("foreign key violation found")
			} else if rowsErr := rows.Err(); rowsErr != nil {
				err = rowsErr
			}
			if closeErr := rows.Close(); err == nil {
				err = closeErr
			}
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			version, time.Now().UnixMilli())
	}
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}

func (s *Store) Close() error {
	checkpointErr := retrySQLiteContention(func() error {
		_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		return err
	})
	closeErr := s.db.Close()
	if checkpointErr != nil {
		return checkpointErr
	}
	return closeErr
}

func retrySQLiteContention(operation func() error) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := operation()
		if err == nil || !isSQLiteContention(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func isSQLiteContention(err error) bool {
	var coded interface{ Code() int }
	if !errors.As(err, &coded) {
		return false
	}
	// SQLite extended result codes retain the primary code in the low byte.
	code := coded.Code() & 0xff
	return code == 5 || code == 6 // SQLITE_BUSY or SQLITE_LOCKED
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func digestToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func validateToken(token string) error {
	if len(token) < 32 || len(token) > 1024 {
		return invalid("invalid_lease_token", "lease_token must contain at least 32 bytes")
	}
	return nil
}

func normalizeRemote(value string) string {
	return strings.TrimSpace(value)
}

func normalizeRegisteredRemote(value string) string {
	value = normalizeRemote(value)
	parts := strings.Split(value, "/")
	if len(parts) == 3 && strings.EqualFold(parts[0], "github.com") {
		if canonical, err := normalizeManagedGitHubRemote(value); err == nil {
			return canonical
		}
	}
	return value
}

func normalizeManagedGitHubRemote(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".git")
	parts := strings.Split(value, "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "github.com") ||
		!validGitHubOwner(parts[1]) || !validGitHubRepository(parts[2]) {
		return "", invalid(
			"invalid_repository",
			"remote_identity must use the canonical github.com/owner/repository form",
		)
	}
	return strings.ToLower(strings.Join(parts, "/")), nil
}

func validGitHubOwner(value string) bool {
	if len(value) < 1 || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validGitHubRepository(value string) bool {
	if len(value) < 1 || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func scanManagedRepository(row scanner) (protocol.ManagedRepository, error) {
	var repository protocol.ManagedRepository
	var enabled int
	var createdAt, updatedAt int64
	if err := row.Scan(
		&repository.ID,
		&repository.RemoteIdentity,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return repository, err
	}
	repository.Enabled = enabled != 0
	repository.CreatedAt = fromMillis(createdAt)
	repository.UpdatedAt = fromMillis(updatedAt)
	return repository, nil
}

func (s *Store) CreateManagedRepository(
	ctx context.Context,
	input protocol.CreateManagedRepositoryRequest,
) (protocol.ManagedRepository, bool, error) {
	remoteIdentity, err := normalizeManagedGitHubRemote(input.RemoteIdentity)
	if err != nil {
		return protocol.ManagedRepository{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ManagedRepository{}, false, unavailable(err)
	}
	defer tx.Rollback()

	repository, err := scanManagedRepository(tx.QueryRowContext(ctx, `
		SELECT id, remote_identity, enabled, created_at, updated_at
		FROM repositories
		WHERE lower(remote_identity) = lower(?)
	`, remoteIdentity))
	if err == nil {
		var centrallyManaged int
		if err := tx.QueryRowContext(ctx, `
			SELECT centrally_managed FROM repositories WHERE id = ?
		`, repository.ID).Scan(&centrallyManaged); err != nil {
			return protocol.ManagedRepository{}, false, unavailable(err)
		}
		if centrallyManaged == 0 {
			now := s.now().UnixMilli()
			if _, err := tx.ExecContext(ctx, `
				UPDATE repositories
				SET enabled = 1, centrally_managed = 1, updated_at = ?
				WHERE id = ? AND centrally_managed = 0
			`, now, repository.ID); err != nil {
				return protocol.ManagedRepository{}, false, unavailable(err)
			}
			repository.Enabled = true
			repository.UpdatedAt = fromMillis(now)
		}
		if err := tx.Commit(); err != nil {
			return protocol.ManagedRepository{}, false, unavailable(err)
		}
		return repository, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return protocol.ManagedRepository{}, false, unavailable(err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&count); err != nil {
		return protocol.ManagedRepository{}, false, unavailable(err)
	}
	if count >= protocol.MaxManagedRepositories {
		return protocol.ManagedRepository{}, false, conflict(
			"repository_limit_reached",
			"the managed repository limit has been reached",
		)
	}
	repositoryID, err := newID()
	if err != nil {
		return protocol.ManagedRepository{}, false, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO repositories(
			id, remote_identity, enabled, centrally_managed, created_at, updated_at
		)
		VALUES (?, ?, 1, 1, ?, ?)
	`, repositoryID, remoteIdentity, now, now); err != nil {
		return protocol.ManagedRepository{}, false, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.ManagedRepository{}, false, unavailable(err)
	}
	return protocol.ManagedRepository{
		ID:             repositoryID,
		RemoteIdentity: remoteIdentity,
		Enabled:        true,
		CreatedAt:      fromMillis(now),
		UpdatedAt:      fromMillis(now),
	}, true, nil
}

func (s *Store) ManagedRepositories(ctx context.Context) ([]protocol.ManagedRepository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, remote_identity, enabled, created_at, updated_at
		FROM repositories
		ORDER BY remote_identity
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	repositories := make([]protocol.ManagedRepository, 0)
	for rows.Next() {
		repository, err := scanManagedRepository(rows)
		if err != nil {
			return nil, unavailable(err)
		}
		repositories = append(repositories, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return repositories, nil
}

func (s *Store) ManagedRepository(ctx context.Context, repositoryID string) (protocol.ManagedRepository, error) {
	repository, err := scanManagedRepository(s.db.QueryRowContext(ctx, `
		SELECT id, remote_identity, enabled, created_at, updated_at
		FROM repositories
		WHERE id = ?
	`, strings.TrimSpace(repositoryID)))
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.ManagedRepository{}, ErrNotFound
	}
	if err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	return repository, nil
}

type managedRepositoryEligibility struct {
	online             bool
	health             string
	githubAccess       bool
	acceptsManaged     bool
	cached             bool
	advertised         bool
	reserved           bool
	displayKeyConflict bool
	cacheUse           int
	retentionUse       int
}

func evaluateManagedRepositoryEligibility(
	repository protocol.ManagedRepository,
	state managedRepositoryEligibility,
) (bool, string) {
	switch {
	case !repository.Enabled:
		return false, "Repository routing is disabled."
	case !state.advertised && !isManagedGitHubRemote(repository.RemoteIdentity):
		return false, "Repository source is not supported for managed acquisition."
	case !state.online:
		return false, "Worker is offline."
	case state.health != "healthy":
		return false, "Worker is unhealthy."
	case !state.githubAccess:
		return false, "Worker does not currently report GitHub access."
	case !state.advertised && state.displayKeyConflict:
		return false, "Another advertised repository uses this routing identity."
	case !state.advertised && !state.acceptsManaged:
		return false, "Worker cannot acquire managed repositories and does not advertise this one."
	case !state.advertised && !state.cached && !state.reserved && state.cacheUse >= protocol.MaxRepositoryCacheEntries:
		return false, "Managed repository cache and reservations are full."
	case state.retentionUse >= protocol.MaxRetainedPerRepo:
		return false, "Repository retained-worktree capacity is full."
	case state.cached:
		return true, "Online, healthy, with GitHub access and this repository cached."
	case state.advertised:
		return true, "Online, healthy, with GitHub access and this repository advertised."
	case state.reserved:
		return true, "Online, healthy, with GitHub access and this repository already reserved."
	default:
		return true, "Online, healthy, with GitHub access and managed cache headroom."
	}
}

func isManagedGitHubRemote(remoteIdentity string) bool {
	_, err := normalizeManagedGitHubRemote(remoteIdentity)
	return err == nil
}

func (s *Store) WorkerRepositoryOptions(
	ctx context.Context,
	workerID string,
) ([]protocol.WorkerRepositoryOption, error) {
	workerID = strings.TrimSpace(workerID)
	var health string
	var heartbeat int64
	var encodedSourceAccess, encodedManagedRepositoryIDs []byte
	var acceptsManaged, cacheUse int
	err := s.db.QueryRowContext(ctx, `
		SELECT health, last_heartbeat, source_access_json, managed_repository_ids_json,
		       accepts_managed_repositories,
		       json_array_length(managed_repository_ids_json) + (
		           SELECT COUNT(*)
		           FROM worker_repositories reserved
		           WHERE reserved.worker_id = workers.id
		             AND reserved.dynamic = 1
		             AND reserved.advertised = 1
		             AND NOT EXISTS (
		                 SELECT 1 FROM json_each(workers.managed_repository_ids_json) cached
		                 WHERE cached.value = reserved.repository_id
		             )
		       )
		FROM workers WHERE id = ?
	`, workerID).Scan(
		&health, &heartbeat, &encodedSourceAccess, &encodedManagedRepositoryIDs,
		&acceptsManaged, &cacheUse,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, unavailable(err)
	}
	var sourceAccess []protocol.SourceAccess
	if err := json.Unmarshal(encodedSourceAccess, &sourceAccess); err != nil {
		return nil, unavailable(err)
	}
	var managedRepositoryIDs []string
	if err := json.Unmarshal(encodedManagedRepositoryIDs, &managedRepositoryIDs); err != nil {
		return nil, unavailable(err)
	}
	cachedRepositoryIDs := make(map[string]struct{}, len(managedRepositoryIDs))
	for _, repositoryID := range managedRepositoryIDs {
		cachedRepositoryIDs[repositoryID] = struct{}{}
	}
	baseState := managedRepositoryEligibility{
		online:         s.now().Sub(fromMillis(heartbeat)) <= protocol.WorkerOnlineWindow,
		health:         health,
		githubAccess:   hasSourceAccess(sourceAccess, protocol.SourceAccess{Provider: "github", Hostname: "github.com"}),
		acceptsManaged: acceptsManaged != 0,
		cacheUse:       cacheUse,
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT repository.id, COALESCE(worker_repository.display_key, ''),
		       repository.remote_identity, repository.enabled,
		       CASE
		           WHEN worker_repository.advertised = 1 AND worker_repository.dynamic = 0 THEN 1
		           ELSE 0
		       END,
		       CASE
		           WHEN worker_repository.advertised = 1 AND worker_repository.dynamic = 1 THEN 1
		           ELSE 0
		       END,
		       EXISTS (
		           SELECT 1 FROM worker_repositories conflict
		           WHERE conflict.worker_id = ?
		             AND conflict.display_key = repository.remote_identity
		             AND conflict.repository_id != repository.id
		       ),
		       COALESCE(worker_repository.retained_count, 0) + (
		           SELECT COUNT(*)
		           FROM attempts active_attempt
		           JOIN executions active_execution ON active_execution.id = active_attempt.execution_id
		           JOIN sessions active_session ON active_session.id = active_execution.session_id
		           WHERE active_attempt.worker_id = ?
		             AND active_session.repository_id = repository.id
		             AND active_attempt.state IN ('preparing', 'running')
		       ) + (
		           SELECT COUNT(*)
		           FROM attempts terminal_attempt
		           JOIN executions terminal_execution ON terminal_execution.id = terminal_attempt.execution_id
		           JOIN sessions terminal_session ON terminal_session.id = terminal_execution.session_id
		           WHERE terminal_attempt.worker_id = ?
		             AND terminal_session.repository_id = repository.id
		             AND terminal_attempt.state IN ('succeeded', 'failed', 'cancelled', 'lost')
		             AND terminal_attempt.capacity_acknowledged = 0
		       )
		FROM repositories repository
		LEFT JOIN worker_repositories worker_repository
		  ON worker_repository.worker_id = ? AND worker_repository.repository_id = repository.id
		ORDER BY repository.remote_identity
	`, workerID, workerID, workerID, workerID)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	options := make([]protocol.WorkerRepositoryOption, 0)
	for rows.Next() {
		var option protocol.WorkerRepositoryOption
		var enabled, advertised, reserved, displayKeyConflict int
		var retentionUse int
		if err := rows.Scan(
			&option.ID, &option.Key, &option.RemoteIdentity, &enabled,
			&advertised, &reserved, &displayKeyConflict, &retentionUse,
		); err != nil {
			return nil, unavailable(err)
		}
		_, option.Cached = cachedRepositoryIDs[option.ID]
		option.Enabled = enabled != 0
		option.Advertised = advertised != 0
		state := baseState
		state.cached = option.Cached
		state.advertised = option.Advertised
		state.reserved = reserved != 0
		state.displayKeyConflict = displayKeyConflict != 0
		state.retentionUse = retentionUse
		option.Ready, option.Reason = evaluateManagedRepositoryEligibility(
			protocol.ManagedRepository{ID: option.ID, RemoteIdentity: option.RemoteIdentity, Enabled: option.Enabled},
			state,
		)
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	return options, nil
}

func (s *Store) ManagedRepositoryReadiness(
	ctx context.Context,
	repositoryID string,
) (protocol.ManagedRepositoryReadiness, error) {
	repository, err := s.ManagedRepository(ctx, repositoryID)
	if err != nil {
		return protocol.ManagedRepositoryReadiness{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.name, w.health, w.last_heartbeat, w.source_access_json,
		       w.accepts_managed_repositories,
		       EXISTS (
		           SELECT 1 FROM json_each(w.managed_repository_ids_json) cached
		           WHERE cached.value = ?
		       ),
		       COALESCE(wr.advertised, 0),
		       EXISTS (
		           SELECT 1 FROM worker_repositories conflict
		           WHERE conflict.worker_id = w.id
		             AND conflict.display_key = ?
		             AND conflict.repository_id != ?
		       ),
		       json_array_length(w.managed_repository_ids_json) + (
		           SELECT COUNT(*)
		           FROM worker_repositories reserved
		           WHERE reserved.worker_id = w.id
		             AND reserved.dynamic = 1
		             AND reserved.advertised = 1
		             AND NOT EXISTS (
		                 SELECT 1 FROM json_each(w.managed_repository_ids_json) cached
		                 WHERE cached.value = reserved.repository_id
		             )
		       ),
		       COALESCE(wr.retained_count, 0) + (
		           SELECT COUNT(*)
		           FROM attempts active_attempt
		           JOIN executions active_execution ON active_execution.id = active_attempt.execution_id
		           JOIN sessions active_session ON active_session.id = active_execution.session_id
		           WHERE active_attempt.worker_id = w.id
		             AND active_session.repository_id = ?
		             AND active_attempt.state IN ('preparing', 'running')
		       ) + (
		           SELECT COUNT(*)
		           FROM attempts terminal_attempt
		           JOIN executions terminal_execution ON terminal_execution.id = terminal_attempt.execution_id
		           JOIN sessions terminal_session ON terminal_session.id = terminal_execution.session_id
		           WHERE terminal_attempt.worker_id = w.id
		             AND terminal_session.repository_id = ?
		             AND terminal_attempt.state IN ('succeeded', 'failed', 'cancelled', 'lost')
		             AND terminal_attempt.capacity_acknowledged = 0
		       )
		FROM workers w
		LEFT JOIN worker_repositories wr
		  ON wr.worker_id = w.id AND wr.repository_id = ?
		WHERE w.synthetic = 0
		ORDER BY w.registered_at, w.id
	`, repository.ID, repository.RemoteIdentity, repository.ID,
		repository.ID, repository.ID, repository.ID)
	if err != nil {
		return protocol.ManagedRepositoryReadiness{}, unavailable(err)
	}
	defer rows.Close()

	readiness := protocol.ManagedRepositoryReadiness{
		Workers: make([]protocol.ManagedRepositoryWorkerReadiness, 0),
	}
	now := s.now()
	for rows.Next() {
		var worker protocol.ManagedRepositoryWorkerReadiness
		var health string
		var heartbeat int64
		var encodedSourceAccess []byte
		var acceptsManaged, cached, advertised, displayKeyConflict int
		var cacheUse, retentionUse int
		if err := rows.Scan(
			&worker.ID, &worker.Name, &health, &heartbeat, &encodedSourceAccess,
			&acceptsManaged, &cached, &advertised, &displayKeyConflict,
			&cacheUse, &retentionUse,
		); err != nil {
			return protocol.ManagedRepositoryReadiness{}, unavailable(err)
		}
		var sourceAccess []protocol.SourceAccess
		if err := json.Unmarshal(encodedSourceAccess, &sourceAccess); err != nil {
			return protocol.ManagedRepositoryReadiness{}, unavailable(err)
		}
		worker.Cached = cached != 0
		worker.Advertised = advertised != 0
		worker.Ready, worker.Reason = evaluateManagedRepositoryEligibility(
			repository,
			managedRepositoryEligibility{
				online:             now.Sub(fromMillis(heartbeat)) <= protocol.WorkerOnlineWindow,
				health:             health,
				githubAccess:       hasSourceAccess(sourceAccess, protocol.SourceAccess{Provider: "github", Hostname: "github.com"}),
				acceptsManaged:     acceptsManaged != 0,
				cached:             worker.Cached,
				advertised:         worker.Advertised,
				displayKeyConflict: displayKeyConflict != 0,
				cacheUse:           cacheUse,
				retentionUse:       retentionUse,
			},
		)
		if worker.Ready {
			readiness.RoutingReady = true
		}
		readiness.Workers = append(readiness.Workers, worker)
	}
	if err := rows.Err(); err != nil {
		return protocol.ManagedRepositoryReadiness{}, unavailable(err)
	}
	return readiness, nil
}

func (s *Store) SetManagedRepositoryEnabled(
	ctx context.Context,
	repositoryID string,
	enabled bool,
) (protocol.ManagedRepository, error) {
	repositoryID = strings.TrimSpace(repositoryID)
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	defer tx.Rollback()
	var currentEnabled, centrallyManaged bool
	err = tx.QueryRowContext(ctx, `SELECT enabled, centrally_managed FROM repositories WHERE id = ?`, repositoryID).Scan(&currentEnabled, &centrallyManaged)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.ManagedRepository{}, ErrNotFound
	}
	if err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	if currentEnabled == enabled {
		if !centrallyManaged {
			if _, err := tx.ExecContext(ctx, `
				UPDATE repositories SET centrally_managed = 1, updated_at = ? WHERE id = ?
			`, now, repositoryID); err != nil {
				return protocol.ManagedRepository{}, unavailable(err)
			}
		}
		if err := tx.Commit(); err != nil {
			return protocol.ManagedRepository{}, unavailable(err)
		}
		return s.ManagedRepository(ctx, repositoryID)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE repositories
		SET enabled = ?, centrally_managed = 1, updated_at = ?
		WHERE id = ?
	`, enabled, now, repositoryID)
	if err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	if affected == 0 {
		return protocol.ManagedRepository{}, ErrNotFound
	}
	if !enabled {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM executions
			WHERE state = 'queued' AND session_id IN (
				SELECT id FROM sessions WHERE repository_id = ? AND state = 'queued'
			)
		`, repositoryID); err != nil {
			return protocol.ManagedRepository{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET state = 'blocked', assigned_worker_id = NULL,
			       blocked_reason = 'Repository is disabled.',
			       waiting_reason = 'Repository is disabled.', execution_owner = 'none'
			WHERE repository_id = ? AND state = 'queued'
		`, repositoryID); err != nil {
			return protocol.ManagedRepository{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET schedule_health_status = 'blocked',
			       schedule_health_code = 'repository_disabled',
			       schedule_health_message = 'Enable every selected repository before the next occurrence can run.'
			WHERE schedule_enabled = 1 AND id IN (
				SELECT task_id FROM task_repositories WHERE repository_id = ?
			)
		`, repositoryID); err != nil {
			return protocol.ManagedRepository{}, unavailable(err)
		}
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE tasks SET schedule_health_status = 'healthy',
		       schedule_health_code = '', schedule_health_message = ''
		WHERE schedule_enabled = 1 AND pending_due_at IS NULL
		  AND id IN (SELECT task_id FROM task_repositories WHERE repository_id = ?)
	`, repositoryID); err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	if err := tx.Commit(); err != nil {
		return protocol.ManagedRepository{}, unavailable(err)
	}
	return s.ManagedRepository(ctx, repositoryID)
}

func normalizeSourceAccess(values []protocol.SourceAccess) ([]protocol.SourceAccess, error) {
	if len(values) > 10 {
		return nil, invalid("invalid_source_access", "a worker may advertise at most 10 source access entries")
	}
	seen := make(map[string]bool, len(values))
	result := make([]protocol.SourceAccess, 0, len(values))
	for _, value := range values {
		value.Provider = strings.TrimSpace(value.Provider)
		value.Hostname = strings.ToLower(strings.TrimSpace(value.Hostname))
		if !validProvider(value.Provider) || !validHostname(value.Hostname) {
			return nil, invalid(
				"invalid_source_access",
				"source access provider and hostname must be lowercase bounded identifiers",
			)
		}
		key := value.Provider + "\x00" + value.Hostname
		if seen[key] {
			return nil, invalid("invalid_source_access", "source access entries must be unique")
		}
		seen[key] = true
		result = append(result, value)
	}
	return result, nil
}

func validProvider(value string) bool {
	if len(value) < 1 || len(value) > 50 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validHostname(value string) bool {
	if len(value) < 1 || len(value) > 253 || strings.ContainsAny(value, " /:@") {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Store) resolveRepositoryAlias(ctx context.Context, repositoryID string) (string, error) {
	var canonicalID string
	err := s.db.QueryRowContext(ctx, `
		SELECT repository_id FROM repository_aliases WHERE alias_id = ?
	`, repositoryID).Scan(&canonicalID)
	if errors.Is(err, sql.ErrNoRows) {
		return repositoryID, nil
	}
	if err != nil {
		return "", unavailable(err)
	}
	return canonicalID, nil
}

func (s *Store) HeartbeatWorker(ctx context.Context, workerID string) (protocol.Worker, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE workers SET last_heartbeat = ? WHERE id = ? AND synthetic = 0`, s.now().UnixMilli(), workerID)
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	if updated == 0 {
		var synthetic int
		if err := s.db.QueryRowContext(ctx, `SELECT synthetic FROM workers WHERE id = ?`, workerID).Scan(&synthetic); err == nil && synthetic != 0 {
			return protocol.Worker{}, conflict("synthetic_worker_isolated", "synthetic cloud Workers cannot use Worker heartbeat routes")
		}
		return protocol.Worker{}, ErrNotFound
	}
	return s.Worker(ctx, workerID)
}

func (s *Store) RegisterWorker(ctx context.Context, workerID string, input protocol.WorkerRegistration) (protocol.Worker, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 200 {
		return protocol.Worker{}, invalid("invalid_worker_id", "worker_id is required and must be at most 200 bytes")
	}
	var synthetic int
	if err := s.db.QueryRowContext(ctx, `SELECT synthetic FROM workers WHERE id = ?`, workerID).Scan(&synthetic); err == nil && synthetic != 0 {
		return protocol.Worker{}, conflict("synthetic_worker_isolated", "synthetic cloud Workers cannot register through Worker routes")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.Worker{}, unavailable(err)
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 200 {
		return protocol.Worker{}, invalid("invalid_worker", "worker name is required and must be at most 200 bytes")
	}
	if input.ClaimProtocolVersion != protocol.ClaimProtocolVersion {
		return protocol.Worker{}, conflict(
			"worker_upgrade_required",
			"the Worker uses an incompatible claim protocol; upgrade it before reconnecting",
		)
	}
	labels, err := normalizeWorkerLabels(input.Labels)
	if err != nil {
		return protocol.Worker{}, err
	}
	input.Labels = labels
	input.Runtime = strings.TrimSpace(input.Runtime)
	input.RuntimeVersion = strings.TrimSpace(input.RuntimeVersion)
	if input.Runtime == "" {
		input.Runtime = protocol.RuntimeCodex
	}
	if !protocol.SupportedRuntime(input.Runtime) {
		return protocol.Worker{}, invalid("invalid_runtime", "runtime must be pi, codex, or claude-code")
	}
	if len(input.RuntimeVersion) > 1024 {
		return protocol.Worker{}, invalid("invalid_runtime_version", "runtime_version must be at most 1024 bytes")
	}
	if input.Capacity < protocol.MinWorkerCapacity || input.Capacity > protocol.MaxWorkerCapacity ||
		input.ActiveCount < 0 || input.ActiveCount > input.Capacity {
		return protocol.Worker{}, invalid("invalid_capacity", fmt.Sprintf(
			"capacity must be %d through %d and active_count cannot exceed it",
			protocol.MinWorkerCapacity, protocol.MaxWorkerCapacity))
	}
	if input.Health != "healthy" && input.Health != "unhealthy" {
		return protocol.Worker{}, invalid("invalid_health", "health must be healthy or unhealthy")
	}
	capabilities, err := normalizeWorkerCapabilities(input)
	if err != nil {
		return protocol.Worker{}, err
	}
	input.Capabilities = capabilities
	if input.WeeklyLimit != nil {
		if input.WeeklyLimit.UsedPercent < 0 || input.WeeklyLimit.UsedPercent > 100 ||
			input.WeeklyLimit.ResetsAt.IsZero() {
			return protocol.Worker{}, invalid(
				"invalid_weekly_limit", "weekly_limit must contain a percentage from 0 through 100 and a reset time")
		}
		input.WeeklyLimit.ResetsAt = input.WeeklyLimit.ResetsAt.UTC()
	}
	if input.CapacityHandoffVersion < 0 || input.CapacityHandoffVersion > 1 {
		return protocol.Worker{}, invalid(
			"invalid_capacity_handoff", "capacity_handoff_version must be 0 or 1")
	}
	if len(input.ManagedRepositoryIDs) > protocol.MaxRepositoryCacheEntries {
		return protocol.Worker{}, invalid(
			"invalid_managed_repository_ids",
			"a worker may advertise at most 100 cached managed repository IDs",
		)
	}
	seenManagedRepositoryIDs := make(map[string]bool, len(input.ManagedRepositoryIDs))
	for index, repositoryID := range input.ManagedRepositoryIDs {
		canonicalID, err := s.resolveRepositoryAlias(ctx, repositoryID)
		if err != nil {
			return protocol.Worker{}, err
		}
		input.ManagedRepositoryIDs[index] = canonicalID
		repositoryID = canonicalID
		if !validUUID(repositoryID) || seenManagedRepositoryIDs[repositoryID] {
			return protocol.Worker{}, invalid(
				"invalid_managed_repository_ids",
				"cached managed repository IDs must be unique UUIDs",
			)
		}
		seenManagedRepositoryIDs[repositoryID] = true
	}
	sourceAccess, err := normalizeSourceAccess(input.SourceAccess)
	if err != nil {
		return protocol.Worker{}, err
	}
	input.SourceAccess = sourceAccess
	seenKeys := make(map[string]bool, len(input.Repositories))
	seenRemotes := make(map[string]int, len(input.Repositories))
	normalizedRepositories := make([]protocol.RepositoryRegistration, 0, len(input.Repositories))
	workerRemoteIdentities := make([]string, 0, len(input.Repositories))
	for _, value := range input.Repositories {
		repo := value
		repo.Key = strings.TrimSpace(repo.Key)
		workerRemoteIdentity := normalizeRemote(repo.RemoteIdentity)
		repo.RemoteIdentity = normalizeRegisteredRemote(workerRemoteIdentity)
		if repo.Key == "" || repo.RemoteIdentity == "" || len(repo.Key) > 200 || len(repo.RemoteIdentity) > 2048 {
			return protocol.Worker{}, invalid("invalid_repository", "repository key and remote_identity are required")
		}
		if repo.RetainedCount < 0 {
			return protocol.Worker{}, invalid("invalid_repository", "retained_count cannot be negative")
		}
		if seenKeys[repo.Key] {
			return protocol.Worker{}, invalid("duplicate_repository", "repository keys and identities must be unique per worker")
		}
		seenKeys[repo.Key] = true
		if previousIndex, exists := seenRemotes[repo.RemoteIdentity]; exists {
			previousWorkerRemote := workerRemoteIdentities[previousIndex]
			if workerRemoteIdentity == previousWorkerRemote ||
				!strings.EqualFold(workerRemoteIdentity, previousWorkerRemote) {
				return protocol.Worker{}, invalid(
					"duplicate_repository", "repository identities must be unique per worker")
			}
			if repo.RetainedCount > normalizedRepositories[previousIndex].RetainedCount {
				normalizedRepositories[previousIndex].RetainedCount = repo.RetainedCount
			}
			continue
		}
		seenRemotes[repo.RemoteIdentity] = len(normalizedRepositories)
		normalizedRepositories = append(normalizedRepositories, repo)
		workerRemoteIdentities = append(workerRemoteIdentities, workerRemoteIdentity)
	}
	input.Repositories = normalizedRepositories
	retainedCounts := make(map[string]int)
	retainedAttemptIDs := make(map[string][]string)
	seenRetainedAttempts := make(map[string]string)
	hasUnattributedRetainedSummary := false
	for index := range input.RetainedWorktrees {
		worktree := &input.RetainedWorktrees[index]
		worktree.AttemptID = strings.TrimSpace(worktree.AttemptID)
		worktree.RepositoryID = strings.TrimSpace(worktree.RepositoryID)
		if worktree.RepositoryID != "" {
			canonicalID, err := s.resolveRepositoryAlias(ctx, worktree.RepositoryID)
			if err != nil {
				return protocol.Worker{}, err
			}
			worktree.RepositoryID = canonicalID
		}
		// Older API clients may send display-only summaries before they know the
		// control-plane repository ID. Preserve those summaries, but do not use
		// incomplete or duplicate entries as capacity-handoff evidence.
		if worktree.AttemptID == "" || worktree.RepositoryID == "" {
			hasUnattributedRetainedSummary = true
			continue
		}
		if repositoryID, seen := seenRetainedAttempts[worktree.AttemptID]; seen {
			if repositoryID != worktree.RepositoryID {
				hasUnattributedRetainedSummary = true
			}
			continue
		}
		seenRetainedAttempts[worktree.AttemptID] = worktree.RepositoryID
		retainedCounts[worktree.RepositoryID]++
		retainedAttemptIDs[worktree.RepositoryID] = append(
			retainedAttemptIDs[worktree.RepositoryID], worktree.AttemptID)
	}
	disposedAttemptIDs := make([]string, 0, len(input.DisposedAttemptIDs))
	seenDisposedAttempts := make(map[string]bool, len(input.DisposedAttemptIDs))
	for _, value := range input.DisposedAttemptIDs {
		attemptID := strings.TrimSpace(value)
		if attemptID == "" || len(attemptID) > 200 {
			return protocol.Worker{}, invalid(
				"invalid_disposed_attempts", "disposed attempt IDs must be non-empty and at most 200 bytes")
		}
		if !seenDisposedAttempts[attemptID] {
			seenDisposedAttempts[attemptID] = true
			disposedAttemptIDs = append(disposedAttemptIDs, attemptID)
		}
	}
	retained, err := json.Marshal(input.RetainedWorktrees)
	if err != nil || len(retained) > protocol.MaxBodyBytes {
		return protocol.Worker{}, invalid("invalid_retained_worktrees", "retained worktree summaries are too large")
	}
	sourceAccessJSON, err := json.Marshal(input.SourceAccess)
	if err != nil {
		return protocol.Worker{}, invalid("invalid_source_access", "source access could not be encoded")
	}
	managedRepositoryIDsJSON, err := json.Marshal(input.ManagedRepositoryIDs)
	if err != nil {
		return protocol.Worker{}, invalid(
			"invalid_managed_repository_ids",
			"cached managed repository IDs could not be encoded",
		)
	}
	capabilitiesJSON, err := json.Marshal(input.Capabilities)
	if err != nil || len(capabilitiesJSON) > protocol.MaxBodyBytes {
		return protocol.Worker{}, invalid(
			"invalid_capabilities", "worker capabilities could not be encoded",
		)
	}
	labelsJSON, err := json.Marshal(input.Labels)
	if err != nil {
		return protocol.Worker{}, invalid("invalid_labels", "Worker labels could not be encoded")
	}
	var weeklyLimitUsedPercent, weeklyLimitResetsAt any
	if input.WeeklyLimit != nil {
		weeklyLimitUsedPercent = input.WeeklyLimit.UsedPercent
		weeklyLimitResetsAt = input.WeeklyLimit.ResetsAt.UnixMilli()
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	defer tx.Rollback()
	var existingRuntime string
	err = tx.QueryRowContext(ctx, `SELECT runtime FROM workers WHERE id = ?`, workerID).Scan(&existingRuntime)
	if err == nil && existingRuntime != input.Runtime {
		return protocol.Worker{}, conflict(
			"worker_runtime_changed",
			"a worker runtime cannot change; use a new worker data directory for a new identity",
		)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.Worker{}, unavailable(err)
	}
	advertisedRepositoryIDs := make(map[string]bool, len(input.Repositories))
	for _, repo := range input.Repositories {
		var existingID string
		err := tx.QueryRowContext(ctx, `
			SELECT repository_id FROM worker_repositories
			WHERE worker_id = ? AND display_key = ?
		`, workerID, repo.Key).Scan(&existingID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return protocol.Worker{}, unavailable(err)
		}
		if err == nil {
			var identity string
			if err := tx.QueryRowContext(ctx, `SELECT remote_identity FROM repositories WHERE id = ?`, existingID).Scan(&identity); err != nil {
				return protocol.Worker{}, unavailable(err)
			}
			if identity != repo.RemoteIdentity {
				return protocol.Worker{}, conflict("repository_key_changed", "a repository key cannot be reassigned to a different remote identity")
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO workers(
			id, name, labels_json, worker_version, claim_protocol_version,
			runtime, runtime_version, capacity, active_count,
			health, capabilities_json, source_access_json, accepts_managed_repositories,
			managed_repository_ids_json, retained_worktrees_json,
			weekly_limit_used_percent, weekly_limit_resets_at, registered_at, last_heartbeat
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, labels_json=excluded.labels_json,
			worker_version=excluded.worker_version,
			claim_protocol_version=excluded.claim_protocol_version,
			runtime_version=excluded.runtime_version,
			capacity=excluded.capacity, active_count=excluded.active_count, health=excluded.health,
			capabilities_json=excluded.capabilities_json,
			source_access_json=excluded.source_access_json,
			accepts_managed_repositories=excluded.accepts_managed_repositories,
			managed_repository_ids_json=excluded.managed_repository_ids_json,
			retained_worktrees_json=excluded.retained_worktrees_json,
			weekly_limit_used_percent=excluded.weekly_limit_used_percent,
			weekly_limit_resets_at=excluded.weekly_limit_resets_at,
			last_heartbeat=excluded.last_heartbeat
	`, workerID, input.Name, labelsJSON, input.WorkerVersion, input.ClaimProtocolVersion,
		input.Runtime, input.RuntimeVersion,
		input.Capacity, input.ActiveCount, input.Health, capabilitiesJSON, sourceAccessJSON,
		input.AcceptsManagedRepositories, managedRepositoryIDsJSON, retained,
		weeklyLimitUsedPercent, weeklyLimitResetsAt, now, now)
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE worker_repositories
		SET advertised = CASE WHEN dynamic = 1 AND ? THEN 1 ELSE 0 END,
		    updated_at = ?
		WHERE worker_id = ?
	`, input.AcceptsManagedRepositories, now, workerID); err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	for index, repo := range input.Repositories {
		workerRemoteIdentity := workerRemoteIdentities[index]
		var repositoryID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM repositories WHERE remote_identity = ?`, repo.RemoteIdentity).Scan(&repositoryID)
		if errors.Is(err, sql.ErrNoRows) {
			repositoryID, err = newID()
			if err == nil {
				var repositoryCount int
				err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&repositoryCount)
				if err == nil && repositoryCount >= protocol.MaxManagedRepositories {
					return protocol.Worker{}, conflict(
						"repository_limit_reached",
						"the managed repository limit has been reached",
					)
				}
			}
			if err == nil {
				// A legacy static checkout supports explicit manual assignment, but
				// worker registration must not expand the centrally managed fleet.
				_, err = tx.ExecContext(ctx, `
					INSERT INTO repositories(
						id, remote_identity, enabled, centrally_managed, created_at, updated_at
					)
					VALUES (?, ?, 0, 0, ?, ?)
				`, repositoryID, repo.RemoteIdentity, now, now)
			}
		}
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		advertisedRepositoryIDs[repositoryID] = true
		effectiveRetainedCount := repo.RetainedCount
		if retainedCounts[repositoryID] > effectiveRetainedCount {
			effectiveRetainedCount = retainedCounts[repositoryID]
		}
		var existingKey string
		mappingErr := tx.QueryRowContext(ctx, `
			SELECT display_key FROM worker_repositories
			WHERE worker_id = ? AND repository_id = ?
		`, workerID, repositoryID).Scan(&existingKey)
		switch {
		case mappingErr == nil && existingKey != repo.Key:
			_, err = tx.ExecContext(ctx, `
				UPDATE worker_repositories
				SET display_key = ?, worker_remote_identity = ?, retained_count = ?,
				    advertised = 1, dynamic = 0, updated_at = ?
				WHERE worker_id = ? AND repository_id = ?
			`, repo.Key, workerRemoteIdentity, effectiveRetainedCount, now, workerID, repositoryID)
		case mappingErr == nil:
			_, err = tx.ExecContext(ctx, `
				UPDATE worker_repositories
				SET worker_remote_identity = ?, retained_count = ?,
				    advertised = 1, dynamic = 0, updated_at = ?
				WHERE worker_id = ? AND repository_id = ?
			`, workerRemoteIdentity, effectiveRetainedCount, now, workerID, repositoryID)
		case errors.Is(mappingErr, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `
				INSERT INTO worker_repositories(
					worker_id, display_key, repository_id, worker_remote_identity,
					retained_count, advertised, dynamic, updated_at
				)
				VALUES (?, ?, ?, ?, ?, 1, 0, ?)
			`, workerID, repo.Key, repositoryID, workerRemoteIdentity, effectiveRetainedCount, now)
		default:
			err = mappingErr
		}
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
	}
	dynamicRows, err := tx.QueryContext(ctx, `
		SELECT repository_id
		FROM worker_repositories
		WHERE worker_id = ? AND dynamic = 1
	`, workerID)
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	var dynamicRepositoryIDs []string
	for dynamicRows.Next() {
		var repositoryID string
		if err := dynamicRows.Scan(&repositoryID); err != nil {
			dynamicRows.Close()
			return protocol.Worker{}, unavailable(err)
		}
		dynamicRepositoryIDs = append(dynamicRepositoryIDs, repositoryID)
	}
	if err := dynamicRows.Close(); err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	if err := dynamicRows.Err(); err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	for _, repositoryID := range dynamicRepositoryIDs {
		if input.AcceptsManagedRepositories {
			advertisedRepositoryIDs[repositoryID] = true
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE worker_repositories
			SET retained_count = ?, advertised = ?, updated_at = ?
			WHERE worker_id = ? AND repository_id = ? AND dynamic = 1
		`, retainedCounts[repositoryID], input.AcceptsManagedRepositories, now, workerID, repositoryID); err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		if seenManagedRepositoryIDs[repositoryID] || retainedCounts[repositoryID] > 0 {
			continue
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE worker_repositories
			SET advertised = 0, updated_at = ?
			WHERE worker_id = ?
			  AND repository_id = ?
			  AND dynamic = 1
			  AND retained_count = 0
			  AND NOT EXISTS (
			      SELECT 1
			      FROM executions execution
			      JOIN sessions session ON session.id = execution.session_id
			      WHERE execution.assigned_worker_id = ?
			        AND session.repository_id = ?
			        AND execution.state IN ('queued', 'preparing', 'running')
			  )
		`, now, workerID, repositoryID, workerID, repositoryID)
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		if released, err := result.RowsAffected(); err != nil {
			return protocol.Worker{}, unavailable(err)
		} else if released > 0 {
			delete(advertisedRepositoryIDs, repositoryID)
		}
	}
	canBulkAcknowledge := input.CapacityHandoffVersion == 0 && !hasUnattributedRetainedSummary
	for repositoryID := range retainedCounts {
		if !advertisedRepositoryIDs[repositoryID] {
			canBulkAcknowledge = false
		}
	}
	for _, attemptID := range disposedAttemptIDs {
		var ownerID string
		err := tx.QueryRowContext(ctx, `
			SELECT worker_id
			FROM attempts
			WHERE id = ?
		`, attemptID).Scan(&ownerID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err == nil && ownerID != workerID {
			return protocol.Worker{}, invalid(
				"invalid_disposed_attempts", "disposed attempts must exist and be owned by this worker")
		}
		if err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE attempts
			SET capacity_acknowledged = 1
			WHERE id = ?
		`, attemptID); err != nil {
			return protocol.Worker{}, unavailable(err)
		}
	}
	for repositoryID := range advertisedRepositoryIDs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE attempts
			SET capacity_acknowledged = 1
			WHERE worker_id = ?
			  AND capacity_acknowledged = 0
			  AND state IN ('succeeded', 'failed', 'cancelled', 'lost')
			  AND ? = 0
			  AND ?
			  AND execution_id IN (
			      SELECT e.id
			      FROM executions e
			      JOIN sessions session ON session.id = e.session_id
			      WHERE session.repository_id = ?
			  )
		`, workerID, input.ActiveCount, canBulkAcknowledge, repositoryID); err != nil {
			return protocol.Worker{}, unavailable(err)
		}
		for _, attemptID := range retainedAttemptIDs[repositoryID] {
			if _, err := tx.ExecContext(ctx, `
				UPDATE attempts
				SET capacity_acknowledged = 1
				WHERE id = ?
				  AND worker_id = ?
				  AND state IN ('succeeded', 'failed', 'cancelled', 'lost')
				  AND execution_id IN (
				      SELECT e.id
				      FROM executions e
				      JOIN sessions session ON session.id = e.session_id
				      WHERE session.repository_id = ?
				  )
			`, attemptID, workerID, repositoryID); err != nil {
				return protocol.Worker{}, unavailable(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	return s.Worker(ctx, workerID)
}

func normalizeWorkerLabels(input map[string]string) (map[string]string, error) {
	if len(input) > 20 {
		return nil, invalid("invalid_labels", "a Worker may advertise at most 20 labels")
	}
	labels := make(map[string]string, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedKey != key || len(trimmedKey) > 100 || len(trimmedValue) > 200 {
			return nil, invalid("invalid_labels", "Worker label keys must be trimmed and at most 100 bytes; values must be at most 200 bytes")
		}
		labels[trimmedKey] = trimmedValue
	}
	return labels, nil
}

func normalizeWorkerCapabilities(input protocol.WorkerRegistration) ([]protocol.Capability, error) {
	capabilities := append([]protocol.Capability(nil), input.Capabilities...)
	if len(capabilities) == 0 {
		status := protocol.CapabilityUnhealthy
		if input.Health == "healthy" {
			status = protocol.CapabilityReady
		}
		return []protocol.Capability{{
			Kind: protocol.CapabilityKindRuntime, Name: input.Runtime,
			Status: status, Version: input.RuntimeVersion,
		}}, nil
	}
	if len(capabilities) > 20 {
		return nil, invalid("invalid_capabilities", "a worker may advertise at most 20 capabilities")
	}
	seen := make(map[string]bool, len(capabilities))
	primaryFound := false
	for index := range capabilities {
		capability := &capabilities[index]
		capability.Kind = strings.ToLower(strings.TrimSpace(capability.Kind))
		capability.Name = strings.ToLower(strings.TrimSpace(capability.Name))
		capability.Status = strings.ToLower(strings.TrimSpace(capability.Status))
		capability.Version = strings.TrimSpace(capability.Version)
		capability.Message = strings.TrimSpace(capability.Message)
		if capability.Kind != protocol.CapabilityKindTool && capability.Kind != protocol.CapabilityKindRuntime {
			return nil, invalid("invalid_capabilities", "capability kind must be tool or runtime")
		}
		if capability.Kind == protocol.CapabilityKindRuntime && !protocol.SupportedRuntime(capability.Name) {
			return nil, invalid("invalid_capabilities", "runtime capabilities must be pi, codex, or claude-code")
		}
		if capability.Kind == protocol.CapabilityKindTool && capability.Name != "git" && capability.Name != "gh" {
			return nil, invalid("invalid_capabilities", "tool capabilities must be git or gh")
		}
		switch capability.Status {
		case protocol.CapabilityReady, protocol.CapabilityMissing,
			protocol.CapabilityUnauthenticated, protocol.CapabilityUnhealthy:
		default:
			return nil, invalid(
				"invalid_capabilities",
				"capability status must be ready, missing, unauthenticated, or unhealthy",
			)
		}
		if len(capability.Version) > 1024 || len(capability.Message) > 1024 {
			return nil, invalid("invalid_capabilities", "capability version and message must be at most 1024 bytes")
		}
		key := capability.Kind + ":" + capability.Name
		if seen[key] {
			return nil, invalid("invalid_capabilities", "capabilities must be unique by kind and name")
		}
		seen[key] = true
		primaryFound = primaryFound ||
			(capability.Kind == protocol.CapabilityKindRuntime && capability.Name == input.Runtime)
	}
	if !primaryFound {
		return nil, invalid("invalid_capabilities", "capabilities must include the worker primary runtime")
	}
	return capabilities, nil
}

func (s *Store) Workers(ctx context.Context) ([]protocol.Worker, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.name, w.labels_json, w.worker_version, w.runtime, w.runtime_version,
		       w.capacity, w.active_count, w.health, w.capabilities_json, w.source_access_json,
		       w.accepts_managed_repositories, w.managed_repository_ids_json,
		       w.retained_worktrees_json,
		       w.synthetic, w.registered_at, w.last_heartbeat,
		       COALESCE((
		           SELECT json_extract(run.task_snapshot, '$.name')
		           FROM executions e
		           JOIN sessions session ON session.id = e.session_id
		           JOIN runs run ON run.id = session.run_id
		           WHERE e.assigned_worker_id = w.id AND e.state IN ('preparing', 'running')
		           ORDER BY e.updated_at DESC, e.id DESC
		           LIMIT 1
		       ), '')
		FROM workers w ORDER BY w.registered_at, w.id
	`)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	var workers []protocol.Worker
	for rows.Next() {
		worker, err := scanWorker(rows, s.now())
		if err != nil {
			return nil, unavailable(err)
		}
		workers = append(workers, worker)
	}
	if err := rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return nil, unavailable(err)
	}
	for index := range workers {
		repos, err := s.workerRepositories(ctx, workers[index].ID)
		if err != nil {
			return nil, err
		}
		workers[index].Repositories = repos
	}
	return workers, nil
}

func (s *Store) WorkerSummaries(ctx context.Context) (protocol.WorkerSummaryPage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, runtime, capacity, active_count, health, synthetic, last_heartbeat
		FROM workers ORDER BY registered_at, id
	`)
	if err != nil {
		return protocol.WorkerSummaryPage{}, unavailable(err)
	}
	defer rows.Close()
	page := protocol.WorkerSummaryPage{Workers: make([]protocol.WorkerSummary, 0)}
	for rows.Next() {
		var worker protocol.WorkerSummary
		var synthetic int
		var heartbeat int64
		if err := rows.Scan(&worker.ID, &worker.Name, &worker.Runtime, &worker.Capacity,
			&worker.ActiveCount, &worker.Health, &synthetic, &heartbeat); err != nil {
			return protocol.WorkerSummaryPage{}, unavailable(err)
		}
		worker.LastHeartbeat = fromMillis(heartbeat)
		worker.Online = synthetic != 0 && worker.Health == "healthy" ||
			synthetic == 0 && s.now().Sub(worker.LastHeartbeat) <= protocol.WorkerOnlineWindow
		page.Workers = append(page.Workers, worker)
	}
	if err := rows.Err(); err != nil {
		return protocol.WorkerSummaryPage{}, unavailable(err)
	}
	return page, nil
}

func (s *Store) Worker(ctx context.Context, id string) (protocol.Worker, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT w.id, w.name, w.labels_json, w.worker_version, w.runtime, w.runtime_version,
		       w.capacity, w.active_count, w.health, w.capabilities_json, w.source_access_json,
		       w.accepts_managed_repositories, w.managed_repository_ids_json,
		       w.retained_worktrees_json,
		       w.synthetic, w.registered_at, w.last_heartbeat,
		       COALESCE((
		           SELECT json_extract(run.task_snapshot, '$.name')
		           FROM executions e
		           JOIN sessions session ON session.id = e.session_id
		           JOIN runs run ON run.id = session.run_id
		           WHERE e.assigned_worker_id = w.id AND e.state IN ('preparing', 'running')
		           ORDER BY e.updated_at DESC, e.id DESC
		           LIMIT 1
		       ), '')
		FROM workers w WHERE w.id = ?
	`, id)
	worker, err := scanWorker(row, s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Worker{}, ErrNotFound
	}
	if err != nil {
		return protocol.Worker{}, unavailable(err)
	}
	worker.Repositories, err = s.workerRepositories(ctx, id)
	return worker, err
}

type scanner interface {
	Scan(...any) error
}

func scanWorker(row scanner, now time.Time) (protocol.Worker, error) {
	var worker protocol.Worker
	var labels, capabilities, sourceAccess, managedRepositoryIDs, retained []byte
	var acceptsManagedRepositories, synthetic int
	var registered, heartbeat int64
	if err := row.Scan(&worker.ID, &worker.Name, &labels, &worker.WorkerVersion, &worker.Runtime, &worker.RuntimeVersion,
		&worker.Capacity, &worker.ActiveCount, &worker.Health, &capabilities, &sourceAccess,
		&acceptsManagedRepositories, &managedRepositoryIDs,
		&retained, &synthetic, &registered, &heartbeat,
		&worker.CurrentRunTitle); err != nil {
		return worker, err
	}
	if err := json.Unmarshal(labels, &worker.Labels); err != nil {
		return worker, err
	}
	if err := json.Unmarshal(capabilities, &worker.Capabilities); err != nil {
		return worker, err
	}
	if err := json.Unmarshal(sourceAccess, &worker.SourceAccess); err != nil {
		return worker, err
	}
	worker.AcceptsManagedRepositories = acceptsManagedRepositories != 0
	worker.Synthetic = synthetic != 0
	var repositoryIDs []string
	if err := json.Unmarshal(managedRepositoryIDs, &repositoryIDs); err != nil {
		return worker, err
	}
	worker.RepositoryCacheCount = len(repositoryIDs)
	if err := json.Unmarshal(retained, &worker.RetainedWorktrees); err != nil {
		return worker, err
	}
	worker.RegisteredAt = fromMillis(registered)
	worker.LastHeartbeat = fromMillis(heartbeat)
	worker.Online = worker.Synthetic && worker.Health == "healthy" ||
		!worker.Synthetic && now.Sub(worker.LastHeartbeat) <= protocol.WorkerOnlineWindow
	return worker, nil
}

func (s *Store) workerRepositories(ctx context.Context, workerID string) ([]protocol.Repository, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, wr.display_key,
		       COALESCE(NULLIF(wr.worker_remote_identity, ''), r.remote_identity),
		       wr.retained_count
		FROM worker_repositories wr JOIN repositories r ON r.id = wr.repository_id
		WHERE wr.worker_id = ? AND wr.advertised = 1 ORDER BY wr.display_key
	`, workerID)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	var repos []protocol.Repository
	for rows.Next() {
		var repo protocol.Repository
		if err := rows.Scan(&repo.ID, &repo.Key, &repo.RemoteIdentity, &repo.RetainedCount); err != nil {
			return nil, unavailable(err)
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

type runRoute struct {
	repositoryRemoteIdentity string
	sourceAccess             protocol.SourceAccess
}

type runRouteCandidate struct {
	workerID                   string
	repositoryID               string
	runtime                    string
	capacity                   int
	load                       int
	repositoryAdvertised       bool
	acceptsManagedRepositories bool
}

func runtimeCapabilityReady(capabilities []protocol.Capability, runtime string) bool {
	for _, capability := range capabilities {
		if capability.Kind == protocol.CapabilityKindRuntime && capability.Name == runtime &&
			capability.Status == protocol.CapabilityReady {
			return true
		}
	}
	return false
}

func preferredReadyRuntime(primary string, capabilities []protocol.Capability) string {
	if len(capabilities) == 0 {
		return primary
	}
	if runtimeCapabilityReady(capabilities, primary) {
		return primary
	}
	for _, capability := range capabilities {
		if capability.Kind == protocol.CapabilityKindRuntime && capability.Status == protocol.CapabilityReady {
			return capability.Name
		}
	}
	return ""
}

func normalizeRunRoute(route *runRoute) error {
	route.repositoryRemoteIdentity = normalizeRemote(route.repositoryRemoteIdentity)
	values, err := normalizeSourceAccess([]protocol.SourceAccess{route.sourceAccess})
	if err != nil {
		return err
	}
	if route.repositoryRemoteIdentity == "" || len(route.repositoryRemoteIdentity) > 2048 {
		return invalid(
			"invalid_route",
			"route repository_remote_identity is required and must be at most 2048 bytes",
		)
	}
	route.sourceAccess = values[0]
	if route.sourceAccess.Provider == "github" && route.sourceAccess.Hostname == "github.com" {
		canonical, err := normalizeManagedGitHubRemote(route.repositoryRemoteIdentity)
		if err != nil {
			return invalid(
				"invalid_route",
				"GitHub route repository_remote_identity must use github.com/owner/repository",
			)
		}
		route.repositoryRemoteIdentity = canonical
	}
	return nil
}

func hasSourceAccess(values []protocol.SourceAccess, required protocol.SourceAccess) bool {
	for _, value := range values {
		if value == required {
			return true
		}
	}
	return false
}

func betterRoute(candidate, current runRouteCandidate) bool {
	left := candidate.load * current.capacity
	right := current.load * candidate.capacity
	return left < right || (left == right && candidate.workerID < current.workerID)
}

func (s *Store) selectRunRoute(
	ctx context.Context,
	tx *sql.Tx,
	route runRoute,
	selectedRepositoryID string,
	now int64,
	requireSourceAccess bool,
	allowStaticRepository bool,
	workerID string,
	requiredRuntime string,
) (runRouteCandidate, error) {
	repositoryPredicate := "r.remote_identity = ?"
	repositoryLookup := route.repositoryRemoteIdentity
	if selectedRepositoryID != "" {
		repositoryPredicate = "r.id = ?"
		repositoryLookup = selectedRepositoryID
	} else if route.sourceAccess.Provider == "github" && route.sourceAccess.Hostname == "github.com" {
		repositoryPredicate = "lower(r.remote_identity) = lower(?)"
	}
	var repositoryID, repositoryIdentity string
	var repositoryEnabled int
	err := tx.QueryRowContext(ctx, `
		SELECT r.id, r.remote_identity, r.enabled
		FROM repositories r
		WHERE `+repositoryPredicate+`
		  AND (
		      r.enabled = 1
		      OR (? = 1 AND EXISTS (
		          SELECT 1 FROM worker_repositories available
		          WHERE available.repository_id = r.id
		            AND available.advertised = 1
		            AND available.dynamic = 0
		      ))
			  )
	`, repositoryLookup, allowStaticRepository).Scan(&repositoryID, &repositoryIdentity, &repositoryEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return runRouteCandidate{}, conflict(
			"repository_not_managed",
			"repository is not enabled in the control-plane managed repository catalog",
		)
	}
	if err != nil {
		return runRouteCandidate{}, unavailable(err)
	}
	workerRepositoryIdentity := route.repositoryRemoteIdentity
	if workerRepositoryIdentity == "" {
		workerRepositoryIdentity = repositoryIdentity
	}
	if canonical, normalizeErr := normalizeManagedGitHubRemote(workerRepositoryIdentity); normalizeErr == nil {
		workerRepositoryIdentity = canonical
	}
	requireAdvertisedRepository := allowStaticRepository &&
		(repositoryEnabled == 0 || route.sourceAccess.Provider != "github" || route.sourceAccess.Hostname != "github.com")
	rows, err := tx.QueryContext(ctx, `
		SELECT w.id, w.runtime, w.capabilities_json, w.capacity, w.active_count,
		       w.source_access_json, COALESCE(wr.advertised, 0),
		       w.accepts_managed_repositories,
		       COALESCE((
		           SELECT COUNT(*) FROM executions e
		           WHERE e.assigned_worker_id = w.id AND e.state = 'queued'
		       ), 0)
		FROM workers w
		LEFT JOIN worker_repositories wr
		  ON wr.worker_id = w.id AND wr.repository_id = ?
		WHERE w.health = 'healthy'
		  AND w.synthetic = 0
		  AND w.claim_protocol_version = ?
		  AND w.last_heartbeat >= ?
		  AND (? = '' OR w.id = ?)
		  AND (? = 0 OR COALESCE(wr.advertised, 0) = 1)
		  AND (
		      COALESCE(wr.advertised, 0) = 1
		      OR NOT EXISTS (
		          SELECT 1
		          FROM worker_repositories display_key_conflict
		          WHERE display_key_conflict.worker_id = w.id
		            AND display_key_conflict.display_key = ?
		            AND display_key_conflict.repository_id != ?
		      )
		  )
		  AND (
		      COALESCE(wr.advertised, 0) = 1
		      OR (
		          w.accepts_managed_repositories = 1
		          AND (
		              EXISTS (
		                  SELECT 1
		                  FROM json_each(w.managed_repository_ids_json) cached_repository
		                  WHERE cached_repository.value = ?
		              )
		              OR json_array_length(w.managed_repository_ids_json) + (
		                  SELECT COUNT(*)
		                  FROM worker_repositories reserved_repository
		                  WHERE reserved_repository.worker_id = w.id
		                    AND reserved_repository.dynamic = 1
		                    AND reserved_repository.advertised = 1
		                    AND NOT EXISTS (
		                        SELECT 1
		                        FROM json_each(w.managed_repository_ids_json) cached_repository
		                        WHERE cached_repository.value = reserved_repository.repository_id
		                    )
		              ) < ?
		          )
		      )
		  )
		  AND COALESCE(wr.retained_count, 0) + (
		      SELECT COUNT(*)
		      FROM attempts active_attempt
		      JOIN executions active_execution ON active_execution.id = active_attempt.execution_id
		      JOIN sessions active_session ON active_session.id = active_execution.session_id
		      WHERE active_attempt.worker_id = w.id
		        AND active_session.repository_id = ?
		        AND active_attempt.state IN ('preparing', 'running')
		  ) + (
		      SELECT COUNT(*)
		      FROM attempts terminal_attempt
		      JOIN executions terminal_execution ON terminal_execution.id = terminal_attempt.execution_id
		      JOIN sessions terminal_session ON terminal_session.id = terminal_execution.session_id
		      WHERE terminal_attempt.worker_id = w.id
		        AND terminal_session.repository_id = ?
		        AND terminal_attempt.state IN ('succeeded', 'failed', 'cancelled', 'lost')
		        AND terminal_attempt.capacity_acknowledged = 0
		  ) < ?
		ORDER BY w.id
	`, repositoryID, protocol.ClaimProtocolVersion,
		now-protocol.WorkerOnlineWindow.Milliseconds(), workerID, workerID,
		requireAdvertisedRepository,
		workerRepositoryIdentity, repositoryID,
		repositoryID, protocol.MaxRepositoryCacheEntries,
		repositoryID, repositoryID, protocol.MaxRetainedPerRepo)
	if err != nil {
		return runRouteCandidate{}, unavailable(err)
	}
	defer rows.Close()
	var best runRouteCandidate
	found := false
	for rows.Next() {
		var candidate runRouteCandidate
		var active, queued, repositoryAdvertised, acceptsManagedRepositories int
		var primaryRuntime string
		var encoded, encodedCapabilities []byte
		if err := rows.Scan(
			&candidate.workerID, &primaryRuntime, &encodedCapabilities, &candidate.capacity,
			&active, &encoded, &repositoryAdvertised, &acceptsManagedRepositories, &queued,
		); err != nil {
			rows.Close()
			return runRouteCandidate{}, unavailable(err)
		}
		candidate.repositoryID = repositoryID
		candidate.repositoryAdvertised = repositoryAdvertised != 0
		candidate.acceptsManagedRepositories = acceptsManagedRepositories != 0
		var access []protocol.SourceAccess
		if err := json.Unmarshal(encoded, &access); err != nil {
			return runRouteCandidate{}, unavailable(err)
		}
		var capabilities []protocol.Capability
		if err := json.Unmarshal(encodedCapabilities, &capabilities); err != nil {
			return runRouteCandidate{}, unavailable(err)
		}
		candidate.runtime = requiredRuntime
		if candidate.runtime == "" {
			candidate.runtime = preferredReadyRuntime(primaryRuntime, capabilities)
		}
		if len(capabilities) != 0 && !runtimeCapabilityReady(capabilities, candidate.runtime) {
			continue
		}
		if requireSourceAccess && !(allowStaticRepository && candidate.repositoryAdvertised) &&
			!hasSourceAccess(access, route.sourceAccess) {
			continue
		}
		candidate.load = active + queued
		if !found || betterRoute(candidate, best) {
			best = candidate
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return runRouteCandidate{}, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return runRouteCandidate{}, unavailable(err)
	}
	if !found {
		message := "no healthy online worker can acquire the repository and access its source provider"
		if !requireSourceAccess {
			message = "no healthy online worker can acquire the repository"
		}
		return runRouteCandidate{}, conflict(
			"no_eligible_worker",
			message,
		)
	}
	if !best.repositoryAdvertised {
		if !best.acceptsManagedRepositories {
			return runRouteCandidate{}, unavailable(errors.New("selected worker cannot acquire managed repositories"))
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO worker_repositories(
				worker_id, display_key, repository_id, worker_remote_identity,
				retained_count, advertised, dynamic, updated_at
			)
			VALUES (?, ?, ?, ?, 0, 1, 1, ?)
			ON CONFLICT(worker_id, repository_id) DO UPDATE SET
				display_key=excluded.display_key,
				worker_remote_identity=excluded.worker_remote_identity,
				advertised=1,
				dynamic=1,
				updated_at=excluded.updated_at
		`, best.workerID, workerRepositoryIdentity, repositoryID, workerRepositoryIdentity, now); err != nil {
			return runRouteCandidate{}, unavailable(err)
		}
	}
	return best, nil
}

func (s *Store) selectSessionRoute(
	ctx context.Context,
	tx *sql.Tx,
	repositoryID string,
	repositoryIdentity string,
	now int64,
	workerID string,
	requiredRuntime string,
) (runRouteCandidate, error) {
	var currentIdentity string
	var enabled, centrallyManaged int
	err := tx.QueryRowContext(ctx, `
		SELECT remote_identity, enabled, centrally_managed FROM repositories WHERE id = ?
	`, repositoryID).Scan(&currentIdentity, &enabled, &centrallyManaged)
	if errors.Is(err, sql.ErrNoRows) {
		return runRouteCandidate{}, conflict("repository_not_available", "repository is not configured on a Worker or enabled for managed acquisition")
	}
	if err != nil {
		return runRouteCandidate{}, unavailable(err)
	}
	if repositoryIdentity == "" {
		repositoryIdentity = currentIdentity
	}
	route := runRoute{
		repositoryRemoteIdentity: repositoryIdentity,
		sourceAccess:             protocol.SourceAccess{Provider: "local", Hostname: "localhost"},
	}
	if centrallyManaged != 0 && enabled == 0 {
		return runRouteCandidate{}, conflict(
			"repository_not_managed", "repository is not enabled in the control-plane managed repository catalog",
		)
	}
	requireSourceAccess := false
	if _, githubErr := normalizeManagedGitHubRemote(repositoryIdentity); centrallyManaged != 0 && githubErr == nil {
		route.sourceAccess = protocol.SourceAccess{Provider: "github", Hostname: "github.com"}
		requireSourceAccess = true
	}
	if err := normalizeRunRoute(&route); err != nil {
		return runRouteCandidate{}, err
	}
	return s.selectRunRoute(
		ctx, tx, route, repositoryID, now, requireSourceAccess, true, workerID, requiredRuntime,
	)
}

func serviceErrorCode(err error, code string) bool {
	var serviceErr *ServiceError
	return errors.As(err, &serviceErr) && serviceErr.Code == code
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func fromMillis(value int64) time.Time { return time.UnixMilli(value).UTC() }

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	v := fromMillis(value.Int64)
	return &v
}

func scanAttempt(row scanner) (protocol.Attempt, error) {
	var value protocol.Attempt
	var expiry, created int64
	var supervisor, group sql.NullInt64
	var identity, result, failure sql.NullString
	var started, completed sql.NullInt64
	err := row.Scan(&value.ID, &value.ExecutionID, &value.WorkerID, &value.AttemptNumber,
		&value.State, &expiry, &supervisor, &identity, &group, &result, &failure,
		&started, &completed, &created)
	if supervisor.Valid {
		value.SupervisorPID = &supervisor.Int64
	}
	if group.Valid {
		value.ProcessGroupID = &group.Int64
	}
	value.ProcessIdentity, value.Result, value.Error = identity.String, result.String, failure.String
	value.LeaseExpiresAt, value.CreatedAt = fromMillis(expiry), fromMillis(created)
	value.StartedAt, value.CompletedAt = nullableTime(started), nullableTime(completed)
	return value, err
}

func unavailable(err error) error {
	return &ServiceError{Code: "storage_unavailable", Message: "control-plane storage is unavailable", Status: 503, Err: err}
}

func equalDigest(a, b []byte) bool { return bytes.Equal(a, b) }
