package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/gorilla/mux"
)

type mockS3Client struct {
	s3iface.S3API
}

func (m *mockS3Client) HeadObjectWithContext(ctx context.Context, hoi *s3.HeadObjectInput, opts ...request.Option) (*s3.HeadObjectOutput, error) {
	_ = ctx
	klingon := "klingon"
	return &s3.HeadObjectOutput{ContentLanguage: &klingon}, nil
}

func (m *mockS3Client) GetObjectWithContext(context.Context, *s3.GetObjectInput, ...request.Option) (*s3.GetObjectOutput, error) {
	result := &s3.GetObjectOutput{}
	content := "I am fake file content"
	result.Body = io.NopCloser(strings.NewReader(content))
	length := int64(len(content))
	result.ContentLength = &length
	return result, nil
}

func makeCommissionDate(year, month, day int) *time.Time {
	t := time.Now().AddDate(year, month, day)
	return &t
}

func getTestRouter() *mux.Router {
	TableAuthenticator := &TableAuthenticator{}

	TableAuthenticator.UpdateConfig(&TableAuthenticatorConfig{
		Username: "user",
		Password: "secret",
		Nodes: map[string]*TableAuthenticatorNode{
			"uncommissioned": {
				Restricted: false,
			},
			"commissioned1Y": {
				Restricted:     false,
				CommissionDate: makeCommissionDate(-1, 0, 0),
			},
			"commissioned3Y": {
				Restricted:     false,
				CommissionDate: makeCommissionDate(-3, 0, 0),
			},
			"restrictedNode1": {
				Restricted:     true,
				CommissionDate: makeCommissionDate(-1, 0, 0),
			},
			"restrictedNode2": {
				Restricted:     true,
				CommissionDate: makeCommissionDate(-1, 0, 0),
			},
		},
		RestrictedTasksSubstrings: []string{
			"imagesampler-bottom",
			"imagesampler-left",
			"imagesampler-right",
			"imagesampler-top",
			"audiosampler",
		},
	})

	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: TableAuthenticator,
	}

	return createRouter(handler)
}

func TestHeadRequest(t *testing.T) {
	r := getTestRouter()

	req, err := http.NewRequest("HEAD", "/api/v1/data/j/t/n/0001-sample.jpg", nil)

	if err != nil {
		t.Fatalf("failed: %s", err.Error())
	}

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	//http.HandlerFunc(headFileRequest).ServeHTTP(rr, req)
	t.Logf("body: %s", rr.Body.String())
	status := rr.Code
	if status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
	t.Logf("body: %s", rr.Body.String())

	result := s3.HeadObjectOutput{}

	bs, _ := rr.Body.ReadBytes('@')
	t.Logf("bs: %s", bs)
	json.Unmarshal(bs, &result)

	if result.ContentLanguage == nil {
		t.Fatal("ContentLanguage missing")
	}
	if *(result.ContentLanguage) != "klingon" {
		t.Fatal("ContentLanguage wrong")
	}
}

func timestamp(t time.Time) string {
	return fmt.Sprintf("%d", t.UnixNano())
}

func randomNodeID() string {
	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	s := make([]rune, 16)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}

func generateRandomNodeList(n int) map[string]*TableAuthenticatorNode {
	nodes := make(map[string]*TableAuthenticatorNode)
	for i := 0; i < n; i++ {
		nodes[randomNodeID()] = &TableAuthenticatorNode{
			Restricted:     rand.Intn(2) == 0,
			CommissionDate: makeCommissionDate(-1, 0, 0),
		}
	}
	return nodes
}

func TestFuzzRestrictedNodes(t *testing.T) {
	TableAuthenticator := &TableAuthenticator{}

	nodes := generateRandomNodeList(200)

	TableAuthenticator.UpdateConfig(&TableAuthenticatorConfig{
		Username:                  "user",
		Password:                  "secret",
		Nodes:                     nodes,
		RestrictedTasksSubstrings: []string{},
	})

	handler := &StorageHandler{
		S3API:         &mockS3Client{},
		Authenticator: TableAuthenticator,
	}

	r := createRouter(handler)

	// hmm... we can also fuzz the username / password check...

	for nodeID, node := range nodes {
		url := fmt.Sprintf("/api/v1/data/sage/safe-task/%s/%s-sample.jpg", nodeID, timestamp(time.Now()))

		// test that no auth returns unauthorized for restricted node
		if node.Restricted {
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("failed: %s", err.Error())
			}

			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("should have returned unauthorized for %s", nodeID)
			}
		}

		// test that invalid password returns unauthorized for restricted node
		if node.Restricted {
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				t.Fatalf("failed: %s", err.Error())
			}

			req.SetBasicAuth("userX", "secret")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("should have returned unauthorized for %s", nodeID)
			}
		}

		// test that authorized when correct credentials are provided
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			t.Fatalf("failed: %s", err.Error())
		}

		req.SetBasicAuth("user", "secret")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("should have returned authorized for %s", nodeID)
		}
	}
}

// TODO clean up request code case

