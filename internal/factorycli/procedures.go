package factorycli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func (c command) procedures(ctx context.Context, client apiClient, jsonOutput bool) error {
	page := protocol.ProcedurePage{Procedures: []protocol.Procedure{}}
	cursor := ""
	for {
		query := url.Values{"limit": []string{"200"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var next protocol.ProcedurePage
		if err := client.get(ctx, "/api/v1/procedures?"+query.Encode(), &next, maxResponseBytes); err != nil {
			return err
		}
		page.Procedures = append(page.Procedures, next.Procedures...)
		if next.NextCursor == "" {
			break
		}
		cursor = next.NextCursor
	}
	if jsonOutput {
		if err := writeJSON(c.stdout, page); err != nil {
			return fmt.Errorf("write Procedures JSON: %w", err)
		}
		return nil
	}
	if len(page.Procedures) == 0 {
		_, err := fmt.Fprintln(c.stdout, "No Procedures.")
		return err
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tGENERATION\tRUNTIME\tTIMEOUT\tCONCURRENCY\tARCHIVED"); err != nil {
		return fmt.Errorf("write Procedures output: %w", err)
	}
	for _, procedure := range page.Procedures {
		if _, err := fmt.Fprintf(writer, "%s\t%d\t%s\t%s\t%d\t%t\n",
			oneLine(procedure.Name), procedure.Generation, oneLine(procedure.Runtime),
			(time.Duration(procedure.TimeoutSeconds) * time.Second).String(),
			procedure.ConcurrencyLimit, procedure.Archived); err != nil {
			return fmt.Errorf("write Procedures output: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write Procedures output: %w", err)
	}
	return nil
}

func (c command) runProcedure(server string, jsonOutput bool, arguments []string) error {
	input, wait, explicitKey, err := parseProcedureRunArguments(arguments)
	if err != nil {
		return &usageError{message: err.Error()}
	}
	canonical, _, fingerprint, err := protocol.CanonicalProcedureRunRequest(input)
	if err != nil {
		return &usageError{message: err.Error()}
	}
	if explicitKey && len([]byte(canonical.RequestKey)) > 200 {
		return &usageError{message: "--request-key is limited to 200 bytes"}
	}
	clientContext, cancelClient := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := newAPIClient(clientContext, server, c.client)
	cancelClient()
	if err != nil {
		return err
	}
	var lease *admissionLease
	if explicitKey {
		lease = explicitAdmissionLease(canonical.RequestKey)
	} else {
		lease, err = c.prepareImplicitAdmission(client.admissionEndpoint, fingerprint)
		if err != nil {
			return err
		}
	}
	defer func() { _ = lease.Release() }()
	canonical.RequestKey = lease.RequestKey()

	var admission protocol.ProcedureRunAdmission
	err = client.post(context.Background(), "/api/v1/procedure-runs", canonical, &admission, maxDetailResponseBytes)
	if err != nil {
		var failure *apiFailure
		if !errors.As(err, &failure) || failure.APIError.AdmissionResult != protocol.AdmissionRejectedBeforeCommit {
			return err
		}
		if failure.APIError.RequestKey == "" {
			failure.APIError.RequestKey = canonical.RequestKey
		}
		if err := c.writeProcedureRunFailure(jsonOutput, failure.APIError); err != nil {
			return err
		}
		if err := flushAuthoritativeOutput(c.stdout); err != nil {
			return fmt.Errorf("flush Procedure Run result: %w", err)
		}
		if err := lease.Complete(); err != nil {
			return fmt.Errorf("clear completed Procedure Run admission: %w", err)
		}
		return failure
	}
	if admission.RequestKey != canonical.RequestKey || admission.Run.Run.ID == "" ||
		(admission.Result != protocol.AdmissionAdmitted && admission.Result != protocol.AdmissionReplayed) {
		return errors.New("server returned a malformed Procedure Run admission")
	}
	exitCode := 0
	if wait {
		for {
			code, done := buildWaitResult(admission.Run.Run)
			if done {
				exitCode = code
				break
			}
			timer := time.NewTimer(time.Second)
			<-timer.C
			var detail protocol.RunDetail
			path := "/api/v1/runs/" + url.PathEscape(admission.Run.Run.ID)
			if err := client.get(context.Background(), path, &detail, maxDetailResponseBytes); err != nil {
				return err
			}
			admission.Run = detail
		}
	}
	if err := c.writeProcedureRunAdmission(jsonOutput, admission); err != nil {
		return err
	}
	if err := flushAuthoritativeOutput(c.stdout); err != nil {
		return fmt.Errorf("flush Procedure Run result: %w", err)
	}
	if err := lease.Complete(); err != nil {
		return fmt.Errorf("clear completed Procedure Run admission: %w", err)
	}
	if exitCode != 0 {
		return &commandExit{code: exitCode}
	}
	return nil
}

func parseProcedureRunArguments(arguments []string) (protocol.ProcedureRunRequest, bool, bool, error) {
	if len(arguments) == 0 || strings.TrimSpace(arguments[0]) == "" {
		return protocol.ProcedureRunRequest{}, false, false, errors.New("run requires a Procedure name followed by --repos")
	}
	input := protocol.ProcedureRunRequest{Procedure: arguments[0]}
	wait, reposSeen, explicitKey := false, false, false
	for index := 1; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "--repos":
			if reposSeen {
				return input, wait, explicitKey, errors.New("--repos may be provided only once")
			}
			reposSeen = true
			for index+1 < len(arguments) && !strings.HasPrefix(arguments[index+1], "-") {
				index++
				input.Repositories = append(input.Repositories, arguments[index])
			}
			if len(input.Repositories) == 0 {
				return input, wait, explicitKey, errors.New("--repos requires one or more repositories or all")
			}
		case strings.HasPrefix(argument, "--repos="):
			if reposSeen {
				return input, wait, explicitKey, errors.New("--repos may be provided only once")
			}
			reposSeen = true
			input.Repositories = []string{strings.TrimPrefix(argument, "--repos=")}
		case argument == "--request-key":
			if explicitKey || index+1 >= len(arguments) {
				return input, wait, explicitKey, errors.New("--request-key requires one non-empty value")
			}
			index++
			input.RequestKey, explicitKey = strings.TrimSpace(arguments[index]), true
			if input.RequestKey == "" {
				return input, wait, explicitKey, errors.New("--request-key requires one non-empty value")
			}
		case strings.HasPrefix(argument, "--request-key="):
			if explicitKey {
				return input, wait, explicitKey, errors.New("--request-key may be provided only once")
			}
			input.RequestKey, explicitKey = strings.TrimSpace(strings.TrimPrefix(argument, "--request-key=")), true
			if input.RequestKey == "" {
				return input, wait, explicitKey, errors.New("--request-key requires one non-empty value")
			}
		case argument == "--rebuild":
			if input.Rebuild {
				return input, wait, explicitKey, errors.New("--rebuild may be provided only once")
			}
			input.Rebuild = true
		case argument == "--wait":
			if wait {
				return input, wait, explicitKey, errors.New("--wait may be provided only once")
			}
			wait = true
		default:
			return input, wait, explicitKey, fmt.Errorf("unexpected run argument %q", argument)
		}
	}
	if !reposSeen {
		return input, wait, explicitKey, errors.New("run requires --repos")
	}
	if len(input.Repositories) == 1 && strings.EqualFold(strings.TrimSpace(input.Repositories[0]), "all") {
		input.Repositories = nil
		input.AllRepositories = true
	}
	return input, wait, explicitKey, nil
}

func (c command) writeProcedureRunAdmission(jsonOutput bool, admission protocol.ProcedureRunAdmission) error {
	if jsonOutput {
		if err := writeJSON(c.stdout, admission); err != nil {
			return fmt.Errorf("write Procedure Run JSON: %w", err)
		}
		return nil
	}
	run := admission.Run.Run
	if _, err := fmt.Fprintf(c.stdout,
		"Request key: %s\nAdmission: %s\nProcedure: %s generation %d\nRun: %s\nWork: %d total, %d active, %d ready, %d succeeded, %d needs input, %d no change, %d failed, %d cancelled\n",
		oneLine(admission.RequestKey), oneLine(string(admission.Result)), oneLine(run.Task.Name), run.Task.Generation,
		oneLine(run.ID), run.SessionCount, run.ActiveCount, run.ReadyCount, run.SucceededCount,
		run.NeedsInputCount, run.NoChangeCount, run.FailedCount, run.CancelledCount,
	); err != nil {
		return fmt.Errorf("write Procedure Run output: %w", err)
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "WORK ID\tREPOSITORY\tSTATE"); err != nil {
		return fmt.Errorf("write Procedure Run output: %w", err)
	}
	for _, work := range admission.Run.Sessions {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n",
			oneLine(work.ID), oneLine(work.RepositoryIdentity), oneLine(string(work.State))); err != nil {
			return fmt.Errorf("write Procedure Run output: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write Procedure Run output: %w", err)
	}
	return nil
}

func (c command) writeProcedureRunFailure(jsonOutput bool, failure protocol.APIError) error {
	result := struct {
		Result     protocol.AdmissionResult `json:"result"`
		RequestKey string                   `json:"request_key"`
		Error      protocol.APIError        `json:"error"`
	}{failure.AdmissionResult, failure.RequestKey, failure}
	if jsonOutput {
		if err := writeJSON(c.stdout, result); err != nil {
			return fmt.Errorf("write Procedure Run rejection JSON: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(c.stdout, "Request key: %s\nAdmission: %s\nRejected: %s\n",
		oneLine(failure.RequestKey), oneLine(string(failure.AdmissionResult)), oneLine(failure.Message)); err != nil {
		return fmt.Errorf("write Procedure Run rejection: %w", err)
	}
	return nil
}
