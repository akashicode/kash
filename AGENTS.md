# Kash: Coding Guidelines for AI Agents

> **Project Overview**: Kash is a Go-based CLI framework that compiles raw documents into embedded, pure-Go GraphRAG databases, packaged into ultra-lightweight Docker containers.

## 1. Build, Lint, and Test Commands

### Build
```bash
# Build the CLI binary
go build -o bin/kash ./cmd/Kash

# Build for multiple platforms
GOOS=linux GOARCH=amd64 go build -o bin/kash-linux ./cmd/Kash
GOOS=darwin GOARCH=amd64 go build -o bin/kash-darwin ./cmd/Kash
GOOS=windows GOARCH=amd64 go build -o bin/kash.exe ./cmd/Kash
```

### Lint
```bash
# Run golangci-lint (install first: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
golangci-lint run ./...

# Format code
go fmt ./...

# Vet for common errors
go vet ./...
```

### Test
```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run a single test (by test name pattern)
go test -v -run TestFunctionName ./path/to/package

# Run tests in a specific package
go test -v ./internal/vector

# Run tests with coverage
go test -cover ./...

# Generate coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html

# Run integration tests (requires build tag)
go test -v -tags=integration ./...

# Run benchmarks
go test -bench=. ./...
```

### Docker
```bash
# Build Docker image
docker build -t Kash:latest .

# Run container
docker run -p 8000:8000 Kash:latest
```

---

## 2. Project Structure

Follow standard Go CLI project layout:

```
Kash/
├── cmd/
│   └── Kash/          # Main application entry point
│       └── main.go           # Imports and invokes cmd/root.go
├── cmd/                      # Cobra command definitions
│   ├── root.go               # Root command (Execute(), initConfig)
│   ├── init.go               # `kash init` subcommand
│   └── build.go              # `kash build` subcommand
├── internal/                 # Private application code
│   ├── config/               # Configuration handling (Viper)
│   ├── vector/               # chromem-go vector store operations
│   ├── graph/                # cayley graph database operations
│   ├── llm/                  # go-openai client wrappers
│   ├── mcp/                  # MCP protocol server (mark3labs/mcp-go)
│   ├── chunker/              # Document chunking logic
│   └── server/               # HTTP server (REST, MCP, A2A)
├── pkg/                      # Public packages (if any)
├── api/                      # API contracts (OpenAPI schemas, types)
├── docs/                     # Documentation
├── test/                     # Integration test fixtures
├── go.mod
├── go.sum
├── Makefile
├── Dockerfile
└── .golangci.yml
```

---

## 3. Code Style Guidelines

### Imports
Group imports into three sections, separated by blank lines:
```go
import (
    // 1. Standard library
    "context"
    "fmt"
    "os"

    // 2. Third-party packages
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
    "go.uber.org/zap"

    // 3. Local packages
    "github.com/Kash/internal/config"
    "github.com/Kash/internal/vector"
)
```

### Naming Conventions

| Element | Convention | Example |
|---------|------------|---------|
| **Packages** | lowercase, single word | `vector`, `graph`, `chunker` |
| **Types** | PascalCase | `VectorStore`, `GraphDB` |
| **Interfaces** | PascalCase with `-er` suffix | `Embedder`, `Chunker`, `Reranker` |
| **Functions/Methods** | PascalCase (exported), camelCase (private) | `NewClient()`, `buildIndex()` |
| **Constants** | PascalCase or UPPER_SNAKE_CASE | `DefaultPort`, `MAX_CHUNK_SIZE` |
| **Errors** | `Err` prefix | `ErrNotFound`, `ErrInvalidConfig` |
| **Variables** | camelCase, meaningful names | `embeddingModel`, `vectorStore` |

### Variable Declarations
```go
// GOOD: Short variable declaration
s := "hello"
nums := []int{1, 2, 3}

// GOOD: var for zero values or package-level
var users []User
var config Config

// BAD: Unnecessary type annotation
var _s string = F()  // F() already returns string
```

### Struct Initialization
```go
// GOOD: Use field names
client := &Client{
    BaseURL:    "https://api.openai.com",
    APIKey:     apiKey,
    Timeout:    30 * time.Second,
}

// BAD: Positional initialization
client := &Client{"https://api.openai.com", apiKey, 30 * time.Second}
```

### Error Handling

**Core Principle**: Wrap errors with context, never discard.

```go
// GOOD: Wrap with %w for error chain
func (s *Store) Get(id string) (*Document, error) {
    doc, err := s.db.Find(id)
    if err != nil {
        return nil, fmt.Errorf("get document %q: %w", id, err)
    }
    return doc, nil
}

// GOOD: Define sentinel errors for matching
var ErrNotFound = errors.New("document not found")

// GOOD: Use errors.Is/As for checking
if errors.Is(err, ErrNotFound) {
    // handle not found
}

// GOOD: Custom error types for structured errors
type ValidationError struct {
    Field   string
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation failed on %s: %s", e.Field, e.Message)
}

// BAD: Log and return (causes duplicate logging)
if err != nil {
    log.Printf("error: %v", err)  // Don't log here
    return err
}

// BAD: Discarding error
data, _ := os.ReadFile(path)  // Never ignore errors
```

