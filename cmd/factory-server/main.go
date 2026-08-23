package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/owainlewis/factory/internal/buildinfo"
	"github.com/owainlewis/factory/internal/controlplane"
	"github.com/owainlewis/factory/internal/protocol"
	"github.com/owainlewis/factory/internal/statepath"
	factoryweb "github.com/owainlewis/factory/web"
)

func main() {
	if buildinfo.Requested(os.Args[1:]) {
		fmt.Fprintln(os.Stdout, buildinfo.String("factory-server"))
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "factory-server:", err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	defaultDatabase, dataRoot, err := defaultDatabasePath()
	if err != nil {
		return err
	}
	bootstrap, err := loadServerBootstrapConfig(dataRoot)
	if err != nil {
		return err
	}
	if bootstrap.DefaultBuildRuntime == "" {
		bootstrap.DefaultBuildRuntime = protocol.RuntimeCodex
	}
	if !protocol.SupportedRuntime(bootstrap.DefaultBuildRuntime) {
		return fmt.Errorf(
			"default_build_runtime must be one of %s",
			strings.Join(protocol.SupportedRuntimes(), ", "),
		)
	}
	defaultListen := "127.0.0.1:7337"
	if bootstrap.Listen != "" {
		defaultListen = bootstrap.Listen
	}
	selectedDatabase := defaultDatabase
	if bootstrap.Database != "" {
		selectedDatabase = bootstrap.Database
	}
	listen := flag.String("listen", defaultListen, "loopback HTTP listen address")
	database := flag.String("database", selectedDatabase, "Factory SQLite database path")
	workerListen := flag.String("worker-listen", bootstrap.WorkerListen, "optional remote Worker HTTPS listen address")
	workerTLSCert := flag.String("worker-tls-cert", bootstrap.WorkerTLSCert, "remote Worker TLS certificate path")
	workerTLSKey := flag.String("worker-tls-key", bootstrap.WorkerTLSKey, "remote Worker TLS private key path")
	backup := flag.String("backup", "", "write a consistent database backup and exit")
	restore := flag.String("restore", "", "restore a validated backup into the selected fresh database and exit")
	printListen := flag.Bool("print-listen", false, "print the resolved listen address and exit")
	flag.Parse()
	if err := validateWorkerTLSConfig(*workerListen, *workerTLSCert, *workerTLSKey); err != nil {
		return err
	}
	if *backup != "" && *restore != "" {
		return errors.New("backup and restore modes are mutually exclusive")
	}
	if *printListen && (*backup != "" || *restore != "") {
		return errors.New("print-listen cannot be combined with backup or restore mode")
	}
	if *printListen {
		listenAddress, err := controlplane.ResolveListenAddress(*listen)
		if err != nil {
			return err
		}
		fmt.Println(listenAddress.String())
		return nil
	}
	databaseExplicit := false
	flag.Visit(func(value *flag.Flag) {
		if value.Name == "database" {
			databaseExplicit = true
		}
	})
	if err := validateLegacyServerSelection(
		factoryDataHome(),
		databaseExplicit || bootstrap.Database != "",
		dataRoot,
	); err != nil {
		return err
	}
	if *database == defaultDatabase {
		if err := validateDataRoot(dataRoot); err != nil {
			return err
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	handled, err := runRecoveryMode(rootContext, *database, *backup, *restore, os.Stdout)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	listenAddress, err := controlplane.ResolveListenAddress(*listen)
	if err != nil {
		return err
	}

	store, err := controlplane.Open(rootContext, *database)
	if err != nil {
		return err
	}
	if err := store.SetDefaultBuildRuntime(bootstrap.DefaultBuildRuntime); err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close SQLite: %w", err)
		}
	}()
	expired, err := store.SweepExpired(rootContext)
	if err != nil {
		return fmt.Errorf("startup lease sweep: %w", err)
	}
	if len(expired) > 0 {
		logger.Info("startup_leases_expired", "attempt_count", len(expired))
	}
	for _, lease := range expired {
		logger.Info("state_change",
			"resource_type", "attempt",
			"resource_id", lease.AttemptID,
			"execution_id", lease.ExecutionID,
			"new_state", "lost",
		)
	}
	sweepContext, cancelSweep := context.WithCancel(rootContext)
	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		store.RunSweeper(sweepContext, logger)
	}()
	defer func() {
		cancelSweep()
		<-sweeperDone
	}()
	scheduleContext, cancelSchedules := context.WithCancel(rootContext)
	schedulesDone := make(chan struct{})
	go func() {
		defer close(schedulesDone)
		store.RunTaskScheduler(scheduleContext, logger)
	}()
	defer func() {
		cancelSchedules()
		<-schedulesDone
	}()
	fakeCloudContext, cancelFakeCloud := context.WithCancel(rootContext)
	fakeCloudDone := make(chan struct{})
	go func() {
		defer close(fakeCloudDone)
		store.RunFakeCloudDispatcher(fakeCloudContext, logger)
	}()
	defer func() {
		cancelFakeCloud()
		<-fakeCloudDone
	}()

	listener, err := net.ListenTCP("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	handler := factoryweb.NewHandler(controlplane.NewHandler(store, logger))
	server := controlplane.NewHTTPServer(*listen, handler)
	serverErrors := make(chan error, 3)
	serverCount := 1
	go func() {
		logger.Info("server_started",
			"address", listener.Addr().String(),
			"database", *database,
			"ui_url", "http://"+listener.Addr().String()+"/",
		)
		serverErrors <- server.Serve(listener)
	}()
	var workerServer *http.Server
	if *workerListen != "" {
		workerListener, err := net.Listen("tcp", *workerListen)
		if err != nil {
			return fmt.Errorf("listen for remote Workers: %w", err)
		}
		workerServer = controlplane.NewHTTPServer(*workerListen, controlplane.NewRemoteWorkerHandler(store, logger))
		serverCount++
		go func() {
			logger.Info("worker_server_started", "address", workerListener.Addr().String())
			serverErrors <- workerServer.ServeTLS(workerListener, *workerTLSCert, *workerTLSKey)
		}()
	}
	receivedServerErrors := 0
	var serveErr error
	select {
	case err := <-serverErrors:
		receivedServerErrors = 1
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-rootContext.Done():
	}
	cancelSchedules()
	<-schedulesDone
	cancelFakeCloud()
	<-fakeCloudDone
	cancelSweep()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil && serveErr == nil {
		serveErr = fmt.Errorf("shut down HTTP server: %w", err)
	}
	if workerServer != nil {
		if err := workerServer.Shutdown(shutdown); err != nil && serveErr == nil {
			serveErr = fmt.Errorf("shut down remote Worker server: %w", err)
		}
	}
	for receivedServerErrors < serverCount {
		err := <-serverErrors
		receivedServerErrors++
		if !errors.Is(err, http.ErrServerClosed) && serveErr == nil {
			serveErr = err
		}
	}
	if serveErr != nil {
		return fmt.Errorf("serve HTTP: %w", serveErr)
	}
	cancelSweep()
	<-sweeperDone
	logger.Info("server_stopped")
	return nil
}

func validateWorkerTLSConfig(listen, certificate, key string) error {
	configured := 0
	for _, value := range []string{listen, certificate, key} {
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured != 0 && configured != 3 {
		return errors.New("worker-listen, worker-tls-cert, and worker-tls-key must be configured together")
	}
	if configured == 3 {
		if _, _, err := net.SplitHostPort(listen); err != nil {
			return fmt.Errorf("remote Worker listen address must include host and port: %w", err)
		}
	}
	return nil
}

func runRecoveryMode(
	ctx context.Context,
	database, backup, restore string,
	stdout io.Writer,
) (bool, error) {
	if restore != "" {
		if err := controlplane.RestoreBackup(ctx, restore, database); err != nil {
			return true, err
		}
		fmt.Fprintf(stdout, "restored Factory database to %s\n", database)
		return true, nil
	}
	if backup != "" {
		if err := controlplane.BackupDatabase(ctx, database, backup); err != nil {
			return true, err
		}
		fmt.Fprintf(stdout, "created Factory database backup at %s\n", backup)
		return true, nil
	}
	return false, nil
}

func defaultDatabasePath() (database string, root string, err error) {
	root = factoryDataHome()
	if root == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", fmt.Errorf("resolve home directory: %w", homeErr)
		}
		root = filepath.Join(home, ".factory")
	}
	return filepath.Join(root, "server", "factory.sqlite3"), root, nil
}

