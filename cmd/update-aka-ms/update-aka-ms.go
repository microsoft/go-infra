// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/microsoft/go-infra/buildmodel"
	"github.com/microsoft/go-infra/buildmodel/buildassets"
	"github.com/microsoft/go-infra/executil"
	"github.com/microsoft/go-infra/goversion"
)

const description = `
update-aka-ms creates aka.ms links based on a given build asset JSON file. This command uses an
MSBuild task supplied by .NET Arcade to carry out the communication with aka.ms services. Therefore,
it must be executed within the go-infra repository, where it can use the eng directory that sets up
the Arcade task. The .NET SDK must also be installed on the machine, and 'dotnet' on PATH.

Example:

  go run ./cmd/update-aka-ms -build-asset-json /downloads/assets.json -version 1.17.8-1

All non-flag args are passed through to the MSBuild project. Use this to configure the link
ownership information and to add authentication. Keep in mind that because this command uses the
standard flag library, all flag args must be passed before the first non-flag arg.

See UpdateAkaMSLinks.csproj for information about the MSBuild properties that must be set.
`

var latestShortLinkPrefix = flag.String(
	"prefix", "golang/release/dev/latest/",
	"The shortened URL prefix to use, including '/'. The default value includes 'dev' and is not intended for production use.")

var validateVersionFlag = flag.String(
	"version", "",
	"A Microsoft-built Go version, in 1.2.3-1[-fips] format.\n"+
		"If specified, must match the build asset JSON's version or this command will fail.\n"+
		"The string 'nil' is treated as the same as not setting the value, for use in CI.\n"+
		"Optionally use this to validate expectations.")

func main() {
	help := flag.Bool("h", false, "Print this help message.")
	buildAssetJSON := flag.String("build-asset-json", "", "[Required] The path of a build asset JSON file describing the Go build to update to.")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "\nUsage:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "%s\n\n", description)
	}
	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(0)
	}

	if *buildAssetJSON == "" {
		flag.Usage()
		log.Fatal("No build asset JSON specified.\n")
	}
	if *validateVersionFlag == "nil" {
		*validateVersionFlag = ""
	}

	if err := createAkaMSLinks(*buildAssetJSON); err != nil {
		log.Fatalf("error: %v\n", err)
	}

	log.Println("\nSuccess.")
}

func createAkaMSLinks(assetFilePath string) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	akaMSDir := filepath.Join(wd, "eng", "artifacts", "akams")
	if err := os.MkdirAll(akaMSDir, os.ModeDir|os.ModePerm); err != nil {
		return err
	}

	var b buildassets.BuildAssets
	if err := buildmodel.ReadJSONFile(assetFilePath, &b); err != nil {
		return err
	}

	if *validateVersionFlag != "" {
		assetVersion := b.GoVersion().Full()
		inputVersion := goversion.New(*validateVersionFlag).Full()
		if assetVersion != inputVersion {
			return fmt.Errorf("build asset JSON version %q doesn't match input version %q", assetVersion, inputVersion)
		}
	}

	linkPairs, err := createLinkPairs(b)
	if err != nil {
		return err
	}

	content, err := propsFileContent(linkPairs)
	if err != nil {
		return err
	}

	projectPath := filepath.Join(wd, "eng", "publishing", "UpdateAkaMSLinks", "UpdateAkaMSLinks.csproj")
	propsPath := filepath.Join(akaMSDir, "AkaMSLinks.props")
	if err := os.WriteFile(propsPath, []byte(content), 0666); err != nil {
		return err
	}

	log.Printf("---- File content for generated file %v\n%v\n", propsPath, content)

	cmd := exec.Command(
		"dotnet", "build", projectPath,
		fmt.Sprintf("/p:LinkItemPropsFile=\"%v\"", propsPath))
	// Pass any additional args through. Likely /p:Key=Value and /bl:Something.binlog
	cmd.Args = append(cmd.Args, flag.Args()...)
	return executil.Run(cmd)
}

type akaMSLinkPair struct {
	Short  string `xml:"Include,attr"`
	Target string `xml:"TargetUrl,attr"`
}

type akaMSPropsFile struct {
	XMLName   xml.Name        `xml:"Project"`
	ItemGroup []akaMSLinkPair `xml:">AkaMSLink"`
}

func createLinkPairs(assets buildassets.BuildAssets) ([]akaMSLinkPair, error) {
	v := assets.GoVersion()
	// The partial versions that we want to link to a specific build.
	// For example, 1.18-fips -> 1.18.2-1-fips.
	partial := []string{
		v.MajorMinor() + v.NoteWithPrefix(),
		v.MajorMinorPatch() + v.NoteWithPrefix(),
		// Also include the fully specified version. This lets people use a pretty link even if they
		// do need to pin to a specific version.
		v.Full(),
	}

	goSrcURLParts := strings.Split(assets.GoSrcURL, "/")
	if len(goSrcURLParts) < 3 {
		return nil, fmt.Errorf("unable to determine build number from %#q: not enough '/' segments to be an asset URL", assets.GoSrcURL)
	}
	buildNumber := goSrcURLParts[len(goSrcURLParts)-2]

	urls := make([]string, 0, 3*(len(assets.Arches)+1))
	for _, a := range assets.Arches {
		urls = appendURLAndVerificationURLs(urls, a.URL)
	}
	urls = appendURLAndVerificationURLs(urls, assets.GoSrcURL)

	pairs := make([]akaMSLinkPair, 0, len(urls)*len(partial))

	for _, p := range partial {
		for _, u := range urls {
			urlParts := strings.Split(u, "/")
			if len(urlParts) < 3 {
				return nil, fmt.Errorf("unable to determine short link for %#q: not enough '/' segments to be an asset URL", u)
			}
			filename := urlParts[len(urlParts)-1]
			// Make our aka.ms links more like official Go links: remove '.' between first parts.
			if strings.HasPrefix(filename, "go.") {
				filename = "go" + strings.TrimPrefix(filename, "go.")
			}
			f, err := makeFloatingFilename(filename, buildNumber, p)
			if err != nil {
				return nil, fmt.Errorf("unable to process URL %#q: %w", u, err)
			}

			pairs = append(pairs, akaMSLinkPair{
				Short:  *latestShortLinkPrefix + f,
				Target: u,
			})
		}
	}

	return pairs, nil
}

func appendURLAndVerificationURLs(u []string, url string) []string {
	u = append(u, url, url+".sha256")
	if strings.HasSuffix(url, ".tar.gz") {
		u = append(u, url+".sig")
	}
	return u
}

func makeFloatingFilename(filename, buildNumber, floatVersion string) (string, error) {
	f := strings.ReplaceAll(filename, buildNumber, floatVersion)
	if f == filename {
		return "", fmt.Errorf("unable to find buildNumber %#q in filename %#q", buildNumber, filename)
	}
	return f, nil
}

func propsFileContent(pairs []akaMSLinkPair) (string, error) {
	x, err := xml.MarshalIndent(akaMSPropsFile{ItemGroup: pairs}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(x) + "\n", nil
}
