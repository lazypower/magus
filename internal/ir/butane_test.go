package ir

import (
	"os"
	"path/filepath"
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
