package checks

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/277pawan/freerev-cli/internal/config"
)

// HTTPHealth performs application-aware recovery checks without a database connection.
func HTTPHealth(item config.Check) ([]Result, error) {
	url := strings.TrimSpace(item.URL)
	if url == "" {
		return nil, fmt.Errorf("http_health: url is required")
	}

	method := strings.TrimSpace(item.Method)
	if method == "" {
		method = http.MethodGet
	}

	expectStatus := item.ExpectStatus
	if expectStatus == 0 {
		expectStatus = 200
	}

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	res, err := client.Do(req)
	if err != nil {
		return []Result{{
			Name:    "http_health",
			Status:  StatusFail,
			Message: fmt.Sprintf("HTTP health check failed: %v", err),
		}}, nil
	}
	defer res.Body.Close()

	name := item.Name
	if name == "" {
		name = "http_health"
	}

	if res.StatusCode != expectStatus {
		return []Result{{
			Name:    name,
			Status:  StatusFail,
			Message: fmt.Sprintf("expected HTTP %d, got %d from %s", expectStatus, res.StatusCode, url),
		}}, nil
	}

	return []Result{{
		Name:    name,
		Status:  StatusPass,
		Message: fmt.Sprintf("HTTP %s %s returned %d", method, url, res.StatusCode),
	}}, nil
}
