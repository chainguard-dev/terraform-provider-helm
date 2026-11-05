package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"chainguard.dev/sdk/helm/images"
	helmv1 "chainguard.dev/sdk/helm/v1"
	"github.com/google/go-cmp/cmp"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	"sigs.k8s.io/yaml"
)

var (
	digest1 = "sha256:" + strings.Repeat("1", 64)
	digest2 = "sha256:" + strings.Repeat("2", 64)
	digest3 = "sha256:" + strings.Repeat("3", 64)
	digest4 = "sha256:" + strings.Repeat("4", 64)
	digest5 = "sha256:" + strings.Repeat("5", 64)
	digest6 = "sha256:" + strings.Repeat("6", 64)
	digest7 = "sha256:" + strings.Repeat("7", 64)
)

// The fixture is a chart "mirror" that maps its own images "app" (required)
// and "helper" (optional), aliases its "spegel" dependency as
// "registry-mirror", depends on "redis" twice as "cache" and "queue", and
// depends on "plain", which has no cg.json. The redis subchart maps an image
// that reuses the ID "app" and has its own "metrics" subchart.
const (
	rootCG = `{"images": {
  "app": {"values": {"app": {"image": {"registry": "${registry}", "repository": "${repo}", "tag": "${tag}", "digest": "${digest}"}}}},
  "helper": {"values": {"helper": {"image": "${ref}"}}, "requirement": "optional"}
}}`
	spegelCG  = `{"images": {"spegel": {"values": {"image": {"repository": "${registry_repo}", "digest": "${digest}"}, "metrics": {"enabled": true, "port": 9090, "ratio": 0.5}}}}}`
	redisCG   = `{"images": {"app": {"values": {"image": {"registry": "${registry}", "repository": "${repo}", "tag": "${pseudo_tag}"}}}}}`
	metricsCG = `{"images": {"exporter": {"values": {"image": {"repository": "${registry_repo}", "tag": "${tag}"}}}}}`
)

func fixtureFiles() map[string]string {
	return map[string]string{
		"Chart.yaml": `apiVersion: v2
name: mirror
version: 1.0.0
dependencies:
  - name: spegel
    alias: registry-mirror
  - name: redis
    alias: cache
  - name: redis
    alias: queue
  - name: plain
`,
		"cg.json": rootCG,
		"values.yaml": `# Root values.
app:
  image:
    registry: docker.io
    repository: library/app
    tag: latest
    digest: ""
helper:
  # Optional helper.
  image: docker.io/library/helper:latest
`,
		"templates/deploy.yaml":                   "kind: Deployment\n",
		"charts/spegel/Chart.yaml":                "apiVersion: v2\nname: spegel\nversion: 0.1.0\n",
		"charts/spegel/cg.json":                   spegelCG,
		"charts/spegel/values.yaml":               "image:\n  repository: ghcr.io/spegel-org/spegel\n  digest: \"\"\n",
		"charts/redis/Chart.yaml":                 "apiVersion: v2\nname: redis\nversion: 7.0.0\ndependencies:\n  - name: metrics\n",
		"charts/redis/cg.json":                    redisCG,
		"charts/redis/values.yaml":                "image:\n  registry: docker.io\n  repository: library/redis\n  tag: latest\n",
		"charts/redis/templates/sts.yaml":         "kind: StatefulSet\n",
		"charts/redis/charts/metrics/Chart.yaml":  "apiVersion: v2\nname: metrics\nversion: 1.0.0\n",
		"charts/redis/charts/metrics/cg.json":     metricsCG,
		"charts/redis/charts/metrics/values.yaml": "image:\n  repository: docker.io/exporter\n  tag: latest\n",
		"charts/plain/Chart.yaml":                 "apiVersion: v2\nname: plain\nversion: 1.0.0\n",
		"charts/plain/values.yaml":                "replicas: 1\n",
	}
}

