package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"golang.org/x/time/rate"

	"github.com/lightstep/terraform-provider-lightstep/version"
)

const (
	DefaultRateLimitPerSecond = 2
	DefaultTimeoutSeconds     = 60
	DefaultUserAgent          = "terraform-provider-lightstep"
)

type Headers map[string]string

type APIResponseCarrier interface {
	GetHTTPResponse() *http.Response
	GetStatusCode() int
}

// APIClientError contains the HTTP Response(for inspection of the error code) as well as the error message
type APIClientError struct {
	Response *http.Response
	Message  string
}

func (a APIClientError) Error() string {
	return a.Message
}
func (a APIClientError) GetHTTPResponse() *http.Response {
	return a.Response
}

func (a APIClientError) GetStatusCode() int {
	if a.Response == nil {
		return -1 // not a known http status code
	}

	return a.Response.StatusCode
}

// Envelope represents a generic response from the API
type Envelope struct {
	Data json.RawMessage `json:"data"`
}

// genericAPIResponse represents a generic response from the Lightstep Public API
type genericAPIResponse[T any] struct {
	Data T `json:"data"`
}

type Client struct {
	apiKey      string
	baseURL     string
	orgName     string
	client      *retryablehttp.Client
	rateLimiter *rate.Limiter
	contentType string
	userAgent   string
}

// NewClient gets a client for the public API
func NewClient(apiKey string, orgName string, env string) *Client {
	return NewClientWithUserAgent(apiKey, orgName, env, fmt.Sprintf("%s/%s", DefaultUserAgent, version.ProviderVersion))
}

func NewClientWithUserAgent(apiKey string, orgName string, env string, userAgent string) *Client {
	// Let the user override the API base URL.
	// e.g. http://localhost:8080
	envBaseURL := os.Getenv("LIGHTSTEP_API_BASE_URL")

	var baseURL string
	if envBaseURL != "" {
		// User specified a base URL, let that take priority.
		baseURL = envBaseURL
	} else if env == "public" {
		baseURL = "https://api.lightstep.com"
	} else {
		baseURL = fmt.Sprintf("https://api-%v.lightstep.com", env)
	}

	fullBaseURL := fmt.Sprintf("%s/public/v0.2/%v", baseURL, orgName)

	rateLimitStr := os.Getenv("LIGHTSTEP_API_RATE_LIMIT")
	rateLimit, err := strconv.Atoi(rateLimitStr)
	if err != nil {
		rateLimit = DefaultRateLimitPerSecond
	}

	// Default client retries 5xx and 429 errors.
	newClient := retryablehttp.NewClient()
	newClient.HTTPClient.Timeout = DefaultTimeoutSeconds * time.Second

	return &Client{
		apiKey:      apiKey,
		orgName:     orgName,
		baseURL:     fullBaseURL,
		userAgent:   userAgent,
		rateLimiter: rate.NewLimiter(rate.Limit(rateLimit), 1),
		client:      newClient,
		contentType: "application/vnd.api+json",
	}
}

// CallAPI calls the given API and unmarshals the result to into result.
func (c *Client) CallAPI(ctx context.Context, httpMethod string, suffix string, data interface{}, result interface{}) error {
	return callAPI(
		ctx,
		c,
		fmt.Sprintf("%v/%v", c.baseURL, suffix),
		httpMethod,
		Headers{
			"Authorization":   fmt.Sprintf("bearer %v", c.apiKey),
			"User-Agent":      c.userAgent,
			"X-Lightstep-Org": c.orgName,
			"Content-Type":    c.contentType,
			"Accept":          c.contentType,
		},
		data,
		result,
	)
}

func executeAPIRequest(ctx context.Context, c *Client, req *retryablehttp.Request, result interface{}) error {
	if len(os.Getenv("LS_DISABLE_RATE_LIMIT")) == 0 {
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return err
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return APIClientError{
			Response: resp,
			Message:  fmt.Sprintf("%v failed: %v: %v", req.Method, req.URL, err),
		}
	}
	defer resp.Body.Close() // nolint: errcheck

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return APIClientError{
			Response: resp,
			Message:  fmt.Sprintf("status %d (%s): %q", resp.StatusCode, resp.Status, string(body)),
		}
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return APIClientError{
				Response: resp,
				Message:  fmt.Sprintf("status %d (%s): %q: %v", resp.StatusCode, resp.Status, string(body), err),
			}
		}
	}

	return nil
}

func createJSONRequest(
	ctx context.Context,
	httpMethod string,
	url string,
	data interface{},
	headers map[string]string,
) (*retryablehttp.Request, error) {
	var body io.Reader

	if data != nil {
		jsonData, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		if !httpMethodSupportsRequestBody(httpMethod) {
			log.Printf("this HTTP method does not support a request body: %v", httpMethod)
		}
		body = bytes.NewReader(jsonData)
	}

	req, err := retryablehttp.NewRequest(httpMethod, url, body)
	if err != nil {
		return nil, err
	}

	req = req.WithContext(ctx)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	return req, nil
}

// callAPI is a helper function that enables flexibly issuing an API Request
func callAPI(
	ctx context.Context,
	c *Client,
	url string,
	httpMethod string,
	headers Headers,
	data interface{},
	result interface{},
) error {
	req, err := createJSONRequest(
		ctx,
		httpMethod,
		url,
		data,
		headers,
	)
	if err != nil {
		return err
	}

	// Do the request.
	return executeAPIRequest(ctx, c, req, result)
}

func httpMethodSupportsRequestBody(method string) bool {
	return method != "GET" && method != "DELETE"
}

func (c *Client) GetStreamIDByLink(ctx context.Context, url string) (string, error) {
	response := Envelope{}
	str := Stream{}
	err := callAPI(ctx,
		c,
		url,
		"GET",
		Headers{
			"Authorization": fmt.Sprintf("bearer %v", c.apiKey),
			"Content-Type":  c.contentType,
			"Accept":        c.contentType,
		}, nil, &response)
	if err != nil {
		return "", err
	}

	err = json.Unmarshal(response.Data, &str)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal response json: %v", err)
	}

	return str.ID, nil
}

// OrgName returns the name of the organization that this client will make request on behalf to.
func (c *Client) OrgName() string {
	return c.orgName
}
