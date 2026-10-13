package installed

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dotm/internal/manifest"

	"github.com/stretchr/testify/suite"
)

type ConfigurationLifecycleSuite struct {
	suite.Suite
	data *manifest.Data
}

func TestConfigurationLifecycleSuite(t *testing.T) {
	suite.Run(t, new(ConfigurationLifecycleSuite))
}

func (s *ConfigurationLifecycleSuite) fixture(
	apps map[string]manifest.Application,
	roots ...string,
) (string, manifest.Resolved) {
	s.T().Helper()
	dir := s.T().TempDir()
	desired := s.fixtureAt(dir, apps, roots...)
	return dir, desired
}

func (s *ConfigurationLifecycleSuite) fixtureAt(
	dir string,
	apps map[string]manifest.Application,
	roots ...string,
) manifest.Resolved {
	s.T().Helper()
	s.T().Setenv("HOME", dir)
	s.T().Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	s.T().Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	s.T().Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	s.T().Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	s.T().Setenv("PATH", "")
	data := manifest.Data{CatalogDir: dir, Apps: apps}
	for _, app := range apps {
		for i, path := range app.Configs {
			data.FileDeclarations = append(data.FileDeclarations, manifest.FileDeclaration{
				Application: app.ID, Field: "configs", Index: i, Input: path, Path: path,
			})
		}
	}
	s.data = &data
	desired, err := data.Resolve(roots)
	s.Require().NoError(err)
	return desired
}

func (s *ConfigurationLifecycleSuite) hook(dir, name, body string) {
	s.T().Helper()
	// #nosec G306 -- Private executable fixture used only by this test.
	s.Require().NoError(os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700))
}

func (s *ConfigurationLifecycleSuite) log(dir string) string {
	s.T().Helper()
	data, err := os.ReadFile(filepath.Join(dir, "hooks.log"))
	s.Require().NoError(err)
	return string(data)
}

func (s *ConfigurationLifecycleSuite) repository(dir string, files map[string]string) string {
	s.T().Helper()
	s.T().Setenv("PATH", "/usr/bin:/bin")
	if _, err := exec.LookPath("chezmoi"); err != nil {
		s.T().Skip("chezmoi is required for real-backend lifecycle tests")
	}
	work := filepath.Join(dir, "source-work")
	remote := filepath.Join(dir, "source.git")
	for _, args := range [][]string{{"init", "--bare", "--initial-branch=main", remote}, {"init", "-b", "main", work}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err := cmd.CombinedOutput()
		s.Require().NoError(err, "git %v: %s", args, output)
	}
	git := func(args ...string) {
		s.T().Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		output, err := cmd.CombinedOutput()
		s.Require().NoError(err, "git %v: %s", args, output)
	}
	git("config", "user.name", "Lifecycle Test")
	git("config", "user.email", "lifecycle@example.invalid")
	template, err := os.ReadFile(filepath.Join("..", "..", "examples", "chezmoi-policy", ".chezmoiignore"))
	s.Require().NoError(err)
	files[".chezmoiignore"] = string(template)
	configFile := filepath.Join(dir, "config", "chezmoi", "chezmoi.toml")
	source := filepath.Join(dir, "data", "chezmoi")
	s.Require().NoError(os.MkdirAll(filepath.Dir(configFile), 0o700))
	s.Require().NoError(os.WriteFile(configFile, []byte(fmt.Sprintf(
		"sourceDir = %q\ndestDir = %q\n", source, dir)), 0o600))
	for name, content := range files {
		path := filepath.Join(work, name)
		s.Require().NoError(os.MkdirAll(filepath.Dir(path), 0o700))
		s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))
	}
	git("add", ".")
	git("commit", "-m", "configuration fixtures")
	git("remote", "add", "origin", remote)
	git("push", "-u", "origin", "main")
	return remote
}

func (s *ConfigurationLifecycleSuite) sourceOptions(repository string) ConfigurationOptions {
	return ConfigurationOptions{
		Source: &manifest.ConfigurationSource{Backend: "chezmoi", Repository: repository},
		Data:   s.data, Configuration: "test",
	}
}

func (s *ConfigurationLifecycleSuite) configure(desired manifest.Resolved) (Report, error) {
	s.T().Helper()
	return Configure(context.Background(), desired, io.Discard, ConfigurationOptions{})
}