// chartDataFrom builds the data section of a chart APK holding files under the
// chart directory "mirror".
func chartDataFrom(t *testing.T, files map[string]string) *chartData {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		content := files[rel]
		if err := tw.WriteHeader(&tar.Header{Name: "mirror/" + rel, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatalf("writing tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	return &chartData{name: "mirror", data: &buf}
}

func mapping(t *testing.T, cg string) *images.Mapping {
	t.Helper()
	m, err := images.Parse(strings.NewReader(cg))
	if err != nil {
		t.Fatalf("parsing mapping: %v", err)
	}
	return m
}

func ref(repo, tag, digest string) *helmv1.ChartImage {
	return &helmv1.ChartImage{RepoName: "cgr.dev/chainguard-private/" + repo, Tag: tag, Digest: digest}
}

// validTree supplies every mapped slot of the fixture. The registry-mirror
// slot is FIPS-like: its repository differs from the alias and the image ID,
// and it is digest-only because its template needs no tag.
func validTree(t *testing.T) *helmv1.ChartImages {
	t.Helper()
	redis := func(appRef, exporterRef *helmv1.ChartImage) *helmv1.ChartImages {
		return &helmv1.ChartImages{
			Refs:     map[string]*helmv1.ChartImage{"app": appRef},
			Template: mapping(t, redisCG),
			Subcharts: map[string]*helmv1.ChartImages{
				"metrics": {
					Refs:     map[string]*helmv1.ChartImage{"exporter": exporterRef},
					Template: mapping(t, metricsCG),
				},
			},
		}
	}
	return &helmv1.ChartImages{
		Refs: map[string]*helmv1.ChartImage{
			"app":    ref("app", "1.2.3", digest1),
			"helper": ref("helper", "0.1", digest2),
		},
		Template: mapping(t, rootCG),
		Subcharts: map[string]*helmv1.ChartImages{
			"registry-mirror": {
				Refs:     map[string]*helmv1.ChartImage{"spegel": ref("spegel-fips", "", digest3)},
				Template: mapping(t, spegelCG),
			},
			"cache": redis(ref("redis", "7.0", digest4), ref("exporter", "1.0", digest5)),
			"queue": redis(ref("redis-fips", "7.1", digest6), ref("exporter-fips", "1.1", digest7)),
		},
	}
}

// layerFiles returns the files of the packaged chart layer keyed by path.
func layerFiles(t *testing.T, c Chart) map[string]string {
	t.Helper()
	layers, err := c.Layers()
	if err != nil {
		t.Fatalf("Layers() = %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("len(Layers()) = %d, want 1", len(layers))
	}
	mt, err := layers[0].MediaType()
	if err != nil {
		t.Fatalf("MediaType() = %v", err)
	}
	if mt != helmregistry.ChartLayerMediaType {
		t.Errorf("layer media type = %q, want %q", mt, helmregistry.ChartLayerMediaType)
	}
	rc, err := layers[0].Uncompressed()
	if err != nil {
		t.Fatalf("Uncompressed() = %v", err)
	}
	defer rc.Close()
	files := map[string]string{}
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading layer: %v", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading %s: %v", hdr.Name, err)
		}
		files[hdr.Name] = string(b)
	}
	return files
}

func TestBuildChartImages(t *testing.T) {
	tree := validTree(t)
	raw, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshaling tree: %v", err)
	}
	parsed, err := ParseChartImages(raw)
	if err != nil {
		t.Fatalf("ParseChartImages() = %v", err)
	}
	// The input is the "images" tree of a chart lock.
	if diff := cmp.Diff(tree, parsed); diff != "" {
		t.Errorf("ParseChartImages() mismatch (-want +got):\n%s", diff)
	}

	got, err := (&BuildConfig{ChartImages: parsed}).build(chartDataFrom(t, fixtureFiles()))
	if err != nil {
		t.Fatalf("build() = %v", err)
	}
	unpatched, err := (&BuildConfig{}).build(chartDataFrom(t, fixtureFiles()))
	if err != nil {
		t.Fatalf("build() without chart images = %v", err)
	}

	gotFiles := layerFiles(t, got)
	var values map[string]any
	if err := yaml.Unmarshal([]byte(gotFiles["mirror/values.yaml"]), &values); err != nil {
		t.Fatalf("parsing packaged values.yaml: %v", err)
	}
	redis := func(appRepo, appTag, exporterRepo, exporterTag string) map[string]any {
		return map[string]any{
			"image": map[string]any{"registry": "cgr.dev", "repository": appRepo, "tag": appTag},
			"metrics": map[string]any{
				"image": map[string]any{"repository": exporterRepo, "tag": exporterTag},
			},
		}
	}
	want := map[string]any{
		"app": map[string]any{"image": map[string]any{
			"registry": "cgr.dev", "repository": "chainguard-private/app", "tag": "1.2.3", "digest": digest1,
		}},
		"helper": map[string]any{"image": "cgr.dev/chainguard-private/helper:0.1@" + digest2},
		"registry-mirror": map[string]any{
			"image":   map[string]any{"repository": "cgr.dev/chainguard-private/spegel-fips", "digest": digest3},
			"metrics": map[string]any{"enabled": true, "port": float64(9090), "ratio": 0.5},
		},
		"cache": redis("chainguard-private/redis", "7.0@"+digest4, "cgr.dev/chainguard-private/exporter", "1.0"),
		"queue": redis("chainguard-private/redis-fips", "7.1@"+digest6, "cgr.dev/chainguard-private/exporter-fips", "1.1"),
	}
	if diff := cmp.Diff(want, values); diff != "" {
		t.Errorf("packaged values.yaml mismatch (-want +got):\n%s", diff)
	}
	for _, comment := range []string{"# Root values.", "# Optional helper."} {
		if !strings.Contains(gotFiles["mirror/values.yaml"], comment) {
			t.Errorf("packaged values.yaml lost comment %q:\n%s", comment, gotFiles["mirror/values.yaml"])
		}
	}

	// Only the top-level values.yaml changes; subchart values and every other
	// file keep their packaged bytes.
	wantFiles := layerFiles(t, unpatched)
	delete(wantFiles, "mirror/values.yaml")
	delete(gotFiles, "mirror/values.yaml")
	if diff := cmp.Diff(wantFiles, gotFiles); diff != "" {
		t.Errorf("packaged files mismatch (-want +got):\n%s", diff)
	}

	md, err := got.Metadata()
	if err != nil {
		t.Fatalf("Metadata() = %v", err)
	}
	if md.Name != "mirror" || md.Version != "1.0.0" {
		t.Errorf("Metadata() = %s %s, want mirror 1.0.0", md.Name, md.Version)
	}
	gotDigest, err := got.Digest()
	if err != nil {
		t.Fatalf("Digest() = %v", err)
	}
	unpatchedDigest, err := unpatched.Digest()
	if err != nil {
		t.Fatalf("Digest() = %v", err)
	}
	if gotDigest == unpatchedDigest {
		t.Errorf("Digest() = %v for both patched and unpatched charts", gotDigest)
	}
}

