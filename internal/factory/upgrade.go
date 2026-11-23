package factory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/config"
	"go.yaml.in/yaml/v3"
)

// ManifestName is the hash manifest stored inside the factory directory,
// recording the SHA-256 of each shipped file as last installed or upgraded.
const ManifestName = "manifest.yaml"

const manifestHeader = "# Written by machinist init and machinist factory upgrade.\n" +
	"# Hashes of shipped factory files as last installed, used to tell edited files from out-of-date ones.\n"

// FileStatus describes how one installed factory file compares to the binary's
// shipped copy and the manifest from when it was installed.
type FileStatus string

const (
	// FileCurrent matches the binary's shipped file.
	FileCurrent FileStatus = "current"
	// FileCustomized was edited by the team on top of the current shipped
	// version (the manifest already matches the binary's shipped copy).
	FileCustomized FileStatus = "customized"
	// FileOutdated matches the older shipped hash in the manifest (unedited).
	FileOutdated FileStatus = "outdated"
	// FileEdited differs from both the binary's shipped copy and the manifest
	// (or has no manifest entry), so upgrading would overwrite local edits.
	FileEdited FileStatus = "edited"
	// FileMissing is a shipped file that does not exist in the factory.
	FileMissing FileStatus = "missing"
)

// FileUpdate describes one shipped factory file that needs attention.
type FileUpdate struct {
	Path   string     `json:"path"`
	Status FileStatus `json:"status"`
}

// Manifest records the hash of each shipped file as last installed.
type Manifest struct {
	Files map[string]string `yaml:"files"`
}

// UpgradeAction is what Upgrade did to one shipped file.
type UpgradeAction string

const (
	ActionUpdated UpgradeAction = "updated"
	ActionCreated UpgradeAction = "created"
	ActionSkipped UpgradeAction = "skipped"
)

// UpgradeEntry reports the result of upgrading one shipped factory file.
type UpgradeEntry struct {
	Path   string
	Status FileStatus
	Action UpgradeAction
	// Diff is the unified diff from the installed file to the shipped file
	// when Action is ActionSkipped.
	Diff string
}

// UpgradeResult summarizes one run of Upgrade.
type UpgradeResult struct {
	Entries         []UpgradeEntry
	ManifestUpdated bool
}

// Shipped returns the starter factory files embedded in this Machinist binary.
func Shipped() fs.FS {
	sub, err := fs.Sub(machinistexamples.Files, "factory")
	if err != nil {
		panic(err)
	}
	return sub
}

// HashBytes returns the manifest digest format for a file body.
func HashBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LoadManifest reads <directory>/manifest.yaml. A missing manifest is not an
// error and returns an empty manifest with exists == false.
func LoadManifest(directory string) (Manifest, bool, error) {
	path := filepath.Join(directory, ManifestName)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{Files: map[string]string{}}, false, nil
	}
	if err != nil {
		return Manifest{}, false, fmt.Errorf("inspect %s: %w", ManifestName, err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, false, fmt.Errorf("%s already exists and is not a regular file", ManifestName)
	}
	if info.Size() > maxFileBytes {
		return Manifest{}, false, fmt.Errorf("%s exceeds %d bytes", ManifestName, maxFileBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read %s: %w", ManifestName, err)
	}
	var manifest Manifest
	if err := config.DecodeStrict(path, body, &manifest); err != nil {
		return Manifest{}, false, err
	}
	if manifest.Files == nil {
		manifest.Files = map[string]string{}
	}
	return manifest, true, nil
}

// WriteManifest writes <directory>/manifest.yaml atomically.
func WriteManifest(directory string, manifest Manifest) error {
	path := filepath.Join(directory, ManifestName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s already exists and is not a regular file", ManifestName)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", ManifestName, err)
	}
	if manifest.Files == nil {
		manifest.Files = map[string]string{}
	}
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode %s: %w", ManifestName, err)
	}
	body := append([]byte(manifestHeader), encoded...)
	return writeFileAtomic(path, body, 0o600)
}

