// Package dsl defines the versioned JSON syntax for executable Seele WorkPlans.
package dsl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Version is the only supported version of the Seele WorkPlan JSON DSL.
const Version = 1

const autoKind = "auto"

// Plan is the complete, executable JSON representation of a WorkPlan.
type Plan struct {
	Version int    `json:"version"`
	Entry   string `json:"entry"`
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
}

// Node is a task executed by an auto subagent.
type Node struct {
	ID    string `json:"id"`
	Input string `json:"input"`
	Kind  string `json:"kind"`
}

// Edge defines a directed dependency from one node to another.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ValidationError identifies the JSON path and reason for a DSL error.
type ValidationError struct {
	Path   string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("Seele DSL validation error at %s: %s", e.Path, e.Reason)
}

// SyntaxError identifies an invalid JSON character position and reason.
type SyntaxError struct {
	Line   int
	Column int
	Reason string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("Seele DSL syntax error at line %d, column %d: %s", e.Line, e.Column, e.Reason)
}

// Parse decodes and validates a complete versioned Seele WorkPlan document.
func Parse(data string) (*Plan, error) {
	raw := []byte(data)
	if err := ensureValidJSON(raw); err != nil {
		return nil, err
	}

	root, err := object(raw, "$")
	if err != nil {
		return nil, err
	}
	if err := allowedFields(root, "$", "version", "entry", "nodes", "edges"); err != nil {
		return nil, err
	}

	version, err := requiredInt(root, "version", "$.version")
	if err != nil {
		return nil, err
	}
	entry, err := requiredString(root, "entry", "$.entry")
	if err != nil {
		return nil, err
	}
	nodeValues, err := requiredArray(root, "nodes", "$.nodes")
	if err != nil {
		return nil, err
	}
	edgeValues, err := requiredArray(root, "edges", "$.edges")
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		Version: version,
		Entry:   entry,
		Nodes:   make([]Node, 0, len(nodeValues)),
		Edges:   make([]Edge, 0, len(edgeValues)),
	}
	for i, value := range nodeValues {
		path := fmt.Sprintf("$.nodes[%d]", i)
		item, err := object(value, path)
		if err != nil {
			return nil, err
		}
		if err := allowedFields(item, path, "id", "input", "kind"); err != nil {
			return nil, err
		}
		id, err := requiredString(item, "id", path+".id")
		if err != nil {
			return nil, err
		}
		input, err := requiredString(item, "input", path+".input")
		if err != nil {
			return nil, err
		}
		kind, err := requiredString(item, "kind", path+".kind")
		if err != nil {
			return nil, err
		}
		plan.Nodes = append(plan.Nodes, Node{ID: id, Input: input, Kind: kind})
	}
	for i, value := range edgeValues {
		path := fmt.Sprintf("$.edges[%d]", i)
		item, err := object(value, path)
		if err != nil {
			return nil, err
		}
		if err := allowedFields(item, path, "from", "to"); err != nil {
			return nil, err
		}
		from, err := requiredString(item, "from", path+".from")
		if err != nil {
			return nil, err
		}
		to, err := requiredString(item, "to", path+".to")
		if err != nil {
			return nil, err
		}
		plan.Edges = append(plan.Edges, Edge{From: from, To: to})
	}

	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

// Validate checks document version, node kinds, references, DAG structure,
// and reachability. It is also used before programmatically-created plans are
// exported or compiled.
func (p *Plan) Validate() error {
	if p == nil {
		return validationError("$", "document must not be null")
	}
	if p.Version != Version {
		return validationError("$.version", fmt.Sprintf("must be %d, got %d", Version, p.Version))
	}
	if strings.TrimSpace(p.Entry) == "" {
		return validationError("$.entry", "must be a non-empty node ID")
	}
	if len(p.Nodes) == 0 {
		return validationError("$.nodes", "must contain at least one node")
	}

	nodeIndex := make(map[string]int, len(p.Nodes))
	for i, n := range p.Nodes {
		path := fmt.Sprintf("$.nodes[%d]", i)
		if strings.TrimSpace(n.ID) == "" {
			return validationError(path+".id", "must be a non-empty string")
		}
		if previous, exists := nodeIndex[n.ID]; exists {
			return validationError(path+".id", fmt.Sprintf("duplicates node ID %q already declared at $.nodes[%d].id", n.ID, previous))
		}
		if strings.TrimSpace(n.Input) == "" {
			return validationError(path+".input", "must be a non-empty task prompt")
		}
		if n.Kind != autoKind {
			return validationError(path+".kind", fmt.Sprintf("must be %q in DSL version %d, got %q", autoKind, Version, n.Kind))
		}
		nodeIndex[n.ID] = i
	}
	if _, exists := nodeIndex[p.Entry]; !exists {
		return validationError("$.entry", fmt.Sprintf("references node %q, but no such node is declared", p.Entry))
	}

	adjacency := make(map[string][]edgeRef, len(p.Nodes))
	edgeIndex := make(map[string]int, len(p.Edges))
	for i, e := range p.Edges {
		path := fmt.Sprintf("$.edges[%d]", i)
		if strings.TrimSpace(e.From) == "" {
			return validationError(path+".from", "must be a non-empty node ID")
		}
		if strings.TrimSpace(e.To) == "" {
			return validationError(path+".to", "must be a non-empty node ID")
		}
		if _, exists := nodeIndex[e.From]; !exists {
			return validationError(path+".from", fmt.Sprintf("references undeclared node %q", e.From))
		}
		if _, exists := nodeIndex[e.To]; !exists {
			return validationError(path+".to", fmt.Sprintf("references undeclared node %q", e.To))
		}
		if e.From == e.To {
			return validationError(path, fmt.Sprintf("self-edge %q -> %q creates a cycle", e.From, e.To))
		}
		key := e.From + "\x00" + e.To
		if previous, exists := edgeIndex[key]; exists {
			return validationError(path, fmt.Sprintf("duplicates edge %q -> %q already declared at $.edges[%d]", e.From, e.To, previous))
		}
		edgeIndex[key] = i
		adjacency[e.From] = append(adjacency[e.From], edgeRef{to: e.To, index: i})
	}
	if err := validateAcyclic(p.Nodes, adjacency); err != nil {
		return err
	}
	if err := validateReachable(p, adjacency); err != nil {
		return err
	}
	return nil
}

