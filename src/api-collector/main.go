/*******************************************************************************
 * Copyright (c) 2024 Contributors to the Eclipse Foundation
 *
 * See the NOTICE file(s) distributed with this work for additional
 * information regarding copyright ownership.
 *
 * This program and the accompanying materials are made available under the
 * terms of the Apache License, Version 2.0 which is available at
 * https://www.apache.org/licenses/LICENSE-2.0.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
 * WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
 * License for the specific language governing permissions and limitations
 * under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 ******************************************************************************/

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/google/go-github/v61/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v2"
)

const (
	API_DOCS_REPO = "api-hub"
	API_SPEC_PATH = "/docs/api/openAPI.yaml"
)

type OpenAPIInfo struct {
	Version string `yaml:"version"`
	Title   string `yaml:"title"`
}
type OpenAPISpec struct {
	OpenAPI string      `yaml:"openapi"`
	Info    OpenAPIInfo `yaml:"info"`
}

const MetadataFilename = ".tractusx"

type Metadata struct {
	ProductName  string   `yaml:"product"`
	OpenApiSpecs []string `yaml:"openApiSpecs"`
}

func main() {
	owner, token := getArgs()
	ctx := context.Background()
	client := getAuthenticatedClient(ctx, token)
	repos, err := getOrgRepos(ctx, owner, client)
	if err != nil {
		log.Fatalf("Error fetching repos: %s", err)
	}
	for _, repo := range repos {
		log.Println("Scanning repo ", *repo.Name)
		downloadedSpecs := downloadAPISpecs(ctx, client, owner, *repo.Name)
		if len(downloadedSpecs) > 0 {
			log.Println("List of downloaded OpenAPI specs:")
			for _, downloadedSpec := range downloadedSpecs {
				log.Printf("- %s\n", downloadedSpec)
			}
		} else {
			log.Printf("No OpenAPI specs found in .tractusx metadata.")
		}
	}
}

func MetadataFromFile(fileContent []byte) (*Metadata, error) {
	var metadata Metadata

	err := yaml.Unmarshal(fileContent, &metadata)
	if err != nil {
		fmt.Println("Error parsing product metadata file")
		return nil, err
	}

	return &metadata, nil
}

func getArgs() (string, string) {
	owner := flag.String("owner", "", "Specify GitHub User or Organization")
	token := flag.String("token", "", "Specify GitHub Token")
	flag.Parse()

	if *owner == "" || *token == "" {
		log.Fatalln("Missing required arguments, please specify -owner [owner] and -token [token]")
	}
	return *owner, *token
}

func getAuthenticatedClient(ctx context.Context, gitToken string) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: gitToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	return github.NewClient(tc)
}

func getOrgRepos(ctx context.Context, gitOwner string, client *github.Client) ([]*github.Repository, error) {
    opt := &github.RepositoryListOptions{
        ListOptions: github.ListOptions{},
    }

    var allRepos []*github.Repository

    for {
        repos, response, err := client.Repositories.List(ctx, gitOwner, opt)
        if err != nil {
            return nil, err
        }
        allRepos = append(allRepos, repos...)
        if response.NextPage == 0 {
            break
        }
        opt.Page = response.NextPage
    }

    return allRepos, nil
}

func getAPISpecsUrlsFromMetadata(ctx context.Context, client *github.Client, owner string, repo string) ([]string, error) {
	metadataFile, _, _, err := client.Repositories.GetContents(ctx, owner, repo, MetadataFilename, &github.RepositoryContentGetOptions{
		Ref: "main",
	})
	if err != nil {
		return nil, fmt.Errorf("error getting .tractusx metadata file: %v", err)
	}
	content, err := metadataFile.GetContent()
	if err != nil {
		return nil, fmt.Errorf("error getting metadata content: %v", err)
	}
	m, err := MetadataFromFile([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("error parsing metadata: %v", err)
	}
	return m.OpenApiSpecs, nil
}

func downloadAPISpecs(ctx context.Context, client *github.Client, owner string, repo string) []string {
	var downloadedSpecs []string
	specsUrls, err := getAPISpecsUrlsFromMetadata(ctx, client, owner, repo)
	if err != nil {
		log.Printf("%v\n", err)
	}
	if len(specsUrls) == 0 {
		specsUrls = append(specsUrls, fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s", owner, repo, API_SPEC_PATH))
	}
	for _, url := range specsUrls {
		specContent, err := getAPISpecFromUrl(url)
		if err != nil {
			log.Printf("%v\n", err)
			continue
		}
		urlSplit := strings.Split(url, "/")
		fileName := urlSplit[len(urlSplit)-1]
		specPath, err := saveAPISpec(fileName, specContent, repo)
		if err != nil {
			log.Printf("%v\n", err)
			continue
		}
		downloadedSpecs = append(downloadedSpecs, specPath)
	}
	return downloadedSpecs
}

func getAPISpecFromUrl(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return []byte{}, fmt.Errorf("error downloading API spec file: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return []byte{}, fmt.Errorf("bad HTTP status: %s", resp.Status)

	}
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, fmt.Errorf("error reading HTTP response: %v", err)
	}
	return content, nil
}

func saveAPISpec(fileName string, content []byte, repo string) (string, error) {
	var spec OpenAPISpec
	err := yaml.Unmarshal([]byte(content), &spec)
	if err != nil {
		return "", fmt.Errorf("error parsing OpenAPI spec yaml format: %v", err)
	}
	specVer := spec.Info.Version
	// check if it's KIT API
	if strings.Contains(fileName, "kit_") {
		nameSplit := strings.Split(fileName, "_")
		specVer = fmt.Sprintf("kit-%s-%s", nameSplit[1], specVer)
	}
	dirPath := path.Join("docs", repo, specVer)
	err = os.MkdirAll(dirPath, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("error creating directory: %v", err)
	}
	filePath := path.Join(dirPath, fileName)
	err = os.WriteFile(filePath, content, 0644)
	if err != nil {
		return "", fmt.Errorf("error saving OpenAPI spec content to file: %v", err)
	}
	return filePath, nil
}
