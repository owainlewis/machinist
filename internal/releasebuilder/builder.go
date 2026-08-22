package releasebuilder

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

var DefaultTargets = []Target{
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
}

type Options struct {
	Root    string
	Output  string
	Version string
	Commit  string
	Targets []Target
	Stdout  io.Writer
}

type module struct {
	Path    string
	Version string
	Sum     string
	Dir     string
	Main    bool
	Replace *module
}

type npmPackage struct {
	Path        string
	Name        string
	Version     string
	License     string
	LicenseFile string
	Resolved    string
	Integrity   string
}

type npmLock struct {
	Packages map[string]struct {
		Version   string `json:"version"`
		Resolved  string `json:"resolved"`
		Integrity string `json:"integrity"`
		License   string `json:"license"`
		Dev       bool   `json:"dev"`
		Link      bool   `json:"link"`
	} `json:"packages"`
}

type npmLicenseManifest struct {
	Version  int               `json:"version"`
	Packages map[string]string `json:"packages"`
}

type manifest struct {
	Version           string   `json:"version"`
	Commit            string   `json:"commit"`
	SourceDate        string   `json:"source_date"`
	GoVersion         string   `json:"go_version"`
	Targets           []Target `json:"targets"`
	ReproducibleBuild bool     `json:"reproducible_build"`
}

