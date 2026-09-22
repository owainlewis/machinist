package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/owainlewis/machinist/internal/artifacts"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

var ErrArtifactInvalid = errors.New("invalid artifact")
var ErrArtifactConflict = errors.New("artifact path already has different content")
var ErrArtifactExpired = errors.New("artifact has expired")

const artifactColumns = "id,job_id,run_id,path,content_type,size,checksum,created_at,expired_at,storage_key"

type scanner interface{ Scan(...any) error }

func scanArtifact(row scanner) (a protocol.Artifact, key string, err error) {
	var created string
	var expired sql.NullString
	err = row.Scan(&a.ID, &a.JobID, &a.RunID, &a.Path, &a.ContentType, &a.Size, &a.Checksum, &created, &expired, &key)
	if err != nil {
		return
	}
	a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if expired.Valid {
		t, e := time.Parse(time.RFC3339Nano, expired.String)
		if e != nil {
			err = e
		}
		a.ExpiredAt = &t
	}
	return
}
func (s *Store) configureStorage(c config.ResolvedStorage) error {
	storage, err := artifacts.NewFilesystem(c.Path)
	if err != nil {
		return err
	}
	s.artifactStore = storage
	s.storageConfig = c
	return nil
}
func checkArtifactLease(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, run, instance, token string, now time.Time) (string, error) {
	var job, state, owner, lease string
	var expiry sql.NullInt64
	err := q.QueryRowContext(ctx, "SELECT job_id,state,COALESCE(worker_instance,''),COALESCE(lease_token,''),lease_expires_at FROM runs WHERE id=?", run).Scan(&job, &state, &owner, &lease, &expiry)
	if err != nil {
		return "", err
	}
	if state != "running" || !expiry.Valid || expiry.Int64 <= now.UnixNano() {
		return "", ErrRunState
	}
	if owner != instance || lease != token {
		return "", ErrLeaseConflict
	}
	return job, nil
}
func (s *Store) PublishArtifact(ctx context.Context, run, instance, token, name string, reader io.Reader) (protocol.Artifact, error) {
	var empty protocol.Artifact
	if !artifacts.ValidPath(name) {
		return empty, fmt.Errorf("%w: invalid path", ErrArtifactInvalid)
	}
	if _, err := checkArtifactLease(ctx, s.db, run, instance, token, s.now()); err != nil {
		return empty, err
	}
	pending, err := s.artifactStore.Stage(reader, s.storageConfig.MaxFileBytes)
	if err != nil {
		return empty, err
	}
	defer pending.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	job, err := checkArtifactLease(ctx, tx, run, instance, token, s.now())
	if err != nil {
		return empty, err
	}
	existing, _, err := scanArtifact(tx.QueryRowContext(ctx, "SELECT "+artifactColumns+" FROM artifacts WHERE run_id=? AND path=?", run, name))
	if err == nil {
		if existing.Checksum != pending.Checksum || existing.Size != pending.Size {
			return empty, ErrArtifactConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var size, count int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0),COUNT(*) FROM artifacts WHERE run_id=?", run).Scan(&size, &count); err != nil {
		return empty, err
	}
	if count >= 1000 || pending.Size > s.storageConfig.MaxRunBytes-size {
		return empty, fmt.Errorf("%w: run output limit exceeded", ErrArtifactInvalid)
	}
	f, err := os.Open(pending.Path)
	if err != nil {
		return empty, err
	}
	head := make([]byte, 512)
	n, readErr := f.Read(head)
	f.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return empty, readErr
	}
	kind := http.DetectContentType(head[:n])
	// The MIME type is metadata, never permission to render active content.
	if ext := mime.TypeByExtension(path.Ext(name)); ext != "" {
		kind = ext
	}
	sum := sha256.Sum256([]byte(run + "\x00" + name + "\x00" + pending.Checksum))
	id := "artifact_" + hex.EncodeToString(sum[:])
	key := job + "/" + run + "/" + id
	if err = s.artifactStore.Publish(pending, key); err != nil {
		return empty, err
	}
	a := protocol.Artifact{ID: id, JobID: job, RunID: run, Path: name, ContentType: kind, Size: pending.Size, Checksum: pending.Checksum, CreatedAt: s.now().UTC()}
	_, err = tx.ExecContext(ctx, "INSERT INTO artifacts(id,job_id,run_id,path,content_type,size,checksum,created_at,storage_key) VALUES(?,?,?,?,?,?,?,?,?)", a.ID, job, run, name, kind, a.Size, a.Checksum, a.CreatedAt.Format(time.RFC3339Nano), key)
	if err != nil {
		return empty, err
	}
	return a, tx.Commit()
}
func (s *Store) ListArtifacts(ctx context.Context, job string) ([]protocol.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+artifactColumns+" FROM artifacts WHERE job_id=? ORDER BY created_at,id", job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []protocol.Artifact{}
	for rows.Next() {
		a, _, e := scanArtifact(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
func (s *Store) OpenArtifact(ctx context.Context, id string) (protocol.Artifact, *os.File, error) {
	a, key, err := scanArtifact(s.db.QueryRowContext(ctx, "SELECT "+artifactColumns+" FROM artifacts WHERE id=?", id))
	if err != nil {
		return a, nil, err
	}
	if a.ExpiredAt != nil {
		return a, nil, ErrArtifactExpired
	}
	f, err := s.artifactStore.Open(key)
	return a, f, err
}

// Tombstone first so retries cannot bind files while cleanup removes them.
// Keep metadata and retry failed deletes on the next maintenance pass.
func (s *Store) ExpireArtifacts(ctx context.Context) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE artifacts SET expired_at=? WHERE expired_at IS NULL AND
 (NOT EXISTS(SELECT 1 FROM jobs WHERE jobs.id=artifacts.job_id) OR EXISTS(SELECT 1 FROM jobs WHERE jobs.id=artifacts.job_id AND state IN ('succeeded','failed','cancelled') AND julianday(updated_at)<=julianday(?)))`, now.Format(time.RFC3339Nano), now.Add(-s.storageConfig.Retention).Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,storage_key FROM artifacts WHERE expired_at IS NOT NULL AND deleted=0 LIMIT 1000")
	if err != nil {
		return err
	}
	keys := map[string]string{}
	for rows.Next() {
		var id, key string
		if err = rows.Scan(&id, &key); err != nil {
			rows.Close()
			return err
		}
		keys[id] = key
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, key := range keys {
		if err = s.artifactStore.Delete(key); err != nil {
			return fmt.Errorf("delete expired artifact: %w", err)
		}
		if _, err = s.db.ExecContext(ctx, "UPDATE artifacts SET deleted=1 WHERE id=?", id); err != nil {
			return err
		}
	}
	return nil
}
