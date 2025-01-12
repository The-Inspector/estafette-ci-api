package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/opentracing/opentracing-go"
	"github.com/sethgrid/pester"
)

// DockerHubAPIClient communicates with docker hub api
type DockerHubAPIClient interface {
	GetToken(string) (DockerHubToken, error)
	GetDigest(context.Context, DockerHubToken, string, string) (DockerImageDigest, error)
	GetDigestCached(context.Context, string, string) (DockerImageDigest, error)
}

type dockerHubAPIClientImpl struct {
	tokens  map[string]DockerHubToken
	digests map[string]DockerImageDigest
}

// NewDockerHubAPIClient returns a new docker.dockerHubAPIClientImpl
func NewDockerHubAPIClient() (DockerHubAPIClient, error) {
	return &dockerHubAPIClientImpl{
		tokens:  make(map[string]DockerHubToken),
		digests: make(map[string]DockerImageDigest),
	}, nil
}

// GetToken creates an estafette-ci-builder job in Kubernetes to run the estafette build
func (cl *dockerHubAPIClientImpl) GetToken(repository string) (token DockerHubToken, err error) {

	url := fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%v:pull", repository)

	response, err := pester.Get(url)
	if err != nil {
		return
	}
	defer response.Body.Close()

	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return
	}

	// unmarshal json body
	err = json.Unmarshal(body, &token)
	if err != nil {
		return
	}

	return
}

func (cl *dockerHubAPIClientImpl) GetDigest(ctx context.Context, token DockerHubToken, repository string, tag string) (digest DockerImageDigest, err error) {

	span, ctx := opentracing.StartSpanFromContext(ctx, "DockerHubApi::GetDigest")
	defer span.Finish()

	url := fmt.Sprintf("https://index.docker.io/v2/%v/manifests/%v", repository, tag)

	// create client, in order to add headers
	client := pester.NewExtendedClient(&http.Client{Transport: &nethttp.Transport{}})
	client.MaxRetries = 3
	client.Backoff = pester.ExponentialJitterBackoff
	client.KeepLog = true
	client.Timeout = time.Second * 10
	request, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return
	}

	// add tracing context
	request = request.WithContext(opentracing.ContextWithSpan(request.Context(), span))

	// collect additional information on setting up connections
	request, ht := nethttp.TraceRequest(span.Tracer(), request)

	// add headers
	request.Header.Add("Authorization", fmt.Sprintf("%v %v", "Bearer", token.Token))
	request.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")

	// perform actual request
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	ht.Finish()

	digest = DockerImageDigest{
		Digest:    response.Header.Get("Docker-Content-Digest"),
		ExpiresIn: 300,
		FetchedAt: time.Now().UTC(),
	}

	return
}

func (cl *dockerHubAPIClientImpl) GetDigestCached(ctx context.Context, repository string, tag string) (digest DockerImageDigest, err error) {

	span, ctx := opentracing.StartSpanFromContext(ctx, "DockerHubApi::GetDigestCached")
	defer span.Finish()

	key := fmt.Sprintf("%v:%v", repository, tag)

	// fetch digest from cache or renew
	if val, ok := cl.digests[key]; ok && !val.IsExpired() {
		// digest exists and is still valid
		digest = val
		return
	}

	// fetch token from cache or renew
	var token DockerHubToken
	if val, ok := cl.tokens[repository]; !ok || val.IsExpired() {
		// token doesn't exist or is no longer valid, renew
		token, err = cl.GetToken(repository)
		if err != nil {
			return
		}
		cl.tokens[repository] = token
	}
	token = cl.tokens[repository]

	// digest doesn't exist or is no longer valid, renew
	digest, err = cl.GetDigest(ctx, token, repository, tag)
	if err != nil {
		return
	}
	cl.digests[key] = digest

	return
}

// DockerHubToken is a bearer token to authenticate requests with
type DockerHubToken struct {
	Token     string    `json:"token"`
	ExpiresIn int       `json:"expires_in"`
	IssuedAt  time.Time `json:"issued_at"`
}

func (t *DockerHubToken) ExpiresAt() time.Time {
	return t.IssuedAt.Add(time.Duration(t.ExpiresIn) * time.Second)
}

func (t *DockerHubToken) IsExpired() bool {
	return time.Now().UTC().After(t.ExpiresAt())
}

type DockerImageDigest struct {
	Digest    string
	ExpiresIn int
	FetchedAt time.Time
}

func (t *DockerImageDigest) ExpiresAt() time.Time {
	return t.FetchedAt.Add(time.Duration(t.ExpiresIn) * time.Second)
}

func (t *DockerImageDigest) IsExpired() bool {
	return time.Now().UTC().After(t.ExpiresAt())
}