func Build(ctx context.Context, options Options) error {
	if err := validateInputs(options.Version, options.Commit); err != nil {
		return err
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return fmt.Errorf("resolve release output: %w", err)
	}
	if err := prepareOutput(output); err != nil {
		return err
	}
	targets := append([]Target(nil), options.Targets...)
	if len(targets) == 0 {
		targets = append(targets, DefaultTargets...)
	}
	if err := validateTargets(targets); err != nil {
		return err
	}
	sourceTime, err := gitSourceTime(ctx, root, options.Commit)
	if err != nil {
		return err
	}
	goVersion, err := commandOutput(ctx, root, releaseGoEnvironment(), "go", "env", "GOVERSION")
	if err != nil {
		return err
	}
	goLicense, err := readGoLicense(ctx, root)
	if err != nil {
		return err
	}
	modules, err := listModules(ctx, root)
	if err != nil {
		return err
	}
	npmPackages, err := listNPMPackages(root)
	if err != nil {
		return err
	}
	notices, err := renderNotices(strings.TrimSpace(goVersion), goLicense, modules, npmPackages, root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(output, "THIRD_PARTY_NOTICES.txt"), notices, 0o644); err != nil {
		return fmt.Errorf("write third-party notices: %w", err)
	}

	staging, err := os.MkdirTemp("", "factory-release-")
	if err != nil {
		return fmt.Errorf("create release staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	for _, target := range targets {
		if options.Stdout != nil {
			fmt.Fprintf(options.Stdout, "building %s/%s\n", target.OS, target.Arch)
		}
		binaryDirectory := filepath.Join(staging, target.OS+"-"+target.Arch)
		if err := os.MkdirAll(binaryDirectory, 0o700); err != nil {
			return fmt.Errorf("create binary staging directory: %w", err)
		}
		ldflags := strings.Join([]string{
			"-s", "-w", "-buildid=",
			"-X", "github.com/owainlewis/factory/internal/buildinfo.Version=" + options.Version,
			"-X", "github.com/owainlewis/factory/internal/buildinfo.Commit=" + options.Commit,
		}, " ")
		environment := releaseGoEnvironment("CGO_ENABLED=0", "GOOS="+target.OS, "GOARCH="+target.Arch)
		if err := runCommand(ctx, root, environment, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", binaryDirectory, "./cmd/factory", "./cmd/factory-server", "./cmd/factory-worker"); err != nil {
			return err
		}
		for _, binary := range []string{"factory", "factory-server", "factory-worker"} {
			if err := verifyBuildMetadata(ctx, filepath.Join(binaryDirectory, binary), target, options.Version, options.Commit); err != nil {
				return err
			}
		}
		archiveName := archiveName(options.Version, target)
		archivePath := filepath.Join(output, archiveName)
		files := []archiveFile{
			{name: "factory", path: filepath.Join(binaryDirectory, "factory"), mode: 0o755},
			{name: "factory-server", path: filepath.Join(binaryDirectory, "factory-server"), mode: 0o755},
			{name: "factory-worker", path: filepath.Join(binaryDirectory, "factory-worker"), mode: 0o755},
			{name: "LICENSE", path: filepath.Join(root, "LICENSE"), mode: 0o644},
			{name: "README.md", path: filepath.Join(root, "README.md"), mode: 0o644},
			{name: "CHANGELOG.md", path: filepath.Join(root, "CHANGELOG.md"), mode: 0o644},
			{name: "RELEASE.md", path: filepath.Join(root, "docs", "release.md"), mode: 0o644},
			{name: "THIRD_PARTY_NOTICES.txt", path: filepath.Join(output, "THIRD_PARTY_NOTICES.txt"), mode: 0o644},
		}
		if err := writeArchive(archivePath, archiveRoot(options.Version, target), files, sourceTime); err != nil {
			return err
		}
		archiveDigest, err := fileSHA256(archivePath)
		if err != nil {
			return err
		}
		sbom, err := renderSPDX(options.Version, options.Commit, sourceTime, archiveName, archiveDigest, modules, npmPackages)
		if err != nil {
			return err
		}
		if err := os.WriteFile(archivePath+".spdx.json", sbom, 0o644); err != nil {
			return fmt.Errorf("write SBOM: %w", err)
		}
	}

	manifestBody, err := json.MarshalIndent(manifest{
		Version: options.Version, Commit: options.Commit,
		SourceDate: sourceTime.UTC().Format(time.RFC3339), GoVersion: strings.TrimSpace(goVersion),
		Targets: targets, ReproducibleBuild: true,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode release manifest: %w", err)
	}
	manifestBody = append(manifestBody, '\n')
	if err := os.WriteFile(filepath.Join(output, "release-manifest.json"), manifestBody, 0o644); err != nil {
		return fmt.Errorf("write release manifest: %w", err)
	}
	if err := writeChecksums(output); err != nil {
		return err
	}
	return nil
}

func validateInputs(version, commit string) error {
	if !versionPattern.MatchString(version) {
		return errors.New("release version must look like v1.2.3 or v1.2.3-rc.1")
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("release commit must be a full 40-character lowercase Git commit")
	}
	return nil
}

func validateTargets(targets []Target) error {
	seen := map[Target]bool{}
	for _, target := range targets {
		if (target.OS != "linux" && target.OS != "darwin") || (target.Arch != "amd64" && target.Arch != "arm64") {
			return fmt.Errorf("unsupported release target %s/%s", target.OS, target.Arch)
		}
		if seen[target] {
			return fmt.Errorf("duplicate release target %s/%s", target.OS, target.Arch)
		}
		seen[target] = true
	}
	return nil
}

func prepareOutput(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create release output: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect release output: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("release output exists and is not a directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read release output: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("release output directory must be empty")
	}
	return nil
}

func gitSourceTime(ctx context.Context, root, commit string) (time.Time, error) {
	head, err := commandOutput(ctx, root, nil, "git", "rev-parse", "HEAD")
	if err != nil {
		return time.Time{}, err
	}
	if strings.TrimSpace(head) != commit {
		return time.Time{}, fmt.Errorf("release commit %s does not match checkout HEAD %s", commit, strings.TrimSpace(head))
	}
	value, err := commandOutput(ctx, root, nil, "git", "show", "-s", "--format=%ct", commit)
	if err != nil {
		return time.Time{}, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse commit timestamp: %w", err)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func listModules(ctx context.Context, root string) ([]module, error) {
	if err := downloadLockedModules(ctx, root, runCommand); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-m", "-json", "all")
	command.Dir = root
	command.Env = append(os.Environ(), releaseGoEnvironment()...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("read module inventory: %w", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start module inventory: %w", err)
	}
	decoder := json.NewDecoder(stdout)
	var modules []module
	for {
		var item module
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			command.Process.Kill()
			command.Wait()
			return nil, fmt.Errorf("decode module inventory: %w", err)
		}
		if !item.Main {
			modules = append(modules, item)
		}
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("list Go modules: %w", err)
	}
	if err := validateModuleReplacements(modules); err != nil {
		return nil, err
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules, nil
}

type commandRunner func(context.Context, string, []string, string, ...string) error

func downloadLockedModules(ctx context.Context, root string, run commandRunner) error {
	moduleFile, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	sumFile, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		return fmt.Errorf("read go.sum: %w", err)
	}

	directory, err := os.MkdirTemp("", "factory-release-mod-")
	if err != nil {
		return fmt.Errorf("create temporary module files: %w", err)
	}
	defer os.RemoveAll(directory)
	temporaryModule := filepath.Join(directory, "release.mod")
	temporarySum := filepath.Join(directory, "release.sum")
	if err := os.WriteFile(temporaryModule, moduleFile, 0o600); err != nil {
		return fmt.Errorf("write temporary go.mod: %w", err)
	}
	if err := os.WriteFile(temporarySum, sumFile, 0o600); err != nil {
		return fmt.Errorf("write temporary go.sum: %w", err)
	}
	if err := run(ctx, root, releaseGoEnvironment(), "go", "mod", "download", "-modfile="+temporaryModule, "all"); err != nil {
		return err
	}

	resolvedModule, err := os.ReadFile(temporaryModule)
	if err != nil {
		return fmt.Errorf("read resolved temporary go.mod: %w", err)
	}
	resolvedSum, err := os.ReadFile(temporarySum)
	if err != nil {
		return fmt.Errorf("read resolved temporary go.sum: %w", err)
	}
	if !bytes.Equal(resolvedModule, moduleFile) || !bytes.Equal(resolvedSum, sumFile) {
		return errors.New("go.mod or go.sum does not fully lock the release module graph; run go mod download all and commit the result")
	}
	return nil
}

func validateModuleReplacements(modules []module) error {
	for _, item := range modules {
		if item.Dir == "" {
			return fmt.Errorf("module %s %s has no downloaded source directory", item.Path, item.Version)
		}
		if item.Replace == nil {
			continue
		}
		if item.Replace.Version == "" {
			return fmt.Errorf("module %s uses local replacement %s; releases require versioned module provenance", item.Path, item.Replace.Path)
		}
		if item.Replace.Path == "" || item.Replace.Dir == "" {
			return fmt.Errorf("module %s has incomplete replacement provenance", item.Path)
		}
	}
	return nil
}

func listNPMPackages(root string) ([]npmPackage, error) {
	lockBody, err := os.ReadFile(filepath.Join(root, "web", "package-lock.json"))
	if err != nil {
		return nil, fmt.Errorf("read npm lockfile: %w", err)
	}
	var lock npmLock
	if err := json.Unmarshal(lockBody, &lock); err != nil {
		return nil, fmt.Errorf("decode npm lockfile: %w", err)
	}
	manifestRoot := filepath.Join(root, "third_party", "npm")
	manifestBody, err := os.ReadFile(filepath.Join(manifestRoot, "licenses.json"))
	if err != nil {
		return nil, fmt.Errorf("read npm license manifest: %w", err)
	}
	var manifest npmLicenseManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return nil, fmt.Errorf("decode npm license manifest: %w", err)
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("unsupported npm license manifest version %d", manifest.Version)
	}
	var packages []npmPackage
	seen := map[string]bool{}
	for path, item := range lock.Packages {
		if path == "" || item.Dev || item.Link || !strings.Contains(path, "node_modules/") {
			continue
		}
		licenseFile, exists := manifest.Packages[path]
		if !exists {
			return nil, fmt.Errorf("production npm package %s is missing from third_party/npm/licenses.json", path)
		}
		if licenseFile == "" || filepath.Base(licenseFile) != licenseFile {
			return nil, fmt.Errorf("production npm package %s has an invalid license file", path)
		}
		licensePath := filepath.Join(manifestRoot, licenseFile)
		info, err := os.Lstat(licensePath)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("production npm package %s license %s is missing or not regular", path, licenseFile)
		}
		name := path[strings.LastIndex(path, "node_modules/")+len("node_modules/"):]
		if name == "" || item.Version == "" || item.License == "" || item.Resolved == "" {
			return nil, fmt.Errorf("production npm package %s has incomplete locked metadata", path)
		}
		packages = append(packages, npmPackage{
			Path: path, Name: name, Version: item.Version, License: item.License,
			LicenseFile: licenseFile, Resolved: item.Resolved, Integrity: item.Integrity,
		})
		seen[path] = true
	}
	for path := range manifest.Packages {
		if !seen[path] {
			return nil, fmt.Errorf("npm license manifest entry %s is not a locked production package", path)
		}
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })
	if len(packages) == 0 {
		return nil, errors.New("npm production dependency inventory is empty")
	}
	return packages, nil
}

