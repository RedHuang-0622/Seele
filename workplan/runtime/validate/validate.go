// Package validate provides graph validation: DAG checks, cycle detection, orphan detection.
package validate

import (
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// Graph validates the entire graph: entry node, edge references, cycles, orphans.
func Graph(g *graph.Graph) error {
	if err := EntryNode(g); err != nil {
		return err
	}
	if err := EdgeReferences(g); err != nil {
		return err
	}
	if err := Cyclic(g); err != nil {
		return err
	}
	if err := Orphan(g); err != nil {
		return err
	}
	return nil
}

// EntryNode checks that the entry node exists.
func EntryNode(g *graph.Graph) error {
	entry := g.Entry()
	if entry == "" {
		return nil // graph with no entry is valid (empty graph)
	}
	if g.GetNode(entry) == nil {
		return fmt.Errorf("entry node %q not found", entry)
	}
	return nil
}

// EdgeReferences checks that all edge targets refer to existing nodes.
func EdgeReferences(g *graph.Graph) error {
	nodes := g.AllNodes()
	nodeSet := make(map[string]bool, len(nodes))
	for _, id := range nodes {
		nodeSet[id] = true
	}
	for _, e := range g.AllEdges() {
		if _, ok := nodeSet[e.To]; !ok {
			return fmt.Errorf("edge %q -> %q: target node %q not found", e.From, e.To, e.To)
		}
	}
	return nil
}

// Cyclic checks for cycles using DFS.
func Cyclic(g *graph.Graph) error {
	nodes := g.AllNodes()
	edges := g.AllEdges()
	adj := make(map[string][]string)
	for _, id := range nodes {
		adj[id] = nil
	}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	white := make(map[string]bool) // unvisited
	gray := make(map[string]bool)  // in current path
	black := make(map[string]bool) // fully processed

	for _, id := range nodes {
		white[id] = true
	}

	var dfs func(id string) error
	dfs = func(id string) error {
		white[id] = false
		gray[id] = true
		for _, next := range adj[id] {
			if gray[next] {
				return fmt.Errorf("cycle detected: %q -> %q", id, next)
			}
			if white[next] {
				if err := dfs(next); err != nil {
					return err
				}
			}
		}
		gray[id] = false
		black[id] = true
		return nil
	}

	for _, id := range nodes {
		if white[id] {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Orphan checks for nodes that are not reachable from the entry.
func Orphan(g *graph.Graph) error {
	entry := g.Entry()
	if entry == "" {
		return nil
	}
	edges := g.AllEdges()

	hasIncoming := make(map[string]bool)
	for _, e := range edges {
		hasIncoming[e.To] = true
	}

	var orphans []string
	for _, id := range g.AllNodes() {
		if id == entry {
			continue
		}
		if !hasIncoming[id] {
			orphans = append(orphans, id)
		}
	}
	if len(orphans) > 0 {
		return fmt.Errorf("orphan nodes (no incoming edges): %v", orphans)
	}
	return nil
}
