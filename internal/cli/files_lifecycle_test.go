package cli_test

import (
	"os"
	"path/filepath"

	"dotm/internal/cli"
)

func (s *FilesCLISuite) TestLifecycleConfigOnlyAppliesManagedFileAndRunsHooks() {
	marker := filepath.Join(s.base, "hooks.log")
	hook := filepath.Join(s.dataDir, "hook.sh")
	// #nosec G306 -- Private executable configuration hook fixture.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\necho called >> \"$HOOK_MARKER\"\n"), 0o700))
	s.T().Setenv("HOOK_MARKER", marker)
	s.Require().NoError(os.MkdirAll(filepath.Join(s.home, ".config"), 0o700))
	s.write(filepath.Join(s.source, "dot_config", "lifecycle"), "applied\n")
	s.commitLifecycleSource(s.source, "lifecycle source")
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[app]\nnix=[]\nconfigs=['~/.config/lifecycle']\npre_cfg_hook='hook.sh'\npost_cfg_hook='hook.sh'\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"app\"]"))
	output, err := s.run("update", "cfg")
	s.Require().NoError(err, output)
	contents, readErr := os.ReadFile(filepath.Join(s.home, ".config", "lifecycle"))
	s.Require().NoError(readErr)
	s.Equal("applied\n", string(contents))
	log, readErr := os.ReadFile(marker)
	s.Require().NoError(readErr)
	s.Equal("called\ncalled\n", string(log))
	policy, readErr := os.ReadFile(s.policyPath)
	s.Require().NoError(readErr)
	s.NotEmpty(policy)
}

func (s *FilesCLISuite) TestLifecyclePolicyErrorStopsApplyAfterPreHook() {
	marker := filepath.Join(s.base, "hooks.log")
	hook := filepath.Join(s.dataDir, "hook.sh")
	// #nosec G306 -- Private executable configuration hook fixture.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\necho called >> \"$HOOK_MARKER\"\n"), 0o700))
	s.T().Setenv("HOOK_MARKER", marker)
	s.write(filepath.Join(s.source, "dot_config", "blocked"), "must not apply\n")
	s.commitLifecycleSource(s.source, "blocked source")
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[app]\nnix=[]\nconfigs=['~/.config/blocked']\npre_cfg_hook='hook.sh'\n"+
			"post_cfg_hook='hook.sh'\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"app\"]"))
	s.write(filepath.Join(s.configHome, "dotm", "files.toml"), "[unmanaged]\n'~/.config' = ['blocked']\n")
	output, err := s.run("update", "cfg")
	s.Require().Error(err, output)
	s.NoFileExists(filepath.Join(s.home, ".config", "blocked"))
	log, readErr := os.ReadFile(marker)
	s.Require().NoError(readErr)
	s.Equal("called\n", string(log), "pre-hook runs before policy error; post-hook must not run")
}

func (s *FilesCLISuite) TestLifecycleWarningsPermitApply() {
	s.Require().NoError(os.MkdirAll(filepath.Join(s.home, ".config"), 0o700))
	s.write(filepath.Join(s.source, "dot_config", "warning-case"), "applied\n")
	s.commitLifecycleSource(s.source, "warning source")
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[app]\nnix=[]\nconfigs=['~/.config/warning-case']\n"+
			"unmanaged=['~/.cache/example','~/.cache/example/child']\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"app\"]"))
	output, err := s.run("update", "cfg")
	s.Require().NoError(err, output)
	s.Contains(output, "warning:")
	contents, readErr := os.ReadFile(filepath.Join(s.home, ".config", "warning-case"))
	s.Require().NoError(readErr)
	s.Equal("applied\n", string(contents))
}

