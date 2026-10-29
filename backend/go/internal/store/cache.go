package store

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"defhop/internal/model"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

const cacheVersion = "v5" // bump: packed workspace.gob for cold start

func init() {
	gob.Register(model.Symbol{})
	gob.Register(model.Location{})
	gob.Register(model.Occ{})
	gob.Register(model.FileSlice{})
	gob.Register(FileMeta{})
	gob.Register(cacheManifest{})
	gob.Register(packedWorkspace{})
}

// packedWorkspace is a single-file cold-start index (avoids opening ~N gob files).
type packedWorkspace struct {
	FP    string
	Files map[string]model.FileSlice // pathKey -> slice
}

func packedPath(cacheDir string) string {
	return filepath.Join(cacheDir, "workspace.gob")
}

// LoadPacked returns file slices when workspace fingerprint matches.
func LoadPacked(cacheDir, wantFP string) map[string]*model.FileSlice {
	if wantFP == "" {
		return nil
	}
	f, err := os.Open(packedPath(cacheDir))
	if err != nil {
		return nil
	}
	defer f.Close()
	var pw packedWorkspace
	if err := gob.NewDecoder(f).Decode(&pw); err != nil || pw.FP != wantFP || len(pw.Files) == 0 {
		return nil
	}
	out := make(map[string]*model.FileSlice, len(pw.Files))
	for k, slice := range pw.Files {
		fd := slice
		// Repair declaration pointers (same as per-file gob load).
		decl := map[string]*model.Location{}
		for i := range fd.Occs {
			o := fd.Occs[i]
			if o != nil && o.Role == "declaration" && o.TargetKey != "" && o.Location != nil {
				decl[o.TargetKey] = o.Location
			}
		}
		for i := range fd.Symbols {
			s := fd.Symbols[i]
			if s != nil && s.Declaration == nil {
				s.Declaration = decl[s.Key]
			}
		}
		out[k] = &fd
	}
	return out
}

// SavePacked writes the whole workspace into one gob for the next cold start.
func SavePacked(cacheDir, fp string, files map[string]*model.FileSlice) error {
	if fp == "" || len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(cacheDir, "version"), []byte(cacheVersion), 0644)
	pw := packedWorkspace{
		FP:    fp,
		Files: make(map[string]model.FileSlice, len(files)),
	}
	for k, fd := range files {
		if fd == nil {
			continue
		}
		cp := *fd
		cp.Symbols = copySymbols(fd.Symbols)
		cp.Occs = copyOccs(fd.Occs)
		pw.Files[k] = cp
	}
	tmp := packedPath(cacheDir) + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	err = gob.NewEncoder(f).Encode(&pw)
	f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(packedPath(cacheDir))
	return os.Rename(tmp, packedPath(cacheDir))
}

// cacheEntry is one persisted file slice plus content fingerprint.
type cacheEntry struct {
	Hash  string
	Slice model.FileSlice
}

// FileMeta lets warm starts skip re-reading unchanged files (mtime+size).
type FileMeta struct {
	Hash  string
	Size  int64
	Mtime int64 // UnixNano
}

type cacheManifest struct {
	Entries map[string]FileMeta // pathKey -> meta
}

func manifestPath(cacheDir string) string {
	return filepath.Join(cacheDir, "manifest.gob")
}

// LoadManifest returns pathKey -> meta (empty map if missing).
func LoadManifest(cacheDir string) map[string]FileMeta {
	f, err := os.Open(manifestPath(cacheDir))
	if err != nil {
		return map[string]FileMeta{}
	}
	defer f.Close()
	var m cacheManifest
	if err := gob.NewDecoder(f).Decode(&m); err != nil || m.Entries == nil {
		return map[string]FileMeta{}
	}
	return m.Entries
}

// SaveManifest persists fingerprints used for the warm-start fast path.
func SaveManifest(cacheDir string, entries map[string]FileMeta) error {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}
	f, err := os.Create(manifestPath(cacheDir))
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(&cacheManifest{Entries: entries})
}

