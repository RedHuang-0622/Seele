package accountpool

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"
)

// LoadMetric converts an account snapshot into a score. Lower is better.
type LoadMetric interface {
	Score(AccountSnapshot) float64
}

// LoadMetricFunc adapts a function into a LoadMetric.
type LoadMetricFunc func(AccountSnapshot) float64

func (f LoadMetricFunc) Score(snapshot AccountSnapshot) float64 { return f(snapshot) }

// OccupancyMetric scores the fraction of occupied semaphore slots.
type OccupancyMetric struct{}

func (OccupancyMetric) Score(snapshot AccountSnapshot) float64 {
	if snapshot.MaxConcurrency <= 0 {
		return math.Inf(1)
	}
	return float64(snapshot.Active) / float64(snapshot.MaxConcurrency)
}

// Selector chooses one account from the currently available candidates.
type Selector interface {
	Select([]AccountSnapshot) (AccountSnapshot, error)
}

// P2CSelector implements Power of Two Choices with an injectable metric and
// random source. Its random source is protected for concurrent calls.
type P2CSelector struct {
	metric LoadMetric
	randMu sync.Mutex
	rand   *rand.Rand
}

func NewP2CSelector(metric LoadMetric, source rand.Source) *P2CSelector {
	if metric == nil {
		metric = OccupancyMetric{}
	}
	if source == nil {
		source = rand.NewSource(time.Now().UnixNano())
	}
	return &P2CSelector{metric: metric, rand: rand.New(source)}
}

func (s *P2CSelector) Select(candidates []AccountSnapshot) (AccountSnapshot, error) {
	if len(candidates) == 0 {
		return AccountSnapshot{}, ErrNoEligibleAccount
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	s.randMu.Lock()
	firstIndex := s.rand.Intn(len(candidates))
	secondIndex := s.rand.Intn(len(candidates) - 1)
	if secondIndex >= firstIndex {
		secondIndex++
	}
	chooseFirstOnTie := s.rand.Intn(2) == 0
	s.randMu.Unlock()

	first := candidates[firstIndex]
	second := candidates[secondIndex]
	firstScore := normalizedScore(s.metric.Score(first))
	secondScore := normalizedScore(s.metric.Score(second))
	switch {
	case firstScore < secondScore:
		return first, nil
	case secondScore < firstScore:
		return second, nil
	case chooseFirstOnTie:
		return first, nil
	default:
		return second, nil
	}
}

func normalizedScore(score float64) float64 {
	if math.IsNaN(score) {
		return math.Inf(1)
	}
	return score
}

func validateSelection(selected AccountSnapshot, candidates []AccountSnapshot) error {
	for _, candidate := range candidates {
		if candidate.ID == selected.ID {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrInvalidSelection, selected.ID)
}
