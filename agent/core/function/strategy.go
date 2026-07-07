package function

import (
	"sync"

	"github.com/RedHuang-0622/Seele/types"
)

// Strategy defines the interface for encoding tools and decoding tool calls
// in a provider-specific format (e.g., OpenAI, Anthropic).
type Strategy interface {
	// EncodeTools converts the internal Tool slice into the provider's native
	// tool definition format.
	EncodeTools(tools []types.Tool) interface{}

	// DecodeToolCall parses a provider-specific raw tool call (typically from
	// a deserialized map) back into a canonical ToolCall pointer. Returns nil
	// if the raw value cannot be decoded.
	DecodeToolCall(raw interface{}) *types.ToolCall
}

// ---------------------------------------------------------------------------
// Global registry of named strategies. Strategies register themselves via
// init() — see openai.go and anthropic.go.
// ---------------------------------------------------------------------------

var (
	registry   = map[string]Strategy{}
	registryMu sync.RWMutex
)

// Register binds name to s. Panics if name is already registered.
func Register(name string, s Strategy) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("function: strategy already registered: " + name)
	}
	registry[name] = s
}

// Get returns the strategy previously registered under name, or nil.
func Get(name string) Strategy {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// Names returns a copy of all registered strategy names.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	nn := make([]string, 0, len(registry))
	for n := range registry {
		nn = append(nn, n)
	}
	return nn
}
