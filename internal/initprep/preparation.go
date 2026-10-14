// Package initprep plans and publishes dotm setup files without acquiring a backend.
// The caller must hold mutation ownership and refuse pending package recovery before Apply.
package initprep

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dotm/internal/chezmoi"
	"dotm/internal/manifest"

	"github.com/pelletier/go-toml/v2"
)

type Options struct {
	DataDir       string
	Configuration string
	Repository    string
}

// Effect describes full proposed or successfully published file content.
type Effect struct {
	Path    string
	Before  string
	After   string
	Created bool
}

type Conflict struct {
	Path   string
	Field  string
	Before string
	After  string
}

// Confirm must explicitly approve each local-intent change; nil never approves.
type Confirm func(context.Context, Conflict) (bool, error)

type Result struct {
	Completed []Effect
}

// Preparation retains private input snapshots; returned effect slices are copies.
// Configuration and Repository describe the resolved values, not backend success.
type Preparation struct {
	Configuration  string
	Repository     string
	inputs         []snapshot
	effects        []Effect
	conflicts      []Conflict
	inferredSource string
}

func (p *Preparation) Effects() []Effect     { return append([]Effect(nil), p.effects...) }
func (p *Preparation) Conflicts() []Conflict { return append([]Conflict(nil), p.conflicts...) }

// Plan performs only local reads, including passive Git inspection when inference is needed.
func Plan(ctx context.Context, options Options) (*Preparation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selectionPath, err := manifest.SelectionPath()
	if err != nil {
		return nil, err
	}
	dataDir := options.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(selectionPath), "data")
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	paths := []string{
		filepath.Join(dataDir, "catalog.toml"), filepath.Join(dataDir, "configurations.toml"),
		selectionPath, filepath.Join(filepath.Dir(selectionPath), "files.toml"),
	}
	p := &Preparation{}
	for _, path := range paths {
		input, inspectErr := inspect(path)
		if inspectErr != nil {
			return nil, inspectErr
		}
		p.inputs = append(p.inputs, input)
	}
	catalog, configurations := p.inputs[0].content, p.inputs[1].content
	data, err := manifest.LoadContents(dataDir, catalog, configurations)
	if err != nil {
		return nil, err
	}
	for _, config := range data.Configs {
		if config.Configs != nil && strings.ContainsAny(config.Configs.Repository, "\x00\r\n") {
			return nil, fmt.Errorf("%s: configuration %q: invalid source repository", paths[1], config.Name)
		}
	}
	var local struct {
		Configuration string `toml:"configuration"`
	}
	if p.inputs[2].exists() {
		local.Configuration, err = manifest.SelectionContents(p.inputs[2].path, p.inputs[2].content)
		if err != nil {
			return nil, err
		}
		if p.inputs[1].exists() {
			if _, ok := data.Configs[local.Configuration]; !ok {
				return nil, fmt.Errorf("%s: unknown configuration %q", selectionPath, local.Configuration)
			}
		}
	}
	// Validate declarations before any missing-input resolution can mask damaged local data.
	if filesErr := validateFiles(data, p.inputs[3]); filesErr != nil {
		return nil, filesErr
	}
	name := options.Configuration
	if name == "" {
		name = local.Configuration
	}
	if name == "" && len(data.Configs) == 1 {
		for candidate := range data.Configs {
			name = candidate
		}
	}
	if name == "" {
		return nil, fmt.Errorf("cannot resolve configuration; specify --configuration")
	}
	config, found := data.Configs[name]
	if p.inputs[1].exists() && !found {
		return nil, fmt.Errorf("unknown configuration %q; select an existing --configuration", name)
	}
	repository := options.Repository
	if repository == "" && config.Configs != nil {
		repository = config.Configs.Repository
	}
	if repository == "" {
		source, sourceErr := chezmoi.SourcePath()
		if sourceErr != nil {
			return nil, sourceErr
		}
		if _, statErr := os.Lstat(source); statErr == nil {
			repository, err = chezmoi.InferRepository(ctx, source)
			p.inferredSource = source
			if err != nil {
				return nil, fmt.Errorf("infer repository: %w", err)
			}
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("inspect source %s: %w", source, statErr)
		} else if parentErr := checkParents(source); parentErr != nil {
			return nil, parentErr
		}
	}
	if strings.TrimSpace(repository) == "" {
		return nil, fmt.Errorf("cannot resolve repository; specify --repository")
	}
	if strings.ContainsAny(repository, "\x00\r\n") {
		return nil, fmt.Errorf("invalid --repository: control characters are not supported")
	}
	p.Configuration, p.Repository = name, repository
	if !p.inputs[1].exists() {
		configurations, err = toml.Marshal(struct {
			Configurations []manifest.Configuration `toml:"configurations"`
		}{[]manifest.Configuration{{
			Name: name, Apps: []string{},
			Configs: &manifest.ConfigurationSource{Backend: "chezmoi", Repository: repository},
		}}})
	} else if config.Configs == nil || config.Configs.Repository != repository {
		configurations, err = editSource(configurations, name, repository)
		before := ""
		if config.Configs != nil {
			before = config.Configs.Repository
		}
		p.conflicts = append(p.conflicts, Conflict{
			Path: paths[1], Field: "configs.repository", Before: before, After: repository,
		})
	}
	if err != nil {
		return nil, err
	}
	selection := p.inputs[2].content
	if !p.inputs[2].exists() {
		selection = []byte("configuration = " + quote(name) + "\n")
	} else if local.Configuration != name {
		selection, err = editSelection(selection, name)
		if err != nil {
			return nil, err
		}
		p.conflicts = append(p.conflicts, Conflict{
			Path: selectionPath, Field: "configuration", Before: local.Configuration, After: name,
		})
	}
	// The final combined model uses the ordinary validators, including every configuration.
	data, err = manifest.LoadContents(dataDir, catalog, configurations)
	if err != nil {
		return nil, err
	}
	if err := validateFiles(data, p.inputs[3]); err != nil {
		return nil, err
	}
	contents := [][]byte{catalog, configurations, selection, p.inputs[3].content}
	for i, input := range p.inputs {
		if !input.exists() || !bytes.Equal(input.content, contents[i]) {
			p.effects = append(p.effects, Effect{
				Path: input.path, Before: string(input.content), After: string(contents[i]), Created: !input.exists(),
			})
		}
	}
	return p, ctx.Err()
}

