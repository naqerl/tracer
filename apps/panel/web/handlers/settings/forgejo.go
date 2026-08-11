package settings

import (
	"errors"
	"net/http"
	"strings"

	"tracer/apps/panel/internal/services/notify/forgejo"
	"tracer/apps/panel/web/handlers/settings/view"
	"tracer/apps/panel/web/templates"
)

// errForgejoFieldsMissing marks requests without a usable base_url/token pair.
var errForgejoFieldsMissing = errors.New("forgejo base URL and token are required")

// errForgejoMalformedOwnerRepo marks "owner/repo" values that cannot be split
// into an owner and a repo.
var errForgejoMalformedOwnerRepo = errors.New("enter a repository as owner/repo")

// forgejoCredentials holds the base URL and token extracted from a notifier
// form plus a client bound to them.
type forgejoCredentials struct {
	baseURL string
	token   string
	client  *forgejo.Client
}

// forgejoCredentialsFromRequest parses and validates the base_url and token
// form fields. Missing fields return errForgejoFieldsMissing; invalid
// credentials return the validation error.
func forgejoCredentialsFromRequest(r *http.Request) (forgejoCredentials, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(r.FormValue("base_url")), "/")
	token := strings.TrimSpace(r.FormValue("token"))
	if baseURL == "" || token == "" {
		return forgejoCredentials{}, errForgejoFieldsMissing
	}
	if err := forgejo.ValidCredentials(baseURL, token); err != nil {
		return forgejoCredentials{}, err
	}
	return forgejoCredentials{
		baseURL: baseURL,
		token:   token,
		client:  forgejo.NewClient(baseURL, token),
	}, nil
}

// forgejoOwnerRepo holds the owner and repo split from an "owner/repo" value.
type forgejoOwnerRepo struct {
	owner string
	repo  string
}

// splitForgejoOwnerRepo splits and validates an "owner/repo" form value.
// Malformed values return errForgejoMalformedOwnerRepo; invalid names return
// the validation error.
func splitForgejoOwnerRepo(v string) (forgejoOwnerRepo, error) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(v), "/")
	if !ok || owner == "" || repo == "" {
		return forgejoOwnerRepo{}, errForgejoMalformedOwnerRepo
	}
	if err := forgejo.ValidateOwnerRepo(owner, repo); err != nil {
		return forgejoOwnerRepo{}, err
	}
	return forgejoOwnerRepo{owner: owner, repo: repo}, nil
}

// forgejoLabelIDs maps label names to their IDs.
func forgejoLabelIDs(labels []forgejo.Label) map[string]int64 {
	ids := make(map[string]int64, len(labels))
	for _, l := range labels {
		ids[l.Name] = l.ID
	}
	return ids
}

// forgejoMilestoneIDs maps milestone titles to their IDs.
func forgejoMilestoneIDs(milestones []forgejo.Milestone) map[string]int64 {
	ids := make(map[string]int64, len(milestones))
	for _, m := range milestones {
		ids[m.Title] = m.ID
	}
	return ids
}

// welcomeValidationMessage strips the service-level wrap from a welcome
// validation error, leaving the forgejo cause for display next to the token
// field.
func welcomeValidationMessage(err error) string {
	const prefix = "could not send welcome notification: welcome validation failed: "
	msg := err.Error()
	if strings.HasPrefix(msg, prefix) {
		return strings.TrimPrefix(msg, prefix)
	}
	return msg
}

// renderForgejoLookupError renders the auth status (out-of-band) and clears
// the owner/repo box after a failed lookup.
func renderForgejoLookupError(w http.ResponseWriter, r *http.Request, status string) {
	templates.FieldStatus("forgejo-auth-status", status, false, true).Render(r.Context(), w)
	view.ForgejoLookup(view.ForgejoLookupProps{}).Render(r.Context(), w)
}

// renderForgejoOptionsOwnerRepoError returns 422 with the owner/repo error
// rendered out-of-band next to its label and clears the selects box.
func renderForgejoOptionsOwnerRepoError(w http.ResponseWriter, r *http.Request, msg string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	templates.FieldStatus("forgejo-owner-repo-error", msg, false, true).Render(r.Context(), w)
	view.ForgejoSelects(view.ForgejoSelectsProps{}).Render(r.Context(), w)
}

// renderForgejoCreateFieldError returns 422 with the field error rendered
// out-of-band next to its label.
func renderForgejoCreateFieldError(w http.ResponseWriter, r *http.Request, id, msg string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	templates.FieldStatus(id, msg, false, true).Render(r.Context(), w)
}
