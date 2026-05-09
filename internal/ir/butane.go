package ir

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	butane "github.com/coreos/butane/config"
	"github.com/coreos/butane/config/common"
	"github.com/coreos/vcontext/report"
)

// maxButaneSize caps fetched Butane bodies at 10 MB. A magus Butane file is
// typically a few KB to low-tens-of-KB; 10 MB is a runaway-upstream guard,
// not a soft target. Past this we stop reading and surface an error rather
// than blow up memory on a wrong URL pointing at a binary download.
const maxButaneSize = 10 * 1024 * 1024

// fetchTimeout bounds HTTP reads. Picked so a slow link doesn't stall apply
// indefinitely, but long enough that a transient blip doesn't kill a real run.
const fetchTimeout = 30 * time.Second

// readButaneSource dispatches on source scheme: local file otherwise.
func readButaneSource(source string) ([]byte, error) {
	if isHTTPURL(source) {
		return fetchButaneHTTP(source)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read butane: %w", err)
	}
	return data, nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func fetchButaneHTTP(rawurl string) ([]byte, error) {
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(rawurl)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawurl, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d %s", rawurl, resp.StatusCode, resp.Status)
	}
	// LimitReader+1 trick: read one byte past the cap so we can detect when
	// the upstream had more to give and surface a clear error rather than
	// silently truncating.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxButaneSize+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: read body: %w", rawurl, err)
	}
	if int64(len(body)) > maxButaneSize {
		return nil, fmt.Errorf("fetch %s: body exceeds %d bytes — refusing to read further", rawurl, maxButaneSize)
	}
	return body, nil
}

// quadletRoot is the canonical search path the systemd-quadlet generator
// uses for operator-supplied quadlets. /usr/share/containers/systemd is the
// image-baked location and is read-only on bootc — we don't manage it.
const quadletRoot = "/etc/containers/systemd/"

// quadletExtensions are the v1-supported quadlet types. .pod, .kube, .image,
// and .build are deferred — they have different generated-service-name
// mappings and aren't on the immediate use case.
var quadletExtensions = []string{".container", ".volume", ".network"}

func isQuadletPath(path string) bool {
	if !strings.HasPrefix(path, quadletRoot) {
		return false
	}
	ext := filepath.Ext(path)
	for _, q := range quadletExtensions {
		if ext == q {
			return true
		}
	}
	return false
}

// LoadButane reads source, runs the Butane → Ignition translation, and
// extracts the magus IR subset from the resulting Ignition spec.
//
// source may be a local filesystem path or an http(s) URL. URLs are fetched
// on every call — there is no local cache and no fallback to a last-known-good
// IR. The reconciler-pattern guarantee is "what apply runs against is what
// the operator currently declared," and silently applying a cached copy
// after a fetch failure would break that.
//
// Translation warnings (non-fatal) are returned in warnings; translation
// errors are returned as the error.
func LoadButane(source string) (*IR, []string, error) {
	data, err := readButaneSource(source)
	if err != nil {
		return nil, nil, err
	}
	ignBytes, report, err := butane.TranslateBytes(data, common.TranslateBytesOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("translate butane %s: %w", source, err)
	}
	warnings := collectWarnings(report)

	var ign ignitionSubset
	if err := json.Unmarshal(ignBytes, &ign); err != nil {
		return nil, warnings, fmt.Errorf("parse ignition output: %w", err)
	}

	out := &IR{}

	for _, f := range ign.Storage.Files {
		contents, err := decodeSource(f.Contents.Source)
		if err != nil {
			return nil, warnings, fmt.Errorf("file %s: %w", f.Path, err)
		}
		// Auto-promote quadlet-shaped files: anything under
		// /etc/containers/systemd/ with a recognized quadlet extension
		// becomes a Quadlet rather than a plain File. The systemd-quadlet
		// generator only scans this path, so detection by location is
		// authoritative — a file at /etc/magus.d/foo.container is not a
		// quadlet to systemd, and it shouldn't be one to magus either.
		if isQuadletPath(f.Path) {
			out.Quadlets = append(out.Quadlets, Quadlet{
				Path:     f.Path,
				Name:     filepath.Base(f.Path),
				Mode:     f.Mode.value(0644),
				UID:      f.User.ID.v,
				GID:      f.Group.ID.v,
				Contents: contents,
			})
			continue
		}
		out.Files = append(out.Files, File{
			Path:     f.Path,
			Mode:     f.Mode.value(0644),
			UID:      f.User.ID.v,
			GID:      f.Group.ID.v,
			Contents: contents,
		})
	}

	for _, d := range ign.Storage.Directories {
		out.Directories = append(out.Directories, Directory{
			Path: d.Path,
			Mode: d.Mode.value(0755),
			UID:  d.User.ID.v,
			GID:  d.Group.ID.v,
		})
	}

	for _, u := range ign.Systemd.Units {
		unit := Unit{
			Name:     u.Name,
			Enabled:  u.Enabled != nil && *u.Enabled,
			Mask:     u.Mask != nil && *u.Mask,
			Contents: derefString(u.Contents),
		}
		for _, di := range u.Dropins {
			unit.DropIns = append(unit.DropIns, DropIn{
				Name:     di.Name,
				Contents: derefString(di.Contents),
			})
		}
		out.Units = append(out.Units, unit)
	}

	return out, warnings, nil
}

