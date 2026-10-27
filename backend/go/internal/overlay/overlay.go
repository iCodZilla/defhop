package overlay

import "sync"

// Entry is the latest editor buffer for a path.
type Entry struct {
	Text string
	Gen  uint64
}

// Store keeps unsaved / latest buffers. Navigation prefers this over disk.
type Store struct {
	mu   sync.RWMutex
	data map[string]*Entry // lowercase slash path
}

func New() *Store {
	return &Store{data: make(map[string]*Entry)}
}

// Put replaces the buffer and bumps generation. Returns the new gen.
func (s *Store) Put(pathKey, text string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.data[pathKey]
	if e == nil {
		e = &Entry{}
		s.data[pathKey] = e
	}
	e.Text = text
	e.Gen++
	return e.Gen
}

// Get returns the overlay entry if present.
func (s *Store) Get(pathKey string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e := s.data[pathKey]
	if e == nil {
		return Entry{}, false
	}
	return *e, true
}

// Gen returns the current generation (0 if absent).
func (s *Store) Gen(pathKey string) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e := s.data[pathKey]; e != nil {
		return e.Gen
	}
	return 0
}

// Remove drops an overlay entry.
func (s *Store) Remove(pathKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, pathKey)
}
