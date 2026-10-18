package cli

import (
	"fmt"
	"io"
)

func colored(out io.Writer, text, shade string) string {
	if statusColor(out) {
		return shade + text + reset
	}
	return text
}

func printOutcome(out io.Writer, label, message, shade string) {
	fmt.Fprintf(out, "%s %s\n", colored(out, label, shade), message)
}
