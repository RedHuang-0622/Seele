// Package codec serializes the generic workplan topology without assigning
// product meaning to nodes. A caller supplies the NodeCodec that knows how to
// materialize its own node implementations.
package codec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	seeleerrors "github.com/RedHuang-0622/Seele/errors"
	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
)

const Version = 1

// NodeDefinition is intentionally opaque to the plan kernel. Type and Data
// are interpreted only by the supplied NodeCodec.
type NodeDefinition struct {
	ID   string          `json:"id"`
	Type string          `json:"type,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// AdjacencyList is a generic directed topology document.
type AdjacencyList struct {
	Version   int                 `json:"version"`
	Entry     string              `json:"entry"`
	Nodes     []NodeDefinition    `json:"nodes"`
	Adjacency map[string][]string `json:"adjacency"`
}

// EdgeDefinition is the portable directed-edge representation used by the
// formal {nodes, edges} JSON shape. It carries no product semantics.
type EdgeDefinition struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// EdgeList is an explicit edge-list document equivalent to AdjacencyList.
type EdgeList struct {
	Version int              `json:"version"`
	Entry   string           `json:"entry"`
	Nodes   []NodeDefinition `json:"nodes"`
	Edges   []EdgeDefinition `json:"edges"`
}

// AdjacencyMatrix is a generic directed topology document. Matrix rows and
// columns use the same order as Nodes; 1 means an edge and 0 means no edge.
type AdjacencyMatrix struct {
	Version int              `json:"version"`
	Entry   string           `json:"entry"`
	Nodes   []NodeDefinition `json:"nodes"`
	Order   []string         `json:"order"`
	Matrix  [][]int          `json:"matrix"`
}

// NodeEncoder serializes node implementation data.
type NodeEncoder interface {
	EncodeNode(node.Node) (NodeDefinition, error)
}

// NodeDecoder materializes node implementation data.
type NodeDecoder interface {
	DecodeNode(NodeDefinition) (node.Node, error)
}

// NodeCodec combines both directions for round-trip workflows.
type NodeCodec interface {
	NodeEncoder
	NodeDecoder
}

// Error is retained as a compatibility alias. New codec errors use the root
// structured envelope with struct/function/step/raw/path fields.
type Error = seeleerrors.Error

// ExportAdjacencyList encodes a Plan as a deterministic adjacency-list JSON
// document. Conditional edges cannot be represented and are rejected with a
// path to the offending edge.
func ExportAdjacencyList(p *coreplan.Plan, enc NodeEncoder) ([]byte, error) {
	doc, err := ToAdjacencyList(p, enc)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ToAdjacencyList converts a Plan into its structured representation.
func ToAdjacencyList(p *coreplan.Plan, enc NodeEncoder) (*AdjacencyList, error) {
	if p == nil {
		return nil, &Error{Path: "$", Reason: "plan must not be nil"}
	}
	if enc == nil {
		return nil, &Error{Path: "$.nodes", Reason: "node encoder is required"}
	}
	if err := p.Validate(); err != nil {
		return nil, &Error{Path: "$", Reason: err.Error(), Cause: err}
	}
	ids := p.AllNodes()
	sort.Strings(ids)
	doc := &AdjacencyList{Version: Version, Entry: p.Entry(), Nodes: make([]NodeDefinition, 0, len(ids)), Adjacency: make(map[string][]string, len(ids))}
	for i, id := range ids {
		definition, err := enc.EncodeNode(p.GetNode(id))
		if err != nil {
			return nil, wrap(fmt.Sprintf("$.nodes[%d]", i), err)
		}
		if definition.ID == "" {
			definition.ID = id
		}
		if definition.ID != id {
			return nil, &Error{Path: fmt.Sprintf("$.nodes[%d].id", i), Reason: fmt.Sprintf("encoder returned ID %q, expected %q", definition.ID, id)}
		}
		doc.Nodes = append(doc.Nodes, definition)
		neighbors := p.GetEdgesFrom(id)
		for j, e := range neighbors {
			if e.Condition != nil {
				return nil, &Error{Path: fmt.Sprintf("$.adjacency[%q][%d]", id, j), Reason: "conditional edges are not representable in adjacency-list format"}
			}
			doc.Adjacency[id] = append(doc.Adjacency[id], e.To)
		}
		sort.Strings(doc.Adjacency[id])
	}
	return doc, nil
}

// ImportAdjacencyList parses, validates, materializes, and seals a Plan.
func ImportAdjacencyList(data []byte, dec NodeDecoder) (*coreplan.Plan, error) {
	var doc AdjacencyList
	if err := decodeJSON(data, &doc); err != nil {
		return nil, err
	}
	return FromAdjacencyList(&doc, dec)
}

// FromAdjacencyList materializes a structured adjacency-list document.
func FromAdjacencyList(doc *AdjacencyList, dec NodeDecoder) (*coreplan.Plan, error) {
	if doc == nil {
		return nil, &Error{Path: "$", Reason: "document must not be null"}
	}
	if err := validateHeader(doc.Version, doc.Entry, doc.Nodes); err != nil {
		return nil, err
	}
	if dec == nil {
		return nil, &Error{Path: "$.nodes", Reason: "node decoder is required to materialize nodes"}
	}
	nodes, err := decodeNodes(doc.Nodes, dec)
	if err != nil {
		return nil, err
	}
	seenEdges := make(map[string]bool)
	edges := make([]edge.Edge, 0)
	known := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		known[n.ID()] = true
	}
	for from, tos := range doc.Adjacency {
		if !known[from] {
			return nil, &Error{Path: fmt.Sprintf("$.adjacency.%s", from), Reason: "references undeclared node"}
		}
		for i, to := range tos {
			path := fmt.Sprintf("$.adjacency[%q][%d]", from, i)
			if !known[to] {
				return nil, &Error{Path: path, Reason: fmt.Sprintf("references undeclared node %q", to)}
			}
			key := from + "\x00" + to
			if seenEdges[key] {
				return nil, &Error{Path: path, Reason: fmt.Sprintf("duplicates edge %q -> %q", from, to)}
			}
			seenEdges[key] = true
			edges = append(edges, edge.Edge{From: from, To: to})
		}
	}
	return buildPlan(nodes, edges, doc.Entry)
}

// ExportEdgeList encodes a Plan using the formal nodes/edges JSON shape.
func ExportEdgeList(p *coreplan.Plan, enc NodeEncoder) ([]byte, error) {
	doc, err := ToEdgeList(p, enc)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ToEdgeList converts a Plan into an explicit edge-list document.
func ToEdgeList(p *coreplan.Plan, enc NodeEncoder) (*EdgeList, error) {
	if p == nil {
		return nil, &Error{Path: "$", Reason: "plan must not be nil"}
	}
	if enc == nil {
		return nil, &Error{Path: "$.nodes", Reason: "node encoder is required"}
	}
	if err := p.Validate(); err != nil {
		return nil, &Error{Path: "$", Reason: err.Error(), Cause: err}
	}
	ids := p.AllNodes()
	sort.Strings(ids)
	doc := &EdgeList{Version: Version, Entry: p.Entry(), Nodes: make([]NodeDefinition, 0, len(ids))}
	for i, id := range ids {
		definition, err := enc.EncodeNode(p.GetNode(id))
		if err != nil {
			return nil, wrap(fmt.Sprintf("$.nodes[%d]", i), err)
		}
		if definition.ID == "" {
			definition.ID = id
		}
		if definition.ID != id {
			return nil, &Error{Path: fmt.Sprintf("$.nodes[%d].id", i), Reason: fmt.Sprintf("encoder returned ID %q, expected %q", definition.ID, id)}
		}
		doc.Nodes = append(doc.Nodes, definition)
	}
	for _, from := range ids {
		for _, candidate := range p.GetEdgesFrom(from) {
			if candidate.Condition != nil {
				return nil, &Error{Path: "$.edges", Reason: "conditional edges are not representable in edge-list format"}
			}
			doc.Edges = append(doc.Edges, EdgeDefinition{From: candidate.From, To: candidate.To})
		}
	}
	sort.Slice(doc.Edges, func(i, j int) bool {
		if doc.Edges[i].From == doc.Edges[j].From {
			return doc.Edges[i].To < doc.Edges[j].To
		}
		return doc.Edges[i].From < doc.Edges[j].From
	})
	return doc, nil
}

// ImportEdgeList parses and materializes the formal nodes/edges JSON shape.
func ImportEdgeList(data []byte, dec NodeDecoder) (*coreplan.Plan, error) {
	var doc EdgeList
	if err := decodeJSON(data, &doc); err != nil {
		return nil, err
	}
	return FromEdgeList(&doc, dec)
}

// FromEdgeList materializes an explicit edge-list document.
func FromEdgeList(doc *EdgeList, dec NodeDecoder) (*coreplan.Plan, error) {
	if doc == nil {
		return nil, &Error{Path: "$", Reason: "document must not be null"}
	}
	if err := validateHeader(doc.Version, doc.Entry, doc.Nodes); err != nil {
		return nil, err
	}
	if dec == nil {
		return nil, &Error{Path: "$.nodes", Reason: "node decoder is required to materialize nodes"}
	}
	nodes, err := decodeNodes(doc.Nodes, dec)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		known[n.ID()] = true
	}
	seen := make(map[string]bool, len(doc.Edges))
	edges := make([]edge.Edge, 0, len(doc.Edges))
	for i, definition := range doc.Edges {
		path := fmt.Sprintf("$.edges[%d]", i)
		if definition.From == "" || definition.To == "" {
			return nil, &Error{Path: path, Reason: "from and to must be non-empty node IDs"}
		}
		if !known[definition.From] {
			return nil, &Error{Path: path + ".from", Reason: fmt.Sprintf("references undeclared node %q", definition.From)}
		}
		if !known[definition.To] {
			return nil, &Error{Path: path + ".to", Reason: fmt.Sprintf("references undeclared node %q", definition.To)}
		}
		key := definition.From + "\x00" + definition.To
		if seen[key] {
			return nil, &Error{Path: path, Reason: fmt.Sprintf("duplicates edge %q -> %q", definition.From, definition.To)}
		}
		seen[key] = true
		edges = append(edges, edge.Edge{From: definition.From, To: definition.To})
	}
	return buildPlan(nodes, edges, doc.Entry)
}

// ExportAdjacencyMatrix encodes a Plan as a deterministic 0/1 matrix.
func ExportAdjacencyMatrix(p *coreplan.Plan, enc NodeEncoder) ([]byte, error) {
	doc, err := ToAdjacencyMatrix(p, enc)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// ToAdjacencyMatrix converts a Plan into its structured representation.
func ToAdjacencyMatrix(p *coreplan.Plan, enc NodeEncoder) (*AdjacencyMatrix, error) {
	if p == nil {
		return nil, &Error{Path: "$", Reason: "plan must not be nil"}
	}
	if enc == nil {
		return nil, &Error{Path: "$.nodes", Reason: "node encoder is required"}
	}
	if err := p.Validate(); err != nil {
		return nil, &Error{Path: "$", Reason: err.Error(), Cause: err}
	}
	order := p.AllNodes()
	sort.Strings(order)
	doc := &AdjacencyMatrix{Version: Version, Entry: p.Entry(), Order: append([]string(nil), order...), Nodes: make([]NodeDefinition, 0, len(order)), Matrix: make([][]int, len(order))}
	index := make(map[string]int, len(order))
	for i, id := range order {
		index[id] = i
		definition, err := enc.EncodeNode(p.GetNode(id))
		if err != nil {
			return nil, wrap(fmt.Sprintf("$.nodes[%d]", i), err)
		}
		if definition.ID == "" {
			definition.ID = id
		}
		if definition.ID != id {
			return nil, &Error{Path: fmt.Sprintf("$.nodes[%d].id", i), Reason: fmt.Sprintf("encoder returned ID %q, expected %q", definition.ID, id)}
		}
		doc.Nodes = append(doc.Nodes, definition)
		doc.Matrix[i] = make([]int, len(order))
	}
	for i, from := range order {
		for j, e := range p.GetEdgesFrom(from) {
			if e.Condition != nil {
				return nil, &Error{Path: fmt.Sprintf("$.matrix[%d][%d]", i, j), Reason: "conditional edges are not representable in adjacency-matrix format"}
			}
			doc.Matrix[i][index[e.To]] = 1
		}
	}
	return doc, nil
}

// ImportAdjacencyMatrix parses, validates, materializes, and seals a Plan.
func ImportAdjacencyMatrix(data []byte, dec NodeDecoder) (*coreplan.Plan, error) {
	var doc AdjacencyMatrix
	if err := decodeJSON(data, &doc); err != nil {
		return nil, err
	}
	return FromAdjacencyMatrix(&doc, dec)
}

// FromAdjacencyMatrix materializes a structured adjacency matrix document.
func FromAdjacencyMatrix(doc *AdjacencyMatrix, dec NodeDecoder) (*coreplan.Plan, error) {
	if doc == nil {
		return nil, &Error{Path: "$", Reason: "document must not be null"}
	}
	if len(doc.Order) != len(doc.Nodes) {
		return nil, &Error{Path: "$.order", Reason: "must contain one node ID for every nodes entry"}
	}
	if err := validateHeader(doc.Version, doc.Entry, doc.Nodes); err != nil {
		return nil, err
	}
	if dec == nil {
		return nil, &Error{Path: "$.nodes", Reason: "node decoder is required to materialize nodes"}
	}
	for i, id := range doc.Order {
		if id != doc.Nodes[i].ID {
			return nil, &Error{Path: fmt.Sprintf("$.order[%d]", i), Reason: fmt.Sprintf("must match nodes[%d].id %q", i, doc.Nodes[i].ID)}
		}
	}
	nodes, err := decodeNodes(doc.Nodes, dec)
	if err != nil {
		return nil, err
	}
	if len(doc.Matrix) != len(nodes) {
		return nil, &Error{Path: "$.matrix", Reason: "row count must equal nodes count"}
	}
	edges := make([]edge.Edge, 0)
	for i, row := range doc.Matrix {
		if len(row) != len(nodes) {
			return nil, &Error{Path: fmt.Sprintf("$.matrix[%d]", i), Reason: "column count must equal nodes count"}
		}
		for j, value := range row {
			if value != 0 && value != 1 {
				return nil, &Error{Path: fmt.Sprintf("$.matrix[%d][%d]", i, j), Reason: "must be 0 or 1"}
			}
			if value == 1 {
				edges = append(edges, edge.Edge{From: doc.Order[i], To: doc.Order[j]})
			}
		}
	}
	return buildPlan(nodes, edges, doc.Entry)
}

func validateHeader(version int, entry string, definitions []NodeDefinition) error {
	if version != Version {
		return &Error{Path: "$.version", Reason: fmt.Sprintf("must be %d, got %d", Version, version)}
	}
	if strings.TrimSpace(entry) == "" && len(definitions) > 0 {
		return &Error{Path: "$.entry", Reason: "must be a non-empty node ID when nodes are present"}
	}
	seen := make(map[string]int, len(definitions))
	for i, definition := range definitions {
		if strings.TrimSpace(definition.ID) == "" {
			return &Error{Path: fmt.Sprintf("$.nodes[%d].id", i), Reason: "must be a non-empty string"}
		}
		if previous, ok := seen[definition.ID]; ok {
			return &Error{Path: fmt.Sprintf("$.nodes[%d].id", i), Reason: fmt.Sprintf("duplicates node ID already declared at $.nodes[%d].id", previous)}
		}
		seen[definition.ID] = i
	}
	if entry != "" {
		if _, ok := seen[entry]; !ok {
			return &Error{Path: "$.entry", Reason: fmt.Sprintf("references undeclared node %q", entry)}
		}
	}
	return nil
}

func decodeNodes(definitions []NodeDefinition, dec NodeDecoder) ([]node.Node, error) {
	nodes := make([]node.Node, 0, len(definitions))
	for i, definition := range definitions {
		built, err := dec.DecodeNode(definition)
		if err != nil {
			return nil, wrap(fmt.Sprintf("$.nodes[%d]", i), err)
		}
		if built == nil {
			return nil, &Error{Path: fmt.Sprintf("$.nodes[%d]", i), Reason: "decoder returned a nil node"}
		}
		if built.ID() != definition.ID {
			return nil, &Error{Path: fmt.Sprintf("$.nodes[%d].id", i), Reason: fmt.Sprintf("decoder returned node ID %q", built.ID())}
		}
		nodes = append(nodes, built)
	}
	return nodes, nil
}

func buildPlan(nodes []node.Node, edges []edge.Edge, entry string) (*coreplan.Plan, error) {
	p, err := coreplan.Build(coreplan.Definition{Nodes: nodes, Edges: edges, Entry: entry})
	if err != nil {
		return nil, &Error{Path: "$", Reason: err.Error(), Cause: err}
	}
	return p, nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if syntax, ok := err.(*json.SyntaxError); ok {
			line, column := lineColumn(data, syntax.Offset)
			return &Error{Path: "$", Line: line, Column: column, Reason: syntax.Error(), Cause: err}
		}
		return &Error{Path: "$", Reason: err.Error(), Cause: err}
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return &Error{Path: "$", Reason: "multiple JSON values are not allowed"}
	}
	return nil
}

func wrap(path string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Path: path, Reason: err.Error(), Cause: err}
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
