package user

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string
	IsAdmin      bool `json:"is_admin"`
}

type AccessPolicy struct {
	Apps []AppAccess
}

type AppAccess struct {
	CollectorName string
	AppName       string
}

// AllowedTo checks if the app available in the collector.
// To check pure collector availability use empty string for appName
func (ap AccessPolicy) AllowedTo(collectorName, appName string) bool {
	if ap.Apps == nil {
		// For admin there are no apps saved
		return true
	}

	for _, a := range ap.Apps {
		if a.CollectorName != collectorName {
			continue
		}

		if appName == "" || appName == a.AppName {
			return true
		}

	}

	return false
}
