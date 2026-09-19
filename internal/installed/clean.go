package installed

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type CleanOptions struct {
	Keep   int
	GC     bool
	DryRun bool
	Yes    bool
}

type generation struct {
	number int
	link   string
	path   string
}

type history struct {
	current   int
	directory string
	link      string
	items     []generation
}

// Clean prunes only the standard XDG profile's history. GC is separately opt-in
// because it can collect unrelated unrooted objects throughout the shared store.
func Clean(ctx context.Context, options CleanOptions, in io.Reader, out, stderr io.Writer) error {
	if options.Keep < 0 {
		return fmt.Errorf("clean: keep count must be nonnegative")
	}
	if !options.DryRun {
		locked, release, lockErr := BeginMutation(ctx)
		if lockErr != nil {
			return lockErr
		}
		defer release()
		ctx = locked
	}
	base, err := stateHome()
	if err != nil {
		return err
	}
	profile := filepath.Join(base, "nix", "profile")
	before, err := readHistory(profile)
	if err != nil {
		return err
	}
	retained, selected := planHistory(before, options.Keep)
	if _, outputErr := fmt.Fprintf(out, "Profile: %s\n", profile); outputErr != nil {
		return outputErr
	}
	if before.current == 0 {
		_, err = fmt.Fprintln(out, "Profile history: absent")
	} else {
		_, err = fmt.Fprintf(
			out,
			"Current generation: %d\nRetained generations: %s\nPrune generations: %s\n",
			before.current,
			formatGenerations(retained),
			formatGenerations(selected),
		)
	}
	if err != nil {
		return err
	}
	if options.GC {
		if _, outputErr := fmt.Fprintln(
			stderr,
			"Warning: store-wide GC may delete unrelated unrooted objects throughout the shared Nix store.",
		); outputErr != nil {
			return outputErr
		}
	}
	if options.DryRun {
		if options.GC {
			if _, outputErr := fmt.Fprintln(
				out,
				"GC dry-run uses current roots before pruning; additional objects may become eligible after pruning.",
			); outputErr != nil {
				return outputErr
			}
			cmd := exec.CommandContext(ctx, "nix", "store", "gc", "--dry-run")
			cmd.Stdout, cmd.Stderr = out, stderr
			if runErr := cmd.Run(); runErr != nil {
				return fmt.Errorf("nix store gc --dry-run: %w", runErr)
			}
		}
		return nil
	}
	if len(selected) == 0 && !options.GC {
		_, outputErr := fmt.Fprintln(out, "Nothing to clean.")
		return outputErr
	}
	if !options.Yes {
		ok, confirmErr := confirmClean(in, stderr)
		if confirmErr != nil || !ok {
			return confirmErr
		}
	}
	current, err := readHistory(profile)
	if err != nil {
		return fmt.Errorf("revalidate profile history: %w", err)
	}
	if !reflect.DeepEqual(before, current) {
		return fmt.Errorf("profile history changed during clean planning; " +
			"rerun clean to review the current generations")
	}
	if len(selected) > 0 {
		args := []string{"--profile", profile, "--delete-generations"}
		for _, n := range selected {
			args = append(args, strconv.Itoa(n))
		}
		cmd := exec.CommandContext(ctx, "nix-env", args...)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		var pruneErr error
		if runErr := cmd.Run(); runErr != nil {
			pruneErr = fmt.Errorf("nix-env --delete-generations: %w", runErr)
		}
		after, inspectErr := readHistory(profile)
		if inspectErr != nil {
			return errors.Join(pruneErr, fmt.Errorf("inspect after prune attempt: %w", inspectErr))
		}
		numbers := make([]int, 0, len(after.items))
		for _, g := range after.items {
			numbers = append(numbers, g.number)
		}
		_, outputErr := fmt.Fprintf(
			out,
			"History after prune attempt — current: %d; generations: %s\n",
			after.current,
			formatGenerations(numbers),
		)
		if pruneErr != nil || outputErr != nil {
			return errors.Join(pruneErr, outputErr)
		}
		expected := before
		expected.items = nil
		for _, g := range before.items {
			i := sort.SearchInts(retained, g.number)
			if i < len(retained) && retained[i] == g.number {
				expected.items = append(expected.items, g)
			}
		}
		if !reflect.DeepEqual(expected, after) {
			return fmt.Errorf(
				"profile history after pruning differs from the approved retained generations; GC was not started",
			)
		}
	}
	if options.GC {
		cmd := exec.CommandContext(ctx, "nix", "store", "gc")
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if runErr := cmd.Run(); runErr != nil {
			return fmt.Errorf("nix store gc: %w; any completed history pruning remains in effect", runErr)
		}
		_, err = fmt.Fprintln(out, "Store-wide garbage collection completed.")
	}
	return err
}