func (s *ConfigurationLifecycleSuite) TestPreFailureSkipsDiamondAndTransitiveConsumersButContinuesSibling() {
	dir, desired := s.fixture(map[string]manifest.Application{
		"failed": {ID: "failed", PreCfgHook: "failed-pre", PostCfgHook: "failed-post"},
		"left":   {ID: "left", Dependencies: []string{"failed"}, PreCfgHook: "left-pre", PostCfgHook: "left-post"},
		"right":  {ID: "right", Dependencies: []string{"failed"}, PreCfgHook: "right-pre", PostCfgHook: "right-post"},
		"diamond": {
			ID: "diamond", Dependencies: []string{"left", "right"},
			PreCfgHook: "diamond-pre", PostCfgHook: "diamond-post",
		},
		"leaf": {ID: "leaf", Dependencies: []string{"diamond"}, PreCfgHook: "leaf-pre", PostCfgHook: "leaf-post"},
		"safe": {ID: "safe", PreCfgHook: "safe-pre", PostCfgHook: "safe-post"},
	}, "leaf", "safe")
	for _, id := range []string{"failed", "left", "right", "diamond", "leaf", "safe"} {
		s.hook(dir, id+"-pre", "printf '"+id+" pre\\n' >> hooks.log")
		s.hook(dir, id+"-post", "printf '"+id+" post\\n' >> hooks.log")
	}
	s.hook(dir, "failed-pre", "printf 'failed pre\\n' >> hooks.log; exit 1")
	report, err := s.configure(desired)
	s.Error(err)
	s.Equal("failed pre\nsafe pre\nsafe post\n", s.log(dir))
	s.Equal(StageFailed, report.Configuration.Applications[0].Pre.Status)
	s.Require().Len(report.Configuration.Applications, 6)
	for _, id := range []string{"left", "right", "diamond", "leaf"} {
		app := configurationApplication(s, report, id)
		s.Equal(StageSkipped, app.Pre.Status, id)
		s.NotEmpty(app.Pre.BlockedBy, id)
		s.Equal(StageSkipped, app.Post.Status, id)
	}
	s.Equal(StageSuccess, configurationApplication(s, report, "safe").Post.Status)
}

func (s *ConfigurationLifecycleSuite) TestPostFailureBlocksOnlyConsumersPostHooks() {
	dir, desired := s.fixture(map[string]manifest.Application{
		"base":  {ID: "base", PostCfgHook: "base-post"},
		"child": {ID: "child", Dependencies: []string{"base"}, PostCfgHook: "child-post"},
		"leaf":  {ID: "leaf", Dependencies: []string{"child"}, PostCfgHook: "leaf-post"},
		"safe":  {ID: "safe", PostCfgHook: "safe-post"},
	}, "leaf", "safe")
	s.hook(dir, "base-post", "printf 'base post\\n' >> hooks.log; exit 3")
	for _, id := range []string{"child", "leaf", "safe"} {
		s.hook(dir, id+"-post", "printf '"+id+" post\\n' >> hooks.log")
	}
	report, err := s.configure(desired)
	s.Error(err)
	s.Equal("base post\nsafe post\n", s.log(dir))
	s.Equal(StageSkipped, configurationApplication(s, report, "child").Post.Status)
	s.Equal(StageSkipped, configurationApplication(s, report, "leaf").Post.Status)
	s.Equal(StageSuccess, configurationApplication(s, report, "safe").Post.Status)
}

func (s *ConfigurationLifecycleSuite) TestHookOnlyAndZeroPackageApplicationsRunWithoutSource() {
	dir, desired := s.fixture(map[string]manifest.Application{
		"aggregator": {ID: "aggregator", Dependencies: []string{"hook-only"}},
		"hook-only":  {ID: "hook-only", PreCfgHook: "hook-pre", PostCfgHook: "hook-post"},
	}, "aggregator")
	s.hook(dir, "hook-pre", "printf 'pre\\n' >> hooks.log")
	s.hook(dir, "hook-post", "printf 'post\\n' >> hooks.log")
	report, err := s.configure(desired)
	s.Require().NoError(err)
	s.Equal("pre\npost\n", s.log(dir))
	s.Equal(StageSuccess, configurationApplication(s, report, "hook-only").Pre.Status)
	s.Equal(StageSuccess, configurationApplication(s, report, "aggregator").Pre.Status)
	_, err = os.Stat(filepath.Join(dir, "data", "chezmoi"))
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *ConfigurationLifecycleSuite) TestIneligiblePackageDependencySkipsHooksAndReportsReason() {
	dir, desired := s.fixture(map[string]manifest.Application{
		"needs-package": {
			ID: "needs-package", Packages: []manifest.Package{{Attr: "hello"}}, PreCfgHook: "dependency-pre",
		},
		"consumer": {
			ID: "consumer", Dependencies: []string{"needs-package"},
			PreCfgHook: "consumer-pre", PostCfgHook: "consumer-post",
		},
		"safe": {ID: "safe", PreCfgHook: "safe-pre", PostCfgHook: "safe-post"},
	}, "consumer", "safe")
	for _, name := range []string{"dependency-pre", "consumer-pre", "consumer-post", "safe-pre", "safe-post"} {
		s.hook(dir, name, "printf '"+name+"\\n' >> hooks.log")
	}
	report, err := s.configure(desired)
	s.Require().NoError(err)
	s.Equal("safe-pre\nsafe-post\n", s.log(dir))
	dependency := configurationApplication(s, report, "needs-package")
	s.Equal(StageSkipped, dependency.Pre.Status)
	s.NotEmpty(dependency.BlockedBy)
	consumer := configurationApplication(s, report, "consumer")
	s.Equal(StageSkipped, consumer.Pre.Status)
	s.NotEmpty(consumer.BlockedBy)
}

