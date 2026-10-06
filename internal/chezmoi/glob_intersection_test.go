package chezmoi

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type globIntersectionSuite struct{ suite.Suite }

func TestGlobIntersectionSuite(t *testing.T) {
	suite.Run(t, new(globIntersectionSuite))
}

func (s *globIntersectionSuite) TestAbsentRootsAndDescendants() {
	for _, test := range []struct {
		rule, root string
		want       bool
	}{
		{"ack/future", "ack", true},
		{"other/**", "ack", false},
		{"ack", "ack", true},
		{"ack/**/future", "ack", true},
		{"{else,{ack,third}}/future", "ack", true},
		{"{ack}/future", "ack", true},
		{"{,ack/}future", "ack", true},
		{"a[c-k]k/*", "ack", true},
		{"a[!c]k/*", "ack", false},
		{"a[^d]k/*", "ack", true},
		{"ack[/]future", "ack", true},
		{`ack/\*`, "ack", true},
		{`ack/\*`, "ack/other", false},
		{"ack/*", "ack/child", true},
		{"ack/*", "ack/child/grandchild", false},
		{"ack/**", "ack/child/grandchild", true},
		{"ack/**", "ack", true},
		{"ack/**/", "ack", true},
		{"ack/**/", "ack/child", false},
		{"ack**/future", "ack/child", false},
		{"**/future", "ack/child", true},
		{"{**,else}/future", "ack/child", true},
		{`ack/\{literal\}`, "ack/{literal}", true},
		{`ack/\{literal\}`, "ack/literal", false},
		{`ack/\[x\]`, "ack/[x]", true},
		{`ack/[\]]`, "ack/]", true},
		{`ack/[\-]`, "ack/-", true},
		{`ack/\\`, `ack/\`, true},
		{"[а-я]к/**", "як", true},
		{"[а-я]к/**", "яx", false},
		{`\!ack/**`, "!ack", true},
		{"ack[/]", "ack", false},
		{"ack/[.]", "ack", false},
		{"ack/[.][.]", "ack", false},
		{"ack/[.][.][.]", "ack", true},
	} {
		s.Run(test.rule+"@"+test.root, func() {
			actual, err := nativeNegationIntersects(test.rule, test.root)
			s.Require().NoError(err)
			s.Equal(test.want, actual)
		})
	}
}

func (s *globIntersectionSuite) TestMalformedRulesFailClosed() {
	for _, rule := range []string{"ack/{child", "ack/[abc", "ack/\\", "ack/[]", "ack/child}"} {
		s.Run(rule, func() {
			_, err := nativeNegationIntersects(rule, "ack")
			s.Error(err)
		})
	}
}
