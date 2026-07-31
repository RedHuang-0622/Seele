package telemetry

// OpenTelemetry-aligned GenAI and error semantic attribute names.
// These constants intentionally mirror the upstream names without importing a
// versioned semconv package into the public API.
const (
	AttributeGenAIOperationName = "gen_ai.operation.name"
	AttributeGenAIProviderName  = "gen_ai.provider.name"
	AttributeGenAIRequestModel  = "gen_ai.request.model"
	AttributeGenAIResponseModel = "gen_ai.response.model"
	AttributeGenAIResponseID    = "gen_ai.response.id"
	AttributeGenAIUsageInput    = "gen_ai.usage.input_tokens"
	AttributeGenAIUsageOutput   = "gen_ai.usage.output_tokens"
	AttributeGenAIToolName      = "gen_ai.tool.name"
	AttributeGenAIToolCallID    = "gen_ai.tool.call.id"
	AttributeGenAIAgentName     = "gen_ai.agent.name"
	AttributeGenAIAgentID       = "gen_ai.agent.id"
	AttributeErrorType          = "error.type"
	AttributeExceptionMessage   = "exception.message"
	AttributeExceptionEscaped   = "exception.escaped"
	AttributeSeeleEventType     = "seele.event.type"
	AttributeSeeleCorrelationID = "seele.correlation.id"
	AttributeSeeleParentSpanID  = "seele.parent_span.id"
	MetricSpanDuration          = "seele.span.duration"
	MetricGenAIInputTokens      = "gen_ai.client.token.usage.input"
	MetricGenAIOutputTokens     = "gen_ai.client.token.usage.output"
)

func semanticOperationName(eventType EventType) string {
	switch eventType {
	case EventAgentStart, EventAgentEnd:
		return "invoke_agent"
	case EventLLMBefore, EventLLMAfter:
		return "chat"
	case EventToolBefore, EventToolAfter:
		return "execute_tool"
	case EventHandoffBefore, EventHandoffAfter:
		return "handoff"
	case EventContextAssembleBefore, EventContextAssembleAfter:
		return "assemble_context"
	case EventContextCompressBefore, EventContextCompressAfter:
		return "compress_context"
	default:
		return ""
	}
}

func withSemanticDefaults(event Event) Event {
	if event.Error != nil {
		event.Status = StatusError
	}
	event.Attributes = cloneAttributes(event.Attributes)
	if event.Attributes == nil {
		event.Attributes = make(Attributes)
	}
	event.Attributes[AttributeSeeleEventType] = string(event.Type)
	if event.CorrelationID != "" {
		event.Attributes[AttributeSeeleCorrelationID] = event.CorrelationID
	}
	if event.ParentSpanID != "" {
		event.Attributes[AttributeSeeleParentSpanID] = event.ParentSpanID
	}
	if _, exists := event.Attributes[AttributeGenAIOperationName]; !exists {
		if operation := semanticOperationName(event.Type); operation != "" {
			event.Attributes[AttributeGenAIOperationName] = operation
		}
	}
	if event.Error != nil {
		if _, exists := event.Attributes[AttributeErrorType]; !exists {
			event.Attributes[AttributeErrorType] = event.Error.Type
		}
		if _, exists := event.Attributes[AttributeExceptionMessage]; !exists {
			event.Attributes[AttributeExceptionMessage] = event.Error.Message
		}
	}
	return event
}
