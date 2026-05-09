package ir

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalButane = `variant: fcos
version: "1.6.0"
storage:
  files:
    - path: /etc/magus.d/ollama.env
      mode: 0644
      contents:
        inline: |
          OLLAMA_HOST=0.0.0.0:11434
  directories:
    - path: /var/lib/magus
      mode: 0755
systemd:
  units:
    - name: magus-healthcheck.timer
      enabled: true
      contents: |
        [Unit]
        Description=Magus healthcheck
        [Timer]
        OnBootSec=5min
        [Install]
        WantedBy=timers.target
`

func TestLoadButaneMinimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bu")
	if err := os.WriteFile(path, []byte(minimalButane), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, _, err := LoadButane(path)
	if err != nil {
		t.Fatalf("LoadButane: %v", err)
	}

	if len(got.Files) != 1 {
		t.Fatalf("Files: want 1, got %d", len(got.Files))
	}
	f := got.Files[0]
	if f.Path != "/etc/magus.d/ollama.env" {
		t.Errorf("Files[0].Path = %q", f.Path)
	}
	if f.Mode != 0o644 {
		t.Errorf("Files[0].Mode = %#o, want 0644", f.Mode)
	}
	if string(f.Contents) != "OLLAMA_HOST=0.0.0.0:11434\n" {
		t.Errorf("Files[0].Contents = %q", f.Contents)
	}

	if len(got.Directories) != 1 {
		t.Fatalf("Directories: want 1, got %d", len(got.Directories))
	}
	if got.Directories[0].Path != "/var/lib/magus" {
		t.Errorf("Directories[0].Path = %q", got.Directories[0].Path)
	}

	if len(got.Units) != 1 {
		t.Fatalf("Units: want 1, got %d", len(got.Units))
	}
	u := got.Units[0]
	if u.Name != "magus-healthcheck.timer" {
		t.Errorf("Units[0].Name = %q", u.Name)
	}
	if !u.Enabled {
		t.Errorf("Units[0].Enabled = false, want true")
	}
}

func TestDecodeSourceVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "data:,hello%20world", "hello world"},
		{"base64", "data:;base64,aGVsbG8=", "hello"},
		{"with-mediatype", "data:text/plain;charset=utf-8;base64,aGVsbG8=", "hello"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeSource(c.in)
			if err != nil {
				t.Fatalf("decodeSource(%q): %v", c.in, err)
			}
			if string(got) != c.want {
				t.Errorf("decodeSource(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestDecodeSourceRejectsRemote(t *testing.T) {
	if _, err := decodeSource("https://example.com/foo"); err == nil {
		t.Error("decodeSource: want error on https, got nil")
	}
}

func TestLoadButaneHTTP(t *testing.T) {
	// Serve a known-good Butane file and load it via URL. Verifies the full
	// HTTP path including translation and IR extraction.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(minimalButane))
	}))
	defer srv.Close()

	got, _, err := LoadButane(srv.URL + "/magus.bu")
	if err != nil {
		t.Fatalf("LoadButane: %v", err)
	}
	if len(got.Files) != 1 {
		t.Errorf("Files: want 1, got %d", len(got.Files))
	}
}

func TestLoadButaneHTTPNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not here", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, _, err := LoadButane(srv.URL + "/missing.bu"); err == nil {
		t.Error("LoadButane: want error on HTTP 404, got nil")
	}
}

func TestLoadButaneHTTPSizeCap(t *testing.T) {
	// Serve a body just over the cap. fetchButaneHTTP must refuse rather
	// than silently truncate to a partial Butane file.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", maxButaneSize+1024)))
	}))
	defer srv.Close()

	_, _, err := LoadButane(srv.URL)
	if err == nil {
		t.Fatal("LoadButane: want size-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error message should mention size cap, got: %v", err)
	}
}

func TestIsHTTPURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/foo.bu": true,
		"http://example.com/foo.bu":  true,
		"/etc/magus/magus.bu":        false,
		"./magus.bu":                 false,
		"file:///tmp/foo.bu":         false, // file:// not a thing in v1
		"":                           false,
	}
	for in, want := range cases {
		if got := isHTTPURL(in); got != want {
			t.Errorf("isHTTPURL(%q) = %v, want %v", in, got, want)
		}
	}
}
