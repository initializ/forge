package memory

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/initializ/forge/forge-core/llm"
)

// Logger is the logging interface used by the memory package.
type Logger interface {
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
	Debug(msg string, fields map[string]any)
}

// ManagerConfig configures a Manager.
type ManagerConfig struct {
	MemoryDir    string       // root directory for memory files
	Embedder     llm.Embedder // nil = keyword-only mode
	Logger       Logger
	SearchConfig SearchConfig
}

// Manager orchestrates long-term memory: file storage, indexing, and search.
type Manager struct {
	fileStore *FileStore
	vecStore  VectorStore
	searcher  *HybridSearcher
	embedder  llm.Embedder
	logger    Logger
}

// NewManager creates a new memory Manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.MemoryDir == "" {
		return nil, fmt.Errorf("memory dir is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = &nopLogger{}
	}

	fileStore, err := NewFileStore(cfg.MemoryDir)
	if err != nil {
		return nil, fmt.Errorf("creating file store: %w", err)
	}

	// Ensure MEMORY.md exists.
	if err := fileStore.EnsureMemoryMD(); err != nil {
		logger.Warn("failed to create MEMORY.md template", map[string]any{"error": err.Error()})
	}

	indexDir := filepath.Join(cfg.MemoryDir, "index")
	vecStore, err := NewFileVectorStore(indexDir)
	if err != nil {
		return nil, fmt.Errorf("creating vector store: %w", err)
	}

	searchCfg := cfg.SearchConfig
	if searchCfg.TopK == 0 {
		searchCfg = DefaultSearchConfig()
	}

	searcher := NewHybridSearcher(vecStore, cfg.Embedder, searchCfg)

	return &Manager{
		fileStore: fileStore,
		vecStore:  vecStore,
		searcher:  searcher,
		embedder:  cfg.Embedder,
		logger:    logger,
	}, nil
}

// Search queries long-term memory with hybrid search.
func (m *Manager) Search(ctx context.Context, query string) ([]SearchResult, error) {
	return m.searcher.Search(ctx, query)
}

// GetFile retrieves a memory file by relative path.
func (m *Manager) GetFile(path string) (string, error) {
	return m.fileStore.ReadFile(path)
}

// AppendDailyLog appends an observation to today's daily log and indexes it.
func (m *Manager) AppendDailyLog(ctx context.Context, observation string) error {
	if err := m.fileStore.AppendDaily(observation); err != nil {
		return err
	}

	// Re-index today's daily log after appending.
	// Use best-effort: log errors but don't fail the append.
	if err := m.indexDailyLog(ctx); err != nil {
		m.logger.Warn("failed to re-index daily log after append", map[string]any{"error": err.Error()})
	}

	return nil
}

// IndexAll indexes all memory files (MEMORY.md + daily logs).
func (m *Manager) IndexAll(ctx context.Context) error {
	files, err := m.fileStore.ListFiles()
	if err != nil {
		return fmt.Errorf("listing memory files: %w", err)
	}

	m.logger.Info("indexing memory files", map[string]any{"count": len(files)})

	for _, f := range files {
		if err := m.IndexFile(ctx, f); err != nil {
			m.logger.Warn("failed to index file", map[string]any{
				"file":  f,
				"error": err.Error(),
			})
		}
	}
	return nil
}

// IndexFile indexes a single memory file by relative path.
func (m *Manager) IndexFile(ctx context.Context, path string) error {
	content, err := m.fileStore.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	// Remove old chunks for this file.
	if err := m.vecStore.DeleteBySource(ctx, path); err != nil {
		return fmt.Errorf("deleting old chunks for %s: %w", path, err)
	}

	// Chunk the content.
	chunks := ChunkText(content, path, 0, 0)
	if len(chunks) == 0 {
		return nil
	}

	// Generate embeddings if embedder available.
	indexed := make([]IndexedChunk, len(chunks))
	if m.embedder != nil {
		texts := make([]string, len(chunks))
		for i, c := range chunks {
			texts[i] = c.Content
		}

		resp, err := m.embedder.Embed(ctx, &llm.EmbeddingRequest{Texts: texts})
		if err != nil {
			m.logger.Warn("embedding failed, indexing without vectors", map[string]any{
				"file":  path,
				"error": err.Error(),
			})
			for i, c := range chunks {
				indexed[i] = IndexedChunk{Chunk: c}
			}
		} else {
			for i, c := range chunks {
				ic := IndexedChunk{Chunk: c}
				if i < len(resp.Embeddings) {
					ic.Vector = resp.Embeddings[i]
				}
				indexed[i] = ic
			}
		}
	} else {
		for i, c := range chunks {
			indexed[i] = IndexedChunk{Chunk: c}
		}
	}

	if err := m.vecStore.Index(ctx, indexed); err != nil {
		return fmt.Errorf("indexing chunks for %s: %w", path, err)
	}

	m.logger.Debug("indexed file", map[string]any{
		"file":   path,
		"chunks": len(indexed),
	})

	return nil
}

// indexDailyLog re-indexes today's daily log file.
func (m *Manager) indexDailyLog(ctx context.Context) error {
	files, err := m.fileStore.ListFiles()
	if err != nil {
		return err
	}

	// Find the most recent daily log (last .md that isn't MEMORY.md).
	for i := len(files) - 1; i >= 0; i-- {
		if files[i] != "MEMORY.md" {
			return m.IndexFile(ctx, files[i])
		}
	}
	return nil
}

// Close flushes the vector store to disk.
func (m *Manager) Close() error {
	return m.vecStore.Close()
}

// nopLogger is a no-op Logger.
type nopLogger struct{}

func (n *nopLogger) Info(_ string, _ map[string]any)  {}
func (n *nopLogger) Warn(_ string, _ map[string]any)  {}
func (n *nopLogger) Error(_ string, _ map[string]any) {}
func (n *nopLogger) Debug(_ string, _ map[string]any) {}