// Chart bytes must not vary between builds of the same input, so the same
// chart pushed to different destinations has one digest. Subchart images
// patched into partial alias blocks add several sibling keys at once, and the
// order of those keys must be stable.
func TestBuildChartImagesReproducible(t *testing.T) {
	patches := map[string][]byte{
		"values.yaml": []byte(`[{"op": "add", "path": "/queue", "value": {"image": {}}}]`),
	}
	build := func() Chart {
		files := fixtureFiles()
		files["values.yaml"] += "cache:\n  image:\n    pullPolicy: IfNotPresent\n"
		got, err := (&BuildConfig{JSONRFC6902Patches: patches, ChartImages: validTree(t)}).build(chartDataFrom(t, files))
		if err != nil {
			t.Fatalf("build() = %v", err)
		}
		return got
	}

	first := build()
	wantDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("Digest() = %v", err)
	}
	wantManifest, err := first.RawManifest()
	if err != nil {
		t.Fatalf("RawManifest() = %v", err)
	}
	wantValues := layerFiles(t, first)["mirror/values.yaml"]
	for _, want := range []string{"pullPolicy: IfNotPresent", "chainguard-private/redis-fips"} {
		if !strings.Contains(wantValues, want) {
			t.Fatalf("packaged values.yaml does not contain %q:\n%s", want, wantValues)
		}
	}

	for i := range 20 {
		got := build()
		gotDigest, err := got.Digest()
		if err != nil {
			t.Fatalf("build %d: Digest() = %v", i, err)
		}
		gotManifest, err := got.RawManifest()
		if err != nil {
			t.Fatalf("build %d: RawManifest() = %v", i, err)
		}
		if gotDigest != wantDigest || !bytes.Equal(gotManifest, wantManifest) {
			t.Fatalf("build %d: Digest() = %v, want %v; values.yaml diff (-want +got):\n%s",
				i, gotDigest, wantDigest, cmp.Diff(wantValues, layerFiles(t, got)["mirror/values.yaml"]))
		}
	}
}

