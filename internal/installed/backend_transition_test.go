package installed

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"dotm/internal/chezmoi"
	"dotm/internal/manifest"
)

func (s *ConfigurationLifecycleSuite) TestUpdatedProfileBackendReplacesOldExecutableAfterPackageCommit() {
	for _, compatible := range []bool{true, false} {
		s.Run(strconv.FormatBool(compatible), func() {
			dir := s.T().TempDir()
			target := filepath.Join(dir, ".config", "tools")
			s.Require().NoError(os.MkdirAll(filepath.Dir(target), 0o700))
			desired := s.fixtureAt(dir, map[string]manifest.Application{
				"tools": {ID: "tools", Packages: []manifest.Package{{Attr: "hello"}}, Configs: []string{target}},
			}, "tools")
			remote := s.repository(dir, map[string]string{"dot_config/tools": "new backend applied\n"})
			backend, err := exec.LookPath("chezmoi")
			s.Require().NoError(err)
			git, err := exec.LookPath("git")
			s.Require().NoError(err)
			recovery := &UpdateRecoverySuite{}
			recovery.SetT(s.T())
			state, profile, source, _ := recovery.updateFixture(false)
			bin := filepath.Join(filepath.Dir(state), "bin")
			s.Require().NoError(os.Symlink(git, filepath.Join(bin, "git")))
			log := filepath.Join(dir, "backend.log")
			var oldExecutable string
			for _, generation := range []int{1, 2} {
				generationBin := filepath.Join(filepath.Dir(state), "generation-"+strconv.Itoa(generation), "bin")
				s.Require().NoError(os.MkdirAll(generationBin, 0o700))
				executable := filepath.Join(generationBin, "chezmoi")
				body := fmt.Sprintf("#!/bin/sh\nprintf '%d\\n' >> %q\n", generation, log)
				if generation == 2 && compatible {
					body += fmt.Sprintf("exec %q \"$@\"\n", backend)
				} else {
					body += "exit 91\n"
				}
				// #nosec G306 -- Private versioned backend wrappers observe actual executed generation.
				s.Require().NoError(os.WriteFile(executable, []byte(body), 0o700))
				if generation == 1 {
					oldExecutable = executable
				}
			}
			// Deliberately carry a stale executable override; configuration must replace it.
			ctx := chezmoi.WithExecutable(context.Background(), oldExecutable)
			report, updateErr := Update(ctx, desired, io.Discard, s.sourceOptions(remote))
			if compatible {
				s.Require().NoError(updateErr)
				s.Equal("new backend applied\n", mustRead(s.T(), target))
			} else {
				s.Require().Error(updateErr)
				s.Equal(SeverityConfiguration, report.Configuration.Severity)
				s.Equal(StageFailed, report.Configuration.SourceStage.Status)
				s.NoFileExists(target)
			}
			observed := mustRead(s.T(), log)
			s.NotContains(observed, "1\n", "the pre-update backend must never be executed")
			s.Contains(observed, "2\n")
			link, linkErr := os.Readlink(profile)
			s.Require().NoError(linkErr)
			s.Equal("profile-2-link", link)
			s.Equal(recoveryAdvancedLock, mustRead(s.T(), filepath.Join(source, "flake.lock")))
			recovery.assertJournalCleared(state)
		})
	}
}
