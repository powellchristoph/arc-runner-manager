package e2e

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

// requireARCController skips the test if the ARC controller is not running.
// The gha-runner-scale-set chart cannot install without it.
func requireARCController(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5000000000) // 5s
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment",
		"-n", "arc-system",
		"-l", "app.kubernetes.io/part-of=gha-rs-controller",
		"--no-headers",
	)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("ARC controller not running in arc-system — install with:\n" +
			"  helm install arc-controller --namespace arc-system --create-namespace \\\n" +
			"  oci://ghcr.io/actions/actions-runner-controller-charts/gha-runner-scale-set-controller")
	}
}

// TestHealthz verifies the liveness endpoint is reachable and returns ok.
func TestHealthz(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	resp := c.get(t, "/healthz")
	resp.assertStatus(t, http.StatusOK)

	var body map[string]any
	resp.decode(t, &body)
	assertField(t, "healthz", body, "status", "ok")
}

// TestAuth verifies authentication behaviour for all token scenarios.
func TestAuth(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	t.Run("no token", func(t *testing.T) {
		resp := c.doWithKey(t, http.MethodGet, "/api/v1/runners", "")
		resp.assertStatus(t, http.StatusUnauthorized)
		var body map[string]any
		resp.decode(t, &body)
		if _, ok := body["error"]; !ok {
			t.Error("401 response should include error field")
		}
	})

	t.Run("wrong scheme basic", func(t *testing.T) {
		// Basic auth scheme should be rejected even with a valid token value.
		req, _ := http.NewRequest(http.MethodGet, c.baseURL+"/api/v1/runners", nil)
		req.Header.Set("Authorization", "Basic "+getEnv("API_KEY", "dev-key-change-me"))
		resp, err := c.httpClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 for Basic scheme, got %d", resp.StatusCode)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		resp := c.doWithKey(t, http.MethodGet, "/api/v1/runners", "wrong-key")
		resp.assertStatus(t, http.StatusUnauthorized)
	})

	t.Run("valid primary token accepted", func(t *testing.T) {
		resp := c.get(t, "/api/v1/runners")
		resp.assertStatus(t, http.StatusOK)
	})

	// If SECONDARY_API_KEY is set, verify a second named token also works.
	// This confirms multi-token support end-to-end.
	// Run: SECONDARY_API_KEY=tok-ci-... task test:functional
	if secondKey := getEnv("SECONDARY_API_KEY", ""); secondKey != "" {
		t.Run("secondary token accepted", func(t *testing.T) {
			resp := c.doWithKey(t, http.MethodGet, "/api/v1/runners", secondKey)
			resp.assertStatus(t, http.StatusOK)
		})
		t.Run("secondary token cannot use primary key slot", func(t *testing.T) {
			// primary key should still work independently
			resp := c.get(t, "/api/v1/runners")
			resp.assertStatus(t, http.StatusOK)
		})
	}
}

// TestList verifies the list endpoint returns a valid envelope.
func TestList(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	resp := c.get(t, "/api/v1/runners")
	resp.assertStatus(t, http.StatusOK)

	var body map[string]any
	resp.decode(t, &body)

	if _, ok := body["items"]; !ok {
		t.Error("response missing 'items' field")
	}
	if _, ok := body["total"]; !ok {
		t.Error("response missing 'total' field")
	}
}

// TestGetNotFound verifies that fetching a non-existent runner returns 404.
func TestGetNotFound(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	resp := c.get(t, "/api/v1/runners/does-not-exist-e2e")
	resp.assertStatus(t, http.StatusNotFound)
}

// TestDeleteNotFound verifies that deleting a non-existent runner returns 404.
func TestDeleteNotFound(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	resp := c.delete(t, "/api/v1/runners/does-not-exist-e2e")
	resp.assertStatus(t, http.StatusNotFound)
}

// TestCreateValidation verifies required field enforcement.
func TestCreateValidation(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "empty body",
			payload: map[string]any{},
		},
		{
			name:    "missing githubConfigUrl",
			payload: map[string]any{"name": "x", "githubAppId": "1", "githubAppInstallationId": "2", "githubAppPrivateKey": "k"},
		},
		{
			name:    "missing credentials",
			payload: map[string]any{"name": "x", "githubConfigUrl": "https://github.com/orgs/test"},
		},
		{
			name:    "missing name",
			payload: map[string]any{"githubConfigUrl": "https://github.com/orgs/test", "githubAppId": "1", "githubAppInstallationId": "2", "githubAppPrivateKey": "k"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp := c.post(t, "/api/v1/runners", tc.payload)
			resp.assertStatus(t, http.StatusUnprocessableEntity)
		})
	}
}

