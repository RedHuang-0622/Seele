package limits

import (
	"fmt"

	"github.com/RedHuang-0622/Seele/types"
)

// Cost describes the quota a single call consumes.
//
// Tokens are charged from an estimate at admission time. The non-streaming
// path settles the estimate against the provider-reported usage; streaming
// paths keep the estimate because Seele's current SSE handling does not expose
// usage, so callers that know better can call Permit.Settle themselves.
type Cost struct {
	// Requests is the number of provider requests; zero is treated as one.
	Requests int `yaml:"requests" json:"requests"`

	// InputTokens is the estimated prompt tokens (images included).
	InputTokens int `yaml:"input_tokens" json:"input_tokens"`

	// OutputTokens is the estimated completion tokens.
	OutputTokens int `yaml:"output_tokens" json:"output_tokens"`

	// Images is the number of image attachments in the request. It only
	// affects the weighted in-flight slot, never the token estimate, because
	// image cost is a function of pixels rather than bytes.
	Images int `yaml:"images" json:"images"`
}

// normalize applies the documented defaults so callers can pass a zero value.
func (c Cost) normalize() Cost {
	if c.Requests <= 0 {
		c.Requests = 1
	}
	if c.InputTokens < 0 {
		c.InputTokens = 0
	}
	if c.OutputTokens < 0 {
		c.OutputTokens = 0
	}
	if c.Images < 0 {
		c.Images = 0
	}
	return c
}

// Tokens is the total token quota this cost consumes.
func (c Cost) Tokens() int { return c.InputTokens + c.OutputTokens }

// Weight is the number of weighted in-flight slots this cost occupies.
// imageWeight below or equal to zero disables image weighting.
func (c Cost) Weight(imageWeight float64) float64 {
	weight := float64(c.Requests)
	if imageWeight > 0 && c.Images > 0 {
		weight += float64(c.Images) * imageWeight
	}
	return weight
}

// validate rejects negative values the caller cannot have meant.
func (c Cost) validate() error {
	switch {
	case c.Requests < 0:
		return fmt.Errorf("%w: cost.requests must be >= 0, got %d", ErrInvalidParams, c.Requests)
	case c.InputTokens < 0:
		return fmt.Errorf("%w: cost.input_tokens must be >= 0, got %d", ErrInvalidParams, c.InputTokens)
	case c.OutputTokens < 0:
		return fmt.Errorf("%w: cost.output_tokens must be >= 0, got %d", ErrInvalidParams, c.OutputTokens)
	case c.Images < 0:
		return fmt.Errorf("%w: cost.images must be >= 0, got %d", ErrInvalidParams, c.Images)
	}
	return nil
}

// CostEstimator derives the admission cost from a request before it is sent.
// The default estimator is a character heuristic; products that know their
// provider's real pricing formula should replace it.
type CostEstimator func(messages []types.Message, tools []types.Tool) Cost

// DefaultEstimator estimates prompt tokens from message text and tool schemas,
// plus a fixed completion allowance.
//
// Text is priced with the same shape Seele uses elsewhere: CJK characters cost
// roughly one token each, ASCII roughly one token per four characters. Image
// parts are not part of the message model yet (Seele G13); when they land, this
// function is the single place that must add the per-tile image charge, and
// Cost.Images is already carried through admission for that purpose.
func DefaultEstimator(messages []types.Message, tools []types.Tool) Cost {
	tokens := 0
	for i := range messages {
		message := messages[i]
		if message.Content != nil {
			tokens += EstimateTextTokens(*message.Content)
		}
		tokens += EstimateTextTokens(message.ReasoningContent)
		for j := range message.ToolCalls {
			tokens += EstimateTextTokens(message.ToolCalls[j].Function.Name)
			tokens += EstimateTextTokens(message.ToolCalls[j].Function.Arguments)
		}
	}
	for i := range tools {
		tokens += EstimateTextTokens(tools[i].Function.Name)
		tokens += EstimateTextTokens(tools[i].Function.Description)
		if parameters := tools[i].Function.Parameters; parameters != nil {
			tokens += EstimateTextTokens(fmt.Sprintf("%v", parameters))
		}
	}
	return Cost{Requests: 1, InputTokens: tokens}
}

// EstimateTextTokens is the shared text heuristic: one token per CJK rune,
// one token per four ASCII bytes, one token per two other bytes.
func EstimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk, ascii, other := 0, 0, 0
	for _, r := range text {
		switch {
		case r < 0x80:
			ascii++
		case isCJK(r):
			cjk++
		default:
			other++
		}
	}
	return cjk + (ascii+3)/4 + (other+1)/2
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return true
	case r >= 0x3400 && r <= 0x4DBF:
		return true
	case r >= 0x3040 && r <= 0x30FF:
		return true
	case r >= 0xAC00 && r <= 0xD7AF:
		return true
	}
	return false
}
