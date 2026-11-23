package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- data model ----------

type Tab struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Sort      int       `json:"sort"`
	Rev       int64     `json:"rev"`
	UpdatedAt time.Time `json:"updated_at"`
}

type FileMeta struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Space struct {
	Name         string      `json:"name"`
	PasswordHash string      `json:"password_hash,omitempty"`
	Readonly     bool        `json:"readonly"`
	CreatedAt    time.Time   `json:"created_at"`
	LastAccess   time.Time   `json:"last_access"`
	Tabs         []*Tab      `json:"tabs"`
	Files        []*FileMeta `json:"files"`

	lastAccessSaved time.Time // not persisted separately; bookkeeping only
}

func (s *Space) findTab(id string) *Tab {
	for _, t := range s.Tabs {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (s *Space) findFile(id string) *FileMeta {
	for _, f := range s.Files {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (s *Space) sortedTabs() []*Tab {
	out := make([]*Tab, len(s.Tabs))
	copy(out, s.Tabs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sort < out[j].Sort })
	return out
}

// ---------- upload registry (in-memory only; abandoned on restart) ----------

type upload struct {
	ID        string
	SpaceName string
	Filename  string
	Size      int64
	Days      int
	Received  int64
	CreatedAt time.Time
}

// ---------- store ----------

type Store struct {
	mu      sync.Mutex
	cfg     *Config
	spaces  map[string]*Space
	uploads map[string]*upload
}

func NewStore(cfg *Config) (*Store, error) {
	for _, sub := range []string{"spaces", "files", "uploads", "backups"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{cfg: cfg, spaces: map[string]*Space{}, uploads: map[string]*upload{}}, nil
}

func spaceKey(name string) string {
	h := sha256.Sum256([]byte(name))
	return hex.EncodeToString(h[:])
}

func (st *Store) spacePath(name string) string {
	return filepath.Join(st.cfg.DataDir, "spaces", spaceKey(name)+".json")
}

func (st *Store) blobPath(spaceName, fileID string) string {
	return filepath.Join(st.cfg.DataDir, "files", spaceKey(spaceName), fileID)
}

func (st *Store) partPath(uploadID string) string {
	return filepath.Join(st.cfg.DataDir, "uploads", uploadID+".part")
}

// EnsureSpace returns the space, creating and persisting it on first access.
// Caller must hold st.mu.
func (st *Store) EnsureSpace(name string) (*Space, error) {
	if sp, ok := st.spaces[name]; ok {
		return sp, nil
	}
	raw, err := os.ReadFile(st.spacePath(name))
	if err == nil {
		var sp Space
		if err := json.Unmarshal(raw, &sp); err != nil {
			return nil, err
		}
		if sp.Tabs == nil {
			sp.Tabs = []*Tab{}
		}
		if sp.Files == nil {
			sp.Files = []*FileMeta{}
		}
		sp.lastAccessSaved = sp.LastAccess
		st.spaces[name] = &sp
		return &sp, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := time.Now()
	sp := &Space{Name: name, CreatedAt: now, LastAccess: now, Tabs: []*Tab{}, Files: []*FileMeta{}, lastAccessSaved: now}
	st.spaces[name] = sp
	if err := st.SaveSpace(sp); err != nil {
		return nil, err
	}
	return sp, nil
}

// LoadSpace returns the space if it exists on disk/in cache, else nil.
// Caller must hold st.mu.
func (st *Store) LoadSpace(name string) (*Space, error) {
	if _, err := os.Stat(st.spacePath(name)); errors.Is(err, os.ErrNotExist) {
		if _, ok := st.spaces[name]; !ok {
			return nil, nil
		}
	}
	return st.EnsureSpace(name)
}

// SaveSpace atomically persists a space. Caller must hold st.mu.
func (st *Store) SaveSpace(sp *Space) error {
	raw, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	p := st.spacePath(sp.Name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Touch updates LastAccess, persisting at most once a minute.
// Caller must hold st.mu.
func (st *Store) Touch(sp *Space) {
	sp.LastAccess = time.Now()
	if time.Since(sp.lastAccessSaved) > time.Minute {
		sp.lastAccessSaved = sp.LastAccess
		_ = st.SaveSpace(sp)
	}
}

// DeleteSpace removes a space, its metadata file and all its blobs.
// Caller must hold st.mu.
func (st *Store) DeleteSpace(name string) error {
	delete(st.spaces, name)
	_ = os.Remove(st.spacePath(name))
	return os.RemoveAll(filepath.Join(st.cfg.DataDir, "files", spaceKey(name)))
}

// ListSpaceNames reads all space names from disk. Caller must hold st.mu.
func (st *Store) ListSpaceNames() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(st.cfg.DataDir, "spaces"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(st.cfg.DataDir, "spaces", e.Name()))
		if err != nil {
			continue
		}
		var probe struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.Name != "" {
			names = append(names, probe.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// TotalFileSize sums the sizes of all stored files across spaces.
// Caller must hold st.mu.
func (st *Store) TotalFileSize() (int64, error) {
	names, err := st.ListSpaceNames()
	if err != nil {
		return 0, err
	}
	var total int64
	for _, n := range names {
		sp, err := st.EnsureSpace(n)
		if err != nil {
			return 0, err
		}
		for _, f := range sp.Files {
			total += f.Size
		}
	}
	return total, nil
}

// Reconcile drops file metadata whose blob is gone, deletes orphan blobs,
// and removes leftover upload parts. Run once at startup.
func (st *Store) Reconcile() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	// leftover parts are always stale after a restart
	parts, _ := os.ReadDir(filepath.Join(st.cfg.DataDir, "uploads"))
	for _, p := range parts {
		_ = os.Remove(filepath.Join(st.cfg.DataDir, "uploads", p.Name()))
	}
	names, err := st.ListSpaceNames()
	if err != nil {
		return err
	}
	for _, n := range names {
		sp, err := st.EnsureSpace(n)
		if err != nil {
			return err
		}
		changed := false
		kept := sp.Files[:0]
		live := map[string]bool{}
		for _, f := range sp.Files {
			if _, err := os.Stat(st.blobPath(n, f.ID)); err == nil {
				kept = append(kept, f)
				live[f.ID] = true
			} else {
				changed = true
			}
		}
		sp.Files = kept
		// orphan blobs
		blobDir := filepath.Join(st.cfg.DataDir, "files", spaceKey(n))
		blobs, _ := os.ReadDir(blobDir)
		for _, b := range blobs {
			if !live[b.Name()] {
				_ = os.Remove(filepath.Join(blobDir, b.Name()))
			}
		}
		if changed {
			_ = st.SaveSpace(sp)
		}
	}
	return nil
}

// SweepExpired deletes files past their expiry. Returns how many were removed.
func (st *Store) SweepExpired() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	names, err := st.ListSpaceNames()
	if err != nil {
		return 0
	}
	now := time.Now()
	removed := 0
	for _, n := range names {
		sp, err := st.EnsureSpace(n)
		if err != nil {
			continue
		}
		kept := sp.Files[:0]
		changed := false
		for _, f := range sp.Files {
			if now.After(f.ExpiresAt) {
				_ = os.Remove(st.blobPath(n, f.ID))
				removed++
				changed = true
			} else {
				kept = append(kept, f)
			}
		}
		sp.Files = kept
		if changed {
			_ = st.SaveSpace(sp)
		}
	}
	return removed
}

// SweepUploads abandons uploads idle since before the cutoff.
func (st *Store) SweepUploads(maxAge time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, up := range st.uploads {
		if up.CreatedAt.Before(cutoff) {
			_ = os.Remove(st.partPath(id))
			delete(st.uploads, id)
		}
	}
}

// Backup copies all space JSON files into backups/<timestamp>/ and prunes
// old backups. File blobs are intentionally excluded.
func (st *Store) Backup() (string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ts := time.Now().Format("20060102-150405")
	dst := filepath.Join(st.cfg.DataDir, "backups", ts)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	srcDir := filepath.Join(st.cfg.DataDir, "spaces")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return "", err
		}
	}
	// prune
	backupsRoot := filepath.Join(st.cfg.DataDir, "backups")
	all, err := os.ReadDir(backupsRoot)
	if err == nil {
		var dirs []string
		for _, d := range all {
			if d.IsDir() {
				dirs = append(dirs, d.Name())
			}
		}
		sort.Strings(dirs)
		for len(dirs) > st.cfg.BackupKeep && len(dirs) > 0 {
			_ = os.RemoveAll(filepath.Join(backupsRoot, dirs[0]))
			dirs = dirs[1:]
		}
	}
	return ts, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// DiskFree returns free bytes on the data volume (Linux statfs).
func DiskFree(dir string) (int64, error) {
	var sfs syscall.Statfs_t
	if err := syscall.Statfs(dir, &sfs); err != nil {
		return 0, err
	}
	return int64(sfs.Bavail) * int64(sfs.Bsize), nil
}
