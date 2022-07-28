// Copyright 2022 Fortio Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package rapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"fortio.org/fortio/fhttp"
)

func Fetch(url string, jsonPayload string) (int, []byte) {
	opts := fhttp.NewHTTPOptions(url)
	opts.DisableFastClient = true      // not get raw/chunked results
	opts.Payload = []byte(jsonPayload) // Will make a POST if not empty
	opts.HTTPReqTimeOut = 10 * time.Second
	return fhttp.Fetch(opts)
}

// If jsonPayload isn't empty we POST otherwise get the url.
func GetResult(t *testing.T, url string, jsonPayload string) (*fhttp.HTTPRunnerResults, []byte) {
	code, bytes := Fetch(url, jsonPayload)
	if code != http.StatusOK {
		t.Errorf("Got unexpected error code: URL %s: %v - %s", url, code, fhttp.DebugSummary(bytes, 512))
	}
	res := fhttp.HTTPRunnerResults{}
	err := json.Unmarshal(bytes, &res)
	if err != nil {
		t.Fatalf("Unable to deserialize results: %q: %v", string(bytes), err)
	}
	return &res, bytes
}

// Same as above but when expecting to get an error reply.
func GetErrorResult(t *testing.T, url string, jsonPayload string) (*ErrorReply, []byte) {
	code, bytes := Fetch(url, jsonPayload)
	if code == http.StatusOK {
		t.Errorf("Got unexpected ok code: URL %s: %v", url, code)
	}
	res := ErrorReply{}
	err := json.Unmarshal(bytes, &res)
	if err != nil {
		t.Fatalf("Unable to deserialize error reply: %q: %v", string(bytes), err)
	}
	return &res, bytes
}

// Same as above but when expecting to get an Async reply.
func GetAsyncResult(t *testing.T, url string, jsonPayload string) (*AsyncReply, []byte) {
	code, bytes := Fetch(url, jsonPayload)
	if code != http.StatusOK {
		t.Errorf("Got unexpected error code: URL %s: %v", url, code)
	}
	res := AsyncReply{}
	err := json.Unmarshal(bytes, &res)
	if err != nil {
		t.Fatalf("Unable to deserialize async reply: %q: %v", string(bytes), err)
	}
	return &res, bytes
}