func readGoLicense(ctx context.Context, root string) ([]byte, error) {
	goRoot, err := commandOutput(ctx, root, releaseGoEnvironment(), "go", "env", "GOROOT")
	if err != nil {
		return nil, err
	}
	licensePath := filepath.Join(strings.TrimSpace(goRoot), "LICENSE")
	info, err := os.Lstat(licensePath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Go toolchain license is missing or not regular: %s", licensePath)
	}
	body, err := os.ReadFile(licensePath)
	if err != nil {
		return nil, fmt.Errorf("read Go toolchain license: %w", err)
	}
	if len(body) == 0 || len(body) > 2<<20 {
		return nil, fmt.Errorf("Go toolchain license has invalid size %d", len(body))
	}
	return body, nil
}

func renderNotices(goVersion string, goLicense []byte, modules []module, npmPackages []npmPackage, root string) ([]byte, error) {
	var output strings.Builder
	output.WriteString("Factory third-party notices\n\n")
	output.WriteString("This file is generated from the Go toolchain, complete Go module graph, and shipped npm packages. License texts follow.\n")
	fmt.Fprintf(&output, "\n================================================================================\nGo toolchain %s runtime and standard library\n\n--- LICENSE ---\n", goVersion)
	goLicenseText := bytesToText(goLicense)
	output.WriteString(goLicenseText)
	if !strings.HasSuffix(goLicenseText, "\n") {
		output.WriteByte('\n')
	}
	for _, item := range modules {
		effective := item
		if item.Replace != nil {
			effective = *item.Replace
		}
		entries, err := os.ReadDir(effective.Dir)
		if err != nil {
			return nil, fmt.Errorf("read licenses for %s: %w", item.Path, err)
		}
		var licenseFiles []string
		for _, entry := range entries {
			if entry.Type().IsRegular() && isLicenseFile(entry.Name()) {
				licenseFiles = append(licenseFiles, entry.Name())
			}
		}
		sort.Strings(licenseFiles)
		if len(licenseFiles) == 0 {
			return nil, fmt.Errorf("module %s %s has no root license or notice file", item.Path, item.Version)
		}
		fmt.Fprintf(&output, "\n================================================================================\n%s %s\n", item.Path, item.Version)
		if item.Replace != nil {
			fmt.Fprintf(&output, "replaced by %s %s\n", effective.Path, effective.Version)
		}
		for _, name := range licenseFiles {
			body, err := os.ReadFile(filepath.Join(effective.Dir, name))
			if err != nil {
				return nil, fmt.Errorf("read %s license %s: %w", item.Path, name, err)
			}
			if len(body) > 2<<20 {
				return nil, fmt.Errorf("license file %s for %s exceeds 2 MiB", name, item.Path)
			}
			fmt.Fprintf(&output, "\n--- %s ---\n", name)
			text := bytesToText(body)
			output.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				output.WriteByte('\n')
			}
		}
	}
	for _, item := range npmPackages {
		body, err := os.ReadFile(filepath.Join(root, "third_party", "npm", item.LicenseFile))
		if err != nil {
			return nil, fmt.Errorf("read npm license for %s: %w", item.Name, err)
		}
		fmt.Fprintf(&output, "\n================================================================================\nnpm:%s %s (%s)\n\n--- %s ---\n", item.Name, item.Version, item.License, item.LicenseFile)
		text := bytesToText(body)
		output.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			output.WriteByte('\n')
		}
	}
	return []byte(output.String()), nil
}

