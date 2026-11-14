package agentrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/owainlewis/machinist/internal/config"
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
}

func NewDaemon(runnerConfig config.Runner, executor Executor, log io.Writer) (*Daemon, error) {
	token, err := runnerConfig.Token()
	if err != nil {
		return nil, err
	}
	return &Daemon{
		config:            runnerConfig,
		client:            NewClient(runnerConfig.ControlPlane, token),
		executor:          executor,
		log:               log,
		pollInterval:      2 * time.Second,
		heartbeatInterval: 30 * time.Second,
		eventInterval:     time.Second,
		retryInterval:     2 * time.Second,
	}, nil
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
	for ctx.Err() == nil {
		var request runner.RunRequest
		err := d.client.Post(ctx, "/api/v1/runners/poll", protocol.PollRequest{Harnesses: d.config.Harnesses(), Kind: d.config.Kind(), Slots: d.config.Slots}, &request)
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
	completion := protocol.Completion{State: string(result.State), Report: result.Report, Tokens: result.Tokens, Cost: result.Cost, Error: result.Error, Stages: result.Stages}
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
