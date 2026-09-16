package manifest

import (
	"fmt"
	"sort"
	"strings"
)

type Resolved struct {
	// Applications are in dependency-first order; root and dependency declaration order is preserved.
	Applications []Application
	Packages     []Package
	CatalogDir   string
}

func (d *Data) Resolve(roots []string) (Resolved, error) {
	result := Resolved{CatalogDir: d.CatalogDir}
	states := make(map[string]uint8)
	packages := make(map[string]bool)
	var stack []string
	var visit func(string) error
	visit = func(id string) error {
		if states[id] == 2 {
			return nil
		}
		if states[id] == 1 {
			start := 0
			for stack[start] != id {
				start++
			}
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(append([]string(nil), stack[start:]...), id), " -> "))
		}
		app, ok := d.Apps[id]
		if !ok {
			return fmt.Errorf("unknown application %q", id)
		}
		states[id] = 1
		stack = append(stack, id)
		for _, dep := range app.Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		states[id] = 2
		result.Applications = append(result.Applications, app)
		for _, p := range app.Packages {
			if !packages[p.Key()] {
				packages[p.Key()] = true
				result.Packages = append(result.Packages, p)
			}
		}
		return nil
	}
	for _, id := range roots {
		if err := visit(id); err != nil {
			return Resolved{}, err
		}
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Key() < result.Packages[j].Key() })
	return result, nil
}