func isLicenseFile(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"LICENSE", "LICENCE", "COPYING", "NOTICE", "PATENTS"} {
		if upper == prefix || strings.HasPrefix(upper, prefix+".") || strings.HasPrefix(upper, prefix+"-") {
			return true
		}
	}
	return false
}

func bytesToText(body []byte) string {
	return strings.ReplaceAll(string(body), "\r\n", "\n")
}

type archiveFile struct {
	name string
	path string
	mode fs.FileMode
}

func writeArchive(path, root string, files []archiveFile, sourceTime time.Time) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create release archive: %w", err)
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close release archive: %w", err)
		}
	}()
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip writer: %w", err)
	}
	gzipWriter.Header.ModTime = sourceTime
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	for _, item := range files {
		body, err := os.ReadFile(item.path)
		if err != nil {
			return fmt.Errorf("read archive file %s: %w", item.name, err)
		}
		header := &tar.Header{
			Name: root + "/" + item.name, Mode: int64(item.mode.Perm()), Size: int64(len(body)),
			ModTime: sourceTime, AccessTime: time.Time{}, ChangeTime: time.Time{},
			Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write archive header: %w", err)
		}
		if _, err := tarWriter.Write(body); err != nil {
			return fmt.Errorf("write archive file: %w", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("close tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("close gzip archive: %w", err)
	}
	return nil
}

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Relationships     []spdxRelationship `json:"relationships"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	SPDXID           string         `json:"SPDXID"`
	Name             string         `json:"name"`
	VersionInfo      string         `json:"versionInfo,omitempty"`
	PackageFileName  string         `json:"packageFileName,omitempty"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	Checksums        []spdxChecksum `json:"checksums,omitempty"`
	ExternalRefs     []spdxRef      `json:"externalRefs,omitempty"`
}

type spdxChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type spdxRef struct {
	ReferenceCategory string `json:"referenceCategory"`
	ReferenceType     string `json:"referenceType"`
	ReferenceLocator  string `json:"referenceLocator"`
}

type spdxRelationship struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}

