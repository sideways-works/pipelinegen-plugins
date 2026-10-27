package graph

import (
	"slices"

	"github.com/sideways-works/pipelinegen-sbom-go/goid"
)

// Step is one unit on the path Why finds, with how it was reached.
type Step struct {
	Unit string
	Note string
}

// Why finds the shortest path from one unit to another over what the
// declarations use: references, a type's methods, and the implementations
// an interface call may reach. A dep:go unit is reached through a package
// that links its module. It returns nil when to can't be reached.
func (t *Tree) Why(from, to string) []Step {
	start, ok := t.byID[goid.ID(from)]
	if !ok {
		return nil
	}

	module, toModule := goid.DepPath(to)
	target, toDecl := t.byID[goid.ID(to)]

	if !toModule && !toDecl {
		return nil
	}

	type hop struct {
		prev int32
		note string
	}

	seen := map[int32]hop{start: {prev: -1}}
	queue := []int32{start}

	path := func(end int32) []Step {
		var out []Step

		for i := end; i >= 0; i = seen[i].prev {
			out = append(out, Step{Unit: string(t.Decls[i].ID), Note: seen[i].note})
		}

		slices.Reverse(out)

		return out
	}

	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]

		if toDecl && i == target {
			return path(i)
		}

		if toModule {
			if p := t.pkgs[t.Decls[i].ID.Dir()]; p != nil && (slices.Contains(p.Deps, module) || slices.Contains(p.TestDeps, module)) {
				return append(path(i), Step{Unit: to, Note: "its package links " + module})
			}
		}

		next := func(js []int32, note string) {
			for _, j := range js {
				if _, ok := seen[j]; !ok {
					seen[j] = hop{prev: i, note: note}
					queue = append(queue, j)
				}
			}
		}

		next(t.refs[i], "referenced")
		next(t.methods[i], "a method of the type")
		next(t.impls[i], "may run through an interface call")
	}

	return nil
}
