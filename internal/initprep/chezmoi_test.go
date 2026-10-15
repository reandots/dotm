package initprep

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"dotm/internal/manifest"
)

func (s *PreparationSuite) TestPlanChezmoiPreservesTOMLFormsAndAddsMembership() {
	for _, tc := range []struct{ name, configs string }{
		{"array-table-empty", "# keep\n[[configurations]]\nname='machine' # name\napps=[] # apps\n"},
		{"array-table-trailing-comma", "[[configurations]]\nname='machine' # keep\n" +
			"apps = [\n  'other', # preserve\n]\n"},
		{"array-table-no-apps-eof", "[[configurations]]\nname='machine' # keep"},
		{"inline-array-no-apps", "configurations = [{ name = 'machine' }] # keep\n"},
		{"inline-array", "configurations = [{ name = 'machine', apps = [] }] # keep\n"},
		{"array-table-nonempty", "[[configurations]]\nname='machine' # keep\napps=['other']\n"},
		{"array-table-empty-comment", "[[configurations]]\nname='machine' # keep\napps=[ # preserve\n]\n"},
		{"array-table-nested-source", "[[configurations]]\nname='machine' # keep\n" +
			"[configurations.configs]\nbackend='chezmoi'\nrepository='repo'\n"},
	} {
		s.Run(tc.name, func() {
			s.write(filepath.Join(s.data, "catalog.toml"), "# catalog\n[other]\nnix = ['hello'] # keep\n", 0o600)
			s.write(filepath.Join(s.data, "configurations.toml"), tc.configs, 0o600)
			p, err := PlanChezmoi(context.Background(), s.data, "machine")
			s.Require().NoError(err)
			r, err := p.Apply(context.Background(), nil)
			s.Require().NoError(err)
			s.Len(r.Completed, 2)
			content, err := os.ReadFile(filepath.Join(s.data, "configurations.toml"))
			s.Require().NoError(err)
			s.Contains(string(content), "# keep")
			if strings.Contains(tc.configs, "# preserve") {
				s.Contains(string(content), "# preserve")
			}
			data, err := manifest.Load(s.data)
			s.Require().NoError(err)
			resolved, err := data.Resolve(data.Configs["machine"].Apps)
			s.Require().NoError(err)
			found := false
			for _, app := range resolved.Applications {
				if app.ID == "chezmoi" {
					found = true
				}
			}
			s.True(found)
			p, err = PlanChezmoi(context.Background(), s.data, "machine")
			s.Require().NoError(err)
			s.Empty(p.Effects())
		})
	}
}

func (s *PreparationSuite) TestPlanChezmoiPreservesCustomAppAndDependencyClosure() {
	s.write(filepath.Join(s.data, "catalog.toml"), "[chezmoi]\nnix = ['custom.chezmoi']\n"+
		"[base]\ndependencies=['chezmoi']\nnix = ['hello']\n", 0o600)
	s.write(filepath.Join(s.data, "configurations.toml"), "[[configurations]]\nname='machine'\napps=['base']\n", 0o600)
	original, err := os.ReadFile(filepath.Join(s.data, "catalog.toml"))
	s.Require().NoError(err)
	p, err := PlanChezmoi(context.Background(), s.data, "machine")
	s.Require().NoError(err)
	s.Empty(p.Effects())
	r, err := p.Apply(context.Background(), nil)
	s.Require().NoError(err)
	s.Empty(r.Completed)
	data, err := manifest.Load(s.data)
	s.Require().NoError(err)
	s.Equal(string(original), string(mustRead(s.T(), filepath.Join(s.data, "catalog.toml"))))
	s.Equal([]string{"base"}, data.Configs["machine"].Apps)
}

func (s *PreparationSuite) TestPlanChezmoiRejectsUnsafeAndStaleInputs() {
	s.write(filepath.Join(s.data, "catalog.toml"), "[base]\nnix=['hello']\n", 0o600)
	s.write(filepath.Join(s.data, "configurations.toml"), "[[configurations]]\nname='machine'\napps=[]\n", 0o600)
	p, err := PlanChezmoi(context.Background(), s.data, "machine")
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(s.data, "catalog.toml"),
		[]byte("# concurrent\n[base]\nnix=['hello']\n"), 0o600))
	_, err = p.Apply(context.Background(), nil)
	s.Error(err)
	s.write(filepath.Join(s.data, "catalog.toml"), "[base]\nnix=['hello']\n", 0o600)
	p, err = PlanChezmoi(context.Background(), s.data, "machine")
	s.Require().NoError(err)
	selectionPath, err := manifest.SelectionPath()
	s.Require().NoError(err)
	s.write(selectionPath, "configuration='machine'\n", 0o600)
	_, err = p.Apply(context.Background(), nil)
	s.Error(err)
	path := filepath.Join(s.data, "configurations.toml")
	s.Require().NoError(os.Remove(path))
	s.Require().NoError(os.Symlink(filepath.Join(s.home, "missing"), path))
	_, err = PlanChezmoi(context.Background(), s.data, "machine")
	s.Error(err)
}

func (s *PreparationSuite) TestPlanChezmoiRejectsMalformedCandidate() {
	s.write(filepath.Join(s.data, "catalog.toml"), "[base]\nnix=['hello']\n", 0o600)
	s.write(filepath.Join(s.data, "configurations.toml"), "[[configurations]]\nname='machine'\napps=[]\n", 0o600)
	selectionPath, err := manifest.SelectionPath()
	s.Require().NoError(err)
	s.write(filepath.Join(filepath.Dir(selectionPath), "files.toml"), "[broken", 0o600)
	_, err = PlanChezmoi(context.Background(), s.data, "machine")
	s.Error(err)
}

func mustRead(t interface {
	Helper()
	Fatal(...any)
}, path string,
) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