func renderSPDX(version, commit string, sourceTime time.Time, archive, digest string, modules []module, npmPackages []npmPackage) ([]byte, error) {
	rootID := "SPDXRef-Package-factory"
	document := spdxDocument{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name:              strings.TrimSuffix(archive, ".tar.gz"),
		DocumentNamespace: "https://github.com/owainlewis/factory/releases/download/" + version + "/" + archive + ".spdx.json",
		CreationInfo:      spdxCreationInfo{Created: sourceTime.UTC().Format(time.RFC3339), Creators: []string{"Tool: factory-release"}},
		Packages: []spdxPackage{{
			SPDXID: rootID, Name: "factory", VersionInfo: version, PackageFileName: archive,
			DownloadLocation: "https://github.com/owainlewis/factory/releases/download/" + version + "/" + archive,
			FilesAnalyzed:    false, LicenseConcluded: "MIT", LicenseDeclared: "MIT",
			Checksums:    []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: digest}},
			ExternalRefs: []spdxRef{{ReferenceCategory: "OTHER", ReferenceType: "vcs", ReferenceLocator: "git+https://github.com/owainlewis/factory@" + commit}},
		}},
		Relationships: []spdxRelationship{{Element: "SPDXRef-DOCUMENT", Type: "DESCRIBES", Related: rootID}},
	}
	packageIDs := map[string]bool{rootID: true}
	addPackage := func(value spdxPackage) {
		if !packageIDs[value.SPDXID] {
			document.Packages = append(document.Packages, value)
			packageIDs[value.SPDXID] = true
		}
	}
	for _, item := range modules {
		requestedID := componentSPDXID("go", item.Path, item.Version)
		addPackage(spdxPackage{
			SPDXID: requestedID, Name: item.Path, VersionInfo: item.Version,
			DownloadLocation: "NOASSERTION",
			FilesAnalyzed:    false, LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
			ExternalRefs: []spdxRef{{ReferenceCategory: "PACKAGE-MANAGER", ReferenceType: "purl", ReferenceLocator: "pkg:golang/" + item.Path + "@" + item.Version}},
		})
		dependencyID := requestedID
		if item.Replace != nil {
			replacementID := componentSPDXID("go", item.Replace.Path, item.Replace.Version)
			addPackage(spdxPackage{
				SPDXID: replacementID, Name: item.Replace.Path, VersionInfo: item.Replace.Version,
				DownloadLocation: "NOASSERTION",
				FilesAnalyzed:    false, LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
				ExternalRefs: []spdxRef{{ReferenceCategory: "PACKAGE-MANAGER", ReferenceType: "purl", ReferenceLocator: "pkg:golang/" + item.Replace.Path + "@" + item.Replace.Version}},
			})
			document.Relationships = append(document.Relationships, spdxRelationship{Element: replacementID, Type: "VARIANT_OF", Related: requestedID})
			dependencyID = replacementID
		}
		document.Relationships = append(document.Relationships, spdxRelationship{Element: rootID, Type: "DEPENDS_ON", Related: dependencyID})
	}
	for _, item := range npmPackages {
		id := componentSPDXID("npm", item.Name, item.Version)
		pkg := spdxPackage{
			SPDXID: id, Name: item.Name, VersionInfo: item.Version,
			DownloadLocation: item.Resolved,
			FilesAnalyzed:    false, LicenseConcluded: "NOASSERTION", LicenseDeclared: item.License,
			ExternalRefs: []spdxRef{{ReferenceCategory: "PACKAGE-MANAGER", ReferenceType: "purl", ReferenceLocator: "pkg:npm/" + strings.ReplaceAll(item.Name, "@", "%40") + "@" + item.Version}},
		}
		if algorithm, checksum, ok := npmIntegrityChecksum(item.Integrity); ok {
			pkg.Checksums = []spdxChecksum{{Algorithm: algorithm, ChecksumValue: checksum}}
		}
		addPackage(pkg)
		document.Relationships = append(document.Relationships, spdxRelationship{Element: rootID, Type: "DEPENDS_ON", Related: id})
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode SPDX SBOM: %w", err)
	}
	return append(encoded, '\n'), nil
}

