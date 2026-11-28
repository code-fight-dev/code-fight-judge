package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const credentialsPath = "/var/run/secrets/kubernetes.io/serviceaccount"

type Client struct {
	http      *http.Client
	Namespace string
}

func NewInClusterClient() (*Client, error) {
	namespace, err := os.ReadFile(filepath.Join(credentialsPath, "namespace"))
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes namespace: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(credentialsPath, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("invalid Kubernetes CA")
	}
	return &Client{
		http: &http.Client{
			Timeout:       5 * time.Second,
			Transport:     &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Namespace: strings.TrimSpace(string(namespace)),
	}, nil
}

func (c *Client) Request(ctx context.Context, method, path string, payload, result any) error {
	token, err := os.ReadFile(filepath.Join(credentialsPath, "token"))
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://kubernetes.default.svc"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		var status meta.Status
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status)
		return fmt.Errorf("Kubernetes %s %s: HTTP %d (%s)", method, path, response.StatusCode, status.Reason)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return err
}
