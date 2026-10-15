package initprep

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	"dotm/internal/manifest"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// PlanChezmoi prepares the catalog membership needed to acquire chezmoi through
// the selected configuration. It never asks approval for these additive edits.
func PlanChezmoi(ctx context.Context, dataDir, configuration string) (*Preparation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dataDir == "" {
		selection, err := manifest.SelectionPath()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(filepath.Dir(selection), "data")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	p := &Preparation{Configuration: configuration}
	selectionPath, err := manifest.SelectionPath()
	if err != nil {
		return nil, err
	}
	paths := []string{
		filepath.Join(dataDir, "catalog.toml"),
		filepath.Join(dataDir, "configurations.toml"),
		filepath.Join(filepath.Dir(selectionPath), "files.toml"),
		selectionPath,
	}
	for _, path := range paths {
		s, inspectErr := inspect(path)
		if inspectErr != nil {
			return nil, inspectErr
		}
		p.inputs = append(p.inputs, s)
	}
	catalog, configs := p.inputs[0].content, p.inputs[1].content
	data, err := manifest.LoadContents(dataDir, catalog, configs)
	if err != nil {
		return nil, err
	}
	config, ok := data.Configs[configuration]
	if !ok {
		return nil, fmt.Errorf("unknown configuration %q", configuration)
	}
	if _, exists := data.Apps["chezmoi"]; !exists {
		catalog = append(append([]byte(nil), catalog...), separator(catalog)...)
		catalog = append(catalog, "[chezmoi]\nnix = [\"chezmoi\"]\n"...)
	}
	resolved, err := data.Resolve(config.Apps)
	if err != nil {
		return nil, err
	}
	included := false
	for _, app := range resolved.Applications {
		if app.ID == "chezmoi" {
			included = true
			break
		}
	}
	if !included {
		configs, err = addConfigurationApp(configs, configuration)
		if err != nil {
			return nil, err
		}
	}
	data, err = manifest.LoadContents(dataDir, catalog, configs)
	if err != nil {
		return nil, err
	}
	if err := validateFiles(data, p.inputs[2]); err != nil {
		return nil, err
	}
	for i, after := range [][]byte{catalog, configs} {
		input := p.inputs[i]
		if !input.exists() || !bytes.Equal(input.content, after) {
			p.effects = append(p.effects, Effect{
				Path: input.path, Before: string(input.content), After: string(after), Created: !input.exists(),
			})
		}
	}
	return p, ctx.Err()
}

func separator(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return []byte("\n\n")
	}
	if len(b) > 0 {
		return []byte("\n")
	}
	return nil
}

// Adds membership without reformatting any unrelated TOML.
func addConfigurationApp(content []byte, name string) ([]byte, error) {
	var doc struct {
		Configurations []struct {
			Name string `toml:"name"`
		} `toml:"configurations"`
	}
	if err := toml.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	selected := -1
	for i, config := range doc.Configurations {
		if config.Name == name {
			selected = i
			break
		}
	}
	if selected < 0 {
		return nil, fmt.Errorf("cannot locate configuration %q", name)
	}
	var parser unstable.Parser
	parser.Reset(content)
	row, active := -1, false
	var tableEnd uint32
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind == unstable.ArrayTable && nodeKey(n) == "configurations" {
			row++
			active = row == selected
			if active {
				tableEnd = keyEnd(n)
			}
			continue
		}
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			active = false
		}
		if n.Kind == unstable.KeyValue && nodeKey(n) == "configurations" && n.Value().Kind == unstable.Array {
			children := n.Value().Children()
			for i := 0; children.Next(); i++ {
				if i == selected {
					return addInlineApp(content, children.Node())
				}
			}
		}
		if active && n.Kind == unstable.KeyValue && nodeKey(n) == "apps" && n.Value().Kind == unstable.Array {
			return appendArray(content, n)
		}
	}
	if parser.Error() != nil {
		return nil, parser.Error()
	}
	if tableEnd == 0 {
		return nil, fmt.Errorf("cannot safely extend configuration %q", name)
	}
	lineEnd := bytes.IndexByte(content[tableEnd:], '\n')
	if lineEnd < 0 {
		return insert(content, len(content), "\napps = [\"chezmoi\"]\n"), nil
	}
	pos := int(tableEnd) + lineEnd + 1
	return insert(content, pos, "apps = [\"chezmoi\"]\n"), nil
}

// Composite AST nodes have no Raw range. Anchor edits to their scalar keys and
// string elements instead of treating a zero composite range as a file offset.
func keyEnd(node *unstable.Node) uint32 {
	var end uint32
	keys := node.Key()
	for keys.Next() {
		key := keys.Node()
		end = key.Raw.Offset + key.Raw.Length
	}
	return end
}

func appendArray(content []byte, field *unstable.Node) ([]byte, error) {
	children := field.Value().Children()
	var lastEnd uint32
	for children.Next() {
		node := children.Node()
		lastEnd = node.Raw.Offset + node.Raw.Length
	}
	if lastEnd != 0 {
		return insert(content, int(lastEnd), `, "chezmoi"`), nil
	}
	start := keyEnd(field)
	opening := bytes.IndexByte(content[start:], '[')
	if opening < 0 {
		return nil, fmt.Errorf("cannot locate configuration application array")
	}
	return insert(content, int(start)+opening+1, `"chezmoi"`), nil
}

func addInlineApp(content []byte, table *unstable.Node) ([]byte, error) {
	if table.Kind != unstable.InlineTable {
		return nil, fmt.Errorf("cannot safely extend inline configuration")
	}
	children := table.Children()
	var firstKey uint32
	for children.Next() {
		node := children.Node()
		if node.Kind != unstable.KeyValue {
			continue
		}
		if firstKey == 0 {
			keys := node.Key()
			keys.Next()
			firstKey = keys.Node().Raw.Offset
		}
		if nodeKey(node) == "apps" && node.Value().Kind == unstable.Array {
			return appendArray(content, node)
		}
	}
	if firstKey == 0 {
		return nil, fmt.Errorf("cannot locate named inline configuration")
	}
	return insert(content, int(firstKey), `apps = ["chezmoi"], `), nil
}

// Insertion offsets can include document boundaries beyond the AST's uint32 ranges.
func insert(content []byte, offset int, value string) []byte {
	updated := make([]byte, 0, len(content)+len(value))
	updated = append(updated, content[:offset]...)
	updated = append(updated, value...)
	return append(updated, content[offset:]...)
}