// InitManifest records hashes for shipped files in directory that currently
// match the binary's shipped copy, without replacing entries already recorded
// in manifest.yaml. It returns true when manifest.yaml was created or updated.
func InitManifest(directory string) (bool, error) {
	return InitManifestAgainst(directory, Shipped())
}

// InitManifestAgainst is InitManifest with an explicit shipped filesystem.
func InitManifestAgainst(directory string, shipped fs.FS) (bool, error) {
	manifest, exists, err := LoadManifest(directory)
	if err != nil {
		return false, err
	}
	files, err := readShippedFiles(shipped)
	if err != nil {
		return false, err
	}
	changed := !exists
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		if _, already := manifest.Files[rel]; already {
			continue
		}
		target := filepath.Join(directory, filepath.FromSlash(rel))
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%s is not a regular file", rel)
		}
		body, err := os.ReadFile(target)
		if err != nil {
			return false, fmt.Errorf("read %s: %w", rel, err)
		}
		shippedHash := HashBytes(files[rel])
		if HashBytes(body) == shippedHash {
			manifest.Files[rel] = shippedHash
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	if err := WriteManifest(directory, manifest); err != nil {
		return false, err
	}
	return true, nil
}

// Check compares directory against the binary's shipped factory files and
// returns every file that is outdated, edited against an older shipped version,
// or missing, sorted by path.
func Check(directory string) ([]FileUpdate, error) {
	return CheckAgainst(directory, Shipped())
}

// CheckAgainst compares directory against an explicit shipped filesystem.
func CheckAgainst(directory string, shipped fs.FS) ([]FileUpdate, error) {
	manifest, _, err := LoadManifest(directory)
	if err != nil {
		return nil, err
	}
	files, err := readShippedFiles(shipped)
	if err != nil {
		return nil, err
	}
	var updates []FileUpdate
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		status, _, err := classifyFile(directory, rel, files[rel], manifest.Files[rel])
		if err != nil {
			return nil, err
		}
		switch status {
		case FileOutdated, FileEdited, FileMissing:
			updates = append(updates, FileUpdate{Path: rel, Status: status})
		}
	}
	return updates, nil
}

// Upgrade updates unedited outdated files and creates missing shipped files in
// directory. Edited files are never overwritten unless force is true; when
// skipped, their entry includes a unified diff against the shipped file.
func Upgrade(directory string, force bool) (UpgradeResult, error) {
	return UpgradeAgainst(directory, Shipped(), force)
}