func TestBuildChartImagesErrors(t *testing.T) {
	tests := []struct {
		name    string
		files   func(map[string]string)
		patches map[string][]byte
		images  map[string]string
		mutate  func(*helmv1.ChartImages)
		wantErr []error // nil means only an error is required
	}{{
		name:    "optional root image omitted",
		mutate:  func(ci *helmv1.ChartImages) { delete(ci.Refs, "helper") },
		wantErr: []error{ErrMissingChartImage},
	}, {
		name:    "required nested image omitted",
		mutate:  func(ci *helmv1.ChartImages) { delete(ci.Subcharts["cache"].Subcharts["metrics"].Refs, "exporter") },
		wantErr: []error{ErrMissingChartImage},
	}, {
		name:    "repeated image ID not satisfied by the root ref",
		mutate:  func(ci *helmv1.ChartImages) { delete(ci.Subcharts["queue"].Refs, "app") },
		wantErr: []error{ErrMissingChartImage},
	}, {
		name:    "mapped subchart alias omitted",
		mutate:  func(ci *helmv1.ChartImages) { delete(ci.Subcharts, "queue") },
		wantErr: []error{ErrMissingChartImage},
	}, {
		name:    "extra root ref",
		mutate:  func(ci *helmv1.ChartImages) { ci.Refs["extra"] = ref("extra", "1", digest1) },
		wantErr: []error{ErrUnexpectedChartImage},
	}, {
		name: "extra nested ref",
		mutate: func(ci *helmv1.ChartImages) {
			ci.Subcharts["registry-mirror"].Refs["spegel-fips"] = ref("spegel-fips", "", digest3)
		},
		wantErr: []error{ErrUnexpectedChartImage},
	}, {
		name: "dependency name instead of its alias",
		mutate: func(ci *helmv1.ChartImages) {
			ci.Subcharts["spegel"] = ci.Subcharts["registry-mirror"]
			delete(ci.Subcharts, "registry-mirror")
		},
		wantErr: []error{ErrUnexpectedChartImage, ErrMissingChartImage},
	}, {
		name: "dependency without cg.json",
		mutate: func(ci *helmv1.ChartImages) {
			ci.Subcharts["plain"] = &helmv1.ChartImages{Template: &images.Mapping{Images: map[string]*images.Image{}}}
		},
		wantErr: []error{ErrUnexpectedChartImage},
	}, {
		name: "nested template differs",
		mutate: func(ci *helmv1.ChartImages) {
			ci.Subcharts["cache"].Template.Images["app"].Values = map[string]any{"image": "${ref}"}
		},
		wantErr: []error{ErrChartImagesTemplate},
	}, {
		name:    "nested template missing",
		mutate:  func(ci *helmv1.ChartImages) { ci.Subcharts["registry-mirror"].Template = nil },
		wantErr: []error{ErrChartImagesTemplate},
	}, {
		name:    "null subchart",
		mutate:  func(ci *helmv1.ChartImages) { ci.Subcharts["cache"] = nil },
		wantErr: []error{ErrInvalidChartImages},
	}, {
		name:    "null nested ref",
		mutate:  func(ci *helmv1.ChartImages) { ci.Subcharts["cache"].Refs["app"] = nil },
		wantErr: []error{ErrInvalidChartImages},
	}, {
		name:    "digest-only ref for a pseudo_tag template",
		mutate:  func(ci *helmv1.ChartImages) { ci.Subcharts["cache"].Refs["app"].Tag = "" },
		wantErr: []error{ErrInvalidChartImages},
	}, {
		name:    "tag hidden in repoName",
		mutate:  func(ci *helmv1.ChartImages) { ci.Refs["app"] = ref("app:1.2.3", "", digest1) },
		wantErr: []error{ErrInvalidChartImages},
	}, {
		name: "several nested failures beside a valid root",
		mutate: func(ci *helmv1.ChartImages) {
			delete(ci.Subcharts["queue"].Subcharts["metrics"].Refs, "exporter")
			ci.Subcharts["cache"].Refs["extra"] = ref("extra", "1", digest1)
		},
		wantErr: []error{ErrMissingChartImage, ErrUnexpectedChartImage},
	}, {
		name:    "packaged chart without cg.json",
		files:   func(f map[string]string) { delete(f, "cg.json") },
		wantErr: []error{ErrChartImagesTemplate},
	}, {
		name: "json patch to a packaged cg.json is authoritative",
		patches: map[string][]byte{
			"charts/redis/charts/metrics/cg.json": []byte(`[{"op": "remove", "path": "/images/exporter"}]`),
		},
		wantErr: []error{ErrChartImagesTemplate},
	}, {
		name: "root value path missing from the packaged values.yaml",
		patches: map[string][]byte{
			"values.yaml": []byte(`[{"op": "remove", "path": "/app/image/digest"}]`),
		},
	}, {
		name:   "conflicts with images",
		images: map[string]string{"app": "cgr.dev/chainguard-private/app:1.2.3@" + digest1},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := fixtureFiles()
			if tc.files != nil {
				tc.files(files)
			}
			tree := validTree(t)
			if tc.mutate != nil {
				tc.mutate(tree)
			}
			got, err := (&BuildConfig{
				JSONRFC6902Patches: tc.patches,
				Images:             tc.images,
				ChartImages:        tree,
			}).build(chartDataFrom(t, files))
			if err == nil {
				t.Fatal("build() = nil error, want error")
			}
			t.Logf("build() = %v", err)
			if got != nil {
				t.Errorf("build() returned a chart with error %v", err)
			}
			for _, want := range tc.wantErr {
				if !errors.Is(err, want) {
					t.Errorf("build() = %v, want errors.Is %v", err, want)
				}
			}
		})
	}
}

