package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupModeRejectsMissingSourceWithoutCreatingState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "missing", "factory.sqlite3")
	destination := filepath.Join(root, "backup", "factory.sqlite3")
	handled, err := runRecoveryMode(context.Background(), source, destination, "", io.Discard)
	if !handled || err == nil {
		t.Fatalf("missing-source backup = handled %v, error %v", handled, err)
	}
	for _, path := range []string{source, source + ".v2-control-plane", destination} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("missing-source CLI backup created %s: %v", path, statErr)
		}
	}
}

func TestDefaultDatabasePathUsesFactoryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_V2_DATA_HOME", "")

	database, root, err := defaultDatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(home, ".factory")
	if root != wantRoot {
		t.Fatalf("root = %q, want %q", root, wantRoot)
	}
	if want := filepath.Join(wantRoot, "server", "factory.sqlite3"); database != want {
		t.Fatalf("database = %q, want %q", database, want)
	}
}

func TestDefaultDatabasePathHonorsOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FACTORY_DATA_HOME", root)

	database, gotRoot, err := defaultDatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if want := filepath.Join(root, "server", "factory.sqlite3"); database != want {
		t.Fatalf("database = %q, want %q", database, want)
	}
}

func TestDefaultDatabasePathHonorsPreviewAlias(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_V2_DATA_HOME", root)

	database, gotRoot, err := defaultDatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if want := filepath.Join(root, "server", "factory.sqlite3"); database != want {
		t.Fatalf("database = %q, want %q", database, want)
	}
}

