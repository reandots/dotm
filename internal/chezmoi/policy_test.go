package chezmoi

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"dotm/internal/manifest"
)

type policySuite struct {
	suite.Suite
	root, checkout, source, home, config string
}

func TestPolicySuite(t *testing.T) {
	if _, err := exec.LookPath("chezmoi"); err != nil {
		t.Skip("chezmoi is required for real-backend policy tests")
	}
	suite.Run(t, new(policySuite))
}

func (s *policySuite) SetupTest() {
	s.root = s.T().TempDir()
	s.checkout = filepath.Join(s.root, "checkout")
	s.source = s.checkout
	s.home = filepath.Join(s.root, "home")
	configDir := filepath.Join(s.root, "xdg-config", "chezmoi")
	s.config = filepath.Join(configDir, "chezmoi.toml")
	for _, dir := range []string{s.checkout, s.home, configDir} {
		s.Require().NoError(os.MkdirAll(dir, 0o700))
	}
	for name, value := range map[string]string{
		"HOME":            s.home,
		"XDG_CONFIG_HOME": filepath.Dir(configDir),
		"XDG_CONFIG_DIRS": filepath.Join(s.root, "system-config"),
		"XDG_DATA_HOME":   filepath.Join(s.root, "data"),
		"XDG_STATE_HOME":  filepath.Join(s.root, "state"),
		"XDG_CACHE_HOME":  filepath.Join(s.root, "cache"),
	} {
		s.T().Setenv(name, value)
	}
	s.configure("")
	s.git("init", "--quiet", s.checkout)
	bare := filepath.Join(s.root, "remote.git")
	s.git("init", "--bare", "--quiet", bare)
	s.git("-C", s.checkout, "remote", "add", "origin", bare)
	s.commit()
}

func (s *policySuite) write(path, content string) {
	s.T().Helper()
	s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
	//nolint:gosec // G703: all caller-provided fixture paths are confined to this test's private temporary directory.
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
}

func (s *policySuite) git(args ...string) {
	s.T().Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	s.Require().NoError(err, "%s: %s", strings.Join(args, " "), out)
}

func (s *policySuite) commit() {
	s.T().Helper()
	s.git("-C", s.checkout, "add", "--all")
	s.git("-C", s.checkout, "-c", "user.name=Policy Test", "-c", "user.email=policy@example.invalid",
		"commit", "--quiet", "--allow-empty", "-m", "fixture")
}

func (s *policySuite) input(config string, declarations ...manifest.FileDeclaration) PolicyInput {
	return PolicyInput{
		Repository: filepath.Join(s.root, "remote.git"), Configuration: config,
		Files: manifest.FileModel{Declarations: declarations},
	}
}

func (s *policySuite) configure(policyPath string) {
	s.T().Helper()
	content := "sourceDir = " + strconv.Quote(s.checkout) + "\ndestDir = " + strconv.Quote(s.home) + "\n"
	if policyPath != "" {
		content += "[data.dotm]\npolicyFile = " + strconv.Quote(policyPath) + "\n"
	}
	s.write(s.config, content)
}

func (s *policySuite) integrate() string {
	s.T().Helper()
	path, err := PolicyPath(Policy{ConfigFile: s.config, SourceDir: s.source, DestDir: s.home})
	s.Require().NoError(err)
	s.configure(path)
	fixture, err := os.ReadFile(filepath.Join("..", "..", "examples", "chezmoi-policy", ".chezmoiignore"))
	s.Require().NoError(err)
	s.write(filepath.Join(s.source, ".chezmoiignore"), string(fixture))
	return path
}