func TestParseChartImages(t *testing.T) {
	appRef := `{"repoName": "cgr.dev/org/app", "tag": "1", "digest": "` + digest1 + `"}`
	valid := `{"refs": {"app": ` + appRef + `},
		"template": {"images": {"app": {"values": {"image": "${ref}"}}}}}`
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{name: "valid", in: valid},
		{name: "digest-only for a ref template", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "digest": "` + digest1 + `"}}, "template": {"images": {"app": {"values": {"image": "${ref}"}}}}}`},
		{name: "empty", in: ``, wantErr: ErrInvalidChartImages},
		{name: "trailing object close", in: valid + `}`, wantErr: ErrInvalidChartImages},
		{name: "trailing array close", in: valid + `]`, wantErr: ErrInvalidChartImages},
		{name: "second JSON value", in: valid + ` null`, wantErr: ErrInvalidChartImages},
		{name: "malformed suffix", in: valid + `x`, wantErr: ErrInvalidChartImages},
		{name: "duplicate top-level field", in: `{"refs": {}, "refs": {}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "duplicate ref ID", in: `{"refs": {"app": ` + appRef + `, "app": ` + appRef + `}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "duplicate ref field", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "repoName": "cgr.dev/org/other", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "repoName and reponame", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "reponame": "cgr.dev/org/other", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "wrong-case ref field", in: `{"refs": {"app": {"RepoName": "cgr.dev/org/app", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "wrong-case top-level field", in: `{"Refs": {}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "duplicate template value key", in: `{"refs": {}, "template": {"images": {"app": {"values": {"image": "${ref}", "image": "${digest}"}}}}}`, wantErr: ErrInvalidChartImages},
		{name: "duplicate subchart alias", in: `{"refs": {}, "template": {"images": {}}, "subcharts": {"a": {"refs": {}, "template": {"images": {}}}, "a": {"refs": {}, "template": {"images": {}}}}}`, wantErr: ErrInvalidChartImages},
		{name: "unknown template field", in: `{"refs": {}, "template": {"images": {}, "image": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "unknown nested field", in: `{"refs": {}, "template": {"images": {}}, "subcharts": {"a": {"refs": {}, "template": {"images": {}}, "ref": {}}}}`, wantErr: ErrInvalidChartImages},
		{name: "malformed", in: `{"refs": `, wantErr: ErrInvalidChartImages},
		{name: "null", in: `null`, wantErr: ErrInvalidChartImages},
		{name: "trailing data", in: valid + `{}`, wantErr: ErrInvalidChartImages},
		{name: "unknown field", in: `{"refs": {}, "template": {"images": {}}, "subChart": {}}`, wantErr: ErrInvalidChartImages},
		{name: "unknown ref field", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "digest": "` + digest1 + `", "registry": "cgr.dev"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "missing template", in: `{"refs": {}}`, wantErr: ErrChartImagesTemplate},
		{name: "null template image", in: `{"refs": {}, "template": {"images": {"app": null}}}`, wantErr: ErrInvalidChartImages},
		{name: "null ref", in: `{"refs": {"app": null}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "null subchart", in: `{"refs": {}, "template": {"images": {}}, "subcharts": {"sub": null}}`, wantErr: ErrInvalidChartImages},
		{name: "relative repoName", in: `{"refs": {"app": {"repoName": "org/app", "tag": "1", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "non-canonical repoName", in: `{"refs": {"app": {"repoName": "docker.io/library/app", "tag": "1", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "digest in repoName", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app@` + digest1 + `", "tag": "1", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "missing digest", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "tag": "1"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "malformed digest", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "tag": "1", "digest": "sha256:abc"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "invalid tag", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "tag": "a b", "digest": "` + digest1 + `"}}, "template": {"images": {}}}`, wantErr: ErrInvalidChartImages},
		{name: "digest-only for a tag template", in: `{"refs": {"app": {"repoName": "cgr.dev/org/app", "digest": "` + digest1 + `"}}, "template": {"images": {"app": {"values": {"tag": "${tag}"}}}}}`, wantErr: ErrInvalidChartImages},
		{name: "nested missing template", in: `{"refs": {}, "template": {"images": {}}, "subcharts": {"a": {"refs": {}, "subcharts": {"b": {"refs": {}}}, "template": {"images": {}}}}}`, wantErr: ErrChartImagesTemplate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseChartImages([]byte(tc.in))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ParseChartImages() = %v, want nil error", err)
				}
				if got == nil {
					t.Fatal("ParseChartImages() = nil tree")
				}
				return
			}
			t.Logf("ParseChartImages() = %v", err)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ParseChartImages() = %v, want errors.Is %v", err, tc.wantErr)
			}
			if got != nil {
				t.Errorf("ParseChartImages() returned a tree with error %v", err)
			}
		})
	}
}

// Template literals must decode exactly as images.Parse decodes the packaged
// cg.json, or a valid tree would not match its chart.
func TestParseChartImagesTemplateLiterals(t *testing.T) {
	template := `{"images": {"app": {"values": {"image": "${ref}", "replicas": 3, "ratio": 0.5, "big": 12345678901, "enabled": false, "labels": ["a", 1, true], "none": null}}}}`
	got, err := ParseChartImages([]byte(`{"refs": {"app": {"repoName": "cgr.dev/org/app", "digest": "` + digest1 + `"}}, "template": ` + template + `}`))
	if err != nil {
		t.Fatalf("ParseChartImages() = %v", err)
	}
	want := mapping(t, template)
	if diff := cmp.Diff(want, got.Template); diff != "" {
		t.Errorf("ParseChartImages().Template mismatch with images.Parse (-want +got):\n%s", diff)
	}
	if !reflect.DeepEqual(want, got.Template) {
		t.Errorf("ParseChartImages().Template is not deeply equal to images.Parse: %#v", got.Template)
	}
}

// Slots whose templates write the same values path must agree on the value;
// otherwise a later patch would silently replace an earlier slot's value.
func TestBuildChartImagesOverlappingValues(t *testing.T) {
	const rootApp = `"app": {"values": {"app": {"image": {"registry": "${registry}", "repository": "${repo}", "tag": "${tag}", "digest": "${digest}"}}}},
  "helper": {"values": {"helper": {"image": "${ref}"}}, "requirement": "optional"}`
	tests := []struct {
		name    string
		rootCG  string // replaces the root cg.json when set
		redisCG string // replaces the redis cg.json when set
		values  string // appended to the root values.yaml
		refs    map[string]*helmv1.ChartImage
		wantErr error // nil means the build must succeed
		anyErr  bool  // the SDK patch itself fails, before any value check
		check   func(t *testing.T, values map[string]any)
	}{{
		name:    "same level, different image IDs",
		rootCG:  `{"images": {` + rootApp + `, "sidecar": {"values": {"app": {"image": {"digest": "${digest}"}}}}}}`,
		refs:    map[string]*helmv1.ChartImage{"sidecar": ref("sidecar", "1", digest7)},
		wantErr: ErrChartImageValues,
	}, {
		name:    "root and alias",
		rootCG:  `{"images": {` + rootApp + `, "cache-redis": {"values": {"cache": {"image": {"repository": "${repo}"}}}}}}`,
		values:  "cache:\n  image:\n    repository: bitnami/redis\n",
		refs:    map[string]*helmv1.ChartImage{"cache-redis": {RepoName: "cgr.dev/root-org/redis-root", Tag: "1", Digest: digest7}},
		wantErr: ErrChartImageValues,
	}, {
		name:    "subchart and its nested alias",
		redisCG: `{"images": {"app": {"values": {"image": {"registry": "${registry}", "repository": "${repo}", "tag": "${pseudo_tag}"}, "metrics": {"image": {"tag": "${tag}"}}}}}}`,
		wantErr: ErrChartImageValues,
	}, {
		name:   "ancestor scalar patched before a descendant write",
		rootCG: `{"images": {` + rootApp + `, "cache-ref": {"values": {"cache": {"image": "${ref}"}}}}}`,
		values: "cache:\n  image: docker.io/library/redis\n",
		refs:   map[string]*helmv1.ChartImage{"cache-ref": ref("redis", "7.0", digest4)},
		anyErr: true,
	}, {
		name:    "descendant write replaced by an ancestor scalar",
		rootCG:  `{"images": {` + rootApp + `, "cache-registry": {"values": {"cache": {"image": {"registry": "${registry}"}}}}}}`,
		redisCG: `{"images": {"app": {"values": {"image": "${ref}"}}}}`,
		values:  "cache:\n  image:\n    registry: docker.io\n",
		refs:    map[string]*helmv1.ChartImage{"cache-registry": ref("redis", "7.0", digest4)},
		wantErr: ErrChartImageValues,
	}, {
		name:   "different refs writing equal values",
		rootCG: `{"images": {` + rootApp + `, "cache-registry": {"values": {"cache": {"image": {"registry": "${registry}"}, "enabled": true, "replicas": 3}}}}}`,
		values: "cache:\n  image:\n    registry: docker.io\n  enabled: false\n  replicas: 1\n",
		refs:   map[string]*helmv1.ChartImage{"cache-registry": {RepoName: "cgr.dev/other-org/other", Tag: "9", Digest: digest7}},
		check: func(t *testing.T, values map[string]any) {
			want := map[string]any{
				"image":    map[string]any{"registry": "cgr.dev", "repository": "chainguard-private/redis", "tag": "7.0@" + digest4},
				"enabled":  true,
				"replicas": float64(3),
				"metrics":  map[string]any{"image": map[string]any{"repository": "cgr.dev/chainguard-private/exporter", "tag": "1.0"}},
			}
			if diff := cmp.Diff(want, values["cache"]); diff != "" {
				t.Errorf("packaged cache values mismatch (-want +got):\n%s", diff)
			}
		},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := fixtureFiles()
			files["values.yaml"] += tc.values
			tree := validTree(t)
			if tc.rootCG != "" {
				files["cg.json"] = tc.rootCG
				tree.Template = mapping(t, tc.rootCG)
			}
			if tc.redisCG != "" {
				files["charts/redis/cg.json"] = tc.redisCG
				tree.Subcharts["cache"].Template = mapping(t, tc.redisCG)
				tree.Subcharts["queue"].Template = mapping(t, tc.redisCG)
			}
			maps.Copy(tree.Refs, tc.refs)

			got, err := (&BuildConfig{ChartImages: tree}).build(chartDataFrom(t, files))
			if tc.anyErr {
				if err == nil || got != nil {
					t.Fatalf("build() = %v, %v; want an error and no chart", got, err)
				}
				t.Logf("build() = %v", err)
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("build() = %v, want errors.Is %v", err, tc.wantErr)
				}
				if got != nil {
					t.Errorf("build() returned a chart with error %v", err)
				}
				t.Logf("build() = %v", err)
				return
			}
			if err != nil {
				t.Fatalf("build() = %v", err)
			}
			var values map[string]any
			if err := yaml.Unmarshal([]byte(layerFiles(t, got)["mirror/values.yaml"]), &values); err != nil {
				t.Fatalf("parsing packaged values.yaml: %v", err)
			}
			tc.check(t, values)
		})
	}
}