func validateNoLegacyServerDefault(newRoot string) error {
	if _, found, err := findPreviewServerState(newRoot); err != nil {
		return err
	} else if found {
		return nil
	}
	legacyRoot := filepath.Join(filepath.Dir(newRoot), ".factory-v2")
	legacyState, found, err := findPreviewServerState(legacyRoot)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf(
			"found preview control-plane state at %s; refusing to abandon durable Runs for the new default; set FACTORY_DATA_HOME=%s to keep using it, or archive the old state after resolving its Runs",
			legacyState,
			legacyRoot,
		)
	}
	return nil
}

func validateLegacyServerSelection(dataHome string, databaseExplicit bool, newRoot string) error {
	if dataHome != "" || databaseExplicit {
		return nil
	}
	return validateNoLegacyServerDefault(newRoot)
}

func findPreviewServerState(root string) (string, bool, error) {
	database := filepath.Join(root, "server", "factory.sqlite3")
	for _, candidate := range []string{database, database + ".v2-control-plane"} {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect preview control-plane state %s: %w", candidate, err)
		}
	}
	return "", false, nil
}

func validateDataRoot(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve Factory data root: %w", err)
	}
	if marker, found, err := statepath.FindRetiredDatabaseMarker(absolute); err != nil {
		return err
	} else if found {
		return fmt.Errorf("refusing a Factory data root below retired local state at %s", marker)
	}
	canonical, err := statepath.CanonicalProspective(absolute)
	if err != nil {
		return fmt.Errorf("canonicalize Factory data root: %w", err)
	}
	if marker, found, err := statepath.FindRetiredDatabaseMarker(canonical); err != nil {
		return err
	} else if found {
		return fmt.Errorf("refusing a Factory data root below retired local state at %s", marker)
	}
	return nil
}

func factoryDataHome() string {
	if value := os.Getenv("FACTORY_DATA_HOME"); value != "" {
		return value
	}
	return os.Getenv("FACTORY_V2_DATA_HOME")
}
