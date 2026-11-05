package chart

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing/fstest"

	sdkchart "chainguard.dev/sdk/helm/chart"
	"chainguard.dev/sdk/helm/images"
	helmv1 "chainguard.dev/sdk/helm/v1"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	sigsjson "sigs.k8s.io/json"
	"sigs.k8s.io/yaml"
)

// Errors reported for a chart images tree. Every failure wraps exactly one of
// them so callers can tell malformed input from a mismatch with the packaged
// chart.
var (
	// ErrInvalidChartImages reports a malformed tree: bad JSON, an unknown
	// field, a nil node or ref, or a ref that is not repository@digest.
	ErrInvalidChartImages = errors.New("invalid chart images")
	// ErrChartImagesTemplate reports a missing template, a packaged chart
	// level without a cg.json mapping, or a template that differs from the
	// packaged mapping.
	ErrChartImagesTemplate = errors.New("chart images template does not match the packaged chart")
	// ErrMissingChartImage reports a mapped image or a mapped subchart alias
	// with no entry in the tree.
	ErrMissingChartImage = errors.New("missing chart image")
	// ErrUnexpectedChartImage reports a ref or subchart alias that the
	// packaged chart does not map.
	ErrUnexpectedChartImage = errors.New("unexpected chart image")
	// ErrChartImageValues reports a packaged values.yaml that does not hold a
	// slot's resolved values, as when another slot writes the same path.
	ErrChartImageValues = errors.New("packaged values do not match chart images")
)

// ParseChartImages decodes a JSON [helmv1.ChartImages] tree for packaging.
//
// Decoding is strict: keys that are unknown, duplicated or match a field only
// case-insensitively, trailing data, null nodes and null refs are rejected,
// and every level needs a template. Unlike a chart lock, each
// ref's repoName is the canonical fully qualified repository
// (registry/path) the chart will reference; it must not carry a tag or
// digest. The digest is required and the tag is optional, but a ref without a
// tag is rejected when its template uses ${tag} or ${pseudo_tag}.
func ParseChartImages(data []byte) (*helmv1.ChartImages, error) {
	var strict *helmv1.ChartImages
	strictErrs, err := sigsjson.UnmarshalStrict(data, &strict)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidChartImages, err)
	}
	if len(strictErrs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalidChartImages, errors.Join(strictErrs...))
	}
	// UnmarshalStrict decodes integers as int64. Decode the accepted input
	// again as images.Parse does, so template values compare equal to the
	// packaged cg.json and patch with the same values.
	var ci *helmv1.ChartImages
	if err := json.Unmarshal(data, &ci); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidChartImages, err)
	}
	if ci == nil {
		return nil, fmt.Errorf("%w: chart images is null", ErrInvalidChartImages)
	}
	if err := checkChartImages(ci, ""); err != nil {
		return nil, err
	}
	return ci, nil
}