func (s *ConfigurationLifecycleSuite) TestEmptyTargetSetDoesNotAcquireSource() {
	dir, desired := s.fixture(map[string]manifest.Application{"empty": {ID: "empty"}}, "empty")
	report, err := s.configure(desired)
	s.Require().NoError(err)
	s.Empty(report.Configuration.Source.Path)
	s.Empty(report.Configuration.Apply.Targets)
	_, err = os.Stat(filepath.Join(dir, "data", "chezmoi"))
	s.ErrorIs(err, os.ErrNotExist)
}

func (s *ConfigurationLifecycleSuite) TestApplicationFailureStillAppliesConsumerAndBlocksItsPostHook() {
	dir := s.T().TempDir()
	desired := s.fixtureAt(dir, map[string]manifest.Application{
		"base": {ID: "base", Configs: []string{filepath.Join(dir, ".config", "conflict")}, PostCfgHook: "base-post"},
		"consumer": {
			ID: "consumer", Dependencies: []string{"base"},
			Configs: []string{filepath.Join(dir, ".config", "safe")}, PostCfgHook: "consumer-post",
		},
	}, "consumer")
	remote := s.repository(dir, map[string]string{
		"dot_config/conflict": "managed\n",
		"dot_config/safe":     "safe\n",
	})
	config := filepath.Join(dir, ".config")
	s.Require().NoError(os.MkdirAll(config, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(config, "conflict"), []byte("user data\n"), 0o600))
	s.hook(dir, "base-post", "printf 'base post\\n' >> hooks.log")
	s.hook(dir, "consumer-post", "printf 'consumer post\\n' >> hooks.log")
	report, err := Configure(context.Background(), desired, io.Discard, s.sourceOptions(remote))
	s.Error(err)
	conflict, readErr := os.ReadFile(filepath.Join(config, "conflict"))
	s.Require().NoError(readErr)
	s.Equal("user data\n", string(conflict))
	safe, readErr := os.ReadFile(filepath.Join(config, "safe"))
	s.Require().NoError(readErr)
	s.Equal("safe\n", string(safe))
	s.Equal(StageFailed, configurationApplication(s, report, "base").Config.Status)
	s.Equal(StageSuccess, configurationApplication(s, report, "consumer").Config.Status)
	s.Equal(StageSkipped, configurationApplication(s, report, "consumer").Post.Status)
	_, statErr := os.Stat(filepath.Join(dir, "hooks.log"))
	s.ErrorIs(statErr, os.ErrNotExist)
}

func (s *ConfigurationLifecycleSuite) TestOverlappingOwnersBlockApplicationBeforeDestinationWrites() {
	dir := s.T().TempDir()
	config := filepath.Join(dir, ".config")
	desired := s.fixtureAt(dir, map[string]manifest.Application{
		"broad":  {ID: "broad", Configs: []string{config}},
		"narrow": {ID: "narrow", Configs: []string{filepath.Join(config, "child.conf")}, PreCfgHook: "narrow-pre"},
		"shared-blocked": {
			ID: "shared-blocked", Configs: []string{filepath.Join(config, "shared.conf")},
			PreCfgHook: "shared-pre",
		},
		"shared-healthy": {ID: "shared-healthy", Configs: []string{filepath.Join(config, "shared.conf")}},
	}, "broad", "narrow", "shared-blocked", "shared-healthy")
	remote := s.repository(dir, map[string]string{
		"dot_config/child.conf":   "child\n",
		"dot_config/sibling.conf": "sibling\n",
		"dot_config/shared.conf":  "shared\n",
	})
	s.hook(dir, "narrow-pre", "printf 'narrow failed\\n' >> hooks.log; exit 1")
	s.hook(dir, "shared-pre", "printf 'shared failed\\n' >> hooks.log; exit 1")
	report, err := Configure(context.Background(), desired, io.Discard, s.sourceOptions(remote))
	s.Error(err)
	s.NoFileExists(filepath.Join(config, "sibling.conf"))
	s.NoFileExists(filepath.Join(config, "shared.conf"))
	s.NoFileExists(filepath.Join(config, "child.conf"))
	s.Equal(StageFailed, configurationApplication(s, report, "narrow").Pre.Status)
	s.Equal(StageFailed, configurationApplication(s, report, "shared-blocked").Pre.Status)
	s.Equal(StageFailed, configurationApplication(s, report, "shared-healthy").Config.Status)
	s.Equal(StageFailed, report.Configuration.PolicyStage.Status)
	s.ErrorContains(err, "overlapping declarations")
}