func TestServerBootstrapConfigIsOptionalAndResolvesRelativeDatabase(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("FACTORY_SERVER_CONFIG", "")

	config, err := loadServerBootstrapConfig(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "" || config.Database != "" || config.path != filepath.Join(dataRoot, "config.toml") {
		t.Fatalf("missing optional config = %#v", config)
	}

	path := filepath.Join(dataRoot, "config.toml")
	if err := os.WriteFile(path, []byte("listen = \"127.0.0.1:7447\"\ndatabase = \"state/factory.sqlite3\"\ndefault_build_runtime = \"PI\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err = loadServerBootstrapConfig(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:7447" || config.Database != filepath.Join(dataRoot, "state", "factory.sqlite3") || config.DefaultBuildRuntime != "pi" {
		t.Fatalf("loaded config = %#v", config)
	}
}

func TestServerBootstrapConfigRejectsUnknownFieldsAndSymlinks(t *testing.T) {
	dataRoot := t.TempDir()
	path := filepath.Join(dataRoot, "config.toml")
	t.Setenv("FACTORY_SERVER_CONFIG", path)
	if err := os.WriteFile(path, []byte("listen = \"127.0.0.1:7337\"\nprovider = \"github\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServerBootstrapConfig(dataRoot); err == nil || !strings.Contains(err.Error(), "unknown Factory server configuration fields: provider") {
		t.Fatalf("unknown field error = %v", err)
	}

	target := filepath.Join(dataRoot, "target.toml")
	if err := os.WriteFile(target, []byte("listen = \"127.0.0.1:7337\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadServerBootstrapConfig(dataRoot); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestServerBootstrapConfigAcceptsRetiredWebhookFieldsDuringUpgrade(t *testing.T) {
	dataRoot := t.TempDir()
	path := filepath.Join(dataRoot, "config.toml")
	t.Setenv("FACTORY_SERVER_CONFIG", path)
	if err := os.WriteFile(path, []byte(`
listen = "127.0.0.1:7337"
webhook_listen = "0.0.0.0:7444"
webhook_tls_cert = "/etc/factory/webhook.crt"
webhook_tls_key = "/etc/factory/webhook.key"
github_webhook_secret_file = "/etc/factory/webhook.secret"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := loadServerBootstrapConfig(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:7337" {
		t.Fatalf("loaded config = %#v", config)
	}
}

func TestRemoteWorkerTLSConfigurationIsAllOrNothing(t *testing.T) {
	for _, values := range [][3]string{
		{"0.0.0.0:7443", "", ""},
		{"", "server.crt", "server.key"},
		{"0.0.0.0:7443", "server.crt", ""},
	} {
		if err := validateWorkerTLSConfig(values[0], values[1], values[2]); err == nil {
			t.Fatalf("accepted partial remote TLS configuration %#v", values)
		}
	}
	if err := validateWorkerTLSConfig("0.0.0.0:7443", "server.crt", "server.key"); err != nil {
		t.Fatalf("complete remote TLS configuration rejected: %v", err)
	}
}

func TestValidateNoLegacyServerDefaultRefusesLegacyState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_DATA_HOME", "")
	t.Setenv("FACTORY_V2_DATA_HOME", "")
	legacyRoot := filepath.Join(home, ".factory-v2")
	legacyDatabase := filepath.Join(legacyRoot, "server", "factory.sqlite3")
	if err := os.MkdirAll(filepath.Dir(legacyDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyDatabase, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	newRoot := filepath.Join(home, ".factory")
	err := validateNoLegacyServerDefault(newRoot)
	if err == nil {
		t.Fatal("preview server state was accepted")
	}
	for _, want := range []string{legacyDatabase, "FACTORY_DATA_HOME=" + legacyRoot} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	if err := validateLegacyServerSelection("", true, newRoot); err != nil {
		t.Fatalf("explicit database selection was blocked: %v", err)
	}
	if err := validateLegacyServerSelection(legacyRoot, false, newRoot); err != nil {
		t.Fatalf("explicit data home was blocked: %v", err)
	}

	t.Setenv("FACTORY_DATA_HOME", legacyRoot)
	database, root, err := defaultDatabasePath()
	if err != nil {
		t.Fatalf("explicit legacy root: %v", err)
	}
	if root != legacyRoot || database != legacyDatabase {
		t.Fatalf("explicit legacy paths = %q, %q", root, database)
	}

	currentDatabase := filepath.Join(newRoot, "server", "factory.sqlite3")
	if err := os.MkdirAll(filepath.Dir(currentDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentDatabase, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNoLegacyServerDefault(newRoot); err != nil {
		t.Fatalf("preview state overrode current default: %v", err)
	}
}

func TestValidateDataRootRefusesRetiredStateAncestor(t *testing.T) {
	retiredRoot := t.TempDir()
	marker := filepath.Join(retiredRoot, "factory.sqlite3")
	if err := os.WriteFile(marker, []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(retiredRoot, "nested", "control-plane")

	err := validateDataRoot(dataRoot)
	if err == nil || !strings.Contains(err.Error(), "below retired local state") {
		t.Fatalf("validate data root error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(retiredRoot, "nested")); !os.IsNotExist(err) {
		t.Fatalf("validation mutated retired root: %v", err)
	}
}

func TestValidateDataRootRefusesSymlinkedRetiredStateAncestor(t *testing.T) {
	retiredRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(retiredRoot, "factory.sqlite3"), []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-retired")
	if err := os.Symlink(retiredRoot, link); err != nil {
		t.Fatal(err)
	}

	err := validateDataRoot(filepath.Join(link, "nested", "control-plane"))
	if err == nil || !strings.Contains(err.Error(), "below retired local state") {
		t.Fatalf("validate symlinked data root error = %v", err)
	}
}

func TestValidateDataRootAllowsSeparatePath(t *testing.T) {
	if err := validateDataRoot(filepath.Join(t.TempDir(), "nested", "control-plane")); err != nil {
		t.Fatalf("validate separate data root: %v", err)
	}
}

func TestValidateDataRootAllowsRetiredRepositorySibling(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".factory")
	retiredRepositoryRoot := filepath.Join(root, "0123456789abcdef0123")
	if err := os.MkdirAll(retiredRepositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(retiredRepositoryRoot, "factory.sqlite3"), []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateDataRoot(root); err != nil {
		t.Fatalf("validate shared Factory home with isolated retired sibling: %v", err)
	}
}