// ToJSON validates and serializes a complete executable plan.
func (p *Plan) ToJSON() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type edgeRef struct {
	to    string
	index int
}

func validateAcyclic(nodes []Node, adjacency map[string][]edgeRef) error {
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		state[id] = 1
		for _, edge := range adjacency[id] {
			switch state[edge.to] {
			case 1:
				return validationError(fmt.Sprintf("$.edges[%d]", edge.index), fmt.Sprintf("creates a cycle through node %q", edge.to))
			case 0:
				if err := visit(edge.to); err != nil {
					return err
				}
			}
		}
		state[id] = 2
		return nil
	}
	for _, n := range nodes {
		if state[n.ID] == 0 {
			if err := visit(n.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReachable(p *Plan, adjacency map[string][]edgeRef) error {
	reachable := map[string]bool{p.Entry: true}
	queue := []string{p.Entry}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, edge := range adjacency[id] {
			if !reachable[edge.to] {
				reachable[edge.to] = true
				queue = append(queue, edge.to)
			}
		}
	}
	for i, n := range p.Nodes {
		if !reachable[n.ID] {
			return validationError(fmt.Sprintf("$.nodes[%d].id", i), fmt.Sprintf("node %q is unreachable from entry %q", n.ID, p.Entry))
		}
	}
	return nil
}

func ensureValidJSON(data []byte) error {
	var value json.RawMessage
	if err := json.Unmarshal(data, &value); err == nil {
		return nil
	} else if syntax, ok := err.(*json.SyntaxError); ok {
		line, column := lineColumn(data, syntax.Offset)
		return &SyntaxError{Line: line, Column: column, Reason: syntax.Error()}
	} else {
		return &SyntaxError{Line: 1, Column: 1, Reason: err.Error()}
	}
}

func lineColumn(data []byte, offset int64) (int, int) {
	line, column := 1, 1
	limit := int(offset) - 1
	if limit > len(data) {
		limit = len(data)
	}
	for i := 0; i < limit; i++ {
		if data[i] == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return line, column
}

func object(value []byte, path string) (map[string]json.RawMessage, error) {
	if jsonType(value) != "object" {
		return nil, validationError(path, fmt.Sprintf("must be an object, got %s", jsonType(value)))
	}
	result := make(map[string]json.RawMessage)
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, validationError(path, err.Error())
	}
	return result, nil
}

func requiredArray(values map[string]json.RawMessage, field, path string) ([]json.RawMessage, error) {
	value, err := required(values, field, path)
	if err != nil {
		return nil, err
	}
	if jsonType(value) != "array" {
		return nil, validationError(path, fmt.Sprintf("must be an array, got %s", jsonType(value)))
	}
	var result []json.RawMessage
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, validationError(path, err.Error())
	}
	return result, nil
}

func requiredString(values map[string]json.RawMessage, field, path string) (string, error) {
	value, err := required(values, field, path)
	if err != nil {
		return "", err
	}
	if jsonType(value) != "string" {
		return "", validationError(path, fmt.Sprintf("must be a string, got %s", jsonType(value)))
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", validationError(path, err.Error())
	}
	return result, nil
}

func requiredInt(values map[string]json.RawMessage, field, path string) (int, error) {
	value, err := required(values, field, path)
	if err != nil {
		return 0, err
	}
	if jsonType(value) != "number" {
		return 0, validationError(path, fmt.Sprintf("must be an integer, got %s", jsonType(value)))
	}
	var result int
	if err := json.Unmarshal(value, &result); err != nil {
		return 0, validationError(path, "must be an integer")
	}
	return result, nil
}

func required(values map[string]json.RawMessage, field, path string) ([]byte, error) {
	value, exists := values[field]
	if !exists {
		return nil, validationError(path, "is required")
	}
	return value, nil
}

func allowedFields(values map[string]json.RawMessage, path string, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		set[field] = true
	}
	unknown := make([]string, 0)
	for field := range values {
		if !set[field] {
			unknown = append(unknown, field)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return validationError(fieldPath(path, unknown[0]), "is not a supported field")
	}
	return nil
}

func fieldPath(path, field string) string {
	if path == "$" {
		return path + "." + field
	}
	return path + "." + field
}

func jsonType(value []byte) string {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return "invalid JSON"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func validationError(path, reason string) error {
	return &ValidationError{Path: path, Reason: reason}
}