// FingerprintResult is the outcome of hashing one source file.
type FingerprintResult struct {
	Path string
	Hash string
	Data []byte // non-nil only when content was read (cache miss / first hash)
	Meta FileMeta
}

// FingerprintFiles stats/hashes paths in parallel.
// When mtime+size match the manifest, content is not re-read (warm path).
func FingerprintFiles(paths []string, prev map[string]FileMeta, onProgress func(done, total int)) []FingerprintResult {
	total := len(paths)
	out := make([]FingerprintResult, total)
	if total == 0 {
		return out
	}
	if prev == nil {
		prev = map[string]FileMeta{}
	}
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	if workers > 32 {
		workers = 32
	}
	var done atomic.Int32
	jobs := make(chan int, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				p := paths[i]
				res := FingerprintResult{Path: p}
				info, err := os.Stat(p)
				if err != nil {
					out[i] = res
					n := int(done.Add(1))
					if onProgress != nil && (n == total || n%64 == 0) {
						onProgress(n, total)
					}
					continue
				}
				key := PathKey(p)
				mtime := info.ModTime().UnixNano()
				size := info.Size()
				if m, ok := prev[key]; ok && m.Hash != "" && m.Size == size && m.Mtime == mtime {
					res.Hash = m.Hash
					res.Meta = m
					out[i] = res
					n := int(done.Add(1))
					if onProgress != nil && (n == total || n%64 == 0) {
						onProgress(n, total)
					}
					continue
				}
				data, err := os.ReadFile(p)
				if err != nil {
					out[i] = res
					n := int(done.Add(1))
					if onProgress != nil && (n == total || n%64 == 0) {
						onProgress(n, total)
					}
					continue
				}
				hash := ContentHash(data)
				res.Hash = hash
				res.Data = data
				res.Meta = FileMeta{Hash: hash, Size: size, Mtime: mtime}
				out[i] = res
				n := int(done.Add(1))
				if onProgress != nil && (n == total || n%64 == 0) {
					onProgress(n, total)
				}
			}
		}()
	}
	for i := range paths {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if onProgress != nil {
		onProgress(total, total)
	}
	return out
}

// LoadFileCache reads per-file gob entries from cacheDir/files/ in parallel.
// pathToHash should include PathKey(path) entries. Returns pathKey -> FileSlice.
func LoadFileCache(cacheDir string, pathToHash map[string]string) map[string]*model.FileSlice {
	dir := filepath.Join(cacheDir, "files")
	out := make(map[string]*model.FileSlice)
	// Deduplicate by PathKey so alternate spellings of the same path are tried once.
	seen := map[string]string{} // pathKey -> hash
	for path, wantHash := range pathToHash {
		key := PathKey(path)
		if _, ok := seen[key]; ok {
			continue
		}
		if wantHash == "" {
			continue
		}
		seen[key] = wantHash
	}
	if len(seen) == 0 {
		return out
	}

	workers := runtime.NumCPU() * 4
	if workers < 8 {
		workers = 8
	}
	if workers > 64 {
		workers = 64
	}
	type job struct{ key, hash string }
	jobs := make(chan job, workers*4)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				fd := loadOneFileCache(dir, j.key, j.hash)
				if fd == nil {
					continue
				}
				mu.Lock()
				out[j.key] = fd
				mu.Unlock()
			}
		}()
	}
	for key, hash := range seen {
		jobs <- job{key: key, hash: hash}
	}
	close(jobs)
	wg.Wait()
	return out
}

// LoadFileCacheKeys loads only the requested pathKeys (hash must match).
func LoadFileCacheKeys(cacheDir string, keysToHash map[string]string) map[string]*model.FileSlice {
	return LoadFileCache(cacheDir, keysToHash)
}

