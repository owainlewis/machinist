package factorycli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const maxPendingAdmissions = 100

type pendingAdmissionEntry struct {
	Scope       string    `json:"scope"`
	Endpoint    string    `json:"endpoint"`
	Fingerprint string    `json:"fingerprint"`
	RequestKey  string    `json:"request_key"`
	CreatedAt   time.Time `json:"created_at"`
}

type admissionJournal struct {
	Version int                     `json:"version"`
	Entries []pendingAdmissionEntry `json:"entries"`
}

type admissionLease struct {
	directory  string
	scope      string
	requestKey string
	lock       *os.File
	journalled bool
}

func (c command) admissionStateDirectory() (string, error) {
	root := c.getenv("FACTORY_DATA_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve Factory data directory: %w", err)
		}
		root = filepath.Join(home, ".factory")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Factory data directory: %w", err)
	}
	return filepath.Join(absolute, "operator", "admissions"), nil
}

func (c command) prepareImplicitAdmission(endpoint string, fingerprint []byte) (*admissionLease, error) {
	directory, err := c.admissionStateDirectory()
	if err != nil {
		return nil, err
	}
	if err := ensureAdmissionDirectory(directory); err != nil {
		return nil, err
	}
	fingerprintText := hex.EncodeToString(fingerprint)
	scopeDigest := sha256.Sum256([]byte(endpoint + "\x00" + fingerprintText))
	scope := hex.EncodeToString(scopeDigest[:])
	lockPath := filepath.Join(directory, "scope-"+scope+".lock")
	lock, err := openAdmissionLock(lockPath)
	if err != nil {
		return nil, err
	}
	if err := lockAdmissionFile(lock, true); err != nil {
		_ = lock.Close()
		if errors.Is(err, errAdmissionLockBusy) {
			return nil, fmt.Errorf("an identical Build admission is already in progress; retry after it finishes")
		}
		return nil, fmt.Errorf("lock Build admission: %w", err)
	}
	lease := &admissionLease{directory: directory, scope: scope, lock: lock, journalled: true}
	entry, found, err := loadOrCreatePendingAdmission(directory, pendingAdmissionEntry{
		Scope: scope, Endpoint: endpoint, Fingerprint: fingerprintText,
	})
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	lease.requestKey = entry.RequestKey
	if found {
		return lease, nil
	}
	return lease, nil
}

func explicitAdmissionLease(requestKey string) *admissionLease {
	return &admissionLease{requestKey: requestKey}
}

func (lease *admissionLease) RequestKey() string { return lease.requestKey }

func (lease *admissionLease) Complete() error {
	if lease == nil || !lease.journalled {
		return nil
	}
	if err := removePendingAdmission(lease.directory, lease.scope, lease.requestKey); err != nil {
		return err
	}
	lease.journalled = false
	return nil
}

func (lease *admissionLease) Release() error {
	if lease == nil || lease.lock == nil {
		return nil
	}
	unlockErr := unlockAdmissionFile(lease.lock)
	closeErr := lease.lock.Close()
	lease.lock = nil
	return errors.Join(unlockErr, closeErr)
}

func ensureAdmissionDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create Build admission journal directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Build admission journal directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Build admission journal path is not a real directory: %s", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure Build admission journal directory: %w", err)
	}
	return nil
}

func openAdmissionLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open Build admission lock %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure Build admission lock %s: %w", path, err)
	}
	return file, nil
}

func withJournalLock(directory string, operation func(string) error) (resultErr error) {
	lock, err := openAdmissionLock(filepath.Join(directory, "journal.lock"))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	if err := lockAdmissionFile(lock, false); err != nil {
		return fmt.Errorf("lock Build admission journal: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, unlockAdmissionFile(lock)) }()
	return operation(filepath.Join(directory, "pending.json"))
}

func loadOrCreatePendingAdmission(directory string, candidate pendingAdmissionEntry) (pendingAdmissionEntry, bool, error) {
	var result pendingAdmissionEntry
	var found bool
	err := withJournalLock(directory, func(path string) error {
		journal, err := readAdmissionJournal(path)
		if err != nil {
			return err
		}
		for _, entry := range journal.Entries {
			if entry.Scope == candidate.Scope {
				if entry.Endpoint != candidate.Endpoint || entry.Fingerprint != candidate.Fingerprint || entry.RequestKey == "" {
					return errors.New("Build admission journal contains a conflicting scope entry")
				}
				result, found = entry, true
				return nil
			}
		}
		if len(journal.Entries) >= maxPendingAdmissions {
			return fmt.Errorf(
				"Build admission journal %s contains %d uncertain requests; recover them or provide --request-key",
				path,
				maxPendingAdmissions,
			)
		}
		requestKey, err := randomRequestKey()
		if err != nil {
			return err
		}
		candidate.RequestKey = requestKey
		candidate.CreatedAt = time.Now().UTC()
		journal.Entries = append(journal.Entries, candidate)
		if err := writeAdmissionJournal(directory, path, journal); err != nil {
			return err
		}
		result = candidate
		return nil
	})
	return result, found, err
}

func removePendingAdmission(directory, scope, requestKey string) error {
	return withJournalLock(directory, func(path string) error {
		journal, err := readAdmissionJournal(path)
		if err != nil {
			return err
		}
		filtered := journal.Entries[:0]
		removed := false
		for _, entry := range journal.Entries {
			if entry.Scope == scope && entry.RequestKey == requestKey {
				removed = true
				continue
			}
			filtered = append(filtered, entry)
		}
		if !removed {
			return errors.New("pending Build admission disappeared before authoritative output was flushed")
		}
		journal.Entries = filtered
		return writeAdmissionJournal(directory, path, journal)
	})
}

func readAdmissionJournal(path string) (admissionJournal, error) {
	journal := admissionJournal{Version: 1, Entries: []pendingAdmissionEntry{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return journal, fmt.Errorf("open Build admission journal: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return journal, fmt.Errorf("inspect Build admission journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return journal, fmt.Errorf("Build admission journal must be a regular owner-only file: %s", path)
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(&journal); err != nil {
		return journal, fmt.Errorf("decode Build admission journal: %w", err)
	}
	if journal.Version != 1 || len(journal.Entries) > maxPendingAdmissions {
		return journal, errors.New("Build admission journal has an unsupported or invalid shape")
	}
	return journal, nil
}

func writeAdmissionJournal(directory, path string, journal admissionJournal) error {
	suffix, err := randomRequestKey()
	if err != nil {
		return err
	}
	temporary := filepath.Join(directory, ".pending-"+suffix+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create Build admission journal: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return fmt.Errorf("encode Build admission journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync Build admission journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Build admission journal: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("install Build admission journal: %w", err)
	}
	removeTemporary = false
	if err := syncAdmissionDirectory(directory); err != nil {
		return fmt.Errorf("sync Build admission journal directory: %w", err)
	}
	return nil
}

func randomRequestKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate Build request key: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
