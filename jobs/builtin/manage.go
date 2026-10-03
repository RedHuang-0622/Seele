// Package builtin exposes the framework-side, product-neutral management tool
// for the jobs root capability.
//
// It is the management half of the contract only: observe / fetch / kill /
// done. The dispatch half (bash_bg, read_batch, fork_subagents ...) is
// product-owned and lives in the host, because a dispatch tool is bound to a
// concrete command and prompt vocabulary (decision D1).
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RedHuang-0622/Seele/jobs"
	roottools "github.com/RedHuang-0622/Seele/tools"
	seeletypes "github.com/RedHuang-0622/Seele/types"
)

const (
	// ProviderName is this provider's registry name.
	ProviderName = "seele_jobs"
	// ManageToolName is the framework management tool's name.
	ManageToolName = "jobs_manage"
)

// manageArguments is the tool's input.
type manageArguments struct {
	Op       string `json:"op" desc:"observe | fetch | kill | done" enum:"observe,fetch,kill,done"`
	Handle   string `json:"handle,omitempty" desc:"job handle such as a12; omit it with op=observe to list the caller scope's jobs"`
	WaitMS   int    `json:"wait_ms,omitempty" desc:"op=fetch wait budget in ms: 0 = default, negative = return immediately, above the ceiling is clamped"`
	MaxBytes int    `json:"max_bytes,omitempty" desc:"op=fetch byte budget for this call; 0 = default"`
}