// UpgradeAgainst performs Upgrade against an explicit shipped filesystem.
func UpgradeAgainst(directory string, shipped fs.FS, force bool) (UpgradeResult, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return UpgradeResult{}, fmt.Errorf("inspect factory directory %s: %w", directory, err)
	}
	if !info.IsDir() {
		return UpgradeResult{}, fmt.Errorf("factory %s is not a directory", directory)
	}
	manifest, exists, err := LoadManifest(directory)
	if err != nil {
		return UpgradeResult{}, err
	}
	files, err := readShippedFiles(shipped)
	if err != nil {
		return UpgradeResult{}, err
	}

	next := Manifest{Files: make(map[string]string, len(files))}
	dirty := false
	var result UpgradeResult

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		shippedBody := files[rel]
		shippedHash := HashBytes(shippedBody)
		recorded := manifest.Files[rel]
		status, diskBody, err := classifyFile(directory, rel, shippedBody, recorded)
		if err != nil {
			return UpgradeResult{}, err
		}
		target := filepath.Join(directory, filepath.FromSlash(rel))

		switch status {
		case FileCurrent:
			next.Files[rel] = shippedHash
			if recorded != shippedHash {
				dirty = true
			}
		case FileCustomized:
			next.Files[rel] = shippedHash
		case FileOutdated:
			if err := writeShippedFile(target, rel, shippedBody); err != nil {
				return UpgradeResult{}, err
			}
			next.Files[rel] = shippedHash
			dirty = true
			result.Entries = append(result.Entries, UpgradeEntry{
				Path:   rel,
				Status: status,
				Action: ActionUpdated,
			})
		case FileMissing:
			if err := writeShippedFile(target, rel, shippedBody); err != nil {
				return UpgradeResult{}, err
			}
			next.Files[rel] = shippedHash
			dirty = true
			result.Entries = append(result.Entries, UpgradeEntry{
				Path:   rel,
				Status: status,
				Action: ActionCreated,
			})
		case FileEdited:
			if force {
				if err := writeShippedFile(target, rel, shippedBody); err != nil {
					return UpgradeResult{}, err
				}
				next.Files[rel] = shippedHash
				dirty = true
				result.Entries = append(result.Entries, UpgradeEntry{
					Path:   rel,
					Status: status,
					Action: ActionUpdated,
				})
			} else {
				if recorded != "" {
					next.Files[rel] = recorded
				}
				result.Entries = append(result.Entries, UpgradeEntry{
					Path:   rel,
					Status: status,
					Action: ActionSkipped,
					Diff:   UnifiedDiff(rel+" (installed)", rel+" (shipped)", string(diskBody), string(shippedBody)),
				})
			}
		}
	}

	// Prune any manifest keys for files no longer shipped.
	for rel := range manifest.Files {
		if _, ok := files[rel]; !ok {
			dirty = true
			break
		}
	}

	if (dirty || !exists) && len(next.Files) > 0 {
		if err := WriteManifest(directory, next); err != nil {
			return UpgradeResult{}, err
		}
		result.ManifestUpdated = true
	}
	return result, nil
}

// WarningLine formats a single-line warning naming every out-of-date file and
// the command to upgrade them. It returns "" when updates is empty.
func WarningLine(updates []FileUpdate) string {
	if len(updates) == 0 {
		return ""
	}
	names := make([]string, len(updates))
	for i, update := range updates {
		switch update.Status {
		case FileEdited:
			names[i] = update.Path + " (edited)"
		case FileMissing:
			names[i] = update.Path + " (missing)"
		default:
			names[i] = update.Path
		}
	}
	noun := "factory files are"
	if len(updates) == 1 {
		noun = "factory file is"
	}
	return fmt.Sprintf("%s out of date (%s); run machinist factory upgrade", noun, strings.Join(names, ", "))
}

func classifyFile(directory, rel string, shippedBody []byte, manifestHash string) (FileStatus, []byte, error) {
	target := filepath.Join(directory, filepath.FromSlash(rel))
	info, err := os.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return FileMissing, nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", rel)
	}
	diskBody, err := os.ReadFile(target)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", rel, err)
	}
	shippedHash := HashBytes(shippedBody)
	diskHash := HashBytes(diskBody)
	switch {
	case diskHash == shippedHash:
		return FileCurrent, diskBody, nil
	case manifestHash != "" && manifestHash == shippedHash:
		return FileCustomized, diskBody, nil
	case manifestHash != "" && diskHash == manifestHash:
		return FileOutdated, diskBody, nil
	default:
		return FileEdited, diskBody, nil
	}
}

func readShippedFiles(shipped fs.FS) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(shipped, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == ManifestName {
			return nil
		}
		body, err := fs.ReadFile(shipped, path)
		if err != nil {
			return fmt.Errorf("read shipped %s: %w", path, err)
		}
		files[path] = body
		return nil
	})
	return files, err
}

func writeShippedFile(target, rel string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", rel, err)
	}
	mode := fs.FileMode(0o600)
	if strings.HasPrefix(rel, "scripts/") {
		mode = 0o700
	}
	return writeFileAtomic(target, body, mode)
}

func writeFileAtomic(path string, body []byte, mode fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".machinist-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if _, err := temp.Write(body); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
