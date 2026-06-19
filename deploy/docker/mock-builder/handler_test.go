package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewHandler(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	t.Run("health endpoint returns ok", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["status"] != "ok" {
			t.Errorf("expected status=ok, got %v", body)
		}
	})

	t.Run("root endpoint returns bundle hash", func(t *testing.T) {
		body := `{"signedTx":["0xdeadbeef"]}`
		resp, err := http.Post(srv.URL+"/", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}

		var respBody map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
			t.Fatal(err)
		}
		if respBody["bundleHash"] != "0xe2e" {
			t.Errorf("expected bundleHash=0xe2e, got %v", respBody)
		}
	})
}

func TestMain_ListenError(t *testing.T) {
	orig := listenAndServe
	listenAndServe = func(_ string, _ http.Handler) error {
		return fmt.Errorf("connection refused")
	}
	defer func() { listenAndServe = orig }()

	main()
}
