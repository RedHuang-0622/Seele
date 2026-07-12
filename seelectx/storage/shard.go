package storage

import (
	"encoding/json"

	types "github.com/RedHuang-0622/Seele/types"
)

const (
	// maxShardTokens is the maximum estimated tokens per shard before splitting.
	maxShardTokens = 8000
	// maxShardMessages is the maximum number of messages per shard before splitting.
	maxShardMessages = 100
)

// estimateTokens estimates the token count of messages using JSON byte-length heuristic.
// formula: len(JSON(messages))/3, matching the convention used in contexts/ctx_manager.
func estimateTokens(msgs []types.Message) int {
	if len(msgs) == 0 {
		return 0
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		return 0
	}
	return (len(b) + 2) / 3
}

// shouldSplit returns true if messages exceed the shard threshold.
func shouldSplit(msgs []types.Message) bool {
	return len(msgs) > maxShardMessages || estimateTokens(msgs) > maxShardTokens
}

// splitMessages divides messages into shards based on the configured thresholds.
// Each shard does not exceed maxShardMessages or maxShardTokens.
func splitMessages(msgs []types.Message) [][]types.Message {
	if !shouldSplit(msgs) {
		return [][]types.Message{msgs}
	}

	var shards [][]types.Message
	remaining := msgs

	for len(remaining) > 0 {
		splitIdx := findSplitIndex(remaining)
		shards = append(shards, remaining[:splitIdx])
		remaining = remaining[splitIdx:]
	}

	return shards
}

// findSplitIndex determines the optimal split point in messages.
// It greedily takes as many messages as possible without exceeding
// maxShardMessages or maxShardTokens.
func findSplitIndex(msgs []types.Message) int {
	limit := maxShardMessages
	if limit > len(msgs) {
		limit = len(msgs)
	}

	// Check each prefix length from 1..limit
	for i := 1; i <= limit; i++ {
		tokens := estimateTokens(msgs[:i])
		if tokens > maxShardTokens {
			if i == 1 {
				// Single message exceeds budget; keep it as its own shard.
				return 1
			}
			return i - 1
		}
	}

	return limit
}