func validateFiles(data *manifest.Data, input snapshot) error {
	// Check every configuration: local declarations must not conceal conflicts in an unselected one.
	if len(data.Configs) == 0 {
		model, err := data.FilesContents(nil, input.path, input.content)
		if err != nil {
			return err
		}
		return model.Err()
	}
	for _, config := range data.Configs {
		model, err := data.FilesContents(config.Apps, input.path, input.content)
		if err != nil {
			return err
		}
		if err := model.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Apply collects all approvals before the first write and rechecks the complete input set.
// It is not a multi-file transaction: Completed remains accurate on later failure.
func (p *Preparation) Apply(ctx context.Context, confirm Confirm) (Result, error) {
	var result Result
	if err := ctx.Err(); err != nil {
		return result, err
	}
	for _, conflict := range p.conflicts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if confirm == nil {
			return result, fmt.Errorf("approval required for %s: %s", conflict.Path, conflict.Field)
		}
		approved, err := confirm(ctx, conflict)
		if err != nil {
			return result, err
		}
		if !approved {
			return result, fmt.Errorf("change refused for %s: %s", conflict.Path, conflict.Field)
		}
	}
	for _, input := range p.inputs {
		if err := input.recheck(); err != nil {
			return result, err
		}
	}
	if p.inferredSource != "" {
		repository, err := chezmoi.InferRepository(ctx, p.inferredSource)
		if err != nil {
			return result, err
		}
		if repository != p.Repository {
			return result, fmt.Errorf("source origin changed since planning; prepare a new plan")
		}
	}
	for _, effect := range p.effects {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		for _, input := range p.inputs {
			if input.path == effect.Path {
				if err := publish(ctx, input, []byte(effect.After)); err != nil {
					return result, fmt.Errorf("write %s: %w", effect.Path, err)
				}
				break
			}
		}
		result.Completed = append(result.Completed, effect)
	}
	return result, nil
}