// readHistory accepts only native sibling generation links. A profile alias to
// another profile's history must not turn cleanup into unrelated history pruning.
func readHistory(profile string) (history, error) {
	exists, err := profileExists(profile)
	if err != nil {
		return history{}, fmt.Errorf("inspect profile %s: %w", profile, err)
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(profile))
	if err != nil {
		if !exists && errors.Is(err, os.ErrNotExist) {
			return history{}, nil
		}
		return history{}, fmt.Errorf("resolve profile directory: %w", err)
	}
	profile = filepath.Join(directory, filepath.Base(profile))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return history{}, fmt.Errorf("list profile generations: %w", err)
	}
	h := history{directory: directory}
	if exists {
		h.link, err = os.Readlink(profile)
		if err != nil {
			return history{}, fmt.Errorf("read current generation link: %w", err)
		}
		h.current, err = generationNumber(filepath.Base(h.link))
		if err != nil {
			return history{}, err
		}
		currentLink := h.link
		if !filepath.IsAbs(currentLink) {
			currentLink = filepath.Join(directory, currentLink)
		}
		if filepath.Clean(currentLink) != fmt.Sprintf("%s-%d-link", profile, h.current) {
			return history{}, fmt.Errorf(
				"current generation must reference this profile's sibling history, not %s",
				h.link,
			)
		}
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "profile-") || !strings.HasSuffix(entry.Name(), "-link") {
			continue
		}
		n, err := generationNumber(entry.Name())
		if err != nil {
			return history{}, err
		}
		path := filepath.Join(directory, entry.Name())
		link, err := os.Readlink(path)
		if err != nil {
			return history{}, fmt.Errorf("read generation %d link: %w", n, err)
		}
		dest, err := filepath.EvalSymlinks(path)
		if err != nil {
			return history{}, fmt.Errorf("resolve generation %d: %w", n, err)
		}
		manifestPath := filepath.Join(dest, "manifest.json")
		info, err := os.Stat(manifestPath)
		if err != nil {
			return history{}, fmt.Errorf("generation %d manifest: %w", n, err)
		}
		if !info.Mode().IsRegular() {
			return history{}, fmt.Errorf("generation %d manifest is not a regular file", n)
		}
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return history{}, fmt.Errorf("read generation %d manifest: %w", n, err)
		}
		var native struct {
			Version  int                        `json:"version"`
			Elements map[string]json.RawMessage `json:"elements"`
		}
		if err := json.Unmarshal(data, &native); err != nil {
			return history{}, fmt.Errorf("generation %d manifest: %w", n, err)
		}
		if native.Version != 3 || native.Elements == nil {
			return history{}, fmt.Errorf(
				"generation %d requires a native version 3 profile manifest with an elements object",
				n,
			)
		}
		h.items = append(h.items, generation{number: n, link: link, path: dest})
	}
	if !exists {
		if len(h.items) != 0 {
			return history{}, fmt.Errorf("profile history exists without an active generation: %s", profile)
		}
		return history{}, nil
	}
	sort.Slice(h.items, func(i, j int) bool { return h.items[i].number < h.items[j].number })
	for _, g := range h.items {
		if g.number == h.current {
			return h, nil
		}
	}
	return history{}, fmt.Errorf("current generation %d is absent from profile history", h.current)
}

func generationNumber(name string) (int, error) {
	raw := strings.TrimSuffix(strings.TrimPrefix(name, "profile-"), "-link")
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || name != "profile-"+strconv.Itoa(n)+"-link" {
		return 0, fmt.Errorf("malformed profile generation link %q", name)
	}
	return n, nil
}

func planHistory(h history, keep int) (retained, selected []int) {
	others := 0
	for i := len(h.items) - 1; i >= 0; i-- {
		n := h.items[i].number
		if n == h.current || others < keep {
			retained = append(retained, n)
			if n != h.current {
				others++
			}
		} else {
			selected = append(selected, n)
		}
	}
	sort.Ints(retained)
	sort.Ints(selected)
	return retained, selected
}

func formatGenerations(numbers []int) string {
	if len(numbers) == 0 {
		return "(none)"
	}
	values := make([]string, len(numbers))
	for i, n := range numbers {
		values[i] = strconv.Itoa(n)
	}
	return strings.Join(values, ", ")
}

func confirmClean(in io.Reader, out io.Writer) (bool, error) {
	reader := bufio.NewReader(in)
	for {
		if _, err := fmt.Fprint(out, "Proceed with clean? [y/N/q] "); err != nil {
			return false, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("clean cancelled before mutation: read confirmation: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		case "q", "quit":
			return false, fmt.Errorf("clean cancelled before mutation")
		default:
			if _, err := fmt.Fprintln(out, "Answer y/yes, n/no (default), or q/quit to cancel."); err != nil {
				return false, err
			}
		}
	}
}
