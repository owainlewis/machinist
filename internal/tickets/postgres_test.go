package tickets

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func openPostgresTestStore(t *testing.T, pgURL string) *Store {
	t.Helper()
	store, _ := openPostgresTestStoreWithDSN(t, pgURL)
	return store
}

func openPostgresTestStoreWithDSN(t *testing.T, pgURL string) (*Store, string) {
	t.Helper()
	suffix, err := randomHex(8)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "test_" + suffix

	adminCfg, err := pgx.ParseConfig(strings.TrimSpace(pgURL))
	if err != nil {
		t.Fatalf("parse TEST_POSTGRES_URL: %v", err)
	}
	adminCfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	adminDB := stdlib.OpenDB(*adminCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := adminDB.ExecContext(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		_ = adminDB.Close()
		t.Fatalf("CREATE SCHEMA %s: %v", schemaName, err)
	}

	parsed, err := url.Parse(strings.TrimSpace(pgURL))
	if err != nil {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("url.Parse TEST_POSTGRES_URL: %v", err)
	}
	q := parsed.Query()
	q.Set("search_path", schemaName)
	parsed.RawQuery = q.Encode()
	testDSN := parsed.String()

	store, err := Open(testDSN)
	if err != nil {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		_ = adminDB.Close()
		t.Fatalf("Open postgres schema %s: %v", schemaName, err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dropCancel()
		_, _ = adminDB.ExecContext(dropCtx, "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE")
		_ = adminDB.Close()
	})
	return store, testDSN
}

func TestPostgresMaxOpenConnsValidation(t *testing.T) {
	t.Setenv(DatabaseMaxOpenConnsEnv, "")
	if n, err := postgresMaxOpenConns(); err != nil || n != defaultPostgresMaxOpenConns {
		t.Fatalf("default = (%d, %v), want (%d, nil)", n, err, defaultPostgresMaxOpenConns)
	}
	t.Setenv(DatabaseMaxOpenConnsEnv, "16")
	if n, err := postgresMaxOpenConns(); err != nil || n != 16 {
		t.Fatalf("valid = (%d, %v), want (16, nil)", n, err)
	}
	for _, bad := range []string{"0", "-5", "101", "abc"} {
		t.Setenv(DatabaseMaxOpenConnsEnv, bad)
		if _, err := postgresMaxOpenConns(); err == nil {
			t.Fatalf("expected error for %s=%q", DatabaseMaxOpenConnsEnv, bad)
		}
	}
}

func TestPostgresMigrationsIdempotent(t *testing.T) {
	pgURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_URL"))
	if pgURL == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	store1, testDSN := openPostgresTestStoreWithDSN(t, pgURL)
	store2, err := Open(testDSN)
	if err != nil {
		t.Fatalf("second Open on same schema: %v", err)
	}
	defer store2.Close()

	var version int
	if err := store1.q().QueryRowContext(t.Context(), `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	want := postgresMigrations[len(postgresMigrations)-1].version
	if version != want {
		t.Fatalf("schema_migrations version = %d, want %d", version, want)
	}
}

func TestConcurrentClaimRunNoDuplicates(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	const totalRuns = 20
	wantIDs := map[string]bool{}
	for i := range totalRuns {
		ticket := createTicket(t, store, fmt.Sprintf("claim:%d", i))
		run, err := store.QueueRun(ctx, ticket.ID, "codex")
		if err != nil {
			t.Fatalf("QueueRun %d: %v", i, err)
		}
		wantIDs[run.ID] = true
	}

	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for g := range 8 {
		wg.Add(1)
		go func(runner string) {
			defer wg.Done()
			for {
				claim, err := store.ClaimRun(ctx, runner, []string{"codex"}, time.Now().Add(time.Minute), nil)
				if err != nil {
					errCh <- err
					return
				}
				if claim == nil {
					return
				}
				mu.Lock()
				claimed[claim.Run.ID]++
				mu.Unlock()
			}
		}(fmt.Sprintf("runner-%d", g))
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("ClaimRun error: %v", err)
	}
	if len(claimed) != totalRuns {
		t.Fatalf("claimed %d unique runs, want %d", len(claimed), totalRuns)
	}
	for id, count := range claimed {
		if !wantIDs[id] || count != 1 {
			t.Fatalf("run %s claimed %d times (expected=%v)", id, count, wantIDs[id])
		}
	}
}

func TestConcurrentCreateTicketDeduplication(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	t.Run("same source key", func(t *testing.T) {
		var createdCount atomic.Int32
		var ids sync.Map
		var wg sync.WaitGroup
		errCh := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tk, created, err := store.CreateTicket(ctx, request("concurrent:same-key"))
				if err != nil {
					errCh <- err
					return
				}
				if created {
					createdCount.Add(1)
				}
				ids.Store(tk.ID, true)
			}()
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatalf("CreateTicket error: %v", err)
		}
		if createdCount.Load() != 1 {
			t.Fatalf("createdCount = %d, want 1", createdCount.Load())
		}
		uniqueIDs := 0
		ids.Range(func(_, _ any) bool { uniqueIDs++; return true })
		if uniqueIDs != 1 {
			t.Fatalf("unique ticket IDs = %d, want 1", uniqueIDs)
		}
	})

	t.Run("same schedule", func(t *testing.T) {
		var createdCount atomic.Int32
		var ids sync.Map
		var wg sync.WaitGroup
		errCh := make(chan error, 8)
		for i := range 8 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				req := request(fmt.Sprintf("sched:key:%d", idx))
				req.Source = "schedule:nightly"
				tk, created, err := store.CreateTicket(ctx, req)
				if err != nil {
					errCh <- err
					return
				}
				if created {
					createdCount.Add(1)
				}
				ids.Store(tk.ID, true)
			}(i)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatalf("CreateTicket schedule error: %v", err)
		}
		if createdCount.Load() != 1 {
			t.Fatalf("schedule createdCount = %d, want 1", createdCount.Load())
		}
		uniqueIDs := 0
		ids.Range(func(_, _ any) bool { uniqueIDs++; return true })
		if uniqueIDs != 1 {
			t.Fatalf("unique schedule ticket IDs = %d, want 1", uniqueIDs)
		}
	})
}

func TestConcurrentConsumeOAuthTransactionSingleUse(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	secret, err := store.CreateOAuthTransaction(ctx, "github", "state-1", "verifier-1", "/settings", "")
	if err != nil {
		t.Fatalf("CreateOAuthTransaction: %v", err)
	}
	var succeeded atomic.Int32
	var notFound atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ConsumeOAuthTransaction(ctx, secret)
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrNotFound):
				notFound.Add(1)
			default:
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("ConsumeOAuthTransaction error: %v", err)
	}
	if succeeded.Load() != 1 || notFound.Load() != 7 {
		t.Fatalf("succeeded=%d notFound=%d, want 1 and 7", succeeded.Load(), notFound.Load())
	}
}

func TestConcurrentCompleteRunSingleTransition(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	ticket := createTicket(t, store, "complete:race")
	claim := claimRun(t, store, ticket)

	var succeeded atomic.Int32
	var notActive atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, transitioned, err := store.CompleteRun(ctx, RunCompletion{
				RunID:  claim.Run.ID,
				Status: RunSucceeded,
			}, func(_ Ticket, _ string) Transition {
				return Transition{State: StateDone, Message: "Finished", Report: "Done"}
			})
			switch {
			case err == nil && transitioned:
				succeeded.Add(1)
			case errors.Is(err, ErrRunNotActive):
				notActive.Add(1)
			default:
				errCh <- fmt.Errorf("unexpected result: transitioned=%v err=%v", transitioned, err)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if succeeded.Load() != 1 || notActive.Load() != 5 {
		t.Fatalf("succeeded=%d notActive=%d, want 1 and 5", succeeded.Load(), notActive.Load())
	}
	entries, err := store.Entries(ctx, ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for _, e := range entries {
		if e.Kind == EntryReport {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("report entries = %d, want 1", reports)
	}
}

func TestConcurrentUpdateUserLastAdminPreserved(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	r1, err := store.ResolveGitHubLogin(ctx, GitHubProfile{ID: "1", Login: "admin1"}, "github:admin1", "")
	if err != nil {
		t.Fatalf("bootstrap admin1: %v", err)
	}
	if _, _, err := store.CreateInvite(ctx, UserRoleAdmin, "admin2", "", r1.User.ID); err != nil {
		t.Fatalf("invite admin2: %v", err)
	}
	r2, err := store.ResolveGitHubLogin(ctx, GitHubProfile{ID: "2", Login: "admin2"}, "github:admin1", "")
	if err != nil {
		t.Fatalf("accept admin2: %v", err)
	}

	memberRole := UserRoleMember
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = store.UpdateUser(ctx, r1.User.ID, &memberRole, nil)
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = store.UpdateUser(ctx, r2.User.ID, &memberRole, nil)
	}()
	wg.Wait()

	// Exactly one demotion should succeed and the other must fail with ErrLastAdmin.
	successes := 0
	lastAdminErrs := 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, ErrLastAdmin):
			lastAdminErrs++
		default:
			t.Fatalf("unexpected UpdateUser error: %v", e)
		}
	}
	if successes != 1 || lastAdminErrs != 1 {
		t.Fatalf("successes=%d lastAdminErrs=%d, want 1 and 1", successes, lastAdminErrs)
	}
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activeAdmins := 0
	for _, u := range users {
		if u.Role == UserRoleAdmin && u.DisabledAt == nil {
			activeAdmins++
		}
	}
	if activeAdmins != 1 {
		t.Fatalf("activeAdmins = %d, want 1", activeAdmins)
	}
}

func TestPostgresMigrationV5AddsVarsColumns(t *testing.T) {
	var v5 *postgresMigration
	for i := range postgresMigrations {
		if postgresMigrations[i].version == 5 {
			v5 = &postgresMigrations[i]
			break
		}
	}
	if v5 == nil {
		t.Fatal("v5 migration not found")
	}
	joined := strings.Join(v5.statements, "\n")
	for _, want := range []string{
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS vars TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE automations ADD COLUMN IF NOT EXISTS vars TEXT NOT NULL DEFAULT ''`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("v5 migration missing %q:\n%s", want, joined)
		}
	}
}

