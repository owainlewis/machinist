package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/harness"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

const callTimeout = 15 * time.Second

// Daemon polls the control plane and runs what it is given, one run per slot.
type Daemon struct {
	config            config.Runner
	client            *Client
	executor          Executor
	log               io.Writer
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	eventInterval     time.Duration
	retryInterval     time.Duration
	// maxBackoff caps the wait between polls while the control plane
	// rejects the runner's token.
	maxBackoff time.Duration
	// rejected is set while the control plane rejects the runner's token, so
	// the slots explain it once rather than on every poll.
	rejected atomic.Bool
	// installed is the harnesses a local runner found on PATH at start, or
	// nil when it did not look.
	installed []string
}

func NewDaemon(runnerConfig config.Runner, executor Executor, log io.Writer) (*Daemon, error) {
	token, err := runnerConfig.Token()
	if err != nil {
		return nil, err
	}
	daemon := &Daemon{
		config:            runnerConfig,
		client:            NewClient(runnerConfig.ControlPlane, token),
		executor:          executor,
		log:               log,
		pollInterval:      2 * time.Second,
		heartbeatInterval: 30 * time.Second,
		eventInterval:     time.Second,
		retryInterval:     2 * time.Second,
		maxBackoff:        time.Minute,
	}
	// Steps on a local runner run the harnesses found on this PATH. A
	// Kubernetes runner's steps run in its image, which it cannot see.
	if runnerConfig.Kind() == "local" {
		daemon.installed = harness.Installed(exec.LookPath)
	}
	return daemon, nil
}

// Run serves every slot until ctx ends. A slot polls only when it is free, so
// the number of slots is the concurrency limit.
func (d *Daemon) Run(ctx context.Context) error {
	var slots sync.WaitGroup
	for range d.config.Slots {
		slots.Go(func() { d.serveSlot(ctx) })
	}
	slots.Wait()
	return nil
}

func (d *Daemon) serveSlot(ctx context.Context) {
	wait := d.pollInterval
	for ctx.Err() == nil {
		var request runner.RunRequest
		poll := protocol.PollRequest{Harnesses: append(d.config.Harnesses(), d.installed...), Probed: d.installed != nil, Kind: d.config.Kind(), Slots: d.config.Slots}
		err := d.client.Post(ctx, "/api/v1/runners/poll", poll, &request)
		if rejectedToken(err) {
			if ctx.Err() == nil && !d.rejected.Swap(true) {
				fmt.Fprintf(d.log, "machinist: the control plane at %s rejected this runner's token (HTTP 401). It may be a different Machinist on that address, or the token was revoked. Polling less often until it is accepted.\n", d.client.base)
			}
			wait = max(wait*2, d.pollInterval)
			if d.maxBackoff > 0 {
				wait = min(wait, d.maxBackoff)
			}
			sleep(ctx, wait)
			continue
		}
		wait = d.pollInterval
		if err == nil && d.rejected.Swap(false) {
			fmt.Fprintf(d.log, "machinist: the control plane at %s accepted this runner's token again.\n", d.client.base)
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(d.log, "machinist: runner poll: %v\n", err)
		}
		if err != nil || request.RunID == "" {
			sleep(ctx, d.pollInterval)
			continue
		}
		d.handle(ctx, request)
	}
}

// rejectedToken reports whether the control plane refused the runner's token.
func rejectedToken(err error) bool {
	var response *ResponseError
	return errors.As(err, &response) && response.Status == http.StatusUnauthorized
}

func sleep(ctx context.Context, duration time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}

// handle runs one request. Reporting outlives ctx, so a stopping runner still
// tells the control plane what happened.
func (d *Daemon) handle(ctx context.Context, request runner.RunRequest) {
	runContext, stopRun := context.WithCancel(ctx)
	defer stopRun()
	leaseContext, stopLease := context.WithCancel(context.WithoutCancel(ctx))
	heartbeats := make(chan struct{})
	go func() {
		defer close(heartbeats)
		d.keepLease(leaseContext, request.RunID, stopRun)
	}()
	stopEvents := d.streamRunEvents(leaseContext, request.RunID)

	if request.GitHubToken != "" {
		request.RefreshToken = func(ctx context.Context) (string, error) {
			var reply protocol.GitHubToken
			err := d.client.Post(ctx, "/api/v1/runs/"+request.RunID+"/github-token", struct{}{}, &reply)
			return reply.Token, err
		}
	}
	result := d.executor.Execute(runContext, request)

	stopEvents()
	d.report(ctx, request.RunID, result)
	stopLease()
	<-heartbeats
	if err := os.RemoveAll(d.runDirectory(request.RunID)); err != nil {
		fmt.Fprintf(d.log, "machinist: remove run %s: %v\n", request.RunID, err)
	}
}

// keepLease renews the lease. It stops the agent when the control plane says so
// or no longer knows the run. A control plane it cannot reach changes nothing.
func (d *Daemon) keepLease(ctx context.Context, runID string, stopRun context.CancelFunc) {
	ticker := time.NewTicker(d.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var beat protocol.HeartbeatResponse
			call, cancel := context.WithTimeout(ctx, callTimeout)
			err := d.client.Post(call, "/api/v1/runs/"+runID+"/heartbeat", struct{}{}, &beat)
			cancel()
			var response *ResponseError
			if errors.As(err, &response) && response.Stopped() {
				stopRun()
			} else if err != nil && ctx.Err() == nil {
				fmt.Fprintf(d.log, "machinist: heartbeat run %s: %v\n", runID, err)
			} else if beat.Cancel {
				stopRun()
			}
		}
	}
}

func (d *Daemon) report(ctx context.Context, runID string, result runner.RunResult) {
	if err := d.retry(ctx, func(call context.Context) error { return d.flushRunEvents(call, runID) }); err != nil {
		fmt.Fprintf(d.log, "machinist: send events for run %s: %v\n", runID, err)
	}
	completion := protocol.Completion{State: string(result.State), Report: result.Report, Error: result.Error, Stages: result.Stages}
	completion.Tokens, completion.Cost = protocol.Totals(result.Stages)
	if err := d.retry(ctx, func(call context.Context) error {
		return d.client.Post(call, "/api/v1/runs/"+runID+"/complete", completion, nil)
	}); err != nil {
		fmt.Fprintf(d.log, "machinist: report run %s: %v\n", runID, err)
	}
}

// retry repeats work while the control plane is unreachable. Once ctx has ended
// it makes one more attempt and gives up, and the lease then expires.
func (d *Daemon) retry(ctx context.Context, work func(context.Context) error) error {
	for {
		call, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
		err := work(call)
		cancel()
		if err == nil || !retryable(err) || ctx.Err() != nil {
			return err
		}
		sleep(ctx, d.retryInterval)
	}
}

func retryable(err error) bool {
	var response *ResponseError
	if errors.As(err, &response) {
		return response.Retryable()
	}
	var network *url.Error
	return errors.As(err, &network)
}
