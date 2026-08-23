package factorycli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func (c command) runBuild(server string, jsonOutput bool, arguments []string) error {
	flags := flag.NewFlagSet("factory build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repository := flags.String("repo", "", "scope every reference to one managed GitHub repository")
	runtime := flags.String("runtime", "", "override the configured Build runtime")
	requestKey := flags.String("request-key", "", "use an explicit idempotency key")
	rebuild := flags.Bool("rebuild", false, "replace each target's latest terminal Work")
	wait := flags.Bool("wait", false, "wait for a terminal or needs-input result")
	orderedArguments, err := interspersedBuildArguments(arguments)
	if err != nil {
		return &usageError{message: err.Error()}
	}
	if err := flags.Parse(orderedArguments); err != nil {
		return &usageError{message: err.Error()}
	}
	explicit := make(map[string]bool)
	flags.Visit(func(value *flag.Flag) { explicit[value.Name] = true })
	if flags.NArg() == 0 {
		return &usageError{message: "build requires at least one work-item reference"}
	}
	if explicit["repo"] && strings.TrimSpace(*repository) == "" {
		return &usageError{message: "--repo requires a non-empty value"}
	}
	if explicit["runtime"] && strings.TrimSpace(*runtime) == "" {
		return &usageError{message: "--runtime requires a non-empty value"}
	}
	if explicit["request-key"] && strings.TrimSpace(*requestKey) == "" {
		return &usageError{message: "--request-key requires a non-empty value"}
	}
	input := protocol.BuildRequest{
		RequestKey: strings.TrimSpace(*requestKey), References: flags.Args(),
		Repository: strings.TrimSpace(*repository), RepositorySpecified: explicit["repo"],
		Runtime: strings.TrimSpace(*runtime), RuntimeSpecified: explicit["runtime"],
		Rebuild: *rebuild,
	}
	canonical, _, fingerprint, err := protocol.CanonicalBuildRequest(input)
	if err != nil {
		return &usageError{message: err.Error()}
	}
	if explicit["request-key"] && len([]byte(canonical.RequestKey)) > 200 {
		return &usageError{message: "--request-key is limited to 200 bytes"}
	}

	clientContext, cancelClient := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := newAPIClient(clientContext, server, c.client)
	cancelClient()
	if err != nil {
		return err
	}
	var lease *admissionLease
	if explicit["request-key"] {
		lease = explicitAdmissionLease(canonical.RequestKey)
	} else {
		lease, err = c.prepareImplicitAdmission(client.admissionEndpoint, fingerprint)
		if err != nil {
			return err
		}
	}
	defer func() { _ = lease.Release() }()
	canonical.RequestKey = lease.RequestKey()

	var admission protocol.BuildAdmission
	err = client.post(context.Background(), "/api/v1/builds", canonical, &admission, maxDetailResponseBytes)
	if err != nil {
		var failure *apiFailure
		if !errors.As(err, &failure) || failure.APIError.AdmissionResult != protocol.AdmissionRejectedBeforeCommit {
			return err
		}
		if failure.APIError.RequestKey == "" {
			failure.APIError.RequestKey = canonical.RequestKey
		}
		if err := c.writeBuildFailure(jsonOutput, failure.APIError); err != nil {
			return err
		}
		if err := flushAuthoritativeOutput(c.stdout); err != nil {
			return fmt.Errorf("flush Build result: %w", err)
		}
		if err := lease.Complete(); err != nil {
			return fmt.Errorf("clear completed Build admission: %w", err)
		}
		return failure
	}
	if admission.RequestKey != canonical.RequestKey || admission.Run.Run.ID == "" ||
		(admission.Result != protocol.AdmissionAdmitted && admission.Result != protocol.AdmissionReplayed) {
		return errors.New("server returned a malformed Build admission")
	}

	exitCode := 0
	if *wait {
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
	if err := c.writeBuildAdmission(jsonOutput, admission); err != nil {
		return err
	}
	if err := flushAuthoritativeOutput(c.stdout); err != nil {
		return fmt.Errorf("flush Build result: %w", err)
	}
	if err := lease.Complete(); err != nil {
		return fmt.Errorf("clear completed Build admission: %w", err)
	}
	if exitCode != 0 {
		return &commandExit{code: exitCode}
	}
	return nil
}

func buildWaitResult(run protocol.Run) (int, bool) {
	if run.NeedsInputCount > 0 {
		return 2, true
	}
	if run.SessionCount > 0 && run.ReadyCount+run.NoChangeCount+run.SucceededCount == run.SessionCount {
		return 0, true
	}
	if run.SessionCount > 0 && run.ReadyCount+run.NoChangeCount+run.SucceededCount+
		run.FailedCount+run.CancelledCount == run.SessionCount {
		return 1, true
	}
	return 0, false
}

func (c command) writeBuildAdmission(jsonOutput bool, admission protocol.BuildAdmission) error {
	if jsonOutput {
		if err := writeJSON(c.stdout, admission); err != nil {
			return fmt.Errorf("write Build JSON: %w", err)
		}
		return nil
	}
	run := admission.Run.Run
	if _, err := fmt.Fprintf(c.stdout,
		"Request key: %s\nAdmission: %s\nRun: %s\nWork: %d total, %d active, %d ready, %d succeeded, %d needs input, %d no change, %d failed, %d cancelled\n",
		oneLine(admission.RequestKey), oneLine(string(admission.Result)), oneLine(run.ID),
		run.SessionCount, run.ActiveCount, run.ReadyCount, run.SucceededCount, run.NeedsInputCount,
		run.NoChangeCount, run.FailedCount, run.CancelledCount,
	); err != nil {
		return fmt.Errorf("write Build output: %w", err)
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "WORK ID\tREPOSITORY\tREFERENCE\tSTATE"); err != nil {
		return fmt.Errorf("write Build output: %w", err)
	}
	for _, work := range admission.Run.Sessions {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			oneLine(work.ID), oneLine(work.RepositoryIdentity),
			oneLine(work.Target.SourceReference), oneLine(string(work.State))); err != nil {
			return fmt.Errorf("write Build output: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("write Build output: %w", err)
	}
	return nil
}

func interspersedBuildArguments(arguments []string) ([]string, error) {
	options := make([]string, 0, len(arguments))
	references := make([]string, 0, len(arguments))
	terminated := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			terminated = true
			references = append(references, arguments[index+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			references = append(references, argument)
			continue
		}
		options = append(options, argument)
		name := strings.TrimLeft(argument, "-")
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			continue
		}
		switch name {
		case "repo", "runtime", "request-key":
			if index+1 >= len(arguments) {
				return nil, fmt.Errorf("flag needs an argument: --%s", name)
			}
			index++
			options = append(options, arguments[index])
		}
	}
	if terminated {
		options = append(options, "--")
	}
	return append(options, references...), nil
}

func (c command) writeBuildFailure(jsonOutput bool, failure protocol.APIError) error {
	result := struct {
		Result     protocol.AdmissionResult `json:"result"`
		RequestKey string                   `json:"request_key"`
		Error      protocol.APIError        `json:"error"`
	}{failure.AdmissionResult, failure.RequestKey, failure}
	if jsonOutput {
		if err := writeJSON(c.stdout, result); err != nil {
			return fmt.Errorf("write Build rejection JSON: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintf(c.stdout, "Request key: %s\nAdmission: %s\nRejected: %s\n",
		oneLine(failure.RequestKey), oneLine(string(failure.AdmissionResult)), oneLine(failure.Message)); err != nil {
		return fmt.Errorf("write Build rejection: %w", err)
	}
	return nil
}

func flushAuthoritativeOutput(output io.Writer) error {
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return err
		}
	}
	file, ok := output.(*os.File)
	if !ok {
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return file.Sync()
	}
	return nil
}