func TestPostgresMigrationV6AddsPreviousPRNumberColumn(t *testing.T) {
	var v6 *postgresMigration
	for i := range postgresMigrations {
		if postgresMigrations[i].version == 6 {
			v6 = &postgresMigrations[i]
			break
		}
	}
	if v6 == nil {
		t.Fatal("v6 migration not found")
	}
	joined := strings.Join(v6.statements, "\n")
	want := `ALTER TABLE tickets ADD COLUMN IF NOT EXISTS previous_pr_number INTEGER NOT NULL DEFAULT 0`
	if !strings.Contains(joined, want) {
		t.Fatalf("v6 migration missing %q:\n%s", want, joined)
	}
}

func TestPostgresMigrationV7AddsGateColumnsAndIndexes(t *testing.T) {
	last := postgresMigrations[len(postgresMigrations)-1]
	if last.version != 7 {
		t.Fatalf("latest postgres migration = v%d, want v7", last.version)
	}
	joined := strings.Join(last.statements, "\n")
	for _, want := range []string{
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_step TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_message TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_details TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_waiting_since TEXT`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_timeout_at TEXT`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_decided_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_decided_at TEXT`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_decision TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS gate_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE tickets ADD COLUMN IF NOT EXISTS remaining_timeout_ms BIGINT`,
		`DROP INDEX IF EXISTS one_active_ticket_per_issue`,
		`state IN ('working','waiting','needs_you','pr_ready')`,
		`DROP INDEX IF EXISTS one_active_ticket_per_schedule`,
		`state IN ('working','waiting')`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("v7 migration missing %q:\n%s", want, joined)
		}
	}
}