func TestGetRequest(t *testing.T) {
	var mytests = map[string]struct {
		requiresAuth bool
		url          string
	}{
		"allow": {
			requiresAuth: false,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/commissioned1Y/%s-sample.jpg", timestamp(time.Now())),
		},
		"allowPast1": {
			requiresAuth: false,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/commissioned1Y/%s-sample.jpg", timestamp(time.Now().AddDate(0, -6, 0))),
		},
		"allowPast2": {
			requiresAuth: false,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/commissioned3Y/%s-sample.jpg", timestamp(time.Now().AddDate(-2, 0, 0))),
		},
		"allowFuture": {
			requiresAuth: false,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/commissioned1Y/%s-sample.jpg", timestamp(time.Now().AddDate(0, 6, 0))),
		},
		"restrictNode1": {
			requiresAuth: true,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/restrictedNode1/%s-sample.jpg", timestamp(time.Now())),
		},
		"restrictNode2": {
			requiresAuth: true,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/restrictedNode2/%s-sample.jpg", timestamp(time.Now())),
		},
		"restrictTask": {
			requiresAuth: true,
			url:          fmt.Sprintf("/api/v1/data/sage/imagesampler-bottom/commissioned1Y/%s-sample.jpg", timestamp(time.Now())),
		},
		"restrictPast1": {
			requiresAuth: true,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/commissioned1Y/%s-sample.jpg", timestamp(time.Now().AddDate(-1, 0, -1))),
		},
		"restrictPast2": {
			requiresAuth: true,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/commissioned3Y/%s-sample.jpg", timestamp(time.Now().AddDate(-3, 0, -1))),
		},
		"restrictUncommissioned": {
			requiresAuth: true,
			url:          fmt.Sprintf("/api/v1/data/sage/safe-task/uncommissioned/%s-sample.jpg", timestamp(time.Now())),
		},
	}

	r := getTestRouter()

	for name, test := range mytests {
		t.Run(name, func(t *testing.T) {
			// test that unauthenticated request fails
			if test.requiresAuth {
				req, err := http.NewRequest("GET", test.url, nil)
				if err != nil {
					t.Fatalf("failed: %s", err.Error())
				}

				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusUnauthorized {
					t.Fatalf("%q should have returned unauthorized", test.url)
				}
			}

			// test that invalid username fails
			if test.requiresAuth {
				req, err := http.NewRequest("GET", test.url, nil)
				if err != nil {
					t.Fatalf("failed: %s", err.Error())
				}

				req.SetBasicAuth("userX", "secret")
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusUnauthorized {
					t.Fatalf("%q should have returned unauthorized", test.url)
				}
			}

			// test that invalid password fails
			if test.requiresAuth {
				req, err := http.NewRequest("GET", test.url, nil)
				if err != nil {
					t.Fatalf("failed: %s", err.Error())
				}

				req.SetBasicAuth("user", "secretY")
				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, req)
				if rr.Code != http.StatusUnauthorized {
					t.Fatalf("%q should have returned unauthorized", test.url)
				}
			}

			req, err := http.NewRequest("GET", test.url, nil)
			if err != nil {
				t.Fatalf("failed: %s", err.Error())
			}

			if test.requiresAuth {
				req.SetBasicAuth("user", "secret")
			}

			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("Access-Control-Allow-Origin != *, instead %q", rr.Header().Get("Access-Control-Allow-Origin"))
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("handler should have return OK instead of %d", rr.Code)
			}

			b, err := io.ReadAll(rr.Result().Body)
			if err != nil {
				t.Fatalf("failed: %s", err.Error())
			}

			expectBody := []byte("I am fake file content")
			if !bytes.Equal(b, expectBody) {
				t.Fatalf("body did not match\ngot: %s\nexpect: %s", b, expectBody)
			}

			if rr.Result().ContentLength != int64(len(expectBody)) {
				t.Fatalf("content length did not match\ngot: %d\nexpect: %d", rr.Result().ContentLength, len(expectBody))
			}

			// TODO add test for Content-Disposition
		})
	}
}

// func TestMiddleware(t *testing.T) {

// 	req, err := http.NewRequest("GET", "/", nil)

// 	if err != nil {
// 		t.Fatalf("failed: %s", err.Error())
// 	}

// 	rr := httptest.NewRecorder()

// 	r := createRouter()
// 	r.ServeHTTP(rr, req)
// 	if status := rr.Code; status != http.StatusOK {
// 		t.Fatalf("handler returned wrong status code: got %v want %v",
// 			status, http.StatusOK)
// 	}

// 	acao := rr.Header().Get("Access-Control-Allow-Origin")
// 	if acao != "*" {
// 		t.Fatalf("Access-Control-Allow-Origin header wrong, got \"%s\"", acao)
// 	}

// 	acam := rr.Header().Values("Access-Control-Allow-Methods")
// 	if len(acam) == 0 {
// 		t.Fatalf("Access-Control-Allow-Origin header empty")
// 	}
// 	if strings.Join(acam, ",") != "GET,OPTIONS" {
// 		t.Fatalf("Access-Control-Allow-Methods header wrong, got \"%s\"", strings.Join(acam, ","))
// 	}
// }