func (s *FilesCLISuite) TestRepeatedCombinedLifecycleRefreshesAndRunsHooks() {
	marker := filepath.Join(s.base, "hooks.log")
	hook := filepath.Join(s.dataDir, "hook.sh")
	// #nosec G306 -- Private executable configuration hook fixture.
	s.Require().NoError(os.WriteFile(hook, []byte("#!/bin/sh\necho called >> \"$HOOK_MARKER\"\n"), 0o700))
	s.T().Setenv("HOOK_MARKER", marker)
	s.Require().NoError(os.MkdirAll(filepath.Join(s.home, ".config"), 0o700))
	s.write(filepath.Join(s.source, "dot_config", "cycle"), "cycle one\n")
	s.commitLifecycleSource(s.source, "cycle one")
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[app]\nnix=[]\nconfigs=['~/.config/cycle']\npre_cfg_hook='hook.sh'\npost_cfg_hook='hook.sh'\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"app\"]"))
	for _, content := range []string{"cycle one\n", "cycle two\n"} {
		if content == "cycle two\n" {
			// Push a remote revision from a separate checkout while the local source stays clean.
			other := filepath.Join(s.base, "other")
			s.git("clone", "--quiet", s.remote, other)
			s.write(filepath.Join(other, "dot_config", "cycle"), content)
			s.commitLifecycleSource(other, "cycle two")
		}
		output, err := s.run("-ui")
		s.Require().NoError(err, output)
		contentsOnDisk, readErr := os.ReadFile(filepath.Join(s.home, ".config", "cycle"))
		s.Require().NoError(readErr)
		s.Equal(content, string(contentsOnDisk))
	}
	log, err := os.ReadFile(marker)
	s.Require().NoError(err)
	s.Equal("called\ncalled\ncalled\ncalled\ncalled\ncalled\ncalled\ncalled\n", string(log))
}

func (s *FilesCLISuite) commitLifecycleSource(checkout, message string) {
	s.T().Helper()
	s.git("-C", checkout, "add", "--all")
	s.git("-C", checkout, "-c", "user.name=Files Test", "-c", "user.email=files@example.invalid",
		"commit", "--quiet", "-m", message)
	s.git("-C", checkout, "push", "--quiet")
}

func (s *FilesCLISuite) TestCombinedPolicyFailureThenRepairRetainsFailureAndReloadsHookInput() {
	s.Require().NoError(os.MkdirAll(filepath.Join(s.home, ".config"), 0o700))
	s.write(filepath.Join(s.source, "dot_config", "repair"), "applied after repair\n")
	s.commitLifecycleSource(s.source, "repairable policy")
	pre := "#!/bin/sh\necho pre >> \"$HOME/cycles.log\"\n" +
		"if [ ! -e \"$HOME/first-cycle\" ]; then\n" +
		"  : > \"$HOME/first-cycle\"\n" +
		"  printf \"[unmanaged]\\n'~/.config' = ['repair']\\n\" > \"$XDG_CONFIG_HOME/dotm/files.toml\"\n" +
		"else\n  printf '[unmanaged]\\n' > \"$XDG_CONFIG_HOME/dotm/files.toml\"\nfi\n"
	// #nosec G306 -- Private executable hook changes isolated local policy input.
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "prepare.sh"), []byte(pre), 0o700))
	// #nosec G306 -- Private executable hook records successful post-configuration work.
	s.Require().NoError(os.WriteFile(filepath.Join(s.dataDir, "post.sh"),
		[]byte("#!/bin/sh\necho post >> \"$HOME/cycles.log\"\n"), 0o700))
	s.write(filepath.Join(s.dataDir, "catalog.toml"),
		"[app]\nconfigs=['~/.config/repair']\npre_cfg_hook='prepare.sh'\npost_cfg_hook='post.sh'\n")
	s.write(filepath.Join(s.dataDir, "configurations.toml"), configurationManifest(s.remote, "[\"app\"]"))
	output, err := s.run("-ui")
	s.Require().Error(err, output)
	s.Equal(1, cli.ExitCode(err))
	content, readErr := os.ReadFile(filepath.Join(s.home, ".config", "repair"))
	s.Require().NoError(readErr)
	s.Equal("applied after repair\n", string(content))
	log, readErr := os.ReadFile(filepath.Join(s.home, "cycles.log"))
	s.Require().NoError(readErr)
	s.Equal("pre\npre\npost\n", string(log))
}