func TestConcurrentAddInputDuplicateSourceKey(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()

	ticket := createTicket(t, store, "concurrent:add-input:base")
	if _, _, err := store.Apply(ctx, ticket.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 12}); err != nil {
		t.Fatal(err)
	}

	var addedCount atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, added, err := store.AddInput(ctx, ticket.ID, Input{
				Kind:      EntryComment,
				Author:    "bob",
				Body:      "same comment",
				SourceKey: "concurrent:add-input:dup",
			})
			if err != nil {
				errCh <- err
				return
			}
			if got.ID != ticket.ID {
				errCh <- fmt.Errorf("got ticket ID %q, want %q", got.ID, ticket.ID)
				return
			}
			if added {
				addedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("AddInput error: %v", err)
	}
	if addedCount.Load() != 1 {
		t.Fatalf("addedCount = %d, want 1", addedCount.Load())
	}
}

func TestConcurrentScheduleContentionInAddInputAndStewardRequeue(t *testing.T) {
	ctx := t.Context()

	t.Run("AddInput vs CreateTicket", func(t *testing.T) {
		store := openStore(t)
		for i := range 10 {
			schedule := fmt.Sprintf("schedule:race-input-%d", i)
			firstReq := request(fmt.Sprintf("race:input:first:%d", i))
			firstReq.Source = schedule
			first, created, err := store.CreateTicket(ctx, firstReq)
			if err != nil || !created {
				t.Fatalf("iter %d: create first: created=%v err=%v", i, created, err)
			}
			if _, _, err := store.Apply(ctx, first.ID, StateWorking, Transition{State: StatePRReady, PRNumber: 100 + i}); err != nil {
				t.Fatal(err)
			}

			freshKey := fmt.Sprintf("race:input:fresh:%d", i)
			var wg sync.WaitGroup
			var inputAdded bool
			var inputErr error
			var secondCreated bool
			var secondErr error

			wg.Add(2)
			go func() {
				defer wg.Done()
				_, inputAdded, inputErr = store.AddInput(ctx, first.ID, Input{
					Kind:      EntryComment,
					Author:    "alice",
					Body:      "Fix lint",
					SourceKey: freshKey,
				})
			}()
			go func() {
				defer wg.Done()
				secondReq := request(fmt.Sprintf("race:input:second:%d", i))
				secondReq.Source = schedule
				_, secondCreated, secondErr = store.CreateTicket(ctx, secondReq)
			}()
			wg.Wait()

			if secondErr != nil {
				t.Fatalf("iter %d: CreateTicket err = %v", i, secondErr)
			}
			switch {
			case inputErr == nil:
				if !inputAdded {
					t.Fatalf("iter %d: AddInput silently dropped fresh SourceKey (added=false, err=nil)", i)
				}
				if secondCreated {
					t.Fatalf("iter %d: both AddInput and CreateTicket transitioned to working", i)
				}
			case errors.Is(inputErr, ErrStillWorking):
				if inputAdded {
					t.Fatalf("iter %d: AddInput returned added=true with ErrStillWorking", i)
				}
				if !secondCreated {
					t.Fatalf("iter %d: AddInput got ErrStillWorking without second ticket created", i)
				}
				if _, err := store.TicketBySourceKey(ctx, freshKey); !errors.Is(err, ErrNotFound) {
					t.Fatalf("iter %d: fresh SourceKey recorded despite ErrStillWorking: err=%v", i, err)
				}
			default:
				t.Fatalf("iter %d: unexpected AddInput err = %v", i, inputErr)
			}
		}
	})

	t.Run("StewardRequeue vs CreateTicket", func(t *testing.T) {
		store := openStore(t)
		for i := range 10 {
			schedule := fmt.Sprintf("schedule:race-steward-%d", i)
			firstReq := request(fmt.Sprintf("race:steward:first:%d", i))
			firstReq.Source = schedule
			first, created, err := store.CreateTicket(ctx, firstReq)
			if err != nil || !created {
				t.Fatalf("iter %d: create first: created=%v err=%v", i, created, err)
			}
			if _, _, err := store.Apply(ctx, first.ID, StateWorking, Transition{
				State:    StatePRReady,
				PRNumber: 200 + i,
				Shipping: `{"head":"sha1"}`,
			}); err != nil {
				t.Fatal(err)
			}
			listed, err := store.Ticket(ctx, first.ID)
			if err != nil {
				t.Fatal(err)
			}

			var wg sync.WaitGroup
			var requeued bool
			var stewardErr error
			var secondCreated bool
			var secondErr error

			wg.Add(2)
			go func() {
				defer wg.Done()
				_, requeued, stewardErr = store.StewardRequeue(ctx, listed, StewardRequeueRequest{
					MaxRetries: 2,
					Prompt:     "fix failing check",
				})
			}()
			go func() {
				defer wg.Done()
				secondReq := request(fmt.Sprintf("race:steward:second:%d", i))
				secondReq.Source = schedule
				_, secondCreated, secondErr = store.CreateTicket(ctx, secondReq)
			}()
			wg.Wait()

			if secondErr != nil {
				t.Fatalf("iter %d: CreateTicket err = %v", i, secondErr)
			}
			if stewardErr != nil {
				t.Fatalf("iter %d: StewardRequeue returned error %v, want nil", i, stewardErr)
			}
			if requeued && secondCreated {
				t.Fatalf("iter %d: both StewardRequeue and CreateTicket transitioned to working", i)
			}
			if !requeued && !secondCreated {
				t.Fatalf("iter %d: neither StewardRequeue nor CreateTicket succeeded", i)
			}
		}
	})
}