### Nil Checks and Early Returns
```go
// GOOD: Guard clauses, early returns
func Process(doc *Document) error {
    if doc == nil {
        return ErrNilDocument
    }
    if doc.ID == "" {
        return &ValidationError{Field: "ID", Message: "required"}
    }
    // ... main logic
    return nil
}

// BAD: Deeply nested
func Process(doc *Document) error {
    if doc != nil {
        if doc.ID != "" {
            // ... deeply nested logic
        } else {
            return errors.New("ID required")
        }
    }
    return nil
}
```

---

## 4. Testing Conventions

### Table-Driven Tests (Required)
```go
func TestChunkDocument(t *testing.T) {
    tests := []struct {
        name      string
        input     string
        chunkSize int
        want      int  // expected chunk count
        wantErr   bool
    }{
        {
            name:      "simple text",
            input:     "Hello world",
            chunkSize: 5,
            want:      3,
            wantErr:   false,
        },
        {
            name:      "empty input",
            input:     "",
            chunkSize: 100,
            want:      0,
            wantErr:   false,
        },
        {
            name:      "invalid chunk size",
            input:     "test",
            chunkSize: 0,
            want:      0,
            wantErr:   true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            chunks, err := ChunkDocument(tt.input, tt.chunkSize)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Len(t, chunks, tt.want)
        })
    }
}
```

### Test File Organization
- Test files: `*_test.go` in same package or `package_test` for external tests
- Use `testify/assert` and `testify/require` for assertions
- `require` for fatal assertions (stops test), `assert` for non-fatal

### Constructor Pattern
Always use `New` constructors, never zero-value structs directly:
```go
// GOOD
func NewVectorStore(cfg *Config) (*VectorStore, error) {
    if cfg == nil {
        return nil, ErrNilConfig
    }
    return &VectorStore{cfg: cfg}, nil
}

// GOOD: Interface for testability
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}
```

---

## 5. Configuration (Viper + YAML)

### Global Config Path
`~/.Kash/config.yaml`

### Config Structure
```go
type Config struct {
    BuildProviders BuildProviders `mapstructure:"build_providers"`
}

type BuildProviders struct {
    LLM      ProviderConfig `mapstructure:"llm"`
    Embedder ProviderConfig `mapstructure:"embedder"`
    Reranker ProviderConfig `mapstructure:"reranker,omitempty"`
}

type ProviderConfig struct {
    BaseURL string `mapstructure:"base_url"`
    APIKey  string `mapstructure:"api_key"`
    Model   string `mapstructure:"model"`
}
```

### Viper Setup (in cmd/root.go)
```go
func initConfig() {
    if cfgFile != "" {
        viper.SetConfigFile(cfgFile)
    } else {
        home, _ := os.UserHomeDir()
        viper.AddConfigPath(filepath.Join(home, ".Kash"))
        viper.SetConfigType("yaml")
        viper.SetConfigName("config")
    }
    viper.AutomaticEnv()
    if err := viper.ReadInConfig(); err != nil {
        fmt.Fprintln(os.Stderr, "warning: no config file found")
    }
}
```

---

## 6. Key Libraries Usage

### spf13/cobra (CLI)
```go
// cmd/build.go
var buildCmd = &cobra.Command{
    Use:   "build",
    Short: "Compile documents into vector and graph databases",
    RunE: func(cmd *cobra.Command, args []string) error {
        return build.Run(cfg)
    },
}
```

### sashabaranov/go-openai (LLM Client)
```go
client := openai.NewClientWithConfig(openai.ClientConfig{
    BaseURL: cfg.BaseURL,
    APIKey:  cfg.APIKey,
})
resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
    Model: cfg.Model,
    Messages: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Hello"},
    },
})
```

### philippgille/chromem-go (Vector Store)
```go
db := chromem.NewDB()
err := db.CreateCollection("documents", nil, chromem.EmbeddingFuncOpenAI{APIKey: key})
collection, _ := db.GetCollection("documents", nil)
_ = collection.AddDocuments(ctx, docs, runtime.NumCPU())
results, _ := collection.Query(ctx, query, 10, nil, nil)
```

---

## 7. Runtime Environment Variables

```bash
LLM_BASE_URL=https://api.openai.com/v1
LLM_API_KEY=sk-xxx
LLM_MODEL=gpt-4o
EMBED_BASE_URL=https://api.voyageai.com/v1
EMBED_API_KEY=pa-xxx
EMBED_MODEL=voyage-3
RERANK_BASE_URL=   # Optional
RERANK_API_KEY=    # Optional
RERANK_MODEL=      # Optional
```

---

## 8. Key Architectural Decisions

1. **Provider Agnostic**: Only OpenAI-compatible APIs. Users provide their own proxies.
2. **Embedded Databases**: No external DB servers. chromem-go and cayley are embedded.
3. **Docker-First Distribution**: Single ~50MB container with baked databases.
4. **Three Interfaces**: REST (`/v1/chat/completions`), MCP (`/mcp`), A2A (`/rpc/agent`).
5. **Build vs Runtime**: Data ingestion happens at `build` time. Runtime only serves queries.
6. **Single Binary**: One `kash` binary acts as both CLI (`init`, `build`) and server (`serve`). A multi-arch base image (`ghcr.io/akashicode/kash`) is published to GHCR. Agent Dockerfiles use `FROM ghcr.io/akashicode/kash:latest` so users can build cross-platform images (amd64 + arm64) with `docker buildx`.
