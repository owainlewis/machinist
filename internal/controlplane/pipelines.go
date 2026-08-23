package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

var pipelineVariablePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

var supportedPipelineVariables = map[string]bool{
	"task.id": true, "task.name": true, "task.prompt": true,
	"run.id": true, "repository": true, "branch": true,
}

func normalizePipeline(input protocol.SavePipelineRequest) (string, []protocol.PipelineStage, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 200 {
		return "", nil, invalid("invalid_pipeline_name", "name is required and limited to 200 characters")
	}
	if len(input.Stages) < 1 || len(input.Stages) > protocol.MaxPipelineStages {
		return "", nil, invalid("invalid_pipeline_stages", "a Pipeline must contain 1 through 20 stages")
	}
	stages := make([]protocol.PipelineStage, len(input.Stages))
	for position, stage := range input.Stages {
		stage.Position = position
		stage.Name = strings.TrimSpace(stage.Name)
		stage.Prompt = strings.TrimSpace(stage.Prompt)
		if stage.Name == "" || len([]byte(stage.Name)) > 200 {
			return "", nil, invalid("invalid_pipeline_stage_name", "each stage name is required and limited to 200 bytes")
		}
		if stage.Prompt == "" || len([]byte(stage.Prompt)) > protocol.MaxTaskPromptBytes {
			return "", nil, invalid("invalid_pipeline_stage_prompt", "each stage prompt is required and limited to 64 KiB")
		}
		for _, match := range pipelineVariablePattern.FindAllStringSubmatch(stage.Prompt, -1) {
			variable := strings.TrimSpace(match[1])
			if !supportedPipelineVariables[variable] {
				return "", nil, invalid("unknown_pipeline_variable", "unsupported Pipeline variable: "+variable)
			}
		}
		stages[position] = stage
	}
	return name, stages, nil
}

func (s *Store) CreatePipeline(ctx context.Context, input protocol.SavePipelineRequest) (protocol.Pipeline, error) {
	name, stages, err := normalizePipeline(input)
	if err != nil {
		return protocol.Pipeline{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Pipeline{}, unavailable(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipelines`).Scan(&count); err != nil {
		return protocol.Pipeline{}, unavailable(err)
	}
	if count >= protocol.MaxPipelines {
		return protocol.Pipeline{}, conflict("pipeline_limit_reached", "Factory is limited to 200 Pipelines")
	}
	id, err := newID()
	if err != nil {
		return protocol.Pipeline{}, unavailable(err)
	}
	now := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipelines(id, name, name_key, generation, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		id, name, normalizeTitleKey(name), now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.Pipeline{}, conflict("pipeline_name_conflict", "a Pipeline with this name already exists")
		}
		return protocol.Pipeline{}, unavailable(err)
	}
	if err := replacePipelineStages(ctx, tx, id, stages); err != nil {
		return protocol.Pipeline{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Pipeline{}, unavailable(err)
	}
	return s.Pipeline(ctx, id)
}

func (s *Store) UpdatePipeline(ctx context.Context, id string, input protocol.SavePipelineRequest) (protocol.Pipeline, error) {
	name, stages, err := normalizePipeline(input)
	if err != nil {
		return protocol.Pipeline{}, err
	}
	if input.ExpectedGeneration < 1 {
		return protocol.Pipeline{}, invalid("pipeline_generation_required", "expected_generation is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Pipeline{}, unavailable(err)
	}
	defer tx.Rollback()
	now := s.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE pipelines SET name = ?, name_key = ?, generation = generation + 1, updated_at = ? WHERE id = ? AND generation = ?`,
		name, normalizeTitleKey(name), now, id, input.ExpectedGeneration)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return protocol.Pipeline{}, conflict("pipeline_name_conflict", "a Pipeline with this name already exists")
		}
		return protocol.Pipeline{}, unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipelines WHERE id = ?`, id).Scan(&exists); err != nil {
			return protocol.Pipeline{}, unavailable(err)
		}
		if exists == 0 {
			return protocol.Pipeline{}, ErrNotFound
		}
		return protocol.Pipeline{}, conflict("pipeline_generation_conflict", "the Pipeline changed; refresh and try again")
	}
	if err := replacePipelineStages(ctx, tx, id, stages); err != nil {
		return protocol.Pipeline{}, err
	}
	if err := tx.Commit(); err != nil {
		return protocol.Pipeline{}, unavailable(err)
	}
	return s.Pipeline(ctx, id)
}

func (s *Store) DeletePipeline(ctx context.Context, id string) error {
	if id == protocol.DefaultPipelineID {
		return conflict("pipeline_delete_not_allowed", "the built-in Single agent Pipeline cannot be deleted")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return unavailable(err)
	}
	defer tx.Rollback()
	var taskCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE pipeline_id = ?`, id).Scan(&taskCount); err != nil {
		return unavailable(err)
	}
	if taskCount != 0 {
		return conflict("pipeline_in_use", "remove this Pipeline from its Tasks before deleting it")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM pipelines WHERE id = ?`, id)
	if err != nil {
		return unavailable(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return unavailable(err)
	}
	return nil
}

func replacePipelineStages(ctx context.Context, tx *sql.Tx, id string, stages []protocol.PipelineStage) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_stages WHERE pipeline_id = ?`, id); err != nil {
		return unavailable(err)
	}
	for _, stage := range stages {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_stages(pipeline_id, position, name, prompt) VALUES (?, ?, ?, ?)`,
			id, stage.Position, stage.Name, stage.Prompt); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

func (s *Store) Pipelines(ctx context.Context) (protocol.PipelinePage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, generation, created_at, updated_at
		FROM pipelines ORDER BY updated_at DESC, id DESC
	`)
	if err != nil {
		return protocol.PipelinePage{}, unavailable(err)
	}
	page := protocol.PipelinePage{Pipelines: make([]protocol.Pipeline, 0)}
	byID := make(map[string]int)
	for rows.Next() {
		var pipeline protocol.Pipeline
		var created, updated int64
		if err := rows.Scan(&pipeline.ID, &pipeline.Name, &pipeline.Generation, &created, &updated); err != nil {
			rows.Close()
			return protocol.PipelinePage{}, unavailable(err)
		}
		pipeline.CreatedAt, pipeline.UpdatedAt = fromMillis(created), fromMillis(updated)
		byID[pipeline.ID] = len(page.Pipelines)
		page.Pipelines = append(page.Pipelines, pipeline)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return protocol.PipelinePage{}, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return protocol.PipelinePage{}, unavailable(err)
	}
	stageRows, err := s.db.QueryContext(ctx, `
		SELECT pipeline_id, position, name FROM pipeline_stages ORDER BY pipeline_id, position
	`)
	if err != nil {
		return protocol.PipelinePage{}, unavailable(err)
	}
	for stageRows.Next() {
		var pipelineID string
		var stage protocol.PipelineStage
		if err := stageRows.Scan(&pipelineID, &stage.Position, &stage.Name); err != nil {
			stageRows.Close()
			return protocol.PipelinePage{}, unavailable(err)
		}
		if index, ok := byID[pipelineID]; ok {
			page.Pipelines[index].Stages = append(page.Pipelines[index].Stages, stage)
		}
	}
	if err := stageRows.Err(); err != nil {
		stageRows.Close()
		return protocol.PipelinePage{}, unavailable(err)
	}
	if err := stageRows.Close(); err != nil {
		return protocol.PipelinePage{}, unavailable(err)
	}
	return page, nil
}

