// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/subcmd"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	artifactTypeLifecycle = "application/vnd.microsoft.artifact.lifecycle"
	annotationNameEoL     = "vnd.microsoft.artifact.lifecycle.end-of-life.date"
)

func init() {
	subcommands = append(subcommands, subcmd.Option{
		Name:    "deprecate",
		Summary: "Deprecate the given image by annotating it with an end-of-life date.",
		Description: `
Examples:

	go run ./cmd/azoras deprecate myregistry.azurecr.io/myimage:sha256:foo
	go run ./cmd/azoras deprecate images.txt -bulk -eol 2022-12-31T23:59:59Z
		`,
		Handle:         handleDeprecate,
		TakeArgsReason: "The fully qualified image to deprecate or a file containing a newline-separated list of images to deprecate if -bulk is set.",
	})
}

func handleDeprecate(p subcmd.ParseFunc) error {
	eolStr := flag.String("eol", "", "The end-of-life date for the image in RFC3339 format. Defaults to the current time.")
	bulk := flag.Bool("bulk", false, "Deprecate multiple images.")
	if err := p(); err != nil {
		return err
	}
	if _, err := exec.LookPath("oras"); err != nil {
		return err
	}
	ref := flag.Arg(0)
	eol := time.Now()
	if *eolStr != "" {
		var err error
		eol, err = time.Parse(time.RFC3339, *eolStr)
		if err != nil {
			return err
		}
	}
	if !*bulk {
		// Deprecate a single image.
		return deprecate(ref, eol)
	}

	// Deprecate multiple images.
	data, err := os.ReadFile(ref)
	if err != nil {
		return err
	}
	images := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	return deprecateBulk(images, eol)
}

// deprecateBulk deprecates multiple images in bulk.
func deprecateBulk(images []string, eol time.Time) error {
	var err error
	for _, image := range images {
		if err1 := deprecate(image, eol); err1 != nil {
			log.Printf("Failed to deprecate image %s: %v\n", image, err1)
			if err == nil {
				err = err1
			}
		}
	}
	return err
}

// deprecate annotates the given image with an end-of-life date.
func deprecate(image string, eol time.Time) error {
	prevs, err := getAnnotation(image, artifactTypeLifecycle, annotationNameEoL)
	if err != nil {
		// Log the error and continue with the deprecation, as this is a best-effort operation.
		log.Printf("Failed to get the EoL date for image %s: %v\n", image, err)
	}
	for _, prev := range prevs {
		t, err := time.Parse(time.RFC3339, prev)
		if err == nil && t.Before(eol) {
			// The image is already deprecated.
			log.Printf("Image %s is already past its EoL date of %s\n", image, prev)
			return nil
		}
	}
	cmdOras := exec.Command(
		"oras", "attach",
		"--artifact-type", artifactTypeLifecycle,
		"--annotation", annotationNameEoL+"="+eol.Format(time.RFC3339),
		image)
	err = executil.Run(cmdOras)
	if err != nil {
		return err
	}
	log.Printf("Image %s deprecated with an EoL date of %s\n", image, eol.Format(time.RFC3339))
	return nil
}

// getAnnotation returns the list of values for the given annotation name on the given image.
func getAnnotation(image, artifactType, name string) ([]string, error) {
	cmd := exec.Command("oras", "discover", "-o", "json", image)
	out, err := executil.CombinedOutput(cmd)
	if err != nil {
		return nil, err
	}
	var index ocispec.Index
	if err := json.Unmarshal([]byte(out), &index); err != nil {
		return nil, err
	}
	var vals []string
	for _, manifest := range index.Manifests {
		if manifest.ArtifactType != artifactType {
			continue
		}
		for key, val := range manifest.Annotations {
			if key == name {
				vals = append(vals, val)
			}
		}
	}
	return vals, nil
}