// manageResponse is the tool's output: a neutral projection of the records it
// touched. Products render their own receipts from these fields.
type manageResponse struct {
	Op          string        `json:"op"`
	Handle      string        `json:"handle,omitempty"`
	Kind        string        `json:"kind,omitempty"`
	State       string        `json:"state,omitempty"`
	ExitCode    int           `json:"exit_code,omitempty"`
	Session     string        `json:"session,omitempty"`
	Subject     string        `json:"subject,omitempty"`
	Node        string        `json:"node,omitempty"`
	Batch       string        `json:"batch,omitempty"`
	Description string        `json:"description,omitempty"`
	Bytes       int64         `json:"bytes,omitempty"`
	Lines       int           `json:"lines,omitempty"`
	Cursor      int64         `json:"cursor,omitempty"`
	Truncated   bool          `json:"truncated,omitempty"`
	Degraded    bool          `json:"degraded,omitempty"`
	Repeated    bool          `json:"repeated,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Output      string        `json:"output,omitempty"`
	Jobs        []jobs.Record `json:"jobs,omitempty"`
	Count       int           `json:"count,omitempty"`
	Hint        string        `json:"hint,omitempty"`
}

// Provider implements roottools.ToolProvider for jobs_manage.
type Provider struct {
	manager jobs.Manager
	meta    *roottools.ToolMeta
}

// Option configures the provider.
type Option func(*Provider)

// WithMeta overrides the tool metadata declared to the permission middleware.
// Only its zero value ("" Kind, no groups, no bits) keeps the default.
func WithMeta(meta roottools.ToolMeta) Option {
	return func(provider *Provider) { provider.meta = &meta }
}

// New builds the provider for one manager.
func New(manager jobs.Manager, options ...Option) *Provider {
	provider := &Provider{manager: manager}
	for _, option := range options {
		if option != nil {
			option(provider)
		}
	}
	return provider
}

// ProviderName implements roottools.ToolProvider.
func (p *Provider) ProviderName() string { return ProviderName }

// Tools implements roottools.ToolProvider.
func (p *Provider) Tools() []roottools.ToolEntry {
	meta := p.meta
	if meta == nil {
		meta = &roottools.ToolMeta{Kind: roottools.ToolKindWrite, Groups: []string{"jobs"}, Bits: roottools.BitWrite | roottools.BitExecute}
	}
	return []roottools.ToolEntry{{
		Definition: seeletypes.Tool{
			Type: "function",
			Function: seeletypes.ToolFunction{
				Name: ManageToolName,
				Description: "Observe, fetch (consuming), kill or retire one dispatched job. " +
					"op=fetch without a handle is invalid; op=observe without a handle lists the caller scope's jobs. " +
					"Never poll: the work table already shows running jobs.",
				Parameters: roottools.SchemaOf(manageArguments{}),
			},
		},
		Handler:      roottools.HandlerFunc(p.execute),
		OutputSchema: roottools.SchemaOf(manageResponse{}),
		Meta:         meta,
	}}
}

// execute routes one management call.
func (p *Provider) execute(ctx context.Context, argumentsJSON string) (string, error) {
	var arguments manageArguments
	if err := decodeArguments(argumentsJSON, &arguments); err != nil {
		return "", err
	}
	if p.manager == nil {
		return "", fmt.Errorf("%s: 未装配 jobs.Manager", ManageToolName)
	}
	handle := jobs.Handle(strings.TrimSpace(arguments.Handle))
	switch strings.ToLower(strings.TrimSpace(arguments.Op)) {
	case "observe":
		return p.observe(ctx, handle)
	case "fetch":
		return p.fetch(ctx, handle, arguments)
	case "kill":
		return p.kill(ctx, handle)
	case "done":
		return p.done(ctx, handle)
	case "":
		return "", fmt.Errorf("%s: op is required (observe | fetch | kill | done)", ManageToolName)
	default:
		return "", fmt.Errorf("%s: unknown op %q (observe | fetch | kill | done)", ManageToolName, arguments.Op)
	}
}

func (p *Provider) observe(ctx context.Context, handle jobs.Handle) (string, error) {
	if handle == "" {
		records := p.manager.Snapshot(p.manager.ScopeOf(ctx))
		return encodeResponse(manageResponse{
			Op: "observe", Jobs: records, Count: len(records),
			Hint: "以上是本作用域在册作业的只读读数（不推进游标、不消费输出）。要结果用它自己的 handle 调 op=fetch。",
		})
	}
	record, ok := p.manager.Observe(handle)
	if !ok {
		return "", fmt.Errorf("%s: 未知句柄 %q（可能已被销项或驱逐，或进程重启后登记表已清空）", ManageToolName, handle)
	}
	return encodeResponse(responseForRecord("observe", record))
}

func (p *Provider) fetch(ctx context.Context, handle jobs.Handle, arguments manageArguments) (string, error) {
	if handle == "" {
		return "", fmt.Errorf("%s: op=fetch 需要 handle", ManageToolName)
	}
	chunk, record, err := p.manager.Fetch(ctx, handle, jobs.FetchBudget{WaitMS: arguments.WaitMS, MaxBytes: arguments.MaxBytes})
	if err != nil {
		return "", fmt.Errorf("%s: %w", ManageToolName, err)
	}
	response := responseForRecord("fetch", record)
	response.Output = chunk
	response.Count = len(chunk)
	if record.State == "running" {
		response.Hint = "作业仍在跑：以上是本轮取回的增量，游标已推进。不要轮询——工作打点表会自己更新。"
	} else {
		response.Hint = "作业已终态且增量已交付；该行已从登记表销项。"
	}
	return encodeResponse(response)
}

func (p *Provider) kill(ctx context.Context, handle jobs.Handle) (string, error) {
	if handle == "" {
		return "", fmt.Errorf("%s: op=kill 需要 handle", ManageToolName)
	}
	if err := p.manager.Kill(ctx, handle); err != nil {
		return "", fmt.Errorf("%s: %w", ManageToolName, err)
	}
	response := manageResponse{Op: "kill", Handle: string(handle), Hint: "已发出终止；已产出内容保留，仍可 op=fetch 取回。"}
	if record, ok := p.manager.Observe(handle); ok {
		response = responseForRecord("kill", record)
		response.Hint = "已发出的终止已落定；已产出内容保留，仍可 op=fetch 取回。"
	}
	return encodeResponse(response)
}

func (p *Provider) done(ctx context.Context, handle jobs.Handle) (string, error) {
	if handle == "" {
		return "", fmt.Errorf("%s: op=done 需要 handle", ManageToolName)
	}
	if err := p.manager.Done(ctx, handle); err != nil {
		return "", fmt.Errorf("%s: %w", ManageToolName, err)
	}
	return encodeResponse(manageResponse{
		Op: "done", Handle: string(handle),
		Hint: "已销项（幂等）：重复 done 无副作用；输出已在销项前那次取回的结果里。",
	})
}

// responseForRecord projects one record into the tool's response shape.
func responseForRecord(op string, record jobs.Record) manageResponse {
	return manageResponse{
		Op: op, Handle: string(record.Handle), Kind: string(record.Kind), State: string(record.State),
		ExitCode: record.ExitCode, Session: record.Scope.Session, Subject: record.Scope.Subject,
		Node: record.Node, Batch: record.Batch, Description: record.Description,
		Bytes: record.Bytes, Lines: record.Lines, Cursor: record.Cursor,
		Truncated: record.Truncated, Degraded: record.Degraded, Repeated: record.Repeated,
		Summary: record.Summary,
	}
}

func encodeResponse(response manageResponse) (string, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("%s: encode response: %w", ManageToolName, err)
	}
	return string(encoded), nil
}

// decodeArguments decodes the tool's arguments strictly: an unknown field is a
// mistake worth reporting rather than silently ignoring.
func decodeArguments(raw string, target interface{}) error {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: arguments: %w", ManageToolName, err)
	}
	return nil
}
