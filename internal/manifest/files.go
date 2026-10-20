package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dotm/internal/pathutil"
)

// FileDeclaration retains the original spelling even when expansion aliases another entry.
// Parent and Input identify a local grouped child; Application is empty for local entries.
type FileDeclaration struct {
	Source      string
	Application string
	Field       string
	Index       int
	Input       string
	Parent      string
	Path        string
	Selected    bool
}

func (d FileDeclaration) String() string {
	if d.Application == "" {
		return fmt.Sprintf("%s: %s[%q][%d] = %q (%s)", d.Source, d.Field, d.Parent, d.Index, d.Input, d.Path)
	}
	return fmt.Sprintf("%s: application %q: %s[%d] = %q (%s)",
		d.Source, d.Application, d.Field, d.Index, d.Input, d.Path)
}

type FileSeverity string

const (
	FileWarning FileSeverity = "warning"
	FileError   FileSeverity = "error"
)

// Path supplies context for general findings without declaration provenance.
// Related points to the other declaration for overlap diagnostics.
type FileDiagnostic struct {
	Severity    FileSeverity
	Message     string
	Path        string
	Declaration FileDeclaration
	Related     *FileDeclaration
}

func (d FileDiagnostic) String() string {
	text := string(d.Severity) + ": "
	if d.Declaration.Source != "" {
		text += d.Declaration.String() + ": "
	} else if d.Path != "" {
		text += d.Path + ": "
	}
	text += d.Message
	if d.Related != nil {
		text += "; overlaps " + d.Related.String()
	}
	return text
}

type FileModel struct {
	Declarations []FileDeclaration
	Diagnostics  []FileDiagnostic
}

// Err reports errors only; warnings never invalidate a declaration model.
func (m FileModel) Err() error {
	var failures []error
	for _, diagnostic := range m.Diagnostics {
		if diagnostic.Severity == FileError {
			failures = append(failures, errors.New(diagnostic.String()))
		}
	}
	return errors.Join(failures...)
}

// FilesPath uses the same machine-local directory as config.toml, without creating it.
func FilesPath() (string, error) {
	selection, err := SelectionPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(selection), "files.toml"), nil
}

// Files constructs the complete catalog model with selected dependency-closure membership.
// An empty localPath omits local input; a missing local file means no local exclusions.
// Decode/I/O failures are returned separately; declaration errors remain in the model.
func (d *Data) Files(roots []string, localPath string) (FileModel, error) {
	return d.files(roots, func(model *FileModel) error {
		if localPath == "" {
			return nil
		}
		return model.loadLocal(localPath)
	})
}

// FilesContents validates planned local declarations without filesystem writes.
func (d *Data) FilesContents(roots []string, path string, content []byte) (FileModel, error) {
	return d.files(roots, func(model *FileModel) error {
		return model.loadLocalContents(path, content, nil)
	})
}

func (d *Data) files(roots []string, loadLocal func(*FileModel) error) (FileModel, error) {
	resolved, err := d.Resolve(roots)
	if err != nil {
		return FileModel{}, err
	}
	selected := make(map[string]bool, len(resolved.Applications))
	for _, app := range resolved.Applications {
		selected[app.ID] = true
	}
	model := FileModel{Declarations: make([]FileDeclaration, len(d.FileDeclarations))}
	copy(model.Declarations, d.FileDeclarations)
	for i := range model.Declarations {
		model.Declarations[i].Selected = selected[model.Declarations[i].Application]
	}
	if err := loadLocal(&model); err != nil {
		return FileModel{}, err
	}
	model.lint()
	return model, nil
}

func (m *FileModel) loadLocal(path string) error {
	content, err := os.ReadFile(path)
	return m.loadLocalContents(path, content, err)
}

func (m *FileModel) loadLocalContents(path string, content []byte, readErr error) error {
	var local struct {
		Unmanaged map[string][]string `toml:"unmanaged"`
	}
	if err := decodeContents(path, content, &local, readErr); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	parents := make([]string, 0, len(local.Unmanaged))
	for parent := range local.Unmanaged {
		parents = append(parents, parent)
	}
	sort.Strings(parents)
	for _, parent := range parents {
		base, err := pathutil.Target(parent)
		if err != nil {
			m.invalid(FileDeclaration{Source: path, Field: "unmanaged", Parent: parent, Input: parent}, err)
			continue
		}
		for i, child := range local.Unmanaged[parent] {
			declaration := FileDeclaration{
				Source: path, Field: "unmanaged", Parent: parent, Input: child, Index: i, Selected: true,
			}
			if child == "" || child == "." || child == ".." || strings.ContainsAny(child, "/\x00") {
				m.invalid(declaration, fmt.Errorf("must be a single child name (no /, NUL, . or ..)"))
				continue
			}
			declaration.Path = filepath.Join(base, child)
			m.Declarations = append(m.Declarations, declaration)
		}
	}
	return nil
}

func (m *FileModel) invalid(declaration FileDeclaration, err error) {
	m.Diagnostics = append(m.Diagnostics, FileDiagnostic{
		Severity: FileError, Message: err.Error(), Declaration: declaration,
	})
}

// lint compares lexical components only: it does not stat paths or resolve symlinks.
func (m *FileModel) lint() {
	for i, a := range m.Declarations {
		for j := i + 1; j < len(m.Declarations); j++ {
			b := m.Declarations[j]
			if !fileOverlap(a.Path, b.Path) {
				continue
			}
			localA, localB := a.Application == "", b.Application == ""
			if localA != localB && ((!localA && !a.Selected) || (!localB && !b.Selected)) {
				continue
			}
			severity := FileWarning
			if a.Field != b.Field || (a.Field == "configs" && a.Application != b.Application) {
				severity = FileError
			}
			m.Diagnostics = append(m.Diagnostics, FileDiagnostic{
				Severity: severity, Message: "declared paths overlap", Declaration: a, Related: &m.Declarations[j],
			})
		}
	}
}

func fileOverlap(a, b string) bool {
	return fileContains(a, b) || fileContains(b, a)
}

func fileContains(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+"/")
}
