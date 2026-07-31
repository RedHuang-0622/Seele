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

// NodeSpec is the product-facing minimum node unit. Input is generic so a
// product can use a string, a typed struct, or a JSON-compatible value without
// changing the Plan kernel.
type NodeSpec[T any] struct {
	ID       string         `json:"id"`
	Input    T              `json:"input"`
	Kind     string         `json:"kind,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EdgeSpec is the product-facing minimum edge unit. Conditions and execution
// behavior stay in the materialized Node/Plan implementation.
type EdgeSpec struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Document is the canonical product-facing nodes + edges DSL document.
// Product meaning of Kind and Input belongs to the supplied NodeFactory.
type Document[T any] struct {
	Version int           `json:"version"`
	Entry   string        `json:"entry"`
	Nodes   []NodeSpec[T] `json:"nodes"`
	Edges   []EdgeSpec    `json:"edges"`
}

// NodeFactory materializes a product node specification into the minimal
// executable Node interface.
type NodeFactory[T any] interface {
	BuildNode(NodeSpec[T]) (node.Node, error)
}

// NodeFactoryFunc adapts a function to NodeFactory.
type NodeFactoryFunc[T any] func(NodeSpec[T]) (node.Node, error)

func (f NodeFactoryFunc[T]) BuildNode(spec NodeSpec[T]) (node.Node, error) {
	if f == nil {
		return nil, fmt.Errorf("node factory is nil")
	}
	return f(spec)
}

// NodeEncoderFunc encodes a runtime node into a product-facing node spec.
type NodeEncoderFunc[T any] func(node.Node) (NodeSpec[T], error)

func (f NodeEncoderFunc[T]) EncodeNode(n node.Node) (NodeSpec[T], error) {
	if f == nil {
		return NodeSpec[T]{}, fmt.Errorf("node encoder is nil")
	}
	return f(n)
}

// Import parses and renders the canonical product-facing document.
func Import[T any](data []byte, factory NodeFactory[T]) (*coreplan.Plan, error) {
	var document Document[T]
	if err := decodeDocument(data, &document); err != nil {
		return nil, err
	}
	return Render(document, factory)
}

// Render validates a document, materializes its nodes, and seals a Plan.
func Render[T any](document Document[T], factory NodeFactory[T]) (*coreplan.Plan, error) {
	if factory == nil {
		return nil, seeleerrors.Wrap(fmt.Errorf("node factory is required"), seeleerrors.Context{
			Code: "workplan.codec.factory", Struct: "NodeFactory", Function: "Render", Step: "factory", Path: "$.nodes",
		})
	}
	if err := validateDocument(document); err != nil {
		return nil, err
	}

	nodes := make([]node.Node, 0, len(document.Nodes))
	for index, spec := range document.Nodes {
		built, err := factory.BuildNode(spec)
		if err != nil {
			return nil, seeleerrors.Wrap(err, seeleerrors.Context{
				Code: "workplan.codec.node", Struct: "NodeSpec", Function: "BuildNode",
				Step: fmt.Sprintf("nodes[%d]", index), Path: fmt.Sprintf("$.nodes[%d]", index), Raw: spec,
			})
		}
		if built == nil {
			return nil, seeleerrors.Wrap(fmt.Errorf("node factory returned nil for %q", spec.ID), seeleerrors.Context{
				Code: "workplan.codec.node", Struct: "NodeSpec", Function: "BuildNode",
				Step: fmt.Sprintf("nodes[%d]", index), Path: fmt.Sprintf("$.nodes[%d]", index), Raw: spec,
			})
		}
		if built.ID() != spec.ID {
			return nil, seeleerrors.Wrap(fmt.Errorf("factory returned node ID %q", built.ID()), seeleerrors.Context{
				Code: "workplan.codec.node", Struct: "NodeSpec", Function: "BuildNode",
				Step: fmt.Sprintf("nodes[%d].id", index), Path: fmt.Sprintf("$.nodes[%d].id", index), Raw: spec,
			})
		}
		nodes = append(nodes, built)
	}

	edges := make([]edge.Edge, 0, len(document.Edges))
	seen := make(map[string]struct{}, len(document.Edges))
	for index, definition := range document.Edges {
		key := definition.From + "\x00" + definition.To
		if _, exists := seen[key]; exists {
			return nil, seeleerrors.Wrap(fmt.Errorf("duplicate edge %q -> %q", definition.From, definition.To), seeleerrors.Context{
				Code: "workplan.codec.edge", Struct: "EdgeSpec", Function: "Render", Step: fmt.Sprintf("edges[%d]", index), Path: fmt.Sprintf("$.edges[%d]", index), Raw: definition,
			})
		}
		seen[key] = struct{}{}
		edges = append(edges, edge.Edge{From: definition.From, To: definition.To})
	}

	plan, err := coreplan.Build(coreplan.Definition{Nodes: nodes, Edges: edges, Entry: document.Entry})
	if err != nil {
		return nil, seeleerrors.Wrap(err, seeleerrors.Context{
			Code: "workplan.codec.plan", Struct: "coreplan.Plan", Function: "Build", Step: "validate", Raw: document,
		})
	}
	return plan, nil
}

// ExportDocument converts a sealed Plan into the canonical product DSL.
func ExportDocument[T any](plan *coreplan.Plan, encoder interface {
	EncodeNode(node.Node) (NodeSpec[T], error)
}) ([]byte, error) {
	document, err := ToDocument(plan, encoder)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(document, "", "  ")
}

// ToDocument converts a Plan into the canonical product DSL representation.
func ToDocument[T any](plan *coreplan.Plan, encoder interface {
	EncodeNode(node.Node) (NodeSpec[T], error)
}) (Document[T], error) {
	if plan == nil {
		return Document[T]{}, seeleerrors.New("workplan.codec.plan", "plan must not be nil")
	}
	if encoder == nil {
		return Document[T]{}, seeleerrors.New("workplan.codec.encoder", "node encoder is required")
	}
	if err := plan.Validate(); err != nil {
		return Document[T]{}, seeleerrors.Wrap(err, seeleerrors.Context{
			Code: "workplan.codec.plan", Struct: "coreplan.Plan", Function: "Validate", Step: "export",
		})
	}

	ids := plan.AllNodes()
	sort.Strings(ids)
	document := Document[T]{Version: Version, Entry: plan.Entry(), Nodes: make([]NodeSpec[T], 0, len(ids))}
	for index, id := range ids {
		spec, err := encoder.EncodeNode(plan.GetNode(id))
		if err != nil {
			return Document[T]{}, seeleerrors.Wrap(err, seeleerrors.Context{
				Code: "workplan.codec.node", Struct: "NodeSpec", Function: "EncodeNode",
				Step: fmt.Sprintf("nodes[%d]", index), Path: fmt.Sprintf("$.nodes[%d]", index), Raw: id,
			})
		}
		if spec.ID == "" {
			spec.ID = id
		}
		if spec.ID != id {
			return Document[T]{}, seeleerrors.New("workplan.codec.node", fmt.Sprintf("encoder returned ID %q, expected %q", spec.ID, id))
		}
		document.Nodes = append(document.Nodes, spec)
	}
	for _, from := range ids {
		for index, candidate := range plan.GetEdgesFrom(from) {
			if candidate.Condition != nil {
				return Document[T]{}, seeleerrors.Wrap(fmt.Errorf("conditional edges are not representable in product DSL"), seeleerrors.Context{
					Code: "workplan.codec.edge", Struct: "EdgeSpec", Function: "Encode", Step: fmt.Sprintf("edges[%d]", index), Path: "$.edges",
				})
			}
			document.Edges = append(document.Edges, EdgeSpec{From: candidate.From, To: candidate.To})
		}
	}
	sort.Slice(document.Edges, func(i, j int) bool {
		if document.Edges[i].From == document.Edges[j].From {
			return document.Edges[i].To < document.Edges[j].To
		}
		return document.Edges[i].From < document.Edges[j].From
	})
	return document, nil
}

func decodeDocument[T any](data []byte, target *Document[T]) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if syntax, ok := err.(*json.SyntaxError); ok {
			line, column := lineColumn(data, syntax.Offset)
			return &seeleerrors.Error{Code: "workplan.codec.json", Message: syntax.Error(), Path: "$", Line: line, Column: column, Cause: err}
		}
		return seeleerrors.Wrap(err, seeleerrors.Context{Code: "workplan.codec.json", Struct: "Document", Function: "Decode", Step: "json", Path: "$", Raw: string(data)})
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return seeleerrors.New("workplan.codec.json", "multiple JSON values are not allowed")
	}
	return nil
}

func validateDocument[T any](document Document[T]) error {
	if document.Version != Version {
		return seeleerrors.Wrap(fmt.Errorf("version must be %d, got %d", Version, document.Version), seeleerrors.Context{
			Code: "workplan.codec.version", Struct: "Document", Function: "Validate", Step: "version", Path: "$.version", Raw: document.Version,
		})
	}
	if strings.TrimSpace(document.Entry) == "" && len(document.Nodes) > 0 {
		return seeleerrors.Wrap(fmt.Errorf("entry must be a non-empty node ID when nodes are present"), seeleerrors.Context{
			Code: "workplan.codec.entry", Struct: "Document", Function: "Validate", Step: "entry", Path: "$.entry", Raw: document.Entry,
		})
	}
	known := make(map[string]struct{}, len(document.Nodes))
	for index, spec := range document.Nodes {
		if strings.TrimSpace(spec.ID) == "" {
			return seeleerrors.Wrap(fmt.Errorf("node ID must not be empty"), seeleerrors.Context{Code: "workplan.codec.node", Struct: "NodeSpec", Function: "Validate", Step: fmt.Sprintf("nodes[%d].id", index), Path: fmt.Sprintf("$.nodes[%d].id", index), Raw: spec})
		}
		if _, exists := known[spec.ID]; exists {
			return seeleerrors.Wrap(fmt.Errorf("duplicate node ID %q", spec.ID), seeleerrors.Context{Code: "workplan.codec.node", Struct: "NodeSpec", Function: "Validate", Step: fmt.Sprintf("nodes[%d].id", index), Path: fmt.Sprintf("$.nodes[%d].id", index), Raw: spec})
		}
		known[spec.ID] = struct{}{}
	}
	if document.Entry != "" {
		if _, exists := known[document.Entry]; !exists {
			return seeleerrors.Wrap(fmt.Errorf("entry references undeclared node %q", document.Entry), seeleerrors.Context{
				Code: "workplan.codec.entry", Struct: "Document", Function: "Validate", Step: "entry", Path: "$.entry", Raw: document.Entry,
			})
		}
	}
	seen := make(map[string]struct{}, len(document.Edges))
	for index, definition := range document.Edges {
		path := fmt.Sprintf("$.edges[%d]", index)
		if strings.TrimSpace(definition.From) == "" || strings.TrimSpace(definition.To) == "" {
			return seeleerrors.Wrap(fmt.Errorf("from and to must be non-empty node IDs"), seeleerrors.Context{Code: "workplan.codec.edge", Struct: "EdgeSpec", Function: "Validate", Step: fmt.Sprintf("edges[%d]", index), Path: path, Raw: definition})
		}
		if _, exists := known[definition.From]; !exists {
			return seeleerrors.Wrap(fmt.Errorf("edge from references undeclared node %q", definition.From), seeleerrors.Context{
				Code: "workplan.codec.edge", Struct: "EdgeSpec", Function: "Validate", Step: fmt.Sprintf("edges[%d].from", index), Path: path + ".from", Raw: definition,
			})
		}
		if _, exists := known[definition.To]; !exists {
			return seeleerrors.Wrap(fmt.Errorf("edge to references undeclared node %q", definition.To), seeleerrors.Context{
				Code: "workplan.codec.edge", Struct: "EdgeSpec", Function: "Validate", Step: fmt.Sprintf("edges[%d].to", index), Path: path + ".to", Raw: definition,
			})
		}
		key := definition.From + "\x00" + definition.To
		if _, exists := seen[key]; exists {
			return seeleerrors.Wrap(fmt.Errorf("duplicate edge %q -> %q", definition.From, definition.To), seeleerrors.Context{
				Code: "workplan.codec.edge", Struct: "EdgeSpec", Function: "Validate", Step: fmt.Sprintf("edges[%d]", index), Path: path, Raw: definition,
			})
		}
		seen[key] = struct{}{}
	}
	return nil
}
