package forgejo

import (
	"os"
	"testing"
)

// getForgejoCredentials returns base URL and token for integration tests.
// It checks environment variables only.
// Returns empty strings if credentials are not available.
func getForgejoCredentials() (baseURL, token string) {
	baseURL = os.Getenv("FORGEJO_BASE_URL")
	token = os.Getenv("FORGEJO_TOKEN")

	return baseURL, token
}

func TestIntegrationListOwners(t *testing.T) {
	baseURL, token := getForgejoCredentials()
	if baseURL == "" || token == "" {
		t.Skip("Skipping integration test: FORGEJO_BASE_URL and FORGEJO_TOKEN not set")
	}

	client := NewClient(baseURL, token)
	owners, err := client.ListOwners(t.Context())
	if err != nil {
		t.Fatalf("ListOwners failed: %v", err)
	}

	if len(owners) == 0 {
		t.Error("Expected at least one owner, got none")
	}

	t.Logf("Found %d owners:", len(owners))
	for _, owner := range owners {
		t.Logf("  - %s (IsOrg: %v)", owner.Name, owner.IsOrg)
	}
}

func TestIntegrationListRepos(t *testing.T) {
	baseURL, token := getForgejoCredentials()
	if baseURL == "" || token == "" {
		t.Skip("Skipping integration test: FORGEJO_BASE_URL and FORGEJO_TOKEN not set")
	}

	client := NewClient(baseURL, token)

	// First get owners to find a valid one
	owners, err := client.ListOwners(t.Context())
	if err != nil {
		t.Fatalf("ListOwners failed: %v", err)
	}

	if len(owners) == 0 {
		t.Skip("No owners available")
	}

	owner := owners[0].Name
	repos, err := client.ListRepos(t.Context(), owner)
	if err != nil {
		t.Fatalf("ListRepos for owner %s failed: %v", owner, err)
	}

	t.Logf("Found %d repos for owner %s:", len(repos), owner)
	for i, repo := range repos {
		if i >= 5 {
			t.Logf("  ... and %d more", len(repos)-5)
			break
		}
		t.Logf("  - %s", repo)
	}
}

func TestIntegrationListLabels(t *testing.T) {
	baseURL, token := getForgejoCredentials()
	if baseURL == "" || token == "" {
		t.Skip("Skipping integration test: FORGEJO_BASE_URL and FORGEJO_TOKEN not set")
	}

	client := NewClient(baseURL, token)

	// Find a repo with labels
	owners, err := client.ListOwners(t.Context())
	if err != nil {
		t.Fatalf("ListOwners failed: %v", err)
	}

	for _, owner := range owners {
		repos, err := client.ListRepos(t.Context(), owner.Name)
		if err != nil {
			continue
		}

		for _, repo := range repos {
			labels, err := client.ListLabels(t.Context(), owner.Name, repo)
			if err != nil {
				continue
			}

			if len(labels) > 0 {
				t.Logf("Found %d labels in %s/%s:", len(labels), owner.Name, repo)
				for i, label := range labels {
					if i >= 5 {
						t.Logf("  ... and %d more", len(labels)-5)
						break
					}
					t.Logf("  - %s (ID: %d)", label.Name, label.ID)
				}
				return
			}
		}
	}

	t.Log("No labels found in any repository")
}

func TestIntegrationListMilestones(t *testing.T) {
	baseURL, token := getForgejoCredentials()
	if baseURL == "" || token == "" {
		t.Skip("Skipping integration test: FORGEJO_BASE_URL and FORGEJO_TOKEN not set")
	}

	client := NewClient(baseURL, token)

	// Find a repo with milestones
	owners, err := client.ListOwners(t.Context())
	if err != nil {
		t.Fatalf("ListOwners failed: %v", err)
	}

	for _, owner := range owners {
		repos, err := client.ListRepos(t.Context(), owner.Name)
		if err != nil {
			continue
		}

		for _, repo := range repos {
			milestones, err := client.ListMilestones(t.Context(), owner.Name, repo)
			if err != nil {
				continue
			}

			if len(milestones) > 0 {
				t.Logf("Found %d milestones in %s/%s:", len(milestones), owner.Name, repo)
				for i, milestone := range milestones {
					if i >= 5 {
						t.Logf("  ... and %d more", len(milestones)-5)
						break
					}
					t.Logf("  - %s (ID: %d)", milestone.Title, milestone.ID)
				}
				return
			}
		}
	}

	t.Log("No milestones found in any repository")
}

func TestIntegrationValidateRepo(t *testing.T) {
	baseURL, token := getForgejoCredentials()
	if baseURL == "" || token == "" {
		t.Skip("Skipping integration test: FORGEJO_BASE_URL and FORGEJO_TOKEN not set")
	}

	client := NewClient(baseURL, token)

	// Find a valid repo
	owners, err := client.ListOwners(t.Context())
	if err != nil {
		t.Fatalf("ListOwners failed: %v", err)
	}

	if len(owners) == 0 {
		t.Skip("No owners available")
	}

	owner := owners[0].Name
	repos, err := client.ListRepos(t.Context(), owner)
	if err != nil {
		t.Fatalf("ListRepos failed: %v", err)
	}

	if len(repos) == 0 {
		t.Skip("No repos available")
	}

	repo := repos[0]

	// Validate the repo
	err = client.ValidateRepo(t.Context(), owner, repo)
	if err != nil {
		t.Errorf("ValidateRepo failed for %s/%s: %v", owner, repo, err)
	} else {
		t.Logf("Successfully validated %s/%s", owner, repo)
	}

	// Try an invalid repo
	err = client.ValidateRepo(t.Context(), owner, "nonexistent-repo-12345")
	if err == nil {
		t.Error("Expected error for nonexistent repo, got nil")
	} else {
		t.Logf("Correctly rejected nonexistent repo: %v", err)
	}
}