func (s *Store) Pipeline(ctx context.Context, id string) (protocol.Pipeline, error) {
	var pipeline protocol.Pipeline
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, generation, created_at, updated_at FROM pipelines WHERE id = ?`, id).
		Scan(&pipeline.ID, &pipeline.Name, &pipeline.Generation, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return pipeline, ErrNotFound
	}
	if err != nil {
		return pipeline, unavailable(err)
	}
	pipeline.CreatedAt, pipeline.UpdatedAt = fromMillis(created), fromMillis(updated)
	rows, err := s.db.QueryContext(ctx, `SELECT position, name, prompt FROM pipeline_stages WHERE pipeline_id = ? ORDER BY position`, id)
	if err != nil {
		return pipeline, unavailable(err)
	}
	for rows.Next() {
		var stage protocol.PipelineStage
		if err := rows.Scan(&stage.Position, &stage.Name, &stage.Prompt); err != nil {
			rows.Close()
			return pipeline, unavailable(err)
		}
		pipeline.Stages = append(pipeline.Stages, stage)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return pipeline, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return pipeline, unavailable(err)
	}
	return pipeline, nil
}

func loadPipelineSnapshot(ctx context.Context, tx *sql.Tx, id string) (protocol.PipelineSnapshot, error) {
	if strings.TrimSpace(id) == "" {
		id = protocol.DefaultPipelineID
	}
	var snapshot protocol.PipelineSnapshot
	if err := tx.QueryRowContext(ctx, `SELECT id, name, generation FROM pipelines WHERE id = ?`, id).
		Scan(&snapshot.ID, &snapshot.Name, &snapshot.Generation); errors.Is(err, sql.ErrNoRows) {
		return snapshot, invalid("pipeline_not_found", "the selected Pipeline does not exist")
	} else if err != nil {
		return snapshot, unavailable(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT position, name, prompt FROM pipeline_stages WHERE pipeline_id = ? ORDER BY position`, id)
	if err != nil {
		return snapshot, unavailable(err)
	}
	for rows.Next() {
		var stage protocol.PipelineStage
		if err := rows.Scan(&stage.Position, &stage.Name, &stage.Prompt); err != nil {
			rows.Close()
			return snapshot, unavailable(err)
		}
		snapshot.Stages = append(snapshot.Stages, stage)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snapshot, unavailable(err)
	}
	if err := rows.Close(); err != nil {
		return snapshot, unavailable(err)
	}
	if len(snapshot.Stages) == 0 {
		return snapshot, conflict("pipeline_empty", "the selected Pipeline has no stages")
	}
	return snapshot, nil
}

func renderPipelinePrompt(template string, values map[string]string) string {
	return pipelineVariablePattern.ReplaceAllStringFunc(template, func(match string) string {
		parts := pipelineVariablePattern.FindStringSubmatch(match)
		return values[strings.TrimSpace(parts[1])]
	})
}
