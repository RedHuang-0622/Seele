package bridge

import (
	"context"
	"fmt"
	"reflect"

	"github.com/RedHuang-0622/Seele/accountpool"
	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/types"
)

// AccountRequestSelector derives the account-pool constraints for one model
// completion. It is deliberately caller supplied: account IDs, account
// metadata, and product routing policy do not belong to Agent or accountpool.
type AccountRequestSelector func(context.Context, []types.Message, []types.Tool) accountpool.AcquireRequest

// AccountCompleterOption configures an AccountCompleter.
type AccountCompleterOption func(*AccountCompleter)

// WithAccountRequestSelector configures request-scoped selection of a client
// lease. Without a selector, the resolver receives an empty AcquireRequest
// and a P2C pool chooses an eligible account.
func WithAccountRequestSelector(selector AccountRequestSelector) AccountCompleterOption {
	return func(completer *AccountCompleter) {
		if selector != nil {
			completer.selector = selector
		}
	}
}

// AccountCompleter adapts an accountpool resolver of synchronous completers to
// agent.Completer. It acquires a lease for exactly one Complete call and
// releases it before returning, even when the provider call fails.
//
// Streaming remains a separate optional Agent capability because a streaming
// lease must span the whole stream rather than a single completion call.
type AccountCompleter struct {
	resolver accountpool.ClientResolver[agent.Completer]
	selector AccountRequestSelector
}

// NewAccountCompleter creates an Agent-compatible completer from either a
// P2C pool or a hard-coded accountpool.StaticResolver. Construction performs
// no provider I/O and does not own the resolver lifecycle.
func NewAccountCompleter(
	resolver accountpool.ClientResolver[agent.Completer],
	options ...AccountCompleterOption,
) (*AccountCompleter, error) {
	if isNilAccountResolver(resolver) {
		return nil, fmt.Errorf("agent.bridge: account resolver is required")
	}
	completer := &AccountCompleter{resolver: resolver}
	for _, option := range options {
		if option != nil {
			option(completer)
		}
	}
	return completer, nil
}

// Complete chooses a client, invokes it once, and releases the account lease.
func (c *AccountCompleter) Complete(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
) (message types.Message, err error) {
	if c == nil || isNilAccountResolver(c.resolver) {
		return types.Message{}, fmt.Errorf("agent.bridge: account resolver is required")
	}
	request := accountpool.AcquireRequest{}
	if c.selector != nil {
		request = c.selector(ctx, messages, tools)
	}
	lease, err := c.resolver.Resolve(ctx, request)
	if err != nil {
		return types.Message{}, fmt.Errorf("agent.bridge: acquire completion client: %w", err)
	}
	if isNilAccountLease(lease) {
		return types.Message{}, fmt.Errorf("agent.bridge: account resolver returned a nil lease")
	}
	defer func() {
		if releaseErr := lease.Release(); releaseErr != nil && err == nil {
			err = fmt.Errorf("agent.bridge: release completion client %q: %w", lease.AccountID(), releaseErr)
		}
	}()
	client := lease.Client()
	if isNilCompleter(client) {
		return types.Message{}, fmt.Errorf("agent.bridge: account %q has no completer", lease.AccountID())
	}
	message, err = client.Complete(ctx, messages, tools)
	if err != nil {
		return types.Message{}, fmt.Errorf("agent.bridge: complete with account %q: %w", lease.AccountID(), err)
	}
	return message, nil
}

func isNilAccountResolver(resolver accountpool.ClientResolver[agent.Completer]) bool {
	return isNilInterface(resolver)
}

func isNilAccountLease(lease accountpool.ClientLease[agent.Completer]) bool {
	return isNilInterface(lease)
}

func isNilCompleter(completer agent.Completer) bool {
	return isNilInterface(completer)
}

func isNilInterface(value any) bool {
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

var _ agent.Completer = (*AccountCompleter)(nil)
