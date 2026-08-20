package builtin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/internal/extension"
)

var (
	ErrUpdateFailed = errors.New("cannot update")

	ErrChecksumMismatch = errors.New("checksum mismatch")

	// ErrNotWritable is separate because the fix is different: the binary is
	// somewhere the user cannot write, so a package manager or sudo owns it.
	ErrNotWritable = errors.New("cannot replace the running binary")
)

const releasesAPI = "https://api.github.com/repos/LeaflowNET/leaflow/releases/latest"

func newUpdateCommand(ext *extension.Context, version string) *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "update to the latest release",
		Long: `Replace this binary with the latest release.

The download is verified against the release's published checksums before
anything is replaced, and the replacement is a rename, so an interrupted
update leaves the existing binary in place.

Installed through a package manager, update through it instead: this would
replace a file it believes it owns.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&check, "check", false, "report whether an update exists, without installing it")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		latest, err := latestVersion(cmd.Context())
		if err != nil {
			return err
		}

		current := strings.TrimPrefix(version, "v")
		newest := strings.TrimPrefix(latest, "v")

		result := map[string]any{
			"current":   current,
			"latest":    newest,
			"available": current != newest,
		}

		// A development build has no version to compare against, so refuse
		// rather than "update" a binary built from a working tree.
		if current == "dev" {
			result["available"] = false
			result["note"] = "this is a development build; update does nothing"

			return ext.Printer.Print(result)
		}

		if check || current == newest {
			return ext.Printer.Print(result)
		}

		path, err := replaceSelf(cmd.Context(), latest)
		if err != nil {
			return err
		}

		result["updated"] = true
		result["path"] = path

		return ext.Printer.Print(result)
	}

	return cmd
}

func latestVersion(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return "", err
	}

	request.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 30 * time.Second}

	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: the releases API returned %d", ErrUpdateFailed, response.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}

	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("%w: no release found", ErrUpdateFailed)
	}

	return release.TagName, nil
}

func replaceSelf(ctx context.Context, version string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}

	// Resolved because a symlink means the real binary lives elsewhere, and
	// replacing the link would leave the original behind.
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	// Checked before downloading, so a Homebrew or system install fails in a
	// second rather than after pulling twenty megabytes.
	if err := writable(filepath.Dir(self)); err != nil {
		return "", err
	}

	number := strings.TrimPrefix(version, "v")
	name := fmt.Sprintf("leaflow_%s_%s_%s.tar.gz", number, runtime.GOOS, runtime.GOARCH)
	base := "https://github.com/LeaflowNET/leaflow/releases/download/" + version

	archive, err := download(ctx, base+"/"+name)
	if err != nil {
		return "", err
	}

	sums, err := download(ctx, base+"/checksums.txt")
	if err != nil {
		return "", err
	}

	if err := verify(archive, sums, name); err != nil {
		return "", err
	}

	binary, err := extractBinary(archive)
	if err != nil {
		return "", err
	}

	// Written beside the target so the rename stays on one filesystem, and
	// renamed over it: replacing a running binary this way is safe, because the
	// running process keeps the inode it already opened.
	staged := self + ".new"

	if err := os.WriteFile(staged, binary, 0o755); err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotWritable, err)
	}

	if err := os.Rename(staged, self); err != nil {
		_ = os.Remove(staged)

		return "", fmt.Errorf("%w: %v", ErrNotWritable, err)
	}

	return self, nil
}

// writable reports whether a new file can be created in dir, which is what the
// rename needs — the binary's own mode says nothing about that.
func writable(dir string) error {
	probe, err := os.CreateTemp(dir, ".leaflow-update-*")
	if err != nil {
		return fmt.Errorf("%w: %s is not writable; update through whatever installed it", ErrNotWritable, dir)
	}

	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)

	return nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned %d", ErrUpdateFailed, url, response.StatusCode)
	}

	// Bounded so a wrong URL answering with something enormous cannot exhaust
	// memory. Releases are a few megabytes.
	return io.ReadAll(io.LimitReader(response.Body, 128<<20))
}

func verify(archive, sums []byte, name string) error {
	sum := sha256.Sum256(archive)
	actual := hex.EncodeToString(sum[:])

	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}

		if strings.TrimPrefix(fields[1], "*") != name {
			continue
		}

		if fields[0] != actual {
			return fmt.Errorf("%w for %s: expected %s, got %s", ErrChecksumMismatch, name, fields[0], actual)
		}

		return nil
	}

	return fmt.Errorf("%w: no checksum published for %s", ErrUpdateFailed, name)
}

func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUpdateFailed, err)
	}
	defer gz.Close()

	reader := tar.NewReader(gz)

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUpdateFailed, err)
		}

		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "leaflow" {
			continue
		}

		return io.ReadAll(io.LimitReader(reader, 128<<20))
	}

	return nil, fmt.Errorf("%w: the archive contains no leaflow binary", ErrUpdateFailed)
}