func componentSPDXID(ecosystem, name, version string) string {
	digest := sha256.Sum256([]byte(ecosystem + "\x00" + name + "\x00" + version))
	return "SPDXRef-Package-" + ecosystem + "-" + hex.EncodeToString(digest[:8])
}

func npmIntegrityChecksum(integrity string) (string, string, bool) {
	algorithm, encoded, found := strings.Cut(integrity, "-")
	if !found || strings.ToLower(algorithm) != "sha512" {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != sha512.Size {
		return "", "", false
	}
	return "SHA512", hex.EncodeToString(decoded), true
}

func writeChecksums(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read release files: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && entry.Name() != "SHA256SUMS" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var output strings.Builder
	for _, name := range names {
		digest, err := fileSHA256(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&output, "%s  %s\n", digest, name)
	}
	if err := os.WriteFile(filepath.Join(directory, "SHA256SUMS"), []byte(output.String()), 0o644); err != nil {
		return fmt.Errorf("write release checksums: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for checksum: %w", filepath.Base(path), err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("checksum %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyBuildMetadata(ctx context.Context, path string, target Target, version, commit string) error {
	output, err := commandOutput(ctx, "", nil, "go", "version", "-m", path)
	if err != nil {
		return err
	}
	for _, expected := range []string{"GOOS=" + target.OS, "GOARCH=" + target.Arch} {
		if !strings.Contains(output, expected) {
			return fmt.Errorf("%s metadata does not contain %q", filepath.Base(path), expected)
		}
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s metadata: %w", filepath.Base(path), err)
	}
	for _, expected := range []string{version, commit} {
		if !bytes.Contains(binary, []byte(expected)) {
			return fmt.Errorf("%s does not embed %q", filepath.Base(path), expected)
		}
	}
	return nil
}

func archiveName(version string, target Target) string {
	return archiveRoot(version, target) + ".tar.gz"
}

func archiveRoot(version string, target Target) string {
	return "factory_" + strings.TrimPrefix(version, "v") + "_" + target.OS + "_" + target.Arch
}

func releaseGoEnvironment(overrides ...string) []string {
	environment := []string{
		"GO111MODULE=on",
		"GOARCH=",
		"GOAMD64=v1",
		"GOARM64=v8.0",
		"GODEBUG=",
		"GOENV=off",
		"GOEXPERIMENT=",
		"GOFIPS140=off",
		"GOFLAGS=",
		"GOOS=",
		"GOWORK=off",
	}
	if moduleCache := os.Getenv("FACTORY_RELEASE_GOMODCACHE"); moduleCache != "" {
		environment = append(environment, "GOMODCACHE="+moduleCache)
	}
	if buildCache := os.Getenv("FACTORY_RELEASE_GOCACHE"); buildCache != "" {
		environment = append(environment, "GOCACHE="+buildCache)
	}
	return append(environment, overrides...)
}

func runCommand(ctx context.Context, directory string, environment []string, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", strings.Join(append([]string{name}, arguments...), " "), err)
	}
	return nil
}

func commandOutput(ctx context.Context, directory string, environment []string, name string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s: %w: %s", strings.Join(append([]string{name}, arguments...), " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func Verify(ctx context.Context, repositoryRoot, directory, version, commit string, target Target, execute bool) error {
	if err := validateInputs(version, commit); err != nil {
		return err
	}
	if err := verifyChecksums(directory); err != nil {
		return err
	}
	sourceTime, err := gitSourceTime(ctx, repositoryRoot, commit)
	if err != nil {
		return err
	}
	goVersion, err := commandOutput(ctx, repositoryRoot, releaseGoEnvironment(), "go", "env", "GOVERSION")
	if err != nil {
		return err
	}
	manifestBody, err := os.ReadFile(filepath.Join(directory, "release-manifest.json"))
	if err != nil {
		return fmt.Errorf("read release manifest: %w", err)
	}
	var actualManifest manifest
	if err := json.Unmarshal(manifestBody, &actualManifest); err != nil {
		return fmt.Errorf("decode release manifest: %w", err)
	}
	expectedManifest := manifest{
		Version: version, Commit: commit, SourceDate: sourceTime.UTC().Format(time.RFC3339),
		GoVersion: strings.TrimSpace(goVersion), Targets: DefaultTargets, ReproducibleBuild: true,
	}
	if !reflect.DeepEqual(actualManifest, expectedManifest) {
		return errors.New("release manifest does not match the source, toolchain, and supported targets")
	}
	modules, err := listModules(ctx, repositoryRoot)
	if err != nil {
		return err
	}
	npmPackages, err := listNPMPackages(repositoryRoot)
	if err != nil {
		return err
	}
	goLicense, err := readGoLicense(ctx, repositoryRoot)
	if err != nil {
		return err
	}
	expectedNotices, err := renderNotices(strings.TrimSpace(goVersion), goLicense, modules, npmPackages, repositoryRoot)
	if err != nil {
		return err
	}
	actualNotices, err := os.ReadFile(filepath.Join(directory, "THIRD_PARTY_NOTICES.txt"))
	if err != nil {
		return fmt.Errorf("read third-party notices: %w", err)
	}
	if !bytes.Equal(actualNotices, expectedNotices) {
		return errors.New("third-party notices do not match the locked Go and npm production dependencies")
	}
	archive := filepath.Join(directory, archiveName(version, target))
	archiveDigest, err := fileSHA256(archive)
	if err != nil {
		return err
	}
	sbomPath := archive + ".spdx.json"
	body, err := os.ReadFile(sbomPath)
	if err != nil {
		return fmt.Errorf("read SBOM: %w", err)
	}
	expectedSBOM, err := renderSPDX(version, commit, sourceTime, filepath.Base(archive), archiveDigest, modules, npmPackages)
	if err != nil {
		return err
	}
	if err := verifySPDXDocument(body, expectedSBOM); err != nil {
		return err
	}
	extracted, err := os.MkdirTemp("", "factory-release-verify-")
	if err != nil {
		return fmt.Errorf("create verification directory: %w", err)
	}
	defer os.RemoveAll(extracted)
	if err := extractArchive(archive, extracted); err != nil {
		return err
	}
	archiveDirectory := filepath.Join(extracted, archiveRoot(version, target))
	for _, binary := range []string{"factory", "factory-server", "factory-worker"} {
		path := filepath.Join(archiveDirectory, binary)
		if err := verifyBuildMetadata(ctx, path, target, version, commit); err != nil {
			return err
		}
		if execute {
			if target.OS != runtime.GOOS || target.Arch != runtime.GOARCH {
				return fmt.Errorf("cannot execute %s/%s artifact on %s/%s", target.OS, target.Arch, runtime.GOOS, runtime.GOARCH)
			}
			output, err := commandOutput(ctx, "", nil, path, "version")
			if err != nil {
				return err
			}
			if !strings.Contains(output, version) || !strings.Contains(output, commit) {
				return fmt.Errorf("%s version output is incomplete: %s", binary, strings.TrimSpace(output))
			}
		}
	}
	for _, required := range []string{"LICENSE", "README.md", "CHANGELOG.md", "RELEASE.md", "THIRD_PARTY_NOTICES.txt"} {
		if info, err := os.Stat(filepath.Join(archiveDirectory, required)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("release archive is missing %s", required)
		}
	}
	archivedNotices, err := os.ReadFile(filepath.Join(archiveDirectory, "THIRD_PARTY_NOTICES.txt"))
	if err != nil || !bytes.Equal(archivedNotices, expectedNotices) {
		return errors.New("archived third-party notices do not match the locked dependency inventory")
	}
	return nil
}

func verifySPDXDocument(actual, expected []byte) error {
	var document spdxDocument
	if err := json.Unmarshal(actual, &document); err != nil {
		return fmt.Errorf("decode SBOM: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("SBOM does not match the archive, source commit, or locked dependency inventory")
	}
	return nil
}

func verifyChecksums(directory string) error {
	file, err := os.Open(filepath.Join(directory, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("open SHA256SUMS: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	listed := map[string]bool{}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fields[0]) || filepath.Base(fields[1]) != fields[1] {
			return fmt.Errorf("invalid SHA256SUMS line %q", scanner.Text())
		}
		if listed[fields[1]] {
			return fmt.Errorf("duplicate SHA256SUMS entry %s", fields[1])
		}
		info, err := os.Lstat(filepath.Join(directory, fields[1]))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("checksummed release file %s is missing or not regular", fields[1])
		}
		digest, err := fileSHA256(filepath.Join(directory, fields[1]))
		if err != nil {
			return err
		}
		if digest != fields[0] {
			return fmt.Errorf("checksum mismatch for %s", fields[1])
		}
		listed[fields[1]] = true
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SHA256SUMS: %w", err)
	}
	if len(listed) == 0 {
		return errors.New("SHA256SUMS is empty")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read release directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		if !entry.Type().IsRegular() || !listed[entry.Name()] {
			return fmt.Errorf("release file %s is not covered by SHA256SUMS", entry.Name())
		}
	}
	return nil
}

func extractArchive(path, destination string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Name == "" || filepath.IsAbs(header.Name) {
			return fmt.Errorf("unsafe archive entry %q", header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(header.Name))
		relative, err := filepath.Rel(destination, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archive entry %q", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create archive directory: %w", err)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(header.Mode)&0o755)
		if err != nil {
			return fmt.Errorf("create extracted file: %w", err)
		}
		_, copyErr := io.CopyN(output, tarReader, header.Size)
		closeErr := output.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %s: %w", header.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close extracted file: %w", closeErr)
		}
	}
	return nil
}
