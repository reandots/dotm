package chezmoi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type InitIntegrationSuite struct {
	suite.Suite
	root, source, config string
}

func TestInitIntegrationSuite(t *testing.T) { suite.Run(t, new(InitIntegrationSuite)) }

func (s *InitIntegrationSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.source = filepath.Join(s.root, "source")
	s.config = filepath.Join(s.root, "config")
	for key, value := range map[string]string{
		"HOME": filepath.Join(s.root, "home"), "XDG_CONFIG_HOME": s.config,
		"XDG_CONFIG_DIRS": filepath.Join(s.root, "system-config"),
	} {
		s.T().Setenv(key, value)
	}
	s.Require().NoError(os.MkdirAll(s.source, 0o700))
	s.git("init", "--quiet", s.source)
	s.git("-C", s.source, "remote", "add", "origin", filepath.Join(s.root, "remote"))
	s.write(filepath.Join(s.source, ".chezmoiroot"), "nested\n")
	s.write(filepath.Join(s.source, "nested", ".chezmoiignore"), "# native rules\n*.db\n")
	s.git("-C", s.source, "add", "--all")
	s.git("-C", s.source, "-c", "user.name=Init Test", "-c", "user.email=init@example.invalid",
		"commit", "--quiet", "-m", "fixture")
}

func (s *InitIntegrationSuite) git(args ...string) {
	s.T().Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	output, err := cmd.CombinedOutput()
	s.Require().NoError(err, "%s", output)
}

func (s *InitIntegrationSuite) write(path, content string) {
	s.T().Helper()
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

func (s *InitIntegrationSuite) TestExactMigrationAndDamagedBlockRefusal() {
	portable, legacy := integrationTemplates()
	block := ownedIntegration(portable)
	for _, old := range []string{"", portable, legacy, ownedIntegration(legacy)} {
		s.Run(old, func() {
			before := "# before\n*.db\npolicyFile-cache\n.dotm.policy-cache\n"
			after := "# after\n*.tmp\n"
			got, changed, err := integrateIgnore([]byte(before+old+after), block, "ignore")
			s.Require().NoError(err)
			s.True(changed)
			if strings.HasPrefix(old, "{{/* dotm:policy begin */}}") {
				s.Equal(before+block+after, string(got))
			} else {
				s.Equal(block+before+after, string(got))
			}
			repeated, changed, err := integrateIgnore(got, block, "ignore")
			s.Require().NoError(err)
			s.False(changed)
			s.Equal(got, repeated)
		})
	}
	for _, damaged := range []string{
		strings.Replace(block, "schemaVersion", "userVersion", 1), block + block,
		"{{/* dotm:policy begin */}}\npartial\n", "{{/* dotm:policy begin */\n",
		legacy + portable, strings.Replace(legacy, "policyFile", "policyFileChanged", 1), block + legacy,
	} {
		_, _, err := integrateIgnore([]byte(damaged), block, "ignore")
		s.Error(err)
	}
}

func (s *InitIntegrationSuite) TestNestedRootPreservesCompatibleSettingsRulesAndModes() {
	settings := filepath.Join(s.config, "chezmoi", "chezmoi.yaml")
	s.write(settings, "mode: file\n# user setting\nverbose: false\n")
	s.Require().NoError(os.Chmod(settings, 0o640))
	ignore := filepath.Join(s.source, "nested", ".chezmoiignore")
	s.Require().NoError(os.Chmod(ignore, 0o640))
	result, err := PrepareIntegration(context.Background(), s.source)
	s.Require().NoError(err)
	s.Require().Len(result.Effects, 1)
	s.Equal(ignore, result.Effects[0].Path)
	content, err := os.ReadFile(ignore)
	s.Require().NoError(err)
	s.True(strings.HasSuffix(string(content), "# native rules\n*.db\n"))
	before, err := os.Stat(ignore)
	s.Require().NoError(err)
	s.Equal(os.FileMode(0o640), before.Mode().Perm())
	result, err = PrepareIntegration(context.Background(), s.source)
	s.Require().NoError(err)
	s.Empty(result.Effects)
	after, err := os.Stat(ignore)
	s.Require().NoError(err)
	s.True(os.SameFile(before, after))
	settingsContent, err := os.ReadFile(settings)
	s.Require().NoError(err)
	s.Equal("mode: file\n# user setting\nverbose: false\n", string(settingsContent))
	s.NoFileExists(filepath.Join(s.config, "chezmoi", "chezmoi.toml"))
}

func (s *InitIntegrationSuite) TestDamagedIntegrationFailsBeforeSettingsCreation() {
	ignore := filepath.Join(s.source, "nested", ".chezmoiignore")
	s.write(ignore, "{{/* dotm:policy begin */}}\n")
	result, err := PrepareIntegration(context.Background(), s.source)
	s.Require().Error(err)
	s.Empty(result.Effects)
	s.NoDirExists(s.config)
}

func (s *InitIntegrationSuite) TestStaleAndSymlinkTargetsCannotBePublished() {
	path := filepath.Join(s.root, "target")
	s.write(path, "original")
	snapshot, err := inspectIntegrationFile(path)
	s.Require().NoError(err)
	s.write(path, "manual edit")
	s.Error(snapshot.publish(context.Background(), []byte("replacement")))
	content, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal("manual edit", string(content))
	s.Require().NoError(os.Symlink(path, filepath.Join(s.root, "link")))
	_, err = inspectIntegrationFile(filepath.Join(s.root, "link"))
	s.Error(err)
	missing := filepath.Join(s.root, "absent")
	snapshot, err = inspectIntegrationFile(missing)
	s.Require().NoError(err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Error(snapshot.publish(ctx, []byte("not published")))
	s.NoFileExists(missing)
}

func (s *InitIntegrationSuite) TestSymlinkParentPublicationAndRetargeting() {
	first := filepath.Join(s.root, "first")
	second := filepath.Join(s.root, "second")
	for _, dir := range []string{first, second} {
		s.Require().NoError(os.MkdirAll(dir, 0o700))
	}
	parent := filepath.Join(s.root, "parent")
	s.Require().NoError(os.Symlink(first, parent))
	path := filepath.Join(parent, "settings")
	snapshot, err := inspectIntegrationFile(path)
	s.Require().NoError(err)
	s.Require().NoError(snapshot.publish(context.Background(), []byte("original")))
	content, err := os.ReadFile(filepath.Join(first, "settings"))
	s.Require().NoError(err)
	s.Equal("original", string(content))

	// Identical file contents must not hide substitution of a symlink ancestor.
	s.write(filepath.Join(second, "settings"), "original")
	snapshot, err = inspectIntegrationFile(path)
	s.Require().NoError(err)
	s.Require().NoError(os.Remove(parent))
	s.Require().NoError(os.Symlink(second, parent))
	s.Error(snapshot.publish(context.Background(), []byte("replacement")))
	for _, dir := range []string{first, second} {
		content, err = os.ReadFile(filepath.Join(dir, "settings"))
		s.Require().NoError(err)
		s.Equal("original", string(content))
	}
}
