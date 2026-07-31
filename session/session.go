package session

import (
	"fmt"
	"reflect"
	"time"

	"github.com/RedHuang-0622/Seele/seelectx"
	"github.com/RedHuang-0622/Seele/seelectx/cache"
	"github.com/RedHuang-0622/Seele/seelectx/storage"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/types"
)

// ContextComponents groups the optional context capabilities consumed by one
// Session. It selects mechanisms only; product policy remains with the caller.
type ContextComponents struct {
	// SystemPrompt is assembled before every model request and is not persisted
	// into the durable conversation history.
	SystemPrompt string

	// Assembler chooses how static prompt blocks, working history, and visible
	// tools become a provider-neutral model request.
	Assembler seelectx.RequestAssembler

	// PromptBlocks are caller-owned static contributions such as plan, task,
	// or skill instructions. They are copied into each model request.
	PromptBlocks []seelectx.PromptBlock

	// ToolResultProcessor chooses the model-visible view of an opaque tool
	// result before it is appended to working history.
	ToolResultProcessor seelectx.ToolResultProcessor

	// Compressor is available only through explicit ReActLoop.CompressNow or a
	// caller-supplied ContextController; Session never creates a policy.
	Compressor seelectx.Compressor

	// Controller receives structured context events. A nil controller means the
	// Session never evaluates or changes context policy on its own.
	Controller seelectx.ContextController
}

// SessionComponents is the explicit construction contract for a user-facing
// Session. Agent is required; every other capability is optional.
type SessionComponents struct {
	// Agent supplies the already assembled LLM client and tool runtime.
	Agent Agent

	// History is the optional durable owner. Session keeps a separate working
	// copy and only loads/saves through this interface during Chat.
	History seelectx.DurableHistory

	Context ContextComponents

	// Telemetry receives OTel-aligned Agent, LLM, and tool lifecycle events.
	// A nil hook leaves the execution path uninstrumented.
	Telemetry telemetry.Hook

	// Tracer is the compatibility tree tracer used by ExportTrace. Prefer
	// Telemetry for new product instrumentation.
	Tracer tracer.Tracer

	// Hooks are optional synchronous progress callbacks for interactive hosts.
	Hooks *LoopHooks

	Cache     cache.Provider
	Store     storage.Storage
	Config    SessionConfig
	SessionID string
	ModelName string
}

// NewSession builds a ReAct conversation from explicit components. It creates
// no provider, tool registry, account pool, durable history, or product
// context policy. Runtime and History remain owned by the caller.
func NewSession(components SessionComponents) (*Session, error) {
	components.normalizeOptionals()
	if err := components.validate(); err != nil {
		return nil, err
	}

	configuration := components.Config.Effective()
	sessionID := components.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	trace := components.Tracer
	if trace == nil {
		trace = &tracer.NoopTracer{}
	}

	session := &Session{
		agent:             components.Agent,
		llm:               components.Agent.LLM(),
		cfg:               configuration,
		sessionID:         sessionID,
		cache:             components.Cache,
		store:             components.Store,
		modelName:         components.ModelName,
		tracer:            trace,
		hooks:             components.Hooks,
		telemetryHook:     components.Telemetry,
		blockSystemPrompt: components.Context.SystemPrompt != "",
	}

	loopOptions := []ReActLoopOption{
		WithSessionID(sessionID),
		WithModelName(components.ModelName),
		WithHistoryOwner(components.History),
	}
	if components.Context.Assembler != nil {
		loopOptions = append(loopOptions, WithRequestAssembler(components.Context.Assembler))
	}
	if components.Context.ToolResultProcessor != nil {
		loopOptions = append(loopOptions, WithToolResultProcessor(components.Context.ToolResultProcessor))
	}
	if components.Context.Compressor != nil {
		loopOptions = append(loopOptions, WithCompressor(components.Context.Compressor))
	}
	if components.Context.Controller != nil {
		loopOptions = append(loopOptions, WithContextController(components.Context.Controller))
	}
	if components.Telemetry != nil {
		loopOptions = append(loopOptions, WithReActTelemetryHook(components.Telemetry))
	}
	if blocks := sessionPromptBlocks(components.Context); len(blocks) > 0 {
		loopOptions = append(loopOptions, WithPromptBlocks(blocks...))
	}

	loop := NewReActLoop(components.Agent, session.llm, loopOptions...)
	loop.cfg = configuration
	loop.tracer = trace
	loop.cache = components.Cache
	loop.respCache = cache.NewResponseCache(components.Cache)
	loop.store = components.Store
	loop.hooks = components.Hooks
	session.loop = loop
	return session, nil
}

// normalizeOptionals converts typed-nil interface values to a plain nil before
// they are passed to the loop. This keeps optional components truly optional:
// a caller may construct them through an interface without accidentally
// enabling a method call on a nil pointer.
func (components *SessionComponents) normalizeOptionals() {
	if nilComponent(components.History) {
		components.History = nil
	}
	if nilComponent(components.Context.Assembler) {
		components.Context.Assembler = nil
	}
	if nilComponent(components.Context.ToolResultProcessor) {
		components.Context.ToolResultProcessor = nil
	}
	if nilComponent(components.Context.Compressor) {
		components.Context.Compressor = nil
	}
	if nilComponent(components.Context.Controller) {
		components.Context.Controller = nil
	}
	if nilComponent(components.Telemetry) {
		components.Telemetry = nil
	}
	if nilComponent(components.Tracer) {
		components.Tracer = nil
	}
	if nilComponent(components.Cache) {
		components.Cache = nil
	}
	if nilComponent(components.Store) {
		components.Store = nil
	}
}

func (components SessionComponents) validate() error {
	if nilComponent(components.Agent) {
		return fmt.Errorf("session: agent is required")
	}
	if nilComponent(components.Agent.LLM()) {
		return fmt.Errorf("session: agent LLM is required")
	}
	return nil
}

func sessionPromptBlocks(components ContextComponents) []seelectx.PromptBlock {
	blocks := make([]seelectx.PromptBlock, 0, len(components.PromptBlocks)+1)
	if components.SystemPrompt != "" {
		prompt := components.SystemPrompt
		blocks = append(blocks, seelectx.PromptBlock{
			Name:     "system",
			Messages: []types.Message{{Role: "system", Content: &prompt}},
		})
	}
	blocks = append(blocks, components.PromptBlocks...)
	return blocks
}

func nilComponent(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
