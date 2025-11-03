package chart

import "testing"

func TestPatchedWith(t *testing.T) {
	patch := `
[
	{"op": "replace", "path": "/image/registry", "value": "cgr.dev"},
	{"op": "replace", "path": "/image/repository", "value": "otherapp"},
	{"op": "replace", "path": "/image/tag", "value": "v2.0.0"},
	{"op": "replace", "path": "/image/digest", "value": "sha256:foobar"}
]`

	tests := []struct {
		name     string
		original string
		filename string
		expected string
	}{{
		name: "patch images in YAML",
		original: `
# A sample Helm values.yaml
image:
  # The registry where the image is hosted
  registry: docker.io
  # The name of the image
  repository: myapp
  # The tag of the image
  tag: v1.0.0
  # The digest of the image
  digest: sha256:abcdef1234567890
`,
		filename: "values.yaml",
		expected: `# A sample Helm values.yaml
image:
  # The registry where the image is hosted
  registry: cgr.dev
  # The name of the image
  repository: otherapp
  # The tag of the image
  tag: v2.0.0
  # The digest of the image
  digest: sha256:foobar
`,
	}, {
		name: "patch images in JSON",
		original: `
{
  "image": {
	"registry": "docker.io",
	"repository": "myapp",
	"tag": "v1.0.0",
	"digest": "sha256:abcdef1234567890"
  }
}
`,
		filename: "values.json",
		expected: `{"image":{"registry":"cgr.dev","repository":"otherapp","tag":"v2.0.0","digest":"sha256:foobar"}}`,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patched, err := patchedWith(tt.filename, []byte(tt.original), []byte(patch))
			if err != nil {
				t.Fatalf("patchedWith() error = %v", err)
			}
			if string(patched) != tt.expected {
				t.Errorf("patchedWith() = \n%s, want \n%s", string(patched), tt.expected)
			}
		})
	}
}
