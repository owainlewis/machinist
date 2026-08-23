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

const (
	maxResponseBytes        = 16 << 20
	maxSummaryResponseBytes = 64 << 20
	maxDetailResponseBytes  = 256 << 20
)

type apiClient struct {
	endpoint          *url.URL
	admissionEndpoint string
	client            *http.Client
}

type workerPage struct {
	Workers []protocol.Worker `json:"workers"`
}

type apiFailure struct {
	Status   string
	APIError protocol.APIError
}

func (failure *apiFailure) Error() string {
	if failure.APIError.Message != "" {
		return fmt.Sprintf("server returned %s: %s", oneLine(failure.Status), oneLine(failure.APIError.Message))
	}
	return fmt.Sprintf("server returned %s", oneLine(failure.Status))
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
	pinnedAddresses, err := validateLoopbackHost(ctx, host)
	if err != nil {
		return apiClient{}, err
	}
	stableHost := strings.ToLower(host)
	if address := net.ParseIP(host); address != nil {
		stableHost = address.String()
	}
	admissionEndpoint := (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(stableHost, strconv.FormatUint(port, 10)),
	}).String()
	// Use the validated literal address so proxies and a second DNS lookup
	// cannot move a loopback-only command away from the local machine.
	parsed.Host = net.JoinHostPort(pinnedAddresses[0].String(), parsed.Port())
	parsed.Path = ""
	clientCopy := *client
	if err := pinLoopbackTransport(&clientCopy, pinnedAddresses); err != nil {
		return apiClient{}, err
	}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return apiClient{endpoint: parsed, admissionEndpoint: admissionEndpoint, client: &clientCopy}, nil
}

func validateLoopbackHost(ctx context.Context, host string) ([]net.IP, error) {
	if address := net.ParseIP(host); address != nil {
		if address.IsLoopback() {
			return []net.IP{address}, nil
		}
		return nil, errors.New("server URL host must be loopback")
	}
	if !strings.EqualFold(host, "localhost") {
		return nil, errors.New("server URL host must be a loopback IP or localhost")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve server URL host: %w", err)
	}
	if len(addresses) == 0 {
		return nil, errors.New("server URL host resolved to no addresses")
	}
	validated := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if !address.IP.IsLoopback() {
			return nil, errors.New("server URL host must resolve only to loopback")
		}
		validated = append(validated, append(net.IP(nil), address.IP...))
	}
	return validated, nil
}

func pinLoopbackTransport(client *http.Client, addresses []net.IP) error {
	var transport *http.Transport
	switch configured := client.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = configured.Clone()
	default:
		return errors.New("HTTP client transport does not support loopback address pinning")
	}
	dial := transport.DialContext
	if dial == nil {
		dialer := &net.Dialer{}
		dial = dialer.DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split HTTP dial address: %w", err)
		}
		failures := make([]error, 0, len(addresses))
		for _, pinned := range addresses {
			connection, err := dial(ctx, network, net.JoinHostPort(pinned.String(), port))
			if err == nil {
				return connection, nil
			}
			failures = append(failures, err)
		}
		return nil, fmt.Errorf("connect to validated loopback addresses: %w", errors.Join(failures...))
	}
	client.Transport = transport
	return nil
}

func (c apiClient) get(ctx context.Context, path string, target any, responseLimit int64) error {
	return c.request(ctx, http.MethodGet, path, nil, target, responseLimit)
}

func (c apiClient) post(ctx context.Context, path string, input, target any, responseLimit int64) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	return c.request(ctx, http.MethodPost, path, body, target, responseLimit)
}

func (c apiClient) request(ctx context.Context, method, path string, requestBody []byte, target any, responseLimit int64) error {
	requestURL := *c.endpoint
	reference, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("build request URL: %w", err)
	}
	requestURL.Path = reference.Path
	requestURL.RawQuery = reference.RawQuery
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(requestBody))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
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
			return &apiFailure{Status: status, APIError: failure.Error}
		}
		return &apiFailure{Status: status}
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

func (c command) status(ctx context.Context, client apiClient, jsonOutput bool, cursor string) error {
	var page protocol.RunListPage
	query := url.Values{"limit": {"50"}, "view": {"summary"}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if err := client.get(ctx, "/api/v1/runs?"+query.Encode(), &page, maxResponseBytes); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(c.stdout, page)
	}
	if len(page.Runs) == 0 {
		_, err := fmt.Fprintln(c.stdout, "No Runs.")
		return err
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN ID\tTASK\tSTATE\tSESSIONS\tACTIVE\tSUCCEEDED\tFAILED\tCANCELLED\tUPDATED")
	for _, run := range page.Runs {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			oneLine(run.ID), oneLine(run.TaskName), oneLine(string(run.State)), run.SessionCount, run.ActiveCount,
			run.SucceededCount, run.FailedCount, run.CancelledCount, formatTime(run.UpdatedAt))
	}
	if page.NextCursor != "" {
		fmt.Fprintf(writer, "Next cursor:\t%s\n", oneLine(page.NextCursor))
	}
	return writer.Flush()
}

func (c command) show(ctx context.Context, client apiClient, runID string, jsonOutput bool) error {
	if jsonOutput {
		var detail protocol.RunDetail
		if err := client.get(ctx, "/api/v1/runs/"+url.PathEscape(runID), &detail, maxDetailResponseBytes); err != nil {
			return err
		}
		return writeJSON(c.stdout, detail)
	}
	var summary protocol.RunSummary
	path := "/api/v1/runs/" + url.PathEscape(runID) + "?view=summary"
	if err := client.get(ctx, path, &summary, maxSummaryResponseBytes); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.stdout, "Run: %s\nTask: %s\nState: %s\nSource: %s\nAdmitted: %s\nUpdated: %s\n",
		oneLine(summary.ID), oneLine(summary.TaskName), oneLine(string(summary.State)), oneLine(summary.Source), formatTime(summary.AdmittedAt), formatTime(summary.UpdatedAt)); err != nil {
		return fmt.Errorf("write show output: %w", err)
	}
	if len(summary.Sessions) == 0 {
		if _, err := fmt.Fprintln(c.stdout, "\nNo Sessions."); err != nil {
			return fmt.Errorf("write show output: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintln(c.stdout); err != nil {
		return fmt.Errorf("write show output: %w", err)
	}
	writer := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "SESSION ID\tREPOSITORY\tSTATE\tWORKER\tATTEMPTS\tRESULT")
	for _, session := range summary.Sessions {
		result := session.Result
		if result == "" {
			result = session.FailureReason
		}
		if result == "" {
			result = session.BlockedReason
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\n",
			oneLine(session.ID), oneLine(session.RepositoryIdentity), oneLine(string(session.State)),
			oneLine(session.AssignedWorkerID), session.AttemptCount, oneLine(result))
	}
	return writer.Flush()
}

func (c command) workers(ctx context.Context, client apiClient, jsonOutput bool) error {
	if jsonOutput {
		var page workerPage
		if err := client.get(ctx, "/api/v1/workers", &page, maxDetailResponseBytes); err != nil {
			return err
		}
		return writeJSON(c.stdout, page)
	}
	var page protocol.WorkerSummaryPage
	if err := client.get(ctx, "/api/v1/workers?view=summary", &page, maxSummaryResponseBytes); err != nil {
		return err
	}
	if len(page.Workers) == 0 {
		_, err := fmt.Fprintln(c.stdout, "No Workers.")
		return err
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
