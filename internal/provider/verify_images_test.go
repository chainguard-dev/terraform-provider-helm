/*
Copyright 2026 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestVerifyImages(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	host := "localhost:" + strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[1]

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	ref, err := name.ParseReference(host + "/present:v1")
	if err != nil {
		t.Fatalf("parsing reference: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("pushing image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	present := fmt.Sprintf("%s/present:v1@%s", host, digest)

	tests := []struct {
		name    string
		images  map[string]string
		wantErr string
	}{
		{name: "no images"},
		{name: "present digest", images: map[string]string{"main": present}},
		{
			name:    "missing digest",
			images:  map[string]string{"main": present, "sidecar": fmt.Sprintf("%s/present:v1@sha256:%064x", host, 0)},
			wantErr: `image "sidecar"`,
		},
		{name: "missing tag", images: map[string]string{"main": host + "/present:absent"}, wantErr: `image "main"`},
		{name: "unparsable reference", images: map[string]string{"main": "not a reference"}, wantErr: `image "main"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyImages(t.Context(), tt.images)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyImages: got error %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("verifyImages: got error %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
