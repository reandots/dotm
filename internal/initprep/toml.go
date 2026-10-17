package initprep

import (
	"bytes"
	"fmt"
	"strings"

	"dotm/internal/manifest"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func quote(value string) string {
	encoded, _ := toml.Marshal(struct {
		Value string `toml:"value"`
	}{value})
	return strings.TrimSpace(strings.TrimPrefix(string(encoded), "value = "))
}

func nodeKey(node *unstable.Node) string {
	var parts []string
	keys := node.Key()
	for keys.Next() {
		parts = append(parts, string(keys.Node().Data))
	}
	return strings.Join(parts, ".")
}

// stringRange finds a semantic scalar through dotted keys and inline tables.
func stringRange(node *unstable.Node, prefix, wanted string) (unstable.Range, bool) {
	if node.Kind == unstable.InlineTable {
		children := node.Children()
		for children.Next() {
			if found, ok := stringRange(children.Node(), prefix, wanted); ok {
				return found, true
			}
		}
	}
	if node.Kind != unstable.KeyValue {
		return unstable.Range{}, false
	}
	key := nodeKey(node)
	if prefix != "" {
		key = prefix + "." + key
	}
	value := node.Value()
	if key == wanted && value.Kind == unstable.String {
		return value.Raw, true
	}
	return stringRange(value, key, wanted)
}

func replace(content []byte, at unstable.Range, value string) []byte {
	end := int(at.Offset + at.Length)
	updated := make([]byte, 0, len(content)-int(at.Length)+len(value))
	updated = append(updated, content[:at.Offset]...)
	updated = append(updated, value...)
	return append(updated, content[end:]...)
}

func editSelection(content []byte, name string) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(content)
	for parser.NextExpression() {
		if at, ok := stringRange(parser.Expression(), "", "configuration"); ok {
			return replace(content, at, quote(name)), nil
		}
	}
	return nil, fmt.Errorf("cannot locate validated local configuration field: %w", parserError(&parser))
}

func parserError(parser *unstable.Parser) error {
	if err := parser.Error(); err != nil {
		return err
	}
	return fmt.Errorf("field is absent")
}

func editSource(content []byte, name, repository string) ([]byte, error) {
	var document struct {
		Configurations []manifest.Configuration `toml:"configurations"`
	}
	if err := toml.Unmarshal(content, &document); err != nil {
		return nil, err
	}
	selected := -1
	backend := ""
	for i, config := range document.Configurations {
		if config.Name == name {
			selected = i
			backend = config.Backend
			break
		}
	}
	var parser unstable.Parser
	parser.Reset(content)
	index, insert := -1, -1
	section := ""
	active := false
	fields := [2]string{"backend", "repository"}
	values := [2]string{"chezmoi", repository}
	var ranges [2]unstable.Range
	var found [2]bool
	inline := false
	capture := func(node *unstable.Node, prefix string) {
		for i, field := range fields {
			if at, ok := stringRange(node, prefix, field); ok {
				ranges[i], found[i] = at, true
			}
		}
	}
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.ArrayTable, unstable.Table:
			key := nodeKey(node)
			if node.Kind == unstable.ArrayTable && key == "configurations" {
				index++
				keys := node.Key()
				keys.Next()
				start := int(keys.Node().Raw.Offset)
				if index == selected {
					if newline := bytes.IndexByte(content[start:], '\n'); newline >= 0 {
						insert = start + newline + 1
					} else {
						insert = len(content)
					}
				}
			}
			active = index == selected && (key == "configurations" || strings.HasPrefix(key, "configurations."))
			section = strings.TrimPrefix(key, "configurations.")
			if key == "configurations" {
				section = ""
			}
		case unstable.KeyValue:
			if nodeKey(node) == "configurations" && node.Value().Kind == unstable.Array {
				children := node.Value().Children()
				for i := 0; children.Next(); i++ {
					if i != selected {
						continue
					}
					config := children.Node()
					capture(config, "")
					insert, inline = int(config.Raw.Offset)+1, true
				}
			}
			if active {
				capture(node, section)
			}
		}
	}
	if parser.Error() != nil {
		return nil, parser.Error()
	}
	if insert < 0 {
		return nil, fmt.Errorf("cannot locate validated source settings for configuration %q", name)
	}
	// Replace later scalars first so earlier offsets remain valid.
	order := [2]int{0, 1}
	if ranges[0].Offset < ranges[1].Offset {
		order = [2]int{1, 0}
	}
	for _, i := range order {
		if found[i] && (i != 0 || backend != "chezmoi") {
			content = replace(content, ranges[i], quote(values[i]))
		}
	}
	addition := ""
	for i, field := range fields {
		if !found[i] {
			addition += field + " = " + quote(values[i])
			if inline {
				addition += ", "
			} else {
				addition += "\n"
			}
		}
	}
	if addition != "" {
		if !inline && insert == len(content) && content[insert-1] != '\n' {
			addition = "\n" + addition
		}
		content = replace(content, unstable.Range{Offset: uint32(insert)}, addition)
	}
	return content, nil
}
