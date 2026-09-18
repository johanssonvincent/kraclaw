package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Memory represents a single memory entry.
type Memory struct {
	ID          string    `json:"id"`
	GroupJID    string    `json:"group_jid"`
	Content     string    `json:"content"`
	Tags        []string  `json:"tags,omitempty"`
	Category    string    `json:"category,omitempty"`
	Importance  float64   `json:"importance,omitempty"` // 0.0 to 1.0
	AccessCount int       `json:"access_count,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastAccessed time.Time `json:"last_accessed,omitempty"`
}

// Category represents a memory category.
type Category string

const (
	// CategoryFact represents factual information.
	CategoryFact Category = "fact"

	// CategoryPreference represents user preferences.
	CategoryPreference Category = "preference"

	// CategoryTask represents task-related information.
	CategoryTask Category = "task"

	// CategoryContext represents contextual information.
	CategoryContext Category = "context"

	// CategoryProcedure represents procedural knowledge.
	CategoryProcedure Category = "procedure"
)

// Config holds memory configuration.
type Config struct {
	// Enabled controls whether memory is active.
	Enabled bool `envconfig:"MEMORY_ENABLED" default:"true"`

	// StoragePath is the directory where memories are stored.
	StoragePath string `envconfig:"MEMORY_STORAGE_PATH" default:"/data/memories"`

	// MaxMemoriesPerGroup is the maximum number of memories per group.
	MaxMemoriesPerGroup int `envconfig:"MEMORY_MAX_PER_GROUP" default:"1000"`

	// AutoExtract controls whether memories are automatically extracted from conversations.
	AutoExtract bool `envconfig:"MEMORY_AUTO_EXTRACT" default:"false"`

	// RecallLimit is the maximum number of memories to include in a recall.
	RecallLimit int `envconfig:"MEMORY_RECALL_LIMIT" default:"10"`

	// ImportanceThreshold is the minimum importance for auto-extracted memories.
	ImportanceThreshold float64 `envconfig:"MEMORY_IMPORTANCE_THRESHOLD" default:"0.5"`
}

// Store manages memory operations.
type Store struct {
	cfg Config
	mu  sync.RWMutex

	// memories stores memories in memory for fast access.
	memories map[string][]*Memory // groupJID -> memories

	// index provides full-text search capability.
	index *searchIndex

	log *slog.Logger
}

// New creates a new memory store.
func New(cfg Config) *Store {
	s := &Store{
		cfg:      cfg,
		memories: make(map[string][]*Memory),
		index:    newSearchIndex(),
		log:      slog.With("component", "memory"),
	}

	return s
}

// Load loads memories from disk.
func (s *Store) Load(ctx context.Context) error {
	s.log.Info("loading memories", "path", s.cfg.StoragePath)

	// Load per-group memory files.
	entries, err := filepath.Glob(filepath.Join(s.cfg.StoragePath, "*.json"))
	if err != nil {
		s.log.Warn("failed to glob memory files", "error", err)
		return nil // Not fatal.
	}

	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := s.loadGroupFile(entry); err != nil {
			s.log.Warn("failed to load memory file", "file", entry, "error", err)
		}
	}

	s.log.Info("memories loaded", "count", s.TotalCount())

	return nil
}

// loadGroupFile loads a single group's memory file.
func (s *Store) loadGroupFile(path string) error {
	var entries []*Memory

	data, err := s.readFile(path)
	if err != nil {
		return fmt.Errorf("read memory file: %w", err)
	}

	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("parse memory file: %w", err)
	}

	for _, entry := range entries {
		s.mu.Lock()
		s.memories[entry.GroupJID] = append(s.memories[entry.GroupJID], entry)
		s.index.add(entry)
		s.mu.Unlock()
	}

	return nil
}

// Save saves all memories to disk.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Group memories by groupJID.
	groups := make(map[string][]*Memory)
	for _, mem := range s.memories {
		for _, m := range mem {
			groups[m.GroupJID] = append(groups[m.GroupJID], m)
		}
	}

	for groupJID, entries := range groups {
		if err := s.saveGroupFile(groupJID, entries); err != nil {
			s.log.Error("failed to save memory file", "group", groupJID, "error", err)
		}
	}

	return nil
}

// saveGroupFile saves a single group's memories.
func (s *Store) saveGroupFile(groupJID string, entries []*Memory) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memories: %w", err)
	}

	filename := filepath.Join(s.cfg.StoragePath, sanitizeFilename(groupJID)+".json")

	if err := s.writeFile(filename, data); err != nil {
		return fmt.Errorf("write memory file: %w", err)
	}

	return nil
}

// Add adds a new memory.
func (s *Store) Add(ctx context.Context, mem *Memory) error {
	if !s.cfg.Enabled {
		return nil
	}

	if mem.ID == "" {
		mem.ID = uuid.New().String()
	}

	mem.CreatedAt = time.Now()
	mem.UpdatedAt = time.Now()
	mem.LastAccessed = time.Now()

	if mem.Importance <= 0 {
		mem.Importance = 0.5
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce max memories per group.
	if len(s.memories[mem.GroupJID]) >= s.cfg.MaxMemoriesPerGroup {
		s.evictOldest(mem.GroupJID)
	}

	s.memories[mem.GroupJID] = append(s.memories[mem.GroupJID], mem)
	s.index.add(mem)

	s.log.Debug("memory added", "id", mem.ID, "group", mem.GroupJID, "category", mem.Category)

	return nil
}

// Get retrieves a memory by ID.
func (s *Store) Get(ctx context.Context, id string) (*Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, mems := range s.memories {
		for _, mem := range mems {
			if mem.ID == id {
				// Update access count.
				mem.AccessCount++
				mem.LastAccessed = time.Now()

				return mem, nil
			}
		}
	}

	return nil, fmt.Errorf("memory not found: %s", id)
}

// List returns all memories for a group.
func (s *Store) List(ctx context.Context, groupJID string) []*Memory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.memories[groupJID]
}

// Search searches for memories matching a query.
func (s *Store) Search(ctx context.Context, groupJID string, query string) []*Memory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get candidate memories for the group.
	candidates := s.memories[groupJID]
	if len(candidates) == 0 {
		return nil
	}

	// Search within candidates.
	results := s.index.search(query, candidates)

	// Limit results.
	if len(results) > s.cfg.RecallLimit {
		results = results[:s.cfg.RecallLimit]
	}

	return results
}

// Recall recalls relevant memories for a given context.
func (s *Store) Recall(ctx context.Context, groupJID string, context string) []*Memory {
	if !s.cfg.Enabled {
		return nil
	}

	// Search for relevant memories.
	results := s.Search(ctx, groupJID, context)

	// Sort by relevance (importance * access_count).
	s.sortByRelevance(results)

	s.log.Debug("memories recalled", "group", groupJID, "count", len(results), "query", context)

	return results
}

// Update updates an existing memory.
func (s *Store) Update(ctx context.Context, mem *Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for groupJID, mems := range s.memories {
		for i, m := range mems {
			if m.ID == mem.ID {
				mem.GroupJID = groupJID
				mem.UpdatedAt = time.Now()
				s.memories[groupJID][i] = mem
				s.index.update(mem)

				s.log.Debug("memory updated", "id", mem.ID)
				return nil
			}
		}
	}

	return fmt.Errorf("memory not found: %s", mem.ID)
}

// Delete deletes a memory.
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for groupJID, mems := range s.memories {
		for i, m := range mems {
			if m.ID == id {
				s.memories[groupJID] = append(mems[:i], mems[i+1:]...)
				s.index.remove(id)

				s.log.Debug("memory deleted", "id", id)
				return nil
			}
		}
	}

	return fmt.Errorf("memory not found: %s", id)
}

// Clear clears all memories for a group.
func (s *Store) Clear(ctx context.Context, groupJID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if mems, ok := s.memories[groupJID]; ok {
		for _, mem := range mems {
			s.index.remove(mem.ID)
		}

		delete(s.memories, groupJID)
	}

	return nil
}

// Count returns the number of memories for a group.
func (s *Store) Count(groupJID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.memories[groupJID])
}

// TotalCount returns the total number of memories across all groups.
func (s *Store) TotalCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, mems := range s.memories {
		total += len(mems)
	}

	return total
}

// FormatPrompt formats memories for inclusion in a system prompt.
func (s *Store) FormatPrompt(memories []*Memory) string {
	if len(memories) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Relevant Memories\n\n")

	for i, mem := range memories {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, mem.Category, mem.Content))

		if len(mem.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("   Tags: %s\n", strings.Join(mem.Tags, ", ")))
		}

		sb.WriteString("\n")
	}

	return sb.String()
}

// evictOldest removes the oldest/least important memories from a group.
func (s *Store) evictOldest(groupJID string) {
	mems := s.memories[groupJID]

	// Sort by relevance (oldest/least accessed first).
	s.sortByRelevanceAsc(mems)

	// Remove bottom 20%.
	removeCount := len(mems) / 5
	if removeCount < 1 {
		removeCount = 1
	}

	for i := 0; i < removeCount && i < len(mems); i++ {
		s.index.remove(mems[i].ID)
	}

	s.memories[groupJID] = mems[removeCount:]
}

// sortByRelevance sorts memories by relevance (highest first).
func (s *Store) sortByRelevance(mems []*Memory) {
	// Simple sort: importance * (1 + log(access_count)).
	for i := 0; i < len(mems); i++ {
		for j := i + 1; j < len(mems); j++ {
			scoreI := mems[i].Importance * float64(1+mems[i].AccessCount)
			scoreJ := mems[j].Importance * float64(1+mems[j].AccessCount)

			if scoreJ > scoreI {
				mems[i], mems[j] = mems[j], mems[i]
			}
		}
	}
}

// sortByRelevanceAsc sorts memories by relevance (lowest first).
func (s *Store) sortByRelevanceAsc(mems []*Memory) {
	for i := 0; i < len(mems); i++ {
		for j := i + 1; j < len(mems); j++ {
			scoreI := mems[i].Importance * float64(1+mems[i].AccessCount)
			scoreJ := mems[j].Importance * float64(1+mems[j].AccessCount)

			if scoreI > scoreJ {
				mems[i], mems[j] = mems[j], mems[i]
			}
		}
	}
}

// searchIndex provides simple full-text search.
type searchIndex struct {
	// termIndex maps lowercase terms to memory IDs.
	termIndex map[string]map[string]float64 // term -> (memoryID -> score)
	mu        sync.RWMutex
}

func newSearchIndex() *searchIndex {
	return &searchIndex{
		termIndex: make(map[string]map[string]float64),
	}
}

// wordRegex matches words for indexing.
var wordRegex = regexp.MustCompile(`\b[a-zA-Z0-9_]{3,}\b`)

func (idx *searchIndex) add(mem *Memory) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	terms := wordRegex.FindAllString(strings.ToLower(mem.Content), -1)

	for _, term := range terms {
		if idx.termIndex[term] == nil {
			idx.termIndex[term] = make(map[string]float64)
		}

		// Score based on importance.
		idx.termIndex[term][mem.ID] = mem.Importance
	}
}

func (idx *searchIndex) update(mem *Memory) {
	idx.remove(mem.ID)
	idx.add(mem)
}

func (idx *searchIndex) remove(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	for term, ids := range idx.termIndex {
		delete(ids, id)
		if len(ids) == 0 {
			delete(idx.termIndex, term)
		}
	}
}

func (idx *searchIndex) search(query string, candidates []*Memory) []*Memory {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	terms := wordRegex.FindAllString(strings.ToLower(query), -1)
	if len(terms) == 0 {
		return nil
	}

	// Score candidates.
	scores := make(map[string]float64)

	for _, mem := range candidates {
		score := 0.0

		for _, term := range terms {
			if termScores, ok := idx.termIndex[term]; ok {
				if s, ok := termScores[mem.ID]; ok {
					score += s
				}
			}
		}

		if score > 0 {
			scores[mem.ID] = score
		}
	}

	// Build result list.
	var results []*Memory
	for _, mem := range candidates {
		if _, ok := scores[mem.ID]; ok {
			results = append(results, mem)
		}
	}

	// Sort by score.
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if scores[results[j].ID] > scores[results[i].ID] {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	return results
}

// readFile reads a file from disk.
func (s *Store) readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFile writes data to a file on disk.
func (s *Store) writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// sanitizeFilename creates a safe filename from a string.
func sanitizeFilename(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(s)
}