func loadOneFileCache(dir, key, wantHash string) *model.FileSlice {
	f, err := os.Open(filepath.Join(dir, fileCacheName(key)))
	if err != nil {
		return nil
	}
	var e cacheEntry
	err = gob.NewDecoder(f).Decode(&e)
	f.Close()
	if err != nil || e.Hash != wantHash {
		return nil
	}
	fd := e.Slice
	if fd.ContentHash == "" {
		fd.ContentHash = e.Hash
	}
	decl := map[string]*model.Location{}
	for i := range fd.Occs {
		o := fd.Occs[i]
		if o != nil && o.Role == "declaration" && o.TargetKey != "" && o.Location != nil {
			decl[o.TargetKey] = o.Location
		}
	}
	for i := range fd.Symbols {
		s := fd.Symbols[i]
		if s != nil && s.Declaration == nil {
			s.Declaration = decl[s.Key]
		}
	}
	return &fd
}

// SaveFileCache writes/updates cached slices for the given files.
func SaveFileCache(cacheDir string, files map[string]*model.FileSlice, pathToHash map[string]string) error {
	dir := filepath.Join(cacheDir, "files")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(cacheDir, "version"), []byte(cacheVersion), 0644)
	for key, fd := range files {
		if fd == nil {
			continue
		}
		hash := pathToHash[key]
		if hash == "" && fd.Path != "" {
			hash = pathToHash[fd.Path]
		}
		if hash == "" && fd.Path != "" {
			hash = pathToHash[PathKey(fd.Path)]
		}
		if hash == "" {
			continue
		}
		entry := cacheEntry{Hash: hash, Slice: *fd}
		entry.Slice.Symbols = copySymbols(fd.Symbols)
		entry.Slice.Occs = copyOccs(fd.Occs)
		name := fileCacheName(key)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		err = gob.NewEncoder(f).Encode(&entry)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func copySymbols(in []*model.Symbol) []*model.Symbol {
	out := make([]*model.Symbol, 0, len(in))
	for _, s := range in {
		if s == nil {
			continue
		}
		cp := *s
		if s.Declaration != nil {
			d := *s.Declaration
			cp.Declaration = &d
		}
		out = append(out, &cp)
	}
	return out
}

func copyOccs(in []*model.Occ) []*model.Occ {
	out := make([]*model.Occ, 0, len(in))
	for _, o := range in {
		if o == nil {
			continue
		}
		cp := *o
		if o.Location != nil {
			l := *o.Location
			cp.Location = &l
		}
		out = append(out, &cp)
	}
	return out
}

func fileCacheName(pathKey string) string {
	sum := sha256.Sum256([]byte(pathKey))
	return hex.EncodeToString(sum[:16]) + ".gob"
}

// ContentHash returns a short hash of file bytes.
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return cacheVersion + ":" + hex.EncodeToString(sum[:16])
}

// ContentHashString hashes text without requiring a separate []byte alloc from callers
// that already hold a string (still copies once inside Sum256).
func ContentHashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return cacheVersion + ":" + hex.EncodeToString(sum[:16])
}

// HashFile reads and hashes a path.
func HashFile(path string) (string, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	return ContentHash(data), data, nil
}

// EnsureCacheVersion wipes the file cache when the format version changes.
func EnsureCacheVersion(cacheDir string) {
	v, err := os.ReadFile(filepath.Join(cacheDir, "version"))
	if err != nil || strings.TrimSpace(string(v)) != cacheVersion {
		_ = os.RemoveAll(filepath.Join(cacheDir, "files"))
		_ = os.Remove(manifestPath(cacheDir))
		_ = os.Remove(packedPath(cacheDir))
		_ = os.MkdirAll(filepath.Join(cacheDir, "files"), 0755)
		_ = os.WriteFile(filepath.Join(cacheDir, "version"), []byte(cacheVersion), 0644)
	}
}

// WipeCache removes all on-disk index artifacts under cacheDir (gob, deps extracts, etc.).
func WipeCache(cacheDir string) {
	if cacheDir == "" {
		return
	}
	_ = os.RemoveAll(cacheDir)
	_ = os.MkdirAll(filepath.Join(cacheDir, "files"), 0755)
	_ = os.WriteFile(filepath.Join(cacheDir, "version"), []byte(cacheVersion), 0644)
}