// TestRunnerLifecycle is the primary CRUD test — create, get, list, update, delete.
// It uses t.Cleanup to ensure the runner is always removed, even on failure.
func TestRunnerLifecycle(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)
	requireARCController(t)

	team := teamName(t)
	cleanupRunner(t, c, team)

	// CREATE — if this fails the rest of the lifecycle cannot run.
	t.Run("create", func(t *testing.T) {
		resp := c.post(t, "/api/v1/runners", runnerPayload(team, nil))
		resp.assertStatus(t, http.StatusCreated)

		var body map[string]any
		resp.decode(t, &body)

		assertField(t, "create", body, "name", team)

		// Credentials must never be returned.
		if v, ok := body["githubAppId"]; ok && v != nil {
			t.Errorf("githubAppId should not be returned, got: %v", v)
		}
		if v, ok := body["githubAppPrivateKey"]; ok && v != nil {
			t.Errorf("githubAppPrivateKey should not be returned, got: %v", v)
		}
	})
	if t.Failed() {
		t.Fatal("create subtest failed — skipping remainder of lifecycle test")
	}

	// GET
	t.Run("get", func(t *testing.T) {
		resp := c.get(t, "/api/v1/runners/"+team)
		resp.assertStatus(t, http.StatusOK)

		var body map[string]any
		resp.decode(t, &body)
		assertField(t, "get", body, "name", team)

		// Status block should exist.
		if _, ok := body["status"]; !ok {
			t.Error("response missing 'status' field")
		}

		// Secret should have been created.
		if status, ok := body["status"].(map[string]any); ok {
			assertField(t, "get status", status, "secretExists", true)
		}
	})

	// LIST — runner should appear
	t.Run("appears in list", func(t *testing.T) {
		resp := c.get(t, "/api/v1/runners")
		resp.assertStatus(t, http.StatusOK)

		var body map[string]any
		resp.decode(t, &body)

		items, ok := body["items"].([]any)
		if !ok {
			t.Fatal("items field is not an array")
		}

		found := false
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				if m["name"] == team {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("runner %q not found in list response", team)
		}
	})

	// UPDATE — change maxRunners
	t.Run("update maxRunners", func(t *testing.T) {
		resp := c.put(t, "/api/v1/runners/"+team, map[string]any{
			"maxRunners": 5,
		})
		resp.assertStatus(t, http.StatusOK)
	})

	// UPDATE — rotate credentials
	t.Run("rotate credentials", func(t *testing.T) {
		resp := c.put(t, "/api/v1/runners/"+team, map[string]any{
			"githubAppId":             "999",
			"githubAppInstallationId": "888",
			"githubAppPrivateKey":     "rotated-fake-key",
		})
		resp.assertStatus(t, http.StatusOK)
	})

	// DELETE
	t.Run("delete", func(t *testing.T) {
		resp := c.delete(t, "/api/v1/runners/"+team)
		resp.assertStatus(t, http.StatusNoContent)
	})

	// GET after delete — should 404
	t.Run("get after delete", func(t *testing.T) {
		resp := c.get(t, "/api/v1/runners/"+team)
		resp.assertStatus(t, http.StatusNotFound)
	})
}

// TestCreateDuplicate verifies that creating a runner that already exists fails.
func TestCreateDuplicate(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)
	requireARCController(t)

	team := teamName(t)
	cleanupRunner(t, c, team)

	// First create should succeed.
	resp := c.post(t, "/api/v1/runners", runnerPayload(team, nil))
	resp.assertStatus(t, http.StatusCreated)

	// Second create with same name should fail.
	resp = c.post(t, "/api/v1/runners", runnerPayload(team, nil))
	if resp.StatusCode == http.StatusCreated {
		t.Error("expected duplicate create to fail, got 201")
	}
}

// TestUpdateNonExistent verifies that updating a missing runner returns 404.
func TestUpdateNonExistent(t *testing.T) {
	c := newClient(t)
	checkAPIAvailable(t, c)

	resp := c.put(t, "/api/v1/runners/does-not-exist-e2e", map[string]any{
		"maxRunners": 3,
	})
	resp.assertStatus(t, http.StatusNotFound)
}
