package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
)

type mockS3Client struct {
	files map[string][]byte
	s3iface.S3API
}

func (m *mockS3Client) HeadObjectWithContext(ctx context.Context, obj *s3.HeadObjectInput, options ...request.Option) (*s3.HeadObjectOutput, error) {
	if obj.Key == nil {
		return nil, fmt.Errorf("no key provided")
	}
	content, ok := m.files[*obj.Key]
	if !ok {
		return nil, fmt.Errorf(s3.ErrCodeNoSuchKey)
	}
	lang := "klingon"
	length := int64(len(content))
	return &s3.HeadObjectOutput{
		ContentLanguage: &lang,
		ContentLength:   &length,
	}, nil
}

func (m *mockS3Client) GetObjectWithContext(ctx context.Context, obj *s3.GetObjectInput, options ...request.Option) (*s3.GetObjectOutput, error) {
	if obj.Key == nil {
		return nil, fmt.Errorf("no key provided")
	}
	content, ok := m.files[*obj.Key]
	if !ok {
		return nil, fmt.Errorf(s3.ErrCodeNoSuchKey)
	}

	length := int64(len(content))
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(content)),
		ContentLength: &length,
	}, nil
}

type mockAuthenticator struct {
	authorized bool
}

func (a *mockAuthenticator) Authorized(f *StorageFile, username, password string, hasAuth bool) bool {
	return a.authorized
}

func getResponse(h http.Handler, method string, url string) *http.Response {
	r, err := http.NewRequest(method, url, nil)
	if err != nil {
		panic(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result()
}

func assertStatusCode(t *testing.T, resp *http.Response, status int) {
	if resp.StatusCode != status {
		t.Errorf("incorrect status code. got: %d want: %d", resp.StatusCode, status)
	}
}

func assertContentLength(t *testing.T, resp *http.Response, length int) {
	if resp.ContentLength != int64(length) {
		t.Errorf("incorrect content length. got: %d want: %d", resp.StatusCode, length)
	}
}

func assertReadContent(t *testing.T, resp *http.Response, content []byte) {
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("error when reading body: %s", err.Error())
	}
	if !bytes.Equal(b, content) {
		t.Errorf("content does not match. got: %v want: %v", b, content)
	}
}

func randomURL() string {
	// TODO(sean) actually make random
	return "sage/task/node/1643842551688168762-sample.jpg"
}

func randomContent(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	return b
}

func TestHandlerHeadUnauthorized(t *testing.T) {
	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: &mockAuthenticator{false},
	}
	resp := getResponse(handler, http.MethodHead, randomURL())
	assertStatusCode(t, resp, http.StatusUnauthorized)
}

func TestHandlerHeadNotFound(t *testing.T) {
	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: &mockAuthenticator{true},
	}
	resp := getResponse(handler, http.MethodHead, randomURL())
	assertStatusCode(t, resp, http.StatusNotFound)
}

func TestHandlerHeadOK(t *testing.T) {
	content := randomContent(273)
	url := randomURL()
	handler := &StorageHandler{
		S3API: &mockS3Client{
			files: map[string][]byte{
				url: content,
			},
		},
		Authenticator: &mockAuthenticator{true},
	}
	resp := getResponse(handler, http.MethodHead, url)
	assertStatusCode(t, resp, http.StatusOK)
	assertContentLength(t, resp, len(content))
}

func TestHandlerGetBadURL(t *testing.T) {
	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: &mockAuthenticator{true},
	}

	testcases := map[string]string{
		"TooFewSlashes":      "task/node/1643842551688168762-sample.jpg",
		"TooManySlashes":     "too/many/node/1643842551688168762-sample.jpg",
		"BadTimestampLength": "sage/task/node/16438425516881687620-sample.jpg",
		"BadTimestampChars":  "sage/task/node/164384X551688168762-sample.jpg",
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			resp := getResponse(handler, http.MethodGet, tc)
			assertStatusCode(t, resp, http.StatusInternalServerError)
		})
	}
}

func TestHandlerGetUnauthorized(t *testing.T) {
	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: &mockAuthenticator{false},
	}
	resp := getResponse(handler, http.MethodGet, randomURL())
	assertStatusCode(t, resp, http.StatusUnauthorized)
	assertReadContent(t, resp, []byte(`{
  "error": "not authorized"
}
`))
}

func TestHandlerGetNotFound(t *testing.T) {
	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: &mockAuthenticator{true},
	}
	resp := getResponse(handler, http.MethodGet, randomURL())
	assertStatusCode(t, resp, http.StatusNotFound)
}

func TestHandlerGetOK(t *testing.T) {
	content := randomContent(273)
	url := randomURL()

	handler := &StorageHandler{
		S3API: &mockS3Client{
			files: map[string][]byte{
				url: content,
			},
		},
		Authenticator: &mockAuthenticator{true},
	}

	resp := getResponse(handler, http.MethodGet, url)
	assertStatusCode(t, resp, http.StatusOK)

	allowOrigin := resp.Header.Get("Access-Control-Allow-Origin")
	if allowOrigin != "*" {
		t.Fatalf("Access-Control-Allow-Origin must be *. got %q", allowOrigin)
	}

	// TODO(sean) check other expected headers
	// methods := resp.Header.Values("Access-Control-Allow-Methods")
	// sort.Strings(methods)
	// if strings.Join(methods, ",") != "GET,HEAD,OPTIONS" {
	// 	t.Fatalf("allow methods must be GET, HEAD and OPTIONS")
	// }

	assertContentLength(t, resp, len(content))
	assertReadContent(t, resp, content)
}
