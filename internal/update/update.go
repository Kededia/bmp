// Package update installs new BMP releases from GitHub.
package update

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is the release tag the release workflow builds in. Builds from
// source stay "dev" and never update themselves.
var Version = "dev"

const (
	latestRelease = "https://api.github.com/repos/Kededia/bmp/releases/latest"
	releaseAsset  = "bmp-windows-x64.zip"
)

// Cleanup removes the copy of bmp.exe the previous update set aside, which
// Windows kept while it ran.
func Cleanup() {
	if executable, err := os.Executable(); err == nil {
		_ = os.Remove(executable + ".old")
	}
}

// Install downloads and installs the latest release when it is newer than
// this build, returning its tag, or "" when this build is current. Windows
// does not overwrite a running program but lets it be renamed, so bmp.exe is
// set aside as bmp.exe.old and the new one takes its place for the next run.
func Install(ctx context.Context) (string, error) {
	if Version == "dev" {
		return "", nil
	}
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	data, err := download(ctx, latestRelease, 1<<20)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return "", fmt.Errorf("read latest release: %w", err)
	}
	if !newer(release.Tag, Version) {
		return "", nil
	}
	assetURL := ""
	for _, asset := range release.Assets {
		if asset.Name == releaseAsset {
			assetURL = asset.URL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("release %s has no %s yet", release.Tag, releaseAsset)
	}
	data, err = download(ctx, assetURL, 200<<20)
	if err != nil {
		return "", err
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open %s: %w", releaseAsset, err)
	}
	files := make(map[string][]byte)
	for _, file := range archive.File {
		name := path.Base(file.Name)
		if name != "bmp.exe" && name != "install-deps.cmd" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return "", err
		}
		files[name], err = io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return "", err
		}
	}
	if len(files["bmp.exe"]) == 0 {
		return "", fmt.Errorf("%s has no bmp.exe", releaseAsset)
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(executable+".new", files["bmp.exe"], 0o755); err != nil {
		return "", fmt.Errorf("save the update: %w", err)
	}
	_ = os.Remove(executable + ".old")
	if err := os.Rename(executable, executable+".old"); err != nil {
		_ = os.Remove(executable + ".new")
		return "", fmt.Errorf("set aside bmp.exe: %w", err)
	}
	if err := os.Rename(executable+".new", executable); err != nil {
		_ = os.Rename(executable+".old", executable)
		return "", fmt.Errorf("install the update: %w", err)
	}
	if script := files["install-deps.cmd"]; len(script) > 0 {
		_ = os.WriteFile(filepath.Join(filepath.Dir(executable), "install-deps.cmd"), script, 0o644)
	}
	// Later checks compare with what is installed now, not with the
	// version still running.
	Version = release.Tag
	return release.Tag, nil
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// GitHub's API refuses requests without a user agent.
	request.Header.Set("User-Agent", "BMP/"+Version)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}

// newer reports whether tag is a later vMAJOR.MINOR.PATCH than current.
func newer(tag, current string) bool {
	parse := func(value string) ([3]int, error) {
		var parts [3]int
		fields := strings.Split(strings.TrimPrefix(value, "v"), ".")
		if len(fields) != 3 {
			return parts, errors.New("not a version")
		}
		for index, field := range fields {
			number, err := strconv.Atoi(field)
			if err != nil {
				return parts, err
			}
			parts[index] = number
		}
		return parts, nil
	}
	latest, err := parse(tag)
	if err != nil {
		return false
	}
	installed, err := parse(current)
	if err != nil {
		return false
	}
	for index := range latest {
		if latest[index] != installed[index] {
			return latest[index] > installed[index]
		}
	}
	return false
}
