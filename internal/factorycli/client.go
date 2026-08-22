package factorycli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/owainlewis/factory/internal/protocol"
)

const maxResponseBytes = 16 << 20

type apiClient struct {
	endpoint *url.URL
	client   *http.Client
}

type workerPage struct {
	Workers []protocol.Worker `json:"workers"`
}

func newAPIClient(ctx context.Context, value string, client *http.Client) (apiClient, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return apiClient{}, fmt.Errorf("parse server URL: %w", err)
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return apiClient{}, errors.New("server must be a plain HTTP loopback URL without credentials, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return apiClient{}, errors.New("server URL must not contain a path")
	}
	host := parsed.Hostname()
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if host == "" || err != nil || port == 0 {
		return apiClient{}, errors.New("server URL must include a loopback host and nonzero port")
	}
	if err := validateLoopbackHost(ctx, host); err != nil {
		return apiClient{}, err
	}
	if strings.EqualFold(host, "localhost") {
		// ProxyFromEnvironment exempts exact lowercase localhost only.
		parsed.Host = net.JoinHostPort("localhost", parsed.Port())
	}
	parsed.Path = ""
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return apiClient{endpoint: parsed, client: &clientCopy}, nil
}

func validateLoopbackHost(ctx context.Context, host string) error {
	if address := net.ParseIP(host); address != nil {
		if address.IsLoopback() {
			return nil
		}
		return errors.New("server URL host must be loopback")
	}
	if !strings.EqualFold(host, "localhost") {
		return errors.New("server URL host must be a loopback IP or localhost")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve server URL host: %w", err)
	}
	if len(addresses) == 0 {
		return errors.New("server URL host resolved to no addresses")
	}
	for _, address := range addresses {
		if !address.IP.IsLoopback() {
			return errors.New("server URL host must resolve only to loopback")
		}
	}
	return nil
}

func (c apiClient) get(ctx context.Context, path string, target any, responseLimit int64) error {
	requestURL := *c.endpoint
	reference, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("build request URL: %w", err)
	}
	requestURL.Path = reference.Path
	requestURL.RawQuery = reference.RawQuery
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", c.endpoint.String(), err)
	}
	defer response.Body.Close()
	limit := responseLimit
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = maxResponseBytes
	}
	reader := io.Reader(response.Body)
	if limit > 0 {
		reader = io.LimitReader(response.Body, limit+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read server response: %w", err)
	}
	if limit > 0 && int64(len(body)) > limit {
		return fmt.Errorf("server response exceeds %d bytes", limit)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := oneLine(response.Status)
		var failure protocol.ErrorBody
		if err := json.Unmarshal(body, &failure); err == nil && failure.Error.Message != "" {
			return fmt.Errorf("server returned %s: %s", status, oneLine(failure.Error.Message))
		}
		return fmt.Errorf("server returned %s", status)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode server response: multiple JSON values")
		}
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}

func (c command) status(ctx context.Context, client apiClient, jsonOutput bool) error {
	var page protocol.RunPage
	if err := client.get(ctx, "/api/v1/runs?limit=50", &page, maxResponseBytes); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(c.stdout, page)
	}
	if len(page.Runs) == 0 {
		fmt.Fprintln(c.stdout, "No Runs.")
		return nil
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN ID\tTASK\tSTATE\tSESSIONS\tACTIVE\tSUCCEEDED\tFAILED\tCANCELLED\tUPDATED")
	for _, run := range page.Runs {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			oneLine(run.ID), oneLine(run.Task.Name), oneLine(string(run.State)), run.SessionCount, run.ActiveCount,
			run.SucceededCount, run.FailedCount, run.CancelledCount, formatTime(run.UpdatedAt))
	}
	if page.NextCursor != "" {
		fmt.Fprintf(writer, "Next cursor:\t%s\n", oneLine(page.NextCursor))
	}
	return writer.Flush()
}

func (c command) show(ctx context.Context, client apiClient, runID string, jsonOutput bool) error {
	var detail protocol.RunDetail
	// Run details include an unbounded retry history, so no finite byte cap can
	// cover every valid response. The command deadline still bounds the read.
	if err := client.get(ctx, "/api/v1/runs/"+url.PathEscape(runID), &detail, 0); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(c.stdout, detail)
	}
	run := detail.Run
	fmt.Fprintf(c.stdout, "Run: %s\nTask: %s\nState: %s\nSource: %s\nAdmitted: %s\nUpdated: %s\n",
		oneLine(run.ID), oneLine(run.Task.Name), oneLine(string(run.State)), oneLine(run.Source), formatTime(run.AdmittedAt), formatTime(run.UpdatedAt))
	if len(detail.Sessions) == 0 {
		fmt.Fprintln(c.stdout, "\nNo Sessions.")
		return nil
	}
	fmt.Fprintln(c.stdout)
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "SESSION ID\tREPOSITORY\tSTATE\tWORKER\tATTEMPTS\tRESULT")
	for _, session := range detail.Sessions {
		result := session.Result
		if result == "" {
			result = session.FailureReason
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\n",
			oneLine(session.ID), oneLine(session.RepositoryIdentity), oneLine(string(session.State)),
			oneLine(session.AssignedWorkerID), len(session.Attempts), oneLine(result))
	}
	return writer.Flush()
}

func (c command) workers(ctx context.Context, client apiClient, jsonOutput bool) error {
	var page workerPage
	if err := client.get(ctx, "/api/v1/workers", &page, maxResponseBytes); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(c.stdout, page)
	}
	if len(page.Workers) == 0 {
		fmt.Fprintln(c.stdout, "No Workers.")
		return nil
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "WORKER ID\tNAME\tONLINE\tHEALTH\tRUNTIME\tACTIVE\tCAPACITY\tLAST HEARTBEAT")
	for _, worker := range page.Workers {
		fmt.Fprintf(writer, "%s\t%s\t%t\t%s\t%s\t%d\t%d\t%s\n",
			oneLine(worker.ID), oneLine(worker.Name), worker.Online, oneLine(worker.Health), oneLine(worker.Runtime),
			worker.ActiveCount, worker.Capacity, formatTime(worker.LastHeartbeat))
	}
	return writer.Flush()
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "-"
	}
	var printable strings.Builder
	for _, character := range value {
		if unicode.IsPrint(character) {
			printable.WriteRune(character)
			continue
		}
		if character <= 0xffff {
			fmt.Fprintf(&printable, "\\u%04X", character)
		} else {
			fmt.Fprintf(&printable, "\\U%08X", character)
		}
	}
	value = printable.String()
	const limit = 80
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-3]) + "..."
}
