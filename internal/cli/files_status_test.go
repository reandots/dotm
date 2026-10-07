package cli

import (
	"bytes"
	"strings"
	"testing"

	"dotm/internal/chezmoi"

	"github.com/stretchr/testify/suite"
)

type filesStatusSuite struct {
	suite.Suite
}

func TestFilesStatusSuite(t *testing.T) {
	suite.Run(t, new(filesStatusSuite))
}

func (s *filesStatusSuite) TestBothDifferencesRemainVisibleWithoutDuplicatingUnsafePath() {
	var out bytes.Buffer
	inventory := chezmoi.InventoryResult{Entries: []chezmoi.InventoryEntry{{
		Path: "/home/test/line\nbreak", Type: "file", Local: 'M', Pending: 'D', Managed: true,
		Applications: []string{"editor"},
	}}}
	s.Require().NoError(printFilesStatus(&out, inventory, false))
	text := out.String()
	s.Equal(1, strings.Count(text, `"/home/test/line\nbreak"`))
	s.NotContains(text, "/home/test/line\nbreak")
	s.Contains(text, "modified")
	s.Contains(text, "deleted")
	s.NotContains(text, "\x1b")
}

func (s *filesStatusSuite) TestWarningsPreventCleanClaim() {
	var clean, warned bytes.Buffer
	s.Require().NoError(printFilesStatus(&clean, chezmoi.InventoryResult{}, false))
	s.Require().NoError(printFilesStatus(&warned, chezmoi.InventoryResult{}, true))
	s.NotEqual(clean.String(), warned.String())
	s.NotContains(warned.String(), "clean")
}

func (s *filesStatusSuite) TestUntrackedLinksAreNotManagedChanges() {
	var out bytes.Buffer
	inventory := chezmoi.InventoryResult{Entries: []chezmoi.InventoryEntry{{
		Path: "/home/test/new", Type: "symlink",
	}}}
	s.Require().NoError(printFilesStatus(&out, inventory, false))
	s.Contains(out.String(), "Untracked files")
	s.Contains(out.String(), "symlink")
	s.NotContains(out.String(), "Local changes")
	s.NotContains(out.String(), "Pending changes from chezmoi")
}
