package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.2.0", 0},
		{"v1.2.0", "1.2.0", 0},
		{"1.2", "1.2.0", 0},
		{"1.2.0", "1.3.0", -1},
		{"1.10.0", "1.9.0", 1},
		{"1.2.1", "1.2", 1},
		{"", "1.0.0", -1},
		{"nope", "1.0.0", -1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestAssetName(t *testing.T) {
	if assetName("linux", "amd64") != "collab-server-linux-amd64" {
		t.Fatal(assetName("linux", "amd64"))
	}
	if assetName("windows", "amd64") != "collab-server-windows-amd64.exe" {
		t.Fatal(assetName("windows", "amd64"))
	}
}

func TestReleaseFromPayloadRejectsForeignHost(t *testing.T) {
	payload := ghRelease{
		TagName: "v9.0.0",
		HTMLURL: "https://github.com/400Interactive/EditorCollaboration_Server/releases/tag/v9.0.0",
		Assets: []ghAsset{{
			Name:               "collab-server-linux-amd64",
			BrowserDownloadURL: "https://evil.example/collab-server-linux-amd64",
		}},
	}
	_, err := releaseFromPayload(payload, "1.2.0", "linux", "amd64", func(host string) bool {
		return host == "github.com" || strings.HasSuffix(host, ".githubusercontent.com")
	})
	if err == nil || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("expected a rejected URL, got %v", err)
	}
}

func TestFetchAndInstall(t *testing.T) {
	body := []byte("new-binary")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/collab/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user agent")
		}
		_ = json.NewEncoder(w).Encode(ghRelease{
			TagName: "v2.0.0",
			HTMLURL: srv.URL + "/release",
			Assets: []ghAsset{
				{Name: "collab-server-linux-arm64", BrowserDownloadURL: srv.URL + "/bin"},
				{Name: "SHA256SUMS", BrowserDownloadURL: srv.URL + "/SHA256SUMS"},
			},
		})
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	mux.HandleFunc("/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(hash + "  collab-server-linux-arm64\n"))
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	host := mustHost(t, srv.URL)
	client := updateClient{
		http:      srv.Client(),
		apiBase:   srv.URL,
		repo:      "acme/collab",
		current:   "1.2.0",
		goos:      "linux",
		goarch:    "arm64",
		extraHost: host,
	}
	info, err := client.fetch()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Newer || info.Version != "2.0.0" || info.AssetName != "collab-server-linux-arm64" {
		t.Fatalf("unexpected release %+v", info)
	}

	dest := filepath.Join(t.TempDir(), "collab-server")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := client.install(info, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("installed %q", got)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin":
			_, _ = w.Write([]byte("payload"))
		default:
			_, _ = w.Write([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  collab-server-linux-amd64\n"))
		}
	}))
	defer srv.Close()
	host := mustHost(t, srv.URL)
	client := updateClient{http: srv.Client(), extraHost: host, goos: "linux", goarch: "amd64"}
	info := releaseInfo{
		Version: "2.0.0", Newer: true, AssetName: "collab-server-linux-amd64",
		AssetURL: srv.URL + "/bin", SumsURL: srv.URL + "/SHA256SUMS",
	}
	dest := filepath.Join(t.TempDir(), "collab-server")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := client.install(info, dest); err == nil {
		t.Fatal("expected a checksum error")
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "old" {
		t.Fatalf("destination was replaced with %q", got)
	}
}

func TestInstallRequiresChecksum(t *testing.T) {
	client := updateClient{goos: "linux", goarch: "amd64"}
	err := client.install(releaseInfo{Version: "2.0.0", Newer: true, AssetName: "collab-server-linux-amd64", AssetURL: "https://github.com/x"}, "unused")
	if err == nil {
		t.Fatal("expected a missing checksum error")
	}
}

func TestReadPluginFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.uplugin")
	raw := []byte(`{"VersionName":"1.2","MarketplaceURL":"https://www.fab.com/example"}`)
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	versionName, marketplace, err := readPluginFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if versionName != "1.2" || marketplace != "https://www.fab.com/example" {
		t.Fatalf("got %s %s", versionName, marketplace)
	}

	link := filepath.Join(dir, "EditorCollaboration.uplugin")
	if err := os.WriteFile(link, []byte("real.uplugin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	versionName, marketplace, err = readPluginFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if versionName != "1.2" || marketplace != "https://www.fab.com/example" {
		t.Fatalf("link got %s %s", versionName, marketplace)
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Hostname()
}
