package node

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// TypedFunctionNode is a generic FunctionNode variant. It transports input
// and output as JSON-backed Value objects while still satisfying Node for old
// runners and custom integrations.
type TypedFunctionNode[I, O any] struct {
	BaseNode
	fn func(context.Context, I) (O, error)
}

// NewTypedFunctionNode creates a generic function node with typed I/O.
func NewTypedFunctionNode[I, O any](id string, fn func(context.Context, I) (O, error)) *TypedFunctionNode[I, O] {
	return &TypedFunctionNode[I, O]{
		BaseNode: NewBaseNode(id, KindMethod),
		fn:       fn,
	}
}

// RunValue decodes the previous Value, invokes the function, and encodes its
// result as a Value.
func (n *TypedFunctionNode[I, O]) RunValue(ctx context.Context, wc *types.WorkflowContext) (types.Value, error) {
	if n == nil {
		return types.Value{}, fmt.Errorf("typed function node: node is nil")
	}
	if n.fn == nil {
		return types.Value{}, fmt.Errorf("typed function node %q: function is nil", n.ID())
	}
	if wc == nil {
		return types.Value{}, fmt.Errorf("typed function node %q: workflow context is nil", n.ID())
	}
	input := types.RawValue(wc.PrevOutput)
	if len(wc.Prev.Raw) > 0 {
		input = wc.Prev
	}
	decoded, err := types.DecodeValue[I](input)
	if err != nil {
		return types.Value{}, fmt.Errorf("typed function node %q: decode input: %w", n.ID(), err)
	}
	output, err := n.fn(ctx, decoded)
	if err != nil {
		return types.Value{}, err
	}
	return types.NewValue(output)
}

// Run preserves the legacy string return contract.
func (n *TypedFunctionNode[I, O]) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	value, err := n.RunValue(ctx, wc)
	if err != nil {
		return "", err
	}
	return value.RawString(), nil
}

var _ ValueNode = (*TypedFunctionNode[any, any])(nil)