func TestRestRunnerRESTApi(t *testing.T) {
	mux, addr := fhttp.DynamicHTTPServer(false)
	mux.HandleFunc("/foo/", fhttp.EchoHandler)
	baseURL := fmt.Sprintf("http://localhost:%d/", addr.Port)
	uiPath := "/fortio/"
	AddHandlers(mux, uiPath, "/tmp")

	restURL := fmt.Sprintf("http://localhost:%d%s%s", addr.Port, uiPath, restRunURI)

	runURL := fmt.Sprintf("%s?qps=%d&url=%s&t=2s", restURL, 100, baseURL)

	res, bytes := GetResult(t, runURL, "")
	if res.RetCodes[200] != 0 {
		t.Errorf("Got unexpected 200s %d on base: %+v - got %s", res.RetCodes[200], res, fhttp.DebugSummary(bytes, 512))
	}
	if res.RetCodes[404] != 2*100 { // 2s at 100qps == 200
		t.Errorf("Got unexpected 404s count %d on base: %+v", res.RetCodes[404], res)
	}
	echoURL := baseURL + "foo/bar?delay=20ms&status=200:100"
	runURL = fmt.Sprintf("%s?qps=%d&url=%s&n=200", restURL, 100, echoURL)
	res, bytes = GetResult(t, runURL, "")
	totalReq := res.DurationHistogram.Count
	httpOk := res.RetCodes[http.StatusOK]
	if totalReq != httpOk {
		t.Errorf("Mismatch between requests %d and ok %v (%+v) - got %s", totalReq, res.RetCodes, res, fhttp.DebugSummary(bytes, 512))
	}
	if res.SocketCount != res.RunnerResults.NumThreads {
		t.Errorf("%d socket used, expected same as thread# %d", res.SocketCount, res.RunnerResults.NumThreads)
	}

	// Check payload is used and that query arg overrides payload
	jsonData := fmt.Sprintf("{\"metadata\": {\"url\":%q, \"save\":\"on\", \"n\":\"200\"}}", echoURL)
	runURL = fmt.Sprintf("%s?jsonPath=.metadata&qps=100&n=100", restURL)
	res, bytes = GetResult(t, runURL, jsonData)
	totalReq = res.DurationHistogram.Count
	httpOk = res.RetCodes[http.StatusOK]
	if totalReq != httpOk {
		t.Errorf("Mismatch between requests %d and ok %v (%+v) - got %s", totalReq, res.RetCodes, res, fhttp.DebugSummary(bytes, 512))
	}
	if totalReq != 100 {
		t.Errorf("Precedence error, value in url query arg (n=100) should be used, we got %d", totalReq)
	}

	// Send a bad (missing unit) duration (test error return)
	runURL = fmt.Sprintf("%s?jsonPath=.metadata&qps=100&n=10&t=42", restURL)
	errObj, bytes := GetErrorResult(t, runURL, jsonData)
	if errObj.Error != "parsing duration '42'" || errObj.Exception != "time: missing unit in duration \"42\"" {
		t.Errorf("Didn't get the expected duration parsing error, got %+v - %s", errObj, fhttp.DebugSummary(bytes, 512))
	}
	// bad json path: doesn't exist
	runURL = fmt.Sprintf("%s?jsonPath=.foo", restURL)
	errObj, bytes = GetErrorResult(t, runURL, jsonData)
	if errObj.Exception != "\"foo\" not found in json" {
		t.Errorf("Didn't get the expected json body access error, got %+v - %s", errObj, fhttp.DebugSummary(bytes, 512))
	}
	// bad json path: wrong type
	runURL = fmt.Sprintf("%s?jsonPath=.metadata.url", restURL)
	errObj, bytes = GetErrorResult(t, runURL, jsonData)
	if errObj.Exception != "\"url\" path is not a map" {
		t.Errorf("Didn't get the expected json type mismatch error, got %+v - %s", errObj, fhttp.DebugSummary(bytes, 512))
	}
	// missing url and a few other cases
	jsonData = `{"metadata": {"n": 200}}`
	runURL = fmt.Sprintf("%s?jsonPath=.metadata", restURL)
	errObj, bytes = GetErrorResult(t, runURL, jsonData)
	if errObj.Error != "URL is required" {
		t.Errorf("Didn't get the expected url missing error, got %+v - %s", errObj, fhttp.DebugSummary(bytes, 512))
	}
	// not well formed json
	jsonData = `{"metadata": {"n":`
	runURL = fmt.Sprintf("%s?jsonPath=.metadata", restURL)
	errObj, bytes = GetErrorResult(t, runURL, jsonData)
	if errObj.Exception != "unexpected end of JSON input" {
		t.Errorf("Didn't get the expected error for truncated/invalid json, got %+v - %s", errObj, fhttp.DebugSummary(bytes, 512))
	}
	// Exercise Hearders code (but hard to test the effect,
	// would need to make a single echo query instead of a run... which the API doesn't do)
	jsonData = `{"metadata": {"headers": ["Foo: Bar", "Blah: BlahV"]}}`
	runURL = fmt.Sprintf("%s?jsonPath=.metadata&qps=90&n=23&url=%s&H=Third:HeaderV", restURL, echoURL)
	res, bytes = GetResult(t, runURL, jsonData)
	if res.RetCodes[http.StatusOK] != 23 {
		t.Errorf("Should have done expected 23 requests, got %+v - %s", res.RetCodes, fhttp.DebugSummary(bytes, 128))
	}
	// Start infinite running run
	runURL = fmt.Sprintf("%s?jsonPath=.metadata&qps=10&t=on&url=%s&save=on&async=on", restURL, echoURL)
	asyncObj, bytes := GetAsyncResult(t, runURL, jsonData)
	if asyncObj.Message != "started" || asyncObj.RunID < 1 {
		t.Errorf("Should started async job got %+v - %s", asyncObj, fhttp.DebugSummary(bytes, 256))
	}
}