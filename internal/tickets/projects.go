package tickets

import (
	"context"
	"time"
)

// Project is a repository someone added in the web app. Repositories in
// config.yaml are projects too, but they live in the file, not here.
type Project struct {
	Repo    string    `json:"repo"`
	AddedBy string    `json:"added_by"`
	AddedAt time.Time `json:"added_at"`
}

// Projects lists the repositories added in the web app, by name.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo,added_by,added_at FROM projects ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := []Project{}
	for rows.Next() {
		var project Project
		var added string
		if err := rows.Scan(&project.Repo, &project.AddedBy, &added); err != nil {
			return nil, err
		}
		if at, err := time.Parse(time.RFC3339Nano, added); err == nil {
			project.AddedAt = at
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

// AddProject adds a repository. Adding one that is already there does nothing.
func (s *Store) AddProject(ctx context.Context, repo, by string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(repo,added_by,added_at) VALUES(?,?,?) ON CONFLICT(repo) DO NOTHING`, repo, by, stamp(s.now()))
	return err
}

// RemoveProject removes a repository added in the web app. It reports whether
// there was one to remove.
func (s *Store) RemoveProject(ctx context.Context, repo string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE repo=?`, repo)
	if err != nil {
		return false, err
	}
	removed, err := result.RowsAffected()
	return removed > 0, err
}