func (s *policySuite) managed() []string {
	s.T().Helper()
	data, err := policyCommand(context.Background(), applyCommand{source: s.checkout},
		"managed", "--nul-path-separator")
	s.Require().NoError(err)
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

func (s *policySuite) publish(input PolicyInput) Policy {
	s.T().Helper()
	policy, err := CompilePolicy(context.Background(), s.checkout, input)
	s.Require().NoError(err)
	s.Require().NoError(PublishPolicy(context.Background(), s.checkout, policy))
	return policy
}

func (s *policySuite) TestLiteralExclusionsPreserveSiblingsAndNativeRules() {
	s.integrate()
	s.write(filepath.Join(s.source, "dot_config", "selected", ".chezmoiignore"), "*.db\n")
	s.write(filepath.Join(s.source, "dot_config", "selected", "cache.db"), "native exclusion")
	s.write(filepath.Join(s.source, "dot_config", "selected", "settings"), "selected")
	names := []string{
		"glob*name", "question?name", "bracket[name]", "brace{a,b}", `back\slash`, "#hash", "!bang",
		"white #hash", " leading", "trailing ", "with\ttab", `{{ fail "MUST_NOT_EXECUTE" }}`,
	}
	declarations := []manifest.FileDeclaration{}
	for _, name := range names {
		s.write(filepath.Join(s.source, "literal_"+name), "hidden")
		s.write(filepath.Join(s.source, "literal_"+name+"-sibling"), "visible")
		declarations = append(declarations, manifest.FileDeclaration{
			Field: "unmanaged", Path: filepath.Join(s.home, name), Input: name, Selected: true,
		})
	}
	s.commit()
	standalone := s.managed()
	s.Contains(standalone, "glob*name")
	s.NotContains(standalone, ".config/selected/cache.db")
	s.publish(s.input("work", declarations...))
	filtered := s.managed()
	for _, name := range names {
		s.NotContains(filtered, name)
		s.Contains(filtered, name+"-sibling")
	}
	s.NotContains(filtered, ".config/selected/cache.db")
	s.Contains(filtered, ".config/selected/settings")
}

func (s *policySuite) TestProfileSwitchNewSourceContentAndEmptyPolicy() {
	s.integrate()
	s.write(filepath.Join(s.source, "dot_config", "one", "settings"), "one")
	s.write(filepath.Join(s.source, "dot_config", "two", "settings"), "two")
	s.commit()
	one := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "one"), Application: "one", Selected: true,
	}
	two := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "two"), Application: "two",
	}
	s.publish(s.input("profile-one", one, two))
	s.Contains(s.managed(), ".config/one/settings")
	s.NotContains(s.managed(), ".config/two/settings")
	s.write(filepath.Join(s.source, "dot_config", "two", "new"), "new source content")
	s.NotContains(s.managed(), ".config/two/new")
	one.Selected, two.Selected = false, true
	s.publish(s.input("profile-two", one, two))
	s.NotContains(s.managed(), ".config/one/settings")
	s.Contains(s.managed(), ".config/two/new")
	s.publish(s.input("empty"))
	s.Contains(s.managed(), ".config/one/settings")
	s.Contains(s.managed(), ".config/two/settings")
}

func (s *policySuite) TestSourceRebindingUsesEffectiveRoot() {
	firstPath := s.integrate()
	first := s.publish(s.input("work"))
	s.write(filepath.Join(s.checkout, ".chezmoiroot"), "effective\n")
	s.source = filepath.Join(s.checkout, "effective")
	secondPath := s.integrate()
	s.NotEqual(firstPath, secondPath)
	s.write(filepath.Join(s.source, "hidden"), "hidden")
	s.commit()
	second := s.publish(s.input("work", manifest.FileDeclaration{
		Field: "unmanaged", Path: filepath.Join(s.home, "hidden"), Selected: true,
	}))
	s.Equal(s.source, second.SourceDir)
	s.NotEqual(first.Freshness, second.Freshness)
	s.NotContains(s.managed(), "hidden")
}

func (s *policySuite) TestCandidateFailurePreservesPolicyAndCleanCheckout() {
	path := s.integrate()
	s.write(filepath.Join(s.source, "visible"), "source")
	s.commit()
	input := s.input("work")
	oldCandidate := s.publish(input)
	old, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.write(filepath.Join(s.source, "new-file"), "new input")
	s.ErrorContains(PublishPolicy(context.Background(), s.checkout, oldCandidate), "stale")
	freshCandidate, err := CompilePolicy(context.Background(), s.checkout, input)
	s.Require().NoError(err)
	_, err = CheckPolicy(context.Background(), s.checkout, freshCandidate, false)
	s.Require().NoError(err, "candidate inspection must not consume stale published policy")
	s.write(filepath.Join(s.source, "bad.tmpl"), `{{ fail "candidate evaluation failure" }}`)
	candidate, err := CompilePolicy(context.Background(), s.checkout, input)
	s.Require().NoError(err)
	s.ErrorContains(PublishPolicy(context.Background(), s.checkout, candidate), "candidate evaluation failure")
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.True(bytes.Equal(old, after), "failed candidate must not replace the published policy")
	s.Require().NoError(os.Remove(filepath.Join(s.source, "bad.tmpl")))
	s.commit()
	s.publish(input)
	status, err := git(context.Background(), s.checkout, "status", "--porcelain=v1", "--untracked-files=all")
	s.Require().NoError(err)
	s.Empty(status)
	_, err = os.Lstat(filepath.Join(s.home, "visible"))
	s.Require().True(os.IsNotExist(err), "publication must not apply source contents")
}

