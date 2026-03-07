package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/richardsondx/IronLark/internal/buildinfo"
)

type Client struct {
	HTTPClient *http.Client
	RepoSlug   string
}

type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (c Client) LatestRelease(ctx context.Context) (Release, error) {
	repo := c.repoSlug()
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Release{}, fmt.Errorf("fetch latest release: %s", resp.Status)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return Release{}, err
	}
	return release, nil
}

func (c Client) UpdateExecutable(ctx context.Context, executablePath string) (string, error) {
	release, err := c.LatestRelease(ctx)
	if err != nil {
		return "", err
	}
	assetName, err := buildinfo.AssetName()
	if err != nil {
		return "", err
	}
	downloadURL := ""
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return "", fmt.Errorf("release %s does not contain asset %s", release.TagName, assetName)
	}
	targetPath, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		targetPath = executablePath
	}
	tmpDir, err := os.MkdirTemp("", "ironlark-update-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, assetName)
	if err := c.downloadFile(ctx, downloadURL, archivePath); err != nil {
		return "", err
	}
	extractedPath, err := extractBinary(archivePath, tmpDir, "lark")
	if err != nil {
		return "", err
	}
	if err := replaceExecutable(targetPath, extractedPath); err != nil {
		return "", err
	}
	return release.TagName, nil
}

func (c Client) downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download release asset: %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func extractBinary(archivePath, destDir, binaryName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}
		outPath := filepath.Join(destDir, binaryName)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return "", err
		}
		if err := out.Close(); err != nil {
			return "", err
		}
		return outPath, nil
	}
	return "", fmt.Errorf("binary %s not found in archive", binaryName)
}

func replaceExecutable(targetPath, sourcePath string) error {
	info, err := os.Stat(targetPath)
	if err != nil {
		return err
	}
	tmpTarget := targetPath + ".tmp"
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmpTarget, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmpTarget, targetPath)
}

func (c Client) repoSlug() string {
	if strings.TrimSpace(c.RepoSlug) != "" {
		return c.RepoSlug
	}
	return buildinfo.RepoSlug
}

func (c Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}
