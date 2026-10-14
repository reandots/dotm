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
	var hasSource bool
	for i, config := range document.Configurations {
		if config.Name == name {
			selected, hasSource = i, config.Configs != nil
			break
		}
	}
	var parser unstable.Parser
	parser.Reset(content)
	index, insert := -1, -1
	section := ""
	active := false
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
					if at, ok := stringRange(config, "", "configs.repository"); ok {
						return replace(content, at, quote(repository)), nil
					}
					if !hasSource {
						return replace(content, unstable.Range{Offset: config.Raw.Offset + 1},
							"configs = {backend = 'chezmoi', repository = "+quote(repository)+"}, "), nil
					}
				}
			}
			if active {
				if at, ok := stringRange(node, section, "configs.repository"); ok {
					return replace(content, at, quote(repository)), nil
				}
			}
		}
	}
	if parser.Error() != nil {
		return nil, parser.Error()
	}
	if !hasSource && insert >= 0 {
		prefix := ""
		if insert == len(content) && (insert == 0 || content[insert-1] != '\n') {
			prefix = "\n"
		}
		return replace(content, unstable.Range{Offset: uint32(insert)},
			prefix+"configs = {backend = 'chezmoi', repository = "+quote(repository)+"}\n"), nil
	}
	return nil, fmt.Errorf("cannot locate validated source settings for configuration %q", name)
}