func (s *policySuite) TestUnrepresentableGeneratedPathsAndUnsupportedMapping() {
	for _, scenario := range []struct{ path, message string }{
		{filepath.Join(s.root, "outside"), "outside or equals"},
		{filepath.Join(s.home, "line\nbreak"), "CR/LF"},
		{filepath.Join(s.home, "carriage\rreturn"), "CR/LF"},
	} {
		_, err := CompilePolicy(context.Background(), s.checkout,
			s.input("work", manifest.FileDeclaration{Field: "unmanaged", Path: scenario.path}))
		s.ErrorContains(err, scenario.message)
	}
}

func (s *policySuite) TestCheckPolicyReportsNativeExposureAndPublishedStaleness() {
	s.integrate()
	s.write(filepath.Join(s.source, "dot_config", "other", "settings"), "hidden")
	fixture, readErr := os.ReadFile(filepath.Join("..", "..", "examples", "chezmoi-policy", ".chezmoiignore"))
	s.Require().NoError(readErr)
	s.write(filepath.Join(s.source, ".chezmoiignore"), string(fixture)+"\n!.config/other/**\n!ack/future\n")
	s.commit()
	declaration := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "other"), Application: "other",
	}
	absent := manifest.FileDeclaration{Field: "unmanaged", Path: filepath.Join(s.home, "ack", "future"), Selected: true}
	policy, err := CompilePolicy(context.Background(), s.checkout, s.input("work", declaration, absent))
	s.Require().NoError(err)
	result, err := CheckPolicy(context.Background(), s.checkout, policy, false)
	s.Require().NoError(err)
	s.NotEmpty(result.Diagnostics)
	s.Require().NoError(PublishPolicy(context.Background(), s.checkout, policy))
	result, err = CheckPolicy(context.Background(), s.checkout, policy, true)
	s.Require().NoError(err)
	s.NotEmpty(result.Diagnostics)
}

func (s *policySuite) TestCheckPolicyWarnsForPresentWhollyHiddenSelectedConfig() {
	s.integrate()
	s.write(filepath.Join(s.source, "dot_config", "selected", "settings"), "selected")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "examples", "chezmoi-policy", ".chezmoiignore"))
	s.Require().NoError(err)
	s.write(filepath.Join(s.source, ".chezmoiignore"), string(fixture)+"\n**\n")
	s.commit()
	declaration := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "selected"), Application: "selected", Selected: true,
	}
	policy, err := CompilePolicy(context.Background(), s.checkout, s.input("work", declaration))
	s.Require().NoError(err)
	result, err := CheckPolicy(context.Background(), s.checkout, policy, false)
	s.Require().NoError(err)
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Severity == manifest.FileWarning {
			found = true
		}
	}
	s.True(found, "present selected config hidden by native rules should warn")
}

func (s *policySuite) TestCheckPolicyRetainsTemplateFailureCause() {
	s.integrate()
	s.write(filepath.Join(s.source, ".chezmoiignore"), `{{ fail "native ignore execution failure" }}`)
	policy, err := CompilePolicy(context.Background(), s.checkout, s.input("work"))
	s.Require().NoError(err)
	_, err = CheckPolicy(context.Background(), s.checkout, policy, false)
	s.ErrorContains(err, "native ignore execution failure")
}