func (s *ConfigurationLifecycleSuite) TestInvalidSourceBlocksAllPostHooksAfterPreEffects() {
	dir := s.T().TempDir()
	config := filepath.Join(dir, ".config", "selected.conf")
	desired := s.fixtureAt(dir, map[string]manifest.Application{
		"target": {ID: "target", Configs: []string{config}, PreCfgHook: "target-pre", PostCfgHook: "target-post"},
		"hooks":  {ID: "hooks", PreCfgHook: "hooks-pre", PostCfgHook: "hooks-post"},
	}, "target", "hooks")
	remote := s.repository(dir, map[string]string{
		"dot_config/selected.conf": "managed\n",
		".chezmoiremove":           "*\n",
	})
	for _, name := range []string{"target-pre", "target-post", "hooks-pre", "hooks-post"} {
		s.hook(dir, name, "printf '"+name+"\\n' >> hooks.log")
	}
	report, err := Configure(context.Background(), desired, io.Discard, s.sourceOptions(remote))
	s.Error(err)
	s.Equal("target-pre\nhooks-pre\n", s.log(dir))
	s.Equal(StageFailed, configurationApplication(s, report, "target").Config.Status)
	s.Equal(StageFailed, configurationApplication(s, report, "hooks").Config.Status)
	s.Equal(StageSkipped, configurationApplication(s, report, "target").Post.Status)
	s.Equal(StageSkipped, configurationApplication(s, report, "hooks").Post.Status)
	s.NoFileExists(config)
}

func (s *ConfigurationLifecycleSuite) TestPolicyFailureKeepsCommittedPackageUpdate() {
	dir := s.T().TempDir()
	target := filepath.Join(dir, ".config", "tools")
	desired := s.fixtureAt(dir, map[string]manifest.Application{
		"tools": {ID: "tools", Packages: []manifest.Package{{Attr: "hello"}}, Configs: []string{target}},
	}, "tools")
	remote := s.repository(dir, map[string]string{"dot_config/tools": "managed\n"})
	recovery := &UpdateRecoverySuite{}
	recovery.SetT(s.T())
	state, profile, source, _ := recovery.updateFixture(false)
	s.T().Setenv("PATH", filepath.Join(filepath.Dir(state), "bin")+":/usr/bin:/bin")
	filesPath, err := manifest.FilesPath()
	s.Require().NoError(err)
	s.Require().NoError(os.MkdirAll(filepath.Dir(filesPath), 0o700))
	s.Require().NoError(os.WriteFile(filesPath, []byte("[unmanaged]\n\"~/.config\" = [\"tools\"]\n"), 0o600))
	report, err := Update(context.Background(), desired, io.Discard, s.sourceOptions(remote))
	s.Require().Error(err)
	s.Equal(StageFailed, report.Configuration.PolicyStage.Status)
	s.Equal(SeverityConfiguration, report.Configuration.Severity)
	s.NoFileExists(target)
	link, readErr := os.Readlink(profile)
	s.Require().NoError(readErr)
	s.Equal("profile-2-link", link)
	s.Equal(recoveryAdvancedLock, mustRead(s.T(), filepath.Join(source, "flake.lock")))
	recovery.assertJournalCleared(state)
}

func configurationApplication(s *ConfigurationLifecycleSuite, report Report, id string) ConfigurationApplication {
	s.T().Helper()
	for _, app := range report.Configuration.Applications {
		if app.ID == id {
			return app
		}
	}
	s.FailNow("configuration application missing", "%s", id)
	return ConfigurationApplication{}
}
