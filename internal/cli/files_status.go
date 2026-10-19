package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"dotm/internal/chezmoi"
)

func printFilesStatus(out io.Writer, inventory chezmoi.InventoryResult, warnings bool) error {
	var local, pending, unknown []chezmoi.InventoryEntry
	for _, entry := range inventory.Entries {
		if !entry.Managed {
			unknown = append(unknown, entry)
		} else if entry.Local != ' ' && entry.Local != 0 {
			local = append(local, entry)
		} else if entry.Pending != ' ' && entry.Pending != 0 {
			pending = append(pending, entry)
		}
	}

	home, _ := os.UserHomeDir()
	if home != "" && filepath.IsAbs(home) {
		home = filepath.Clean(home)
	} else {
		home = ""
	}
	color := statusColor(out)
	if len(local)+len(pending)+len(unknown) == 0 {
		message := "Filesystem status: clean"
		if warnings {
			message = "No filesystem findings (policy warnings were reported)"
		}
		_, err := fmt.Fprintln(out, message)
		return err
	}

	printGroup := func(title, hint string, entries []chezmoi.InventoryEntry, shade string) error {
		if len(entries) == 0 {
			return nil
		}
		if !color {
			shade = ""
		}
		end := ""
		if shade != "" {
			end = reset
		}
		if _, err := fmt.Fprintf(out, "%s%s%s\n", shade, title, end); err != nil {
			return err
		}
		for _, entry := range entries {
			label := "changed"
			state := entry.Local
			if state == ' ' || state == 0 {
				state = entry.Pending
			}
			switch state {
			case 'M':
				label = "modified"
			case 'A', '?':
				label = "added"
			case 'D':
				label = "deleted"
			}
			typeName := entry.Type
			if typeName == "" {
				typeName = "other"
			}
			entryColor := ""
			if color {
				entryColor = yellow
				if !entry.Managed {
					entryColor = red
				}
			}
			entryReset := ""
			if entryColor != "" {
				entryReset = reset
			}
			path := entry.Path
			if home != "" {
				if path == home {
					path = "~"
				} else if relative, found := strings.CutPrefix(path, home+string(filepath.Separator)); found {
					path = "~/" + relative
				}
			}
			// Escape line breaks and terminal controls without surrounding ordinary paths in quotes.
			path = strconv.QuoteToGraphic(path)
			path = path[1 : len(path)-1]
			var writeErr error
			if entry.Managed {
				_, writeErr = fmt.Fprintf(out, "  %s%-8s %-9s %s%s", entryColor, label, typeName, path, entryReset)
			} else {
				_, writeErr = fmt.Fprintf(out, "  %s%-9s %s%s", entryColor, typeName, path, entryReset)
			}
			if writeErr != nil {
				return writeErr
			}
			if entry.Managed {
				if entry.Local != ' ' && entry.Local != 0 && entry.Pending != ' ' && entry.Pending != 0 {
					_, pendingErr := fmt.Fprintf(out, " (pending chezmoi change: %s)", statusLabel(entry.Pending))
					if pendingErr != nil {
						return pendingErr
					}
				}
				if len(entry.Applications) != 0 {
					if _, err := fmt.Fprintf(out, " [owners: %s]", strings.Join(entry.Applications, ", ")); err != nil {
						return err
					}
				}
			}
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(out, "  %s\n\n", hint); err != nil {
			return err
		}
		return nil
	}
	localHint := "Review with `chezmoi diff`; save local files with `chezmoi add` or stop tracking a deleted file " +
		"with `chezmoi forget`. Direct `chezmoi apply` may overwrite local changes; " +
		"dotm's guarded apply does not discard them."
	if err := printGroup("Local changes", localHint, local, yellow); err != nil {
		return err
	}
	pendingHint := "Review desired changes with `chezmoi diff`."
	if err := printGroup("Pending changes from chezmoi", pendingHint, pending, yellow); err != nil {
		return err
	}
	unknownHint := "Acknowledge retained data with native ignores or dotm unmanaged declarations. " +
		"To track a profile-excluded path, select its owning profile first, then use `chezmoi add`."
	return printGroup("Untracked files", unknownHint, unknown, red)
}

func statusLabel(state byte) string {
	switch state {
	case 'M':
		return "modified"
	case 'A', '?':
		return "added"
	case 'D':
		return "deleted"
	default:
		return "changed"
	}
}

func statusColor(out io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return false
	}
	writer, ok := out.(*checkedWriter)
	if ok {
		out = writer.Writer
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	var terminal syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), syscall.TCGETS,
		uintptr(unsafe.Pointer(&terminal)))
	return errno == 0
}

const (
	green  = "\x1b[1;32m"
	yellow = "\x1b[1;33m"
	red    = "\x1b[1;31m"
	reset  = "\x1b[0m"
)