func (s *policySuite) TestCheckPolicyFindsAbsentNestedNegationWithWhitespaceScope() {
	s.integrate()
	s.write(filepath.Join(s.source, "dot_config", "other ", ".chezmoiignore"), "!future\n")
	declaration := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "other "), Application: "other",
	}
	policy, err := CompilePolicy(context.Background(), s.checkout, s.input("work", declaration))
	s.Require().NoError(err)
	result, err := CheckPolicy(context.Background(), s.checkout, policy, false)
	s.Require().NoError(err)
	s.Error(result.Err(), "absent nested paths must not bypass the generated exclusion")
}

func (s *policySuite) TestCheckPolicyEvaluatesNativeIgnoreInLiteralDirectory() {
	s.integrate()
	s.write(filepath.Join(s.source, "literal_assets", ".chezmoiignore"), `{{ fail "not an ignore template" }}`)
	declaration := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, "assets"), Application: "assets", Selected: true,
	}
	policy, err := CompilePolicy(context.Background(), s.checkout, s.input("work", declaration))
	s.Require().NoError(err)
	_, err = CheckPolicy(context.Background(), s.checkout, policy, false)
	s.ErrorContains(err, "not an ignore template",
		"chezmoi evaluates nested native ignores even inside literal_ directories")
}

func (s *policySuite) TestCheckPolicyUsesCombinedPatternSetDefaults() {
	path := s.integrate()
	fixture, err := os.ReadFile(filepath.Join(s.source, ".chezmoiignore"))
	s.Require().NoError(err)
	s.write(filepath.Join(s.source, ".chezmoiignore"), string(fixture)+"\n!ack/future\n")
	s.write(filepath.Join(s.source, "dot_config", "selected", "settings"), "selected")
	selected := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "selected"), Application: "selected", Selected: true,
	}
	other := manifest.FileDeclaration{
		Field: "configs", Path: filepath.Join(s.home, ".config", "other"), Application: "other",
	}
	policy, err := CompilePolicy(context.Background(), s.checkout, s.input("work", selected, other))
	s.Require().NoError(err)
	result, err := CheckPolicy(context.Background(), s.checkout, policy, false)
	s.Require().NoError(err)
	s.NoError(result.Err())
	for _, diagnostic := range result.Diagnostics {
		s.NotEqual(manifest.FileWarning, diagnostic.Severity,
			"adding generated positive patterns changes the negative-only native default")
	}
	_, statErr := os.Lstat(path)
	s.Require().True(os.IsNotExist(statErr), "candidate inspection must not publish policy")
}

func (s *policySuite) TestCompilePolicyRejectsDifferentRepositoryBeforeTemplates() {
	s.write(filepath.Join(s.source, ".chezmoiignore"), `{{ fail "trusted template was evaluated" }}`)
	input := s.input("work")
	input.Repository = filepath.Join(s.root, "other.git")
	_, err := CompilePolicy(context.Background(), s.checkout, input)
	s.ErrorContains(err, "does not match configured repository")
	s.NotContains(err.Error(), "trusted template was evaluated")
}

func (s *policySuite) TestPolicyAcceptsFileURLAliasAndRejectsReplacedOrigin() {
	s.integrate()
	s.write(filepath.Join(s.source, "hidden"), "hidden")
	s.commit()
	input := s.input("work", manifest.FileDeclaration{
		Field: "unmanaged", Path: filepath.Join(s.home, "hidden"), Input: "hidden", Selected: true,
	})
	input.Repository = "file://" + filepath.Join(s.root, "remote.git")
	policy, err := CompilePolicy(context.Background(), s.checkout, input)
	s.Require().NoError(err)
	s.Equal(filepath.Join(s.root, "remote.git"), policy.Repository,
		"the policy template binding uses the effective origin spelling")
	s.Require().NoError(PublishPolicy(context.Background(), s.checkout, policy))
	result, err := CheckPolicy(context.Background(), s.checkout, policy, true)
	s.Require().NoError(err)
	s.NoError(result.Err())
	s.NotContains(s.managed(), "hidden")

	replacement := filepath.Join(s.root, "replacement.git")
	s.git("init", "--bare", "--quiet", replacement)
	s.git("-C", s.checkout, "remote", "set-url", "origin", replacement)
	s.Contains(s.managed(), "hidden", "a policy bound to the previous origin must not filter")
}