func collectWarnings(r report.Report) []string {
	if len(r.Entries) == 0 {
		return nil
	}
	var out []string
	for _, e := range r.Entries {
		out = append(out, e.String())
	}
	return out
}

// ignitionSubset is the slice of the Ignition spec magus consumes. Fields not
// listed here are silently ignored — that's the IR contract: anything outside
// this subset belongs to a different consumer (Ignition itself).
type ignitionSubset struct {
	Storage struct {
		Files       []ignFile      `json:"files"`
		Directories []ignDirectory `json:"directories"`
	} `json:"storage"`
	Systemd struct {
		Units []ignUnit `json:"units"`
	} `json:"systemd"`
}

type ignFile struct {
	Path     string       `json:"path"`
	Mode     intPtr       `json:"mode"`
	User     ignNodeOwner `json:"user"`
	Group    ignNodeOwner `json:"group"`
	Contents struct {
		Source string `json:"source"`
	} `json:"contents"`
}

type ignDirectory struct {
	Path  string       `json:"path"`
	Mode  intPtr       `json:"mode"`
	User  ignNodeOwner `json:"user"`
	Group ignNodeOwner `json:"group"`
}

type ignNodeOwner struct {
	ID intPtr `json:"id"`
}

type ignUnit struct {
	Name     string       `json:"name"`
	Enabled  *bool        `json:"enabled"`
	Mask     *bool        `json:"mask"`
	Contents *string      `json:"contents"`
	Dropins  []ignDropin  `json:"dropins"`
}

type ignDropin struct {
	Name     string  `json:"name"`
	Contents *string `json:"contents"`
}

// intPtr is a *int that survives JSON's null-vs-missing distinction with a
// helpful default accessor.
type intPtr struct {
	v *int
}

func (p *intPtr) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	p.v = &n
	return nil
}

func (p intPtr) value(def int) uint32 {
	if p.v == nil {
		return uint32(def)
	}
	return uint32(*p.v)
}

func (p intPtr) intValue(def int) int {
	if p.v == nil {
		return def
	}
	return *p.v
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// decodeSource decodes an Ignition contents.source URL into raw bytes. v1
// supports the inline forms — data: URLs, both percent-encoded and base64.
// Remote sources (http, https, s3, etc.) are not supported in the IR; magus
// reconciles content it can resolve at parse time.
func decodeSource(src string) ([]byte, error) {
	if src == "" {
		return nil, nil
	}
	if !strings.HasPrefix(src, "data:") {
		return nil, fmt.Errorf("contents.source: only data: URLs are supported, got %q", schemeOf(src))
	}
	body := strings.TrimPrefix(src, "data:")
	comma := strings.IndexByte(body, ',')
	if comma < 0 {
		return nil, fmt.Errorf("contents.source: malformed data URL")
	}
	meta, payload := body[:comma], body[comma+1:]
	isBase64 := false
	for _, part := range strings.Split(meta, ";") {
		if part == "base64" {
			isBase64 = true
			break
		}
	}
	if isBase64 {
		return base64.StdEncoding.DecodeString(payload)
	}
	decoded, err := url.QueryUnescape(payload)
	if err != nil {
		return nil, fmt.Errorf("contents.source: %w", err)
	}
	return []byte(decoded), nil
}

func schemeOf(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i]
	}
	return s
}
