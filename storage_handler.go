package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
)

type StorageHandler struct {
	S3API         s3iface.S3API
	S3Bucket      string
	S3RootFolder  string
	Authenticator Authenticator
	Logger        *log.Logger
}

type StorageFile struct {
	JobID     string
	TaskID    string
	NodeID    string
	Filename  string
	Timestamp time.Time
}

func (h *StorageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Logger != nil {
		h.Logger.Printf("storage handler: %s %s", r.Method, r.URL)
	}

	// storage is always read only, so we allow any origin
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// dispatch request to specific handler func
	switch r.Method {
	case http.MethodOptions:
		// TODO(sean) implement OPTIONS response
	case http.MethodHead:
		h.handleHEAD(w, r)
	case http.MethodGet:
		h.handleGET(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

func (h *StorageHandler) handleHEAD(w http.ResponseWriter, r *http.Request) {
	sf, err := getRequestFileID(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s3key := h.s3KeyForFileID(sf)

	headObjectInput := s3.HeadObjectInput{
		Bucket: &h.S3Bucket,
		Key:    &s3key,
	}

	hoo, err := h.S3API.HeadObjectWithContext(r.Context(), &headObjectInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case s3.ErrCodeNoSuchBucket:
				respondJSONError(w, http.StatusNotFound, "Bucket not found: %s", err.Error())
				return
			case s3.ErrCodeNoSuchKey, "NotFound":
				respondJSONError(w, http.StatusNotFound, "File not found: %s", err.Error())
				return
			}
			aerr.Code()
			respondJSONError(w, http.StatusInternalServerError, "Error getting data, HeadObjectWithContext returned: %s", aerr.Code())
			return
		}

		respondJSONError(w, http.StatusInternalServerError, "Error getting data, HeadObjectWithContext returned: %s", err.Error())
		return
	}

	if hoo.ContentLength != nil {
		w.Header().Add("Content-Length", fmt.Sprintf("%d", *hoo.ContentLength))
	}

	respondJSON(w, http.StatusOK, &hoo)
}

func (h *StorageHandler) handleGET(w http.ResponseWriter, r *http.Request) {
	sf, err := getRequestFileID(r)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s3key := h.s3KeyForFileID(sf)

	username, password, hasAuth := r.BasicAuth()

	if !h.Authenticator.Authorized(sf, username, password, hasAuth) {
		w.Header().Set("WWW-Authenticate", "Basic domain=storage.sagecontinuum.org")
		respondJSONError(w, http.StatusUnauthorized, "not authorized")
		return
	}

	objectInput := s3.GetObjectInput{
		Bucket: &h.S3Bucket,
		Key:    &s3key,
	}

	out, err := h.S3API.GetObjectWithContext(r.Context(), &objectInput)
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, "Error getting data, GetObject returned: %s", err.Error())
		return
	}
	defer out.Body.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", sf.Filename))

	if out.ContentLength != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*out.ContentLength, 10))
	}

	w.WriteHeader(http.StatusOK)

	written, err := io.Copy(w, out.Body)
	fileDownloadByteSize.Add(float64(written))
	if err != nil {
		respondJSONError(w, http.StatusInternalServerError, "Error getting data: %s", err.Error())
		return
	}
}

func parseNanosecondTimestamp(s string) (time.Time, error) {
	nsec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, nsec), nil
}

func extractTimestampFromFilename(s string) (time.Time, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("failed to extract timestamp from filename string %q", s)
	}
	return parseNanosecondTimestamp(parts[0])
}

func getRequestFileID(r *http.Request) (*StorageFile, error) {
	// url format is {jobID}/{taskID}/{nodeID}/{timestampAndFilename}
	parts := strings.SplitN(r.URL.Path, "/", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("invalid path format")
	}

	jobID := parts[0]
	taskID := parts[1]
	nodeID := parts[2]
	filename := parts[3]

	timestamp, err := extractTimestampFromFilename(filename)
	if err != nil {
		return nil, err
	}

	return &StorageFile{
		JobID:     jobID,
		TaskID:    taskID,
		NodeID:    nodeID,
		Filename:  filename,
		Timestamp: timestamp,
	}, nil
}

func (h *StorageHandler) s3KeyForFileID(f *StorageFile) string {
	return path.Join(h.S3RootFolder, f.JobID, f.TaskID, f.NodeID, f.Filename)
}
