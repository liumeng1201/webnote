package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------- config ----------

type Config struct {
	Port            int     `json:"port"`
	DataDir         string  `json:"data_dir"`
	AdminHash       string  `json:"admin_hash,omitempty"`
	Secret          string  `json:"secret,omitempty"`
	BackupIntervalH int     `json:"backup_interval_hours"`
	BackupKeep      int     `json:"backup_keep"`
	FileCapGB       float64 `json:"file_cap_gb"`
	FileMaxMB       int     `json:"file_max_mb"`
	ChunkMB         int     `json:"chunk_mb"`
}

func (c *Config) ChunkBytes() int64  { return int64(c.ChunkMB) << 20 }
func (c *Config) FileMaxBytes() int64 { return int64(c.FileMaxMB) << 20 }
func (c *Config) FileCapBytes() int64 { return int64(c.FileCapGB * float64(int64(1)<<30)) }

// fileConfig mirrors Config with pointers so we can tell "absent" from "zero".
type fileConfig struct {
	Port            *int     `json:"port"`
	DataDir         *string  `json:"data_dir"`
	AdminHash       *string  `json:"admin_hash"`
	Secret          *string  `json:"secret"`
	BackupIntervalH *int     `json:"backup_interval_hours"`
	BackupKeep      *int     `json:"backup_keep"`
	FileCapGB       *float64 `json:"file_cap_gb"`
	FileMaxMB       *int     `json:"file_max_mb"`
	ChunkMB         *int     `json:"chunk_mb"`
}

// loadConfig merges: explicit CLI flag > config file > built-in default,
// then writes the merged result back to the config file (0600).
func loadConfig() (*Config, string, error) {
	fs := flag.NewFlagSet("webnote", flag.ContinueOnError)
	var (
		fPort      = fs.Int("port", 8080, "listen port")
		fDataDir   = fs.String("data-dir", "./data", "data directory")
		fConfig    = fs.String("config", "config.json", "config file path")
		fAdminPw   = fs.String("admin-password", "", "set admin password (stored only as a hash)")
		fBackupInt = fs.Int("backup-interval", 6, "backup interval in hours")
		fBackupKp  = fs.Int("backup-keep", 7, "number of backups to keep")
		fFileCap   = fs.Float64("file-cap-gb", 20, "total file storage cap in GB")
		fFileMax   = fs.Int("file-max-mb", 300, "max size of a single file in MB")
		fChunk     = fs.Int("chunk-mb", 16, "upload chunk size in MB")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, "", err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	cfg := &Config{
		Port: *fPort, DataDir: *fDataDir,
		BackupIntervalH: *fBackupInt, BackupKeep: *fBackupKp,
		FileCapGB: *fFileCap, FileMaxMB: *fFileMax, ChunkMB: *fChunk,
	}
	cfgPath := *fConfig
	if raw, err := os.ReadFile(cfgPath); err == nil {
		var fc fileConfig
		if err := json.Unmarshal(raw, &fc); err != nil {
			return nil, "", fmt.Errorf("parse config %s: %w", cfgPath, err)
		}
		if !set["port"] && fc.Port != nil {
			cfg.Port = *fc.Port
		}
		if !set["data-dir"] && fc.DataDir != nil {
			cfg.DataDir = *fc.DataDir
		}
		if !set["backup-interval"] && fc.BackupIntervalH != nil {
			cfg.BackupIntervalH = *fc.BackupIntervalH
		}
		if !set["backup-keep"] && fc.BackupKeep != nil {
			cfg.BackupKeep = *fc.BackupKeep
		}
		if !set["file-cap-gb"] && fc.FileCapGB != nil {
			cfg.FileCapGB = *fc.FileCapGB
		}
		if !set["file-max-mb"] && fc.FileMaxMB != nil {
			cfg.FileMaxMB = *fc.FileMaxMB
		}
		if !set["chunk-mb"] && fc.ChunkMB != nil {
			cfg.ChunkMB = *fc.ChunkMB
		}
		if fc.AdminHash != nil {
			cfg.AdminHash = *fc.AdminHash
		}
		if fc.Secret != nil {
			cfg.Secret = *fc.Secret
		}
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}
	if set["admin-password"] {
		if *fAdminPw == "" {
			return nil, "", fmt.Errorf("-admin-password must not be empty")
		}
		cfg.AdminHash = hashSecret(*fAdminPw)
	}
	if cfg.Secret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, "", err
		}
		cfg.Secret = hex.EncodeToString(b)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, "", fmt.Errorf("invalid port %d", cfg.Port)
	}
	if cfg.ChunkMB < 1 || cfg.ChunkMB > 64 {
		return nil, "", fmt.Errorf("chunk-mb must be 1..64")
	}
	if cfg.FileMaxMB < 1 {
		return nil, "", fmt.Errorf("file-max-mb must be >= 1")
	}
	// persist merged config (never contains a plaintext password)
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if dir := filepath.Dir(cfgPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", err
		}
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		return nil, "", err
	}
	return cfg, cfgPath, nil
}

// ---------- secret hashing (space passwords & admin password) ----------

func hashSecret(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(pw))
	return "s1$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(h.Sum(nil))
}

func verifySecret(pw, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "s1" {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(pw))
	return subtle.ConstantTimeCompare(h.Sum(nil), want) == 1
}

func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---------- main ----------

func main() {
	log.SetFlags(log.LstdFlags)
	cfg, cfgPath, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := NewStore(cfg)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	if err := store.Reconcile(); err != nil {
		log.Printf("reconcile: %v", err)
	}
	srv := NewServer(cfg, store)

	go backupLoop(cfg, store)
	go sweepLoop(store)

	addr := fmt.Sprintf(":%d", cfg.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("webnote listening on %s (data: %s, config: %s, admin: %s)",
		addr, cfg.DataDir, cfgPath, map[bool]string{true: "enabled", false: "disabled"}[cfg.AdminHash != ""])
	log.Fatal(httpSrv.ListenAndServe())
}

// backupLoop snapshots note data (space JSON files only, never file blobs).
func backupLoop(cfg *Config, store *Store) {
	time.Sleep(time.Minute)
	for {
		if n, err := store.Backup(); err != nil {
			log.Printf("backup: %v", err)
		} else {
			log.Printf("backup done: %s", n)
		}
		time.Sleep(time.Duration(cfg.BackupIntervalH) * time.Hour)
	}
}

// sweepLoop removes expired files and stale unfinished uploads.
func sweepLoop(store *Store) {
	for {
		time.Sleep(10 * time.Minute)
		if n := store.SweepExpired(); n > 0 {
			log.Printf("swept %d expired files", n)
		}
		store.SweepUploads(2 * time.Hour)
	}
}
