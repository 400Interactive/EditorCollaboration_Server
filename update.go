package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGitHubRepo = "400Interactive/EditorCollaboration_Server"
	githubAPIBase     = "https://api.github.com"
	maxDownloadBytes  = 80 << 20
)

// Set with -ldflags -X when the release is built from the Unreal tree.
var (
	pluginVersion        = ""
	pluginMarketplaceURL = ""
)

type pluginDescriptor struct {
	VersionName    string `json:"VersionName"`
	MarketplaceURL string `json:"MarketplaceURL"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

type releaseInfo struct {
	Version   string
	PageURL   string
	AssetName string
	AssetURL  string
	SumsURL   string
	Newer     bool
}

type updateClient struct {
	http      *http.Client
	apiBase   string
	repo      string
	current   string
	goos      string
	goarch    string
	extraHost string
}

func githubRepo() string {
	if repo := strings.TrimSpace(os.Getenv("COLLAB_GITHUB_REPO")); repo != "" {
		return repo
	}
	return defaultGitHubRepo
}

func defaultUpdateClient() updateClient {
	c := updateClient{
		apiBase: githubAPIBase,
		repo:    githubRepo(),
		current: version,
		goos:    runtime.GOOS,
		goarch:  runtime.GOARCH,
	}
	c.http = &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !c.allowHost(req.URL.Hostname()) {
				return fmt.Errorf("%s", T("update.badURL", req.URL.Host))
			}
			return nil
		},
	}
	return c
}

func (c updateClient) allowHost(host string) bool {
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	if c.extraHost != "" && host == strings.ToLower(c.extraHost) {
		return true
	}
	switch host {
	case "api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "codeload.github.com":
		return true
	}
	return strings.HasSuffix(host, ".githubusercontent.com") || strings.HasSuffix(host, ".github.com")
}

func assetName(goos, goarch string) string {
	name := "collab-server-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// compareVersions returns -1, 0, or 1. A missing component counts as 0, and a leading v is ignored.
func compareVersions(a, b string) int {
	pa, aok := versionParts(a)
	pb, bok := versionParts(b)
	if !aok && !bok {
		return 0
	}
	if !aok {
		return -1
	}
	if !bok {
		return 1
	}
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func versionParts(raw string) ([3]int, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "v")
	raw = strings.TrimPrefix(raw, "V")
	if i := strings.IndexAny(raw, "-+"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return [3]int{}, false
	}
	bits := strings.Split(raw, ".")
	if len(bits) > 3 {
		return [3]int{}, false
	}
	var parts [3]int
	for i, bit := range bits {
		n, err := strconv.Atoi(bit)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		parts[i] = n
	}
	return parts, true
}

func readPluginFile(path string) (versionName, marketplace string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	text := strings.TrimSpace(string(raw))
	if text != "" && !strings.HasPrefix(text, "{") {
		target := text
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		raw, err = os.ReadFile(target)
		if err != nil {
			return "", "", err
		}
	}
	var desc pluginDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return "", "", err
	}
	versionName = strings.TrimSpace(desc.VersionName)
	if versionName == "" {
		return "", "", errors.New("VersionName is empty")
	}
	return versionName, strings.TrimSpace(desc.MarketplaceURL), nil
}

func loadPluginDescriptor() {
	if pluginVersion != "" || pluginMarketplaceURL != "" {
		return
	}
	versionName, marketplace, err := readPluginFile("EditorCollaboration.uplugin")
	if err != nil {
		return
	}
	pluginVersion = versionName
	pluginMarketplaceURL = marketplace
}

func (c updateClient) fetch() (releaseInfo, error) {
	var none releaseInfo
	endpoint, err := url.Parse(strings.TrimRight(c.apiBase, "/") + "/repos/" + c.repo + "/releases/latest")
	if err != nil {
		return none, err
	}
	if !c.allowHost(endpoint.Hostname()) {
		return none, fmt.Errorf("%s", T("update.badURL", endpoint.Host))
	}
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return none, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "collab-server/"+version)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.http.Do(req)
	if err != nil {
		return none, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return none, err
	}
	if resp.StatusCode != http.StatusOK {
		return none, fmt.Errorf("GitHub HTTP %d", resp.StatusCode)
	}
	var payload ghRelease
	if err := json.Unmarshal(body, &payload); err != nil {
		return none, err
	}
	return releaseFromPayload(payload, c.current, c.goos, c.goarch, c.allowHost)
}

func releaseFromPayload(payload ghRelease, current, goos, goarch string, allow func(string) bool) (releaseInfo, error) {
	var info releaseInfo
	info.Version = strings.TrimPrefix(strings.TrimSpace(payload.TagName), "v")
	if _, ok := versionParts(payload.TagName); !ok {
		return info, fmt.Errorf("release tag %q is not a version", payload.TagName)
	}
	info.PageURL = payload.HTMLURL
	info.Newer = compareVersions(info.Version, current) > 0
	info.AssetName = assetName(goos, goarch)
	for _, asset := range payload.Assets {
		switch asset.Name {
		case info.AssetName:
			info.AssetURL = asset.BrowserDownloadURL
		case "SHA256SUMS":
			info.SumsURL = asset.BrowserDownloadURL
		}
	}
	if err := validateDownloadURL(info.AssetURL, allow); err != nil && info.AssetURL != "" {
		return info, err
	}
	if err := validateDownloadURL(info.SumsURL, allow); err != nil && info.SumsURL != "" {
		return info, err
	}
	if info.PageURL != "" {
		if err := validateDownloadURL(info.PageURL, allow); err != nil {
			info.PageURL = ""
		}
	}
	return info, nil
}

func validateDownloadURL(raw string, allow func(string) bool) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("%s", T("update.badURL", raw))
	}
	if !allow(parsed.Hostname()) {
		return fmt.Errorf("%s", T("update.badURL", parsed.Host))
	}
	return nil
}

func (c updateClient) install(info releaseInfo, dest string) error {
	if !info.Newer {
		return nil
	}
	if info.AssetURL == "" {
		return fmt.Errorf("release %s has no %s", info.Version, info.AssetName)
	}
	if info.SumsURL == "" {
		return fmt.Errorf("%s", T("update.noChecksum", info.AssetName))
	}
	if err := validateDownloadURL(info.AssetURL, c.allowHost); err != nil {
		return err
	}
	if err := validateDownloadURL(info.SumsURL, c.allowHost); err != nil {
		return err
	}

	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".collab-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	hash := sha256.New()
	if err := download(c, info.AssetURL, io.MultiWriter(tmp, hash)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	sums, err := downloadString(c, info.SumsURL)
	if err != nil {
		return err
	}
	expected, ok := checksumFor(sums, info.AssetName)
	if !ok {
		return fmt.Errorf("%s", T("update.noChecksum", info.AssetName))
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("%s", T("update.checksum"))
	}
	return replaceExecutable(tmpName, dest)
}

func download(c updateClient, rawURL string, dst io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "collab-server/"+version)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return err
	}
	if n > maxDownloadBytes {
		return errors.New("download is too large")
	}
	return nil
}

func downloadString(c updateClient, rawURL string) (string, error) {
	var buf strings.Builder
	if err := download(c, rawURL, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func checksumFor(sums, name string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		file := strings.TrimPrefix(fields[len(fields)-1], "*")
		file = filepath.Base(strings.ReplaceAll(file, "\\", "/"))
		if file == name && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func currentExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}

func canReplaceExecutable() bool {
	if runningInDocker() {
		return false
	}
	exe, err := currentExecutable()
	if err != nil {
		return false
	}
	probe, err := os.CreateTemp(filepath.Dir(exe), ".collab-writable-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	probe.Close()
	return os.Remove(name) == nil
}

func runningInDocker() bool {
	if os.Getenv("COLLAB_DOCKER") == "1" {
		return true
	}
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func updateInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("COLLAB_UPDATE_INTERVAL"))
	if raw == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute {
		return 24 * time.Hour
	}
	return d
}

func updatesEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("COLLAB_UPDATE_CHECK"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (s *server) watchUpdates() {
	s.checkForUpdate()
	ticker := time.NewTicker(updateInterval())
	defer ticker.Stop()
	for {
		select {
		case <-s.shutdownRequested:
			return
		case <-ticker.C:
			s.checkForUpdate()
		}
	}
}

func (s *server) checkForUpdate() {
	info, err := defaultUpdateClient().fetch()
	if err != nil {
		log.Print(T("update.checkFailed", err))
		return
	}
	s.mu.Lock()
	s.updateChecked = true
	s.updateAvailable = info.Newer
	s.latestVersion = info.Version
	s.releasePage = info.PageURL
	already := s.announced == info.Version
	if info.Newer {
		s.announced = info.Version
	}
	auto := s.autoUpdate
	s.mu.Unlock()
	if !info.Newer || already {
		return
	}
	if runningInDocker() {
		log.Print(T("update.docker", info.Version))
		return
	}
	if auto && canReplaceExecutable() {
		s.installOfferedUpdate()
		return
	}
	log.Print(T("update.available", info.Version))
}

func (s *server) installOfferedUpdate() {
	go func() {
		s.updateMu.Lock()
		if s.updateBusy {
			s.updateMu.Unlock()
			return
		}
		s.updateBusy = true
		s.updateMu.Unlock()
		defer func() {
			s.updateMu.Lock()
			s.updateBusy = false
			s.updateMu.Unlock()
		}()

		client := defaultUpdateClient()
		info, err := client.fetch()
		if err != nil {
			log.Print(T("update.checkFailed", err))
			s.forgetAnnouncement()
			return
		}
		if !info.Newer {
			return
		}
		exe, err := currentExecutable()
		if err != nil {
			log.Print(T("update.checkFailed", err))
			s.forgetAnnouncement()
			return
		}
		if err := client.install(info, exe); err != nil {
			log.Print(T("update.checkFailed", err))
			s.forgetAnnouncement()
			return
		}
		log.Print(T("update.restarting", info.Version))
		s.mu.Lock()
		s.restartOnExit = true
		s.mu.Unlock()
		s.requestShutdown()
	}()
}

func (s *server) forgetAnnouncement() {
	s.mu.Lock()
	s.announced = ""
	s.mu.Unlock()
}

func (s *server) offeredUpdate() (versionName, page string, available bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestVersion, s.releasePage, s.updateAvailable
}

func runCheckUpdate() int {
	info, err := defaultUpdateClient().fetch()
	if err != nil {
		fmt.Fprintln(os.Stderr, T("update.checkFailed", err))
		return 1
	}
	if !info.Newer {
		fmt.Println(T("update.upToDate", version))
		return 0
	}
	fmt.Println(T("update.available", info.Version))
	return 2
}

func runApplyUpdate() int {
	client := defaultUpdateClient()
	info, err := client.fetch()
	if err != nil {
		fmt.Fprintln(os.Stderr, T("update.checkFailed", err))
		return 1
	}
	if !info.Newer {
		fmt.Println(T("update.upToDate", version))
		return 0
	}
	exe, err := currentExecutable()
	if err != nil {
		fmt.Fprintln(os.Stderr, T("update.checkFailed", err))
		return 1
	}
	if !canReplaceExecutable() {
		fmt.Fprintln(os.Stderr, T("update.notWritable", exe))
		return 1
	}
	if err := client.install(info, exe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if restartSystemdIfActive() {
		fmt.Println(T("update.restarting", info.Version))
		return 0
	}
	fmt.Println(T("update.installed", info.Version))
	return 0
}

func restartArgs() []string {
	args := make([]string, 0, len(os.Args))
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-update", "-check-update", "-healthcheck", "-stop":
			continue
		default:
			args = append(args, os.Args[i])
		}
	}
	return args
}