// checkChartImages validates one tree level and its subcharts without
// reference to a packaged chart.
func checkChartImages(ci *helmv1.ChartImages, at string) error {
	var errs []error
	if ci.Template == nil {
		errs = append(errs, fmt.Errorf("%w: %s: missing template", ErrChartImagesTemplate, level(at)))
	}
	if ci.Template != nil {
		for _, id := range slices.Sorted(maps.Keys(ci.Template.Images)) {
			if ci.Template.Images[id] == nil {
				errs = append(errs, fmt.Errorf("%w: %s: template image %q is null", ErrInvalidChartImages, level(at), id))
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(ci.Refs)) {
		if err := checkRef(ci, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: image %q: %w", level(at), id, err))
		}
	}
	for _, alias := range slices.Sorted(maps.Keys(ci.Subcharts)) {
		sub := ci.Subcharts[alias]
		if sub == nil {
			errs = append(errs, fmt.Errorf("%w: subchart %q is null", ErrInvalidChartImages, join(at, alias)))
			continue
		}
		errs = append(errs, checkChartImages(sub, join(at, alias)))
	}
	return errors.Join(errs...)
}

func checkRef(ci *helmv1.ChartImages, id string) error {
	ref := ci.Refs[id]
	if ref == nil {
		return fmt.Errorf("%w: null ref", ErrInvalidChartImages)
	}
	repo, err := name.NewRepository(ref.RepoName, name.StrictValidation)
	if err != nil {
		return fmt.Errorf("%w: repoName: %w", ErrInvalidChartImages, err)
	}
	if repo.Name() != ref.RepoName {
		return fmt.Errorf("%w: repoName %q is not canonical, want %q", ErrInvalidChartImages, ref.RepoName, repo.Name())
	}
	h, err := v1.NewHash(ref.Digest)
	if err != nil {
		return fmt.Errorf("%w: digest: %w", ErrInvalidChartImages, err)
	}
	if h.Algorithm != "sha256" {
		return fmt.Errorf("%w: digest %q is not sha256", ErrInvalidChartImages, ref.Digest)
	}
	if ref.Tag != "" {
		if _, err := name.NewTag(ref.RepoName+":"+ref.Tag, name.StrictValidation); err != nil {
			return fmt.Errorf("%w: tag: %w", ErrInvalidChartImages, err)
		}
		return nil
	}
	if field, ok := tagField(ci.Template, id); ok {
		return fmt.Errorf("%w: template uses ${%s} but the ref has no tag", ErrInvalidChartImages, field)
	}
	return nil
}

// tagField reports whether the template for id uses a field that needs a tag.
func tagField(m *images.Mapping, id string) (images.RefField, bool) {
	if m == nil || m.Images[id] == nil {
		return "", false
	}
	var found images.RefField
	one := &images.Mapping{Images: map[string]*images.Image{id: m.Images[id]}}
	// Walk only fails on malformed markers, which the packaged mapping check
	// reports; a tag requirement cannot be derived from them.
	_, _ = one.Walk(func(_ string, tokens images.TokenList) (any, error) {
		tokens.Map(func(f images.RefField) any {
			if f == images.Tag || f == images.PseudoTag {
				found = f
			}
			return nil
		})
		return nil, nil
	})
	return found, found != ""
}

// patchChartImages validates ci against the image mappings packaged in c and
// patches every level's refs into c's top-level values.yaml.
func (c *chart) patchChartImages(ci *helmv1.ChartImages) error {
	if err := checkChartImages(ci, ""); err != nil {
		return err
	}
	packaged, err := c.readImageMappings()
	if err != nil {
		return err
	}
	if err := matchChartImages(ci, packaged, ""); err != nil {
		return err
	}

	patched, _, err := sdkchart.PatchChartImages(c, ci, func(_ *helmv1.ChartImages, _ string, ref *helmv1.ChartImage) (string, string, error) {
		return ref.Reference(""), ref.Digest, nil
	})
	if err != nil {
		return fmt.Errorf("patching chart images: %w", err)
	}
	values, err := sdkchart.ReadValues(patched)
	if err != nil {
		return fmt.Errorf("reading patched values: %w", err)
	}
	// Helm reads values.yaml with sigs.k8s.io/yaml, which yields the same
	// JSON-native types as the resolved template values.
	var got map[string]any
	if err := yaml.Unmarshal(values, &got); err != nil {
		return fmt.Errorf("parsing patched values: %w", err)
	}
	if err := checkChartImageValues(ci, got, nil); err != nil {
		return err
	}
	layers, err := patched.Layers()
	if err != nil {
		return fmt.Errorf("reading patched chart: %w", err)
	}
	c.content = layers[0]
	return nil
}

// checkChartImageValues requires the packaged values to hold every slot's
// values, resolved independently of the other slots, under the slot's alias
// path. Slots may write the same path only with equal values.
func checkChartImageValues(ci *helmv1.ChartImages, values map[string]any, aliases []string) error {
	var errs []error
	at := strings.Join(aliases, ".")
	refs := make(map[string]images.OCIRef, len(ci.Refs))
	for id, ref := range ci.Refs {
		r, err := images.NewRef(ref.Reference(""))
		if err != nil {
			return fmt.Errorf("%s: image %q: %w", level(at), id, err)
		}
		refs[id] = r
	}
	resolved, err := ci.Template.Walk(images.Resolve(refs))
	if err != nil {
		return fmt.Errorf("%s: resolving images: %w", level(at), err)
	}
	scope, present := lookup(values, aliases)
	for _, id := range slices.Sorted(maps.Keys(resolved)) {
		for _, m := range mismatches(scope, present, resolved[id], nil) {
			errs = append(errs, fmt.Errorf("%w: %s: image %q: %q is %s, want %s",
				ErrChartImageValues, level(at), id, append(slices.Clone(aliases), m.path...), m.got, m.want))
		}
	}
	for _, alias := range slices.Sorted(maps.Keys(ci.Subcharts)) {
		errs = append(errs, checkChartImageValues(ci.Subcharts[alias], values, append(slices.Clone(aliases), alias)))
	}
	return errors.Join(errs...)
}

type valueMismatch struct {
	path      []string
	got, want string
}

// mismatches compares want with got, where present reports whether got exists.
// Mappings must hold every wanted key; any other value must be equal.
func mismatches(got any, present bool, want any, at []string) []valueMismatch {
	wantMap, isMap := want.(map[string]any)
	if !isMap {
		if present && reflect.DeepEqual(got, want) {
			return nil
		}
		return []valueMismatch{{path: at, got: describe(got, present), want: describe(want, true)}}
	}
	gotMap, ok := got.(map[string]any)
	if !present || !ok {
		return []valueMismatch{{path: at, got: describe(got, present), want: "a mapping"}}
	}
	var out []valueMismatch
	for _, k := range slices.Sorted(maps.Keys(wantMap)) {
		v, ok := gotMap[k]
		out = append(out, mismatches(v, ok, wantMap[k], append(slices.Clone(at), k))...)
	}
	return out
}

// lookup returns the value at keys and whether it exists.
func lookup(v any, keys []string) (any, bool) {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

func describe(v any, present bool) string {
	switch v := v.(type) {
	case nil:
		if !present {
			return "missing"
		}
		return "null"
	case map[string]any:
		return "a mapping"
	case string:
		return fmt.Sprintf("%q", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// readImageMappings reads the recursive cg.json mappings of the packaged chart
// layer, keyed by subchart alias.
func (c *chart) readImageMappings() (*sdkchart.Chart, error) {
	rc, err := c.content.Uncompressed()
	if err != nil {
		return nil, fmt.Errorf("reading chart layer: %w", err)
	}
	defer rc.Close()

	// sdkchart.Read only needs each chart level's Chart.yaml and cg.json.
	files := fstest.MapFS{}
	rootMapped := false
	tr := tar.NewReader(rc)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading chart layer: %w", err)
		}
		switch path.Base(hdr.Name) {
		case sdkchart.HelmMetadataFilename, images.ChainguardChartMetadataFilename:
		default:
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}
		files[hdr.Name] = &fstest.MapFile{Data: data}
		// The chart layer is rooted at a single chart directory.
		if path.Base(hdr.Name) == images.ChainguardChartMetadataFilename && !strings.Contains(path.Dir(hdr.Name), "/") {
			rootMapped = true
		}
	}

	if !rootMapped {
		return nil, fmt.Errorf("%w: chart: packaged chart has no %s", ErrChartImagesTemplate, images.ChainguardChartMetadataFilename)
	}
	packaged, err := sdkchart.Read(files)
	if err != nil {
		return nil, fmt.Errorf("reading packaged image mappings: %w", err)
	}
	return packaged, nil
}

// matchChartImages requires one tree level to supply exactly the packaged
// mapping: the same template, a ref for every mapped image (optional ones
// included) and nothing else, and the same set of mapped subchart aliases.
func matchChartImages(ci *helmv1.ChartImages, packaged *sdkchart.Chart, at string) error {
	if !reflect.DeepEqual(ci.Template, packaged.Mapping) {
		return fmt.Errorf("%w: %s", ErrChartImagesTemplate, level(at))
	}

	var errs []error
	for _, id := range slices.Sorted(maps.Keys(packaged.Mapping.Images)) {
		if _, ok := ci.Refs[id]; !ok {
			errs = append(errs, fmt.Errorf("%w: %s: image %q has no ref", ErrMissingChartImage, level(at), id))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(ci.Refs)) {
		if _, ok := packaged.Mapping.Images[id]; !ok {
			errs = append(errs, fmt.Errorf("%w: %s: image %q is not in the packaged mapping", ErrUnexpectedChartImage, level(at), id))
		}
	}
	for _, alias := range slices.Sorted(maps.Keys(packaged.Subcharts)) {
		if _, ok := ci.Subcharts[alias]; !ok {
			errs = append(errs, fmt.Errorf("%w: subchart %q is mapped but has no chart images", ErrMissingChartImage, join(at, alias)))
		}
	}
	for _, alias := range slices.Sorted(maps.Keys(ci.Subcharts)) {
		sub, ok := packaged.Subcharts[alias]
		if !ok {
			reason := "is not a dependency of the packaged chart"
			if slices.ContainsFunc(packaged.Meta.Dependencies, func(d sdkchart.Dependency) bool { return d.Key() == alias }) {
				reason = "has no packaged " + images.ChainguardChartMetadataFilename
			}
			errs = append(errs, fmt.Errorf("%w: subchart %q %s", ErrUnexpectedChartImage, join(at, alias), reason))
			continue
		}
		errs = append(errs, matchChartImages(ci.Subcharts[alias], sub, join(at, alias)))
	}
	return errors.Join(errs...)
}

func level(at string) string {
	if at == "" {
		return "chart"
	}
	return fmt.Sprintf("subchart %q", at)
}

func join(at, alias string) string {
	if at == "" {
		return alias
	}
	return at + "." + alias
}